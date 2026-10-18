package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"media-release-watcher/internal/integrations"
)

func (a *App) presenceLeaseSession(ctx context.Context, key int64) (*pgxpool.Conn, error) {
	conn, err := a.db.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil {
		conn.Release()
		return nil, err
	}
	if !locked {
		conn.Release()
		return nil, errors.New("presence sync already running")
	}
	return conn, nil
}

func (a *App) presenceLease(ctx context.Context, key int64) (func(), error) {
	conn, err := a.presenceLeaseSession(ctx, key)
	if err != nil {
		return nil, err
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// A failed unlock poisons this connection; close it rather than returning a
		// session-level lease to the pool indefinitely.
		if _, e := conn.Exec(cleanup, `SELECT pg_advisory_unlock($1)`, key); e != nil {
			_ = conn.Conn().Close(cleanup)
		}
		conn.Release()
	}, nil
}

// arrSearchLease monitors the actual shared lock-owning session, not the
// independent task claim. All pgx operations on this pinned connection are
// serialized; probes use no extra pool capacity and cleanup is bounded.
type arrSearchLease struct {
	conn   *pgxpool.Conn
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	done   chan struct{}
	once   sync.Once
}

const arrSearchLeaseKey int64 = 486722127

func (a *App) searchPresenceLease(ctx context.Context) (*arrSearchLease, error) {
	conn, err := a.presenceLeaseSession(ctx, arrSearchLeaseKey)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	lease := &arrSearchLease{conn: conn, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(lease.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if lease.check(ctx) != nil {
					return
				}
			}
		}
	}()
	return lease, nil
}

