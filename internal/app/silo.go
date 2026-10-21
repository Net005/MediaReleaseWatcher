package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"media-release-watcher/internal/integrations"
)

func (a *App) registerSiloRoutes(mux *http.ServeMux) {
	for _, method := range []string{"GET", "POST"} {
		mux.HandleFunc(method+" /api/integrations/silo/test", a.testSilo)
	}
	mux.HandleFunc("POST /api/silo/sync", func(w http.ResponseWriter, r *http.Request) {
		s, err := a.settings(r.Context())
		if err != nil {
			fail(w, 500, "cannot read settings")
			return
		}
		if !s.Silo.Enabled || s.Silo.URL == "" || s.Silo.APIKey == "" {
			fail(w, 400, "enable and configure Silo before syncing")
			return
		}
		if !a.startPresenceSync("silo", a.syncSilo) {
			fail(w, 503, "application is shutting down")
			return
		}
		respond(w, http.StatusAccepted, map[string]bool{"ok": true})
	})
}
func (a *App) testSilo(w http.ResponseWriter, r *http.Request) {
	s, err := a.settings(r.Context())
	if err != nil {
		fail(w, 500, "cannot read settings")
		return
	}
	client := integrations.NewClient(integrations.Config{SiloURL: s.Silo.URL, SiloAPIKey: s.Silo.APIKey})
	if err = client.SiloTest(r.Context()); err != nil {
		fail(w, 502, providerError(err))
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (a *App) saveSiloPresence(ctx context.Context, r Release, p integrations.LocalAvailability, checkErr error) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if checkErr == nil {
		_, err = a.db.Exec(ctx, `UPDATE releases SET data=jsonb_set(data,'{local_availability}',$2::jsonb),updated_at=now() WHERE id=$1 AND media_type=$3 AND tmdb_id=$4 AND imdb_id=$5 AND COALESCE(data->>'tvdb_id','')=$6`, r.ID, raw, r.Type, r.TMDbID, r.IMDbID, r.TVDbID)
	} else {
		// Keep the last completed catalog status, URL and checked_at on failures.
		// A first failure has status=error; cached available/absent remains evidence
		// with a separate failed-attempt diagnostic, never a false absence.
		status := "error"
		var gap *integrations.SiloCoverageError
		if errors.As(checkErr, &gap) {
			status = "warning"
		}
		diagnostic, _ := json.Marshal(map[string]any{"error": providerError(checkErr), "last_check_status": status, "last_attempt_at": time.Now().UTC()})
		_, err = a.db.Exec(ctx, `UPDATE releases SET data=jsonb_set(data,'{local_availability}',CASE WHEN data->'local_availability'->>'status' IN ('available','absent') THEN (data->'local_availability')||$7::jsonb ELSE $2::jsonb END),updated_at=now() WHERE id=$1 AND media_type=$3 AND tmdb_id=$4 AND imdb_id=$5 AND COALESCE(data->>'tvdb_id','')=$6`, r.ID, raw, r.Type, r.TMDbID, r.IMDbID, r.TVDbID, diagnostic)
	}
	return err
}

// siloSyncWarning is a completed read-only reconciliation with per-title
// coverage gaps, not a failed inventory read. Counts describe this attempt,
// not cached evidence retained on the releases. It implements error solely for
// compatibility with presence-sync callbacks; callers must classify it.
type siloSyncWarning struct {
	Available, Absent, Unknown           int
	Unidentified, Ambiguous, Conflicting int
}

func (w *siloSyncWarning) Error() string {
	return fmt.Sprintf("Silo presence sync completed with coverage warning: %d available, %d absent, %d unknown (%d unidentified, %d ambiguous, %d conflicting); prior verified presence retained", w.Available, w.Absent, w.Unknown, w.Unidentified, w.Ambiguous, w.Conflicting)
}

func presenceSyncOutcome(err error) (string, string) {
	if err == nil {
		return "ok", ""
	}
	// Only a standalone completed result is a warning. A join with a fatal
	// transport, persistence or cancellation error must never be downgraded.
	if warning, ok := err.(*siloSyncWarning); ok {
		return "warning", warning.Error()
	}
	if warning, ok := err.(*arrSyncWarning); ok {
		return "warning", warning.Error()
	}
	return "error", providerError(err)
}

// syncSilo updates only local JSON presence fields; the adapter performs GETs
// exclusively. One client provides one complete catalog cache per run.
func (a *App) syncSilo(ctx context.Context) error {
	unlock, err := a.presenceLease(ctx, 486722126)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := a.settings(ctx)
	if err != nil {
		return err
	}
	if !s.Silo.Enabled {
		return nil
	}
	client := integrations.NewClient(integrations.Config{SiloURL: s.Silo.URL, SiloAPIKey: s.Silo.APIKey})
	releases, err := a.presenceReleases(ctx)
	if err != nil {
		return err
	}
	inventoryErr := client.SiloInventory(ctx)
	var failures []error
	if inventoryErr != nil {
		failures = append(failures, inventoryErr)
	}
	warning := &siloSyncWarning{}
	for _, r := range releases {
		if err = ctx.Err(); err != nil {
			return err
		}
		media, checkErr := presenceMedia(r)
		p := integrations.LocalAvailability{Status: "error", CheckedAt: time.Now().UTC()}
		if checkErr == nil {
			if inventoryErr != nil {
				checkErr = inventoryErr
			} else {
				p, checkErr = client.SiloAvailability(ctx, media)
			}
		}
		var gap *integrations.SiloCoverageError
		if errors.As(checkErr, &gap) {
			p.Status = "unknown"
			warning.Unknown++
			switch gap.Reason {
			case "unidentified":
				warning.Unidentified++
			case "ambiguous":
				warning.Ambiguous++
			case "conflicting":
				warning.Conflicting++
			}
		} else if checkErr != nil {
			failures = append(failures, checkErr)
		} else {
			switch p.Status {
			case "available":
				warning.Available++
			case "absent":
				warning.Absent++
			case "unknown":
				warning.Unknown++
				warning.Unidentified++
				checkErr = &integrations.SiloCoverageError{Reason: "unidentified"}
			}
		}
		if checkErr != nil {
			p.Error = providerError(checkErr)
		}
		if err = a.saveSiloPresence(ctx, r, p, checkErr); err != nil {
			return err
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	if warning.Unknown > 0 {
		return warning
	}
	return nil
}