// check must also run after ArrSearch's final identity GET, immediately before
// its command POST: a periodic heartbeat alone leaves blocked reads unfenced.
func (lease *arrSearchLease) check(ctx context.Context) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.ctx.Err(); err != nil {
		return err
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var owned bool
	err := lease.conn.QueryRow(probe, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=pg_backend_pid() AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND classid=0 AND objid=$1::oid AND objsubid=1 AND granted)`, arrSearchLeaseKey).Scan(&owned)
	if err != nil || !owned {
		lease.cancel()
		return errors.New("shared search lease lost; search stopped")
	}
	return nil
}

func (lease *arrSearchLease) release() {
	lease.once.Do(func() {
		lease.cancel()
		<-lease.done
		lease.mu.Lock()
		defer lease.mu.Unlock()
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := lease.conn.Exec(cleanup, `SELECT pg_advisory_unlock($1)`, arrSearchLeaseKey); err != nil {
			_ = lease.conn.Conn().Close(cleanup)
		}
		lease.conn.Release()
	})
}

func (a *App) presenceReleases(ctx context.Context) ([]Release, error) {
	rows, err := a.db.Query(ctx, `SELECT id,media_type,tmdb_id,imdb_id,COALESCE(data->>'tvdb_id','') FROM releases ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Release{}
	for rows.Next() {
		var r Release
		if err = rows.Scan(&r.ID, &r.Type, &r.TMDbID, &r.IMDbID, &r.TVDbID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func presenceMedia(r Release) (integrations.Media, error) {
	m := integrations.Media{Type: r.Type, IMDbID: r.IMDbID}
	for _, pair := range []struct {
		literal string
		dst     *int
	}{{r.TMDbID, &m.TMDbID}, {r.TVDbID, &m.TVDbID}} {
		if pair.literal == "" {
			continue
		}
		if !numericID.MatchString(pair.literal) {
			return m, errors.New("saved release has invalid literal identity")
		}
		n, e := strconv.Atoi(pair.literal)
		if e != nil {
			return m, errors.New("saved release identity exceeds supported range")
		}
		*pair.dst = n
	}
	if r.IMDbID != "" && !imdbID.MatchString(r.IMDbID) {
		return m, errors.New("saved release has invalid IMDb identity")
	}
	return m, nil
}
func (a *App) saveArrPresence(ctx context.Context, r Release, kind string, p integrations.ArrPresence, checkErr error) error {
	at := time.Now().UTC().Format(time.RFC3339Nano)
	patch := map[string]string{kind + "_error": "", kind + "_last_attempt_at": at, kind + "_last_check_status": "ok"}
	if checkErr != nil {
		patch[kind+"_error"] = providerError(checkErr)
		patch[kind+"_last_check_status"] = "error"
	} else {
		patch[kind+"_checked_at"] = at
		patch[kind] = ""
		patch[kind+"_id"] = ""
		patch[kind+"_ready"] = "false"
		patch[kind+"_file_count"] = "0"
		patch[kind+"_url"] = ""
		patch[kind+"_status"] = "absent"
		if p.ID > 0 {
			patch[kind] = "already_exists"
			patch[kind+"_id"] = strconv.Itoa(p.ID)
			patch[kind+"_ready"] = strconv.FormatBool(p.Ready)
			patch[kind+"_file_count"] = strconv.Itoa(p.FileCount)
			patch[kind+"_url"] = p.URL
			patch[kind+"_status"] = "present"
		}
	}
	raw, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	if checkErr != nil {
		// A failed attempt never changes the timestamp or status of verified
		// evidence. First failures are explicit errors rather than false absence.
		_, err = a.db.Exec(ctx, `UPDATE releases SET acquisition=(CASE WHEN acquisition->>($7::text||'_status') IN ('present','absent') THEN acquisition ELSE acquisition||jsonb_build_object($7::text||'_status','error') END)||$2::jsonb,updated_at=now() WHERE id=$1 AND media_type=$3 AND tmdb_id=$4 AND imdb_id=$5 AND COALESCE(data->>'tvdb_id','')=$6`, r.ID, raw, r.Type, r.TMDbID, r.IMDbID, r.TVDbID, kind)
	} else {
		_, err = a.db.Exec(ctx, `UPDATE releases SET acquisition=acquisition||$2::jsonb,updated_at=now() WHERE id=$1 AND media_type=$3 AND tmdb_id=$4 AND imdb_id=$5 AND COALESCE(data->>'tvdb_id','')=$6`, r.ID, raw, r.Type, r.TMDbID, r.IMDbID, r.TVDbID)
	}
	return err
}

// syncArr is entirely read-only externally. Enabled is an acquisition opt-in,
// never a prerequisite for reading the inventory of a configured manager.
func (a *App) syncArr(ctx context.Context) error {
	unlock, err := a.presenceLease(ctx, 486722125)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := a.settings(ctx)
	if err != nil {
		return err
	}
	releases, err := a.presenceReleases(ctx)
	if err != nil {
		return err
	}
	client := a.client(s)
	var failures []error
	for _, manager := range []struct {
		kind, mediaType string
		cfg             ArrSettings
	}{{"radarr", "movie", s.Radarr}, {"sonarr", "show", s.Sonarr}} {
		if manager.cfg.URL == "" || manager.cfg.APIKey == "" {
			continue
		}
		inventory, inventoryErr := client.ArrInventory(ctx, manager.kind)
		if inventoryErr != nil {
			failures = append(failures, inventoryErr)
		}
		for _, r := range releases {
			if r.Type != manager.mediaType {
				continue
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			var presence integrations.ArrPresence
			checkErr := inventoryErr
			if checkErr == nil {
				media, e := presenceMedia(r)
				checkErr = e
				if checkErr == nil {
					presence, checkErr = inventory.Match(media)
				}
				if checkErr != nil {
					failures = append(failures, checkErr)
				}
			}
			if err = a.saveArrPresence(ctx, r, manager.kind, presence, checkErr); err != nil {
				return err
			}
		}
	}
	return errors.Join(failures...)
}
func (a *App) startPresenceSync(kind string, run func(context.Context) error) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		return false
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		ctx, cancel := context.WithTimeout(a.ctx, 15*time.Minute)
		defer cancel()
		if err := run(ctx); err != nil {
			a.activity(ctx, kind, kind+" sync failed: "+providerError(err), "error")
		} else {
			a.activity(ctx, kind, kind+" read-only presence sync completed", "ok")
		}
	}()
	return true
}
func (a *App) registerArrSyncRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/arr/sync", func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.settings(r.Context()); err != nil {
			fail(w, 500, "cannot read settings")
			return
		}
		if !a.startPresenceSync("arr", a.syncArr) {
			fail(w, 503, "application is shutting down")
			return
		}
		respond(w, http.StatusAccepted, map[string]bool{"ok": true})
	})
	for _, kind := range []string{"radarr", "sonarr"} {
		mux.HandleFunc("GET /api/integrations/"+kind+"/test", func(w http.ResponseWriter, r *http.Request) {
			s, err := a.settings(r.Context())
			if err != nil {
				fail(w, 500, "cannot read settings")
				return
			}
			if _, err = a.client(s).ArrInventory(r.Context(), kind); err != nil {
				fail(w, 502, providerError(err))
				return
			}
			respond(w, 200, map[string]bool{"ok": true})
		})
	}
}
