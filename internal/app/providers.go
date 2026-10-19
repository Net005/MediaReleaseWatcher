package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"media-release-watcher/internal/integrations"
)

func (a *App) client(s Settings) *integrations.Client {
	cfg := integrations.Config{MDBListAPIKey: s.MDBList.APIKey, MDBListBaseURL: a.cfg.MDBListBaseURL, SonarrURL: s.Sonarr.URL, SonarrAPIKey: s.Sonarr.APIKey, RadarrURL: s.Radarr.URL, RadarrAPIKey: s.Radarr.APIKey, PushoverToken: s.Pushover.Token, PushoverUser: s.Pushover.User}
	b, _ := json.Marshal(cfg)
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	if a.providerClient == nil || a.clientConfig != string(b) {
		a.providerClient = integrations.NewClient(cfg)
		a.clientConfig = string(b)
	}
	return a.providerClient
}

// Shared bounded request pacing: at most 2.5 MDBList reads/sec, including UI lookups.
func (a *App) pace(ctx context.Context) error {
	a.providerMu.Lock()
	now := time.Now()
	at := a.nextProvider
	if at.Before(now) {
		at = now
	}
	a.nextProvider = at.Add(400 * time.Millisecond)
	a.providerMu.Unlock()
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (a *App) mdb(ctx context.Context, s Settings, path string, q url.Values) (json.RawMessage, error) {
	if s.MDBList.APIKey == "" {
		return nil, errors.New("MDBList API key is not configured")
	}
	if e := a.pace(ctx); e != nil {
		return nil, e
	}
	return a.client(s).MDBGet(ctx, path, q)
}
func (a *App) fetchRelease(ctx context.Context, s Settings, provider, kind, id string) (Release, error) {
	raw, e := a.mdb(ctx, s, "/"+provider+"/"+kind+"/"+id, nil)
	if e != nil {
		return Release{}, e
	}
	r, people, e := normalizeRelease(raw, kind)
	if e != nil {
		return r, e
	}
	r.MetadataFetchedAt = time.Now().UTC()
	if (provider == "tmdb" && r.TMDbID != id) || (provider == "imdb" && r.IMDbID != id) {
		return Release{}, errors.New("metadata identity does not match requested literal ID")
	}
	if len(people) == 0 && a.cfg.TMDBAPIKey != "" {
		people, e = a.tmdbCast(ctx, &r)
		if e != nil {
			a.activity(ctx, "metadata", "Optional TMDb credits fallback failed; no inferred cast", "warning")
		} else if len(people) > 0 {
			r.Coverage = coverageWithSourceWarnings(r.Coverage, "Actual cast IDs from optional TMDb credits fallback; title metadata from MDBList.")
			return a.saveReleaseSource(ctx, r, people, "tmdb:credits")
		}
	}
	return a.saveRelease(ctx, r, people)
}
func (a *App) addRelease(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Provider string `json:"provider"`
		ID       string `json:"id"`
		Type     string `json:"type"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	if !oneOf(v.Provider, "imdb", "tmdb") || !oneOf(v.Type, "movie", "show") || (v.Provider == "tmdb" && !numericID.MatchString(v.ID)) || (v.Provider == "imdb" && !imdbID.MatchString(v.ID)) {
		fail(w, 400, "provider, literal ID and movie/show type are required")
		return
	}
	s, e := a.settings(r.Context())
	if e != nil {
		fail(w, 500, "cannot read settings")
		return
	}
	release, e := a.fetchRelease(r.Context(), s, v.Provider, v.Type, v.ID)
	if e != nil {
		fail(w, 502, providerError(e))
		return
	}
	respond(w, 201, release)
}
func providerError(e error) string {
	var he *integrations.HTTPError
	if errors.As(e, &he) {
		return he.Error()
	}
	if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
		return "provider request canceled or timed out"
	}
	s := e.Error()
	if strings.Contains(s, "configured") || strings.Contains(s, "metadata") || strings.Contains(s, "literal") {
		return s
	}
	return "provider operation failed; check integration configuration or activity"
}
func (a *App) enrichPerson(ctx context.Context, s Settings, p Actress) Actress {
	raw, e := a.mdb(ctx, s, "/people/"+p.TMDbID, nil)
	if e != nil {
		if strings.HasPrefix(p.MetadataStatus, "unavailable: identity mismatch") {
			return p
		}
		p.MetadataStatus = "unavailable: " + providerError(e) + "; supplied identity retained. MDBList biographies do not provide filmographies."
		return p
	}
	m, e := decodeObject(raw)
	if e != nil {
		if strings.HasPrefix(p.MetadataStatus, "unavailable: identity mismatch") {
			return p
		}
		p.MetadataStatus = "unavailable: invalid MDBList biography"
		return p
	}
	// Provider lookup is not permission to silently replace an imported identity.
	// Reject stale/wrong IDs or a different supplied name before attaching any biography.
	id := first(m, "tmdb", "tmdb_id", "id")
	name := first(m, "name")
	if id != p.TMDbID || (p.Name != "" && name != "" && identityName(name) != identityName(p.Name)) {
		p.MetadataStatus = "unavailable: identity mismatch; supplied name and literal ID retained for manual review"
		return p
	}
	if p.Name == "" {
		p.Name = name
	}
	if v := first(m, "profile", "avatar"); v != "" {
		p.Profile = v
	}
	if v := first(m, "biography"); v != "" {
		p.Biography = v
	}
	if v := dateOnly(first(m, "birthday")); v != "" {
		p.Birthday = v
	}
	if v := first(m, "place_of_birth"); v != "" {
		p.PlaceOfBirth = v
	}
	missing := []string{}
	for _, field := range []struct{ name, value string }{{"portrait", first(m, "profile", "avatar")}, {"biography", first(m, "biography")}, {"birthday", dateOnly(first(m, "birthday"))}, {"place of birth", first(m, "place_of_birth")}} {
		if strings.TrimSpace(field.value) == "" {
			missing = append(missing, field.name)
		}
	}
	p.MetadataStatus = "ok: MDBList person fields present; matches require explicit cast/person links, not filmography"
	if len(missing) > 0 {
		p.MetadataStatus = "partial: MDBList responded; missing " + strings.Join(missing, ", ") + "; no filmography inferred"
	}
	return p
}
func (a *App) addActress(w http.ResponseWriter, r *http.Request) {
	p := Actress{Monitored: true}
	if !readJSON(w, r, &p) {
		return
	}
	id, e := personID(p.TMDbID)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 300 {
		fail(w, 400, "name is required (max 300 bytes)")
		return
	}
	p.TMDbID = id
	var existing int64
	e = a.db.QueryRow(r.Context(), `SELECT id FROM actresses WHERE tmdb_id=$1`, id).Scan(&existing)
	if e == nil {
		got, e := a.getActress(r.Context(), existing)
		if e != nil {
			fail(w, 500, "cannot read actress")
			return
		}
		respond(w, 200, got)
		return
	}
	if e != pgx.ErrNoRows {
		fail(w, 500, "cannot check actress")
		return
	}
	s, e := a.settings(r.Context())
	if e != nil {
		fail(w, 500, "cannot read settings")
		return
	}
	p.ID = 0
	p = a.enrichPerson(r.Context(), s, p)
	p, e = a.saveActress(r.Context(), p)
	if e != nil {
		fail(w, 500, "cannot save actress")
		return
	}
	a.activity(r.Context(), "watcher", "Added local actress watcher; MDBList favorites unchanged", "ok")
	respond(w, 201, p)
}
func (a *App) importActresses(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Entries []struct {
			Name      string  `json:"name"`
			ActorID   string  `json:"actor_id"`
			Group     *string `json:"group"`
			Monitored *bool   `json:"monitored"`
			AutoAdd   *bool   `json:"auto_add"`
			Notify    *bool   `json:"notify"`
		} `json:"entries"`
		Group     *string                    `json:"group"`
		Monitored *bool                      `json:"monitored"`
		AutoAdd   *bool                      `json:"auto_add"`
		Notify    *bool                      `json:"notify"`
		Groups    map[string]json.RawMessage `json:"groups"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	if len(v.Entries) > 5000 {
		fail(w, 400, "max 5000 entries per import")
		return
	}
	for _, p := range v.Entries {
		if !numericID.MatchString(p.ActorID) {
			fail(w, 400, "invalid literal actor_id "+strconv.Quote(p.ActorID)+"; entire import rejected without normalization")
			return
		}
		if p.Name == "" || len(p.Name) > 300 {
			fail(w, 400, "every entry requires name (max 300 bytes)")
			return
		}
	}
	tx, e := a.db.Begin(r.Context())
	if e != nil {
		fail(w, 500, "cannot start import")
		return
	}
	defer tx.Rollback(r.Context())
	added, skipped := 0, 0
	defaults := Actress{Monitored: true}
	if v.Group != nil {
		defaults.Group = *v.Group
	}
	if v.Monitored != nil {
		defaults.Monitored = *v.Monitored
	}
	if v.AutoAdd != nil {
		defaults.AutoAdd = *v.AutoAdd
	}
	if v.Notify != nil {
		defaults.Notify = *v.Notify
	}
	seen := map[string]bool{}
	for _, v := range v.Entries {
		if seen[v.ActorID] {
			skipped++
			continue
		}
		seen[v.ActorID] = true
		ctx := r.Context()
		var before, p Actress
		var b []byte
		e = tx.QueryRow(ctx, `SELECT id,data FROM actresses WHERE tmdb_id=$1 FOR UPDATE`, v.ActorID).Scan(&p.ID, &b)
		if e == nil {
			id := p.ID
			if e = json.Unmarshal(b, &p); e != nil {
				fail(w, 500, "import rolled back")
				return
			}
			p.ID = id
			before = p
			skipped++
		} else if e == pgx.ErrNoRows {
			p = defaults
			p.TMDbID = v.ActorID
			p.Name = v.Name
			p.MetadataStatus = "pending: queued MDBList biography enrichment; no filmography inferred"
			p.UpdatedAt = time.Now().UTC()
			if v.Group != nil {
				p.Group = *v.Group
			}
			if v.Monitored != nil {
				p.Monitored = *v.Monitored
			}
			added++
		} else {
			fail(w, 500, "import rolled back")
			return
		}
		if v.AutoAdd != nil {
			p.AutoAdd = *v.AutoAdd
		}
		if v.Notify != nil {
			p.Notify = *v.Notify
		}
		if p.ID == 0 {
			b, _ = json.Marshal(p)
			e = tx.QueryRow(ctx, `INSERT INTO actresses(tmdb_id,data) VALUES($1,$2) RETURNING id`, p.TMDbID, b).Scan(&p.ID)
		} else {
			updates := map[string]any{}
			if v.AutoAdd != nil {
				updates["auto_add"] = *v.AutoAdd
			}
			if v.Notify != nil {
				updates["notify"] = *v.Notify
			}
			b, _ = json.Marshal(updates)
			_, e = tx.Exec(ctx, `UPDATE actresses SET data=data||$2::jsonb,updated_at=now() WHERE id=$1`, p.ID, b)
		}
		if e != nil {
			fail(w, 500, "import rolled back")
			return
		}
		_, e = tx.Exec(ctx, `INSERT INTO release_people(release_id,actress_id,source) SELECT release_id,$1,source FROM release_cast WHERE tmdb_id=$2 AND $3 NOT LIKE 'unavailable: identity mismatch%' ON CONFLICT DO NOTHING`, p.ID, p.TMDbID, p.MetadataStatus)
		if e == nil {
			e = baselineActorActivation(ctx, tx, before, p)
		}
		if e != nil {
			fail(w, 500, "import rolled back")
			return
		}
	}
	_, e = tx.Exec(r.Context(), `INSERT INTO release_people(release_id,actress_id,source) SELECT rc.release_id,p.id,rc.source FROM release_cast rc JOIN actresses p ON p.tmdb_id=rc.tmdb_id WHERE COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%' ON CONFLICT DO NOTHING`)
	if e == nil {
		_, e = tx.Exec(r.Context(), recomputeMonitoring)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		fail(w, 500, "import rolled back")
		return
	}
	a.activity(r.Context(), "import", fmt.Sprintf("Imported %d local watchers; %d exact-ID duplicates skipped; biography enrichment queued", added, skipped), "ok")
	respond(w, 200, map[string]any{"added": added, "skipped": skipped, "queued": added, "warning": "MDBList /people provides biographies, not filmographies. Only actual cast/person links produce release matches. Remote favorites are unchanged."})
}
func (a *App) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	kind := r.URL.Query().Get("type")
	if kind == "" {
		kind = "any"
	}
	if !oneOf(kind, "any", "movie", "show") || len(q) > 200 {
		fail(w, 400, "invalid search")
		return
	}
	if strings.TrimSpace(q) == "" {
		respond(w, 200, map[string]any{"items": []any{}})
		return
	}
	s, e := a.settings(r.Context())
	if e != nil {
		fail(w, 500, "cannot read settings")
		return
	}
	raw, e := a.mdb(r.Context(), s, "/search/"+kind, url.Values{"query": {q}, "limit": {"50"}})
	if e != nil {
		fail(w, 502, providerError(e))
		return
	}
	m, e := decodeObject(raw)
	if e != nil {
		fail(w, 502, "invalid search response")
		return
	}
	items := array(m["search"])
	if items == nil {
		items = []any{}
	}
	respond(w, 200, map[string]any{"items": items})
}

type discoveryRow struct {
	Raw  json.RawMessage
	Kind string
}

func parseDiscovery(raw json.RawMessage) ([]discoveryRow, string, bool, error) {
	m, e := decodeObject(raw)
	if e != nil {
		return nil, "", false, e
	}
	// An unknown or malformed envelope is not proof of an empty, complete feed.
	recognized := false
	for _, key := range []string{"movies", "shows", "items"} {
		if value, present := m[key]; present {
			if _, ok := value.([]any); !ok {
				return nil, "", false, errors.New("metadata discovery collection must be an array")
			}
			recognized = true
		}
	}
	if !recognized {
		return nil, "", false, errors.New("metadata discovery has no recognized collection")
	}
	if value, present := m["pagination"]; present {
		if _, ok := value.(map[string]any); !ok {
			return nil, "", false, errors.New("metadata discovery pagination must be an object")
		}
	}
	for _, pagination := range []map[string]any{m, object(m["pagination"])} {
		if value, present := pagination["has_more"]; present {
			if _, ok := value.(bool); !ok {
				return nil, "", false, errors.New("metadata discovery has_more must be boolean")
			}
		}
		if value, present := pagination["next_cursor"]; present && value != nil {
			if _, ok := value.(string); !ok {
				return nil, "", false, errors.New("metadata discovery next_cursor must be string or null")
			}
		}
	}
	nestedPagination := object(m["pagination"])
	if outer, present := m["has_more"]; present {
		if inner, nested := nestedPagination["has_more"]; nested && outer != inner {
			return nil, "", false, errors.New("metadata discovery pagination signals conflict")
		}
	}
	if outer, present := m["next_cursor"]; present {
		if inner, nested := nestedPagination["next_cursor"]; nested && outer != inner {
			return nil, "", false, errors.New("metadata discovery cursor signals conflict")
		}
	}
	rows := []discoveryRow{}
	appendRow := func(v any, kind string) {
		b, _ := json.Marshal(v)
		rows = append(rows, discoveryRow{Raw: b, Kind: kind})
	}
	for _, key := range []string{"movies", "shows"} {
		kind := "movie"
		if key == "shows" {
			kind = "show"
		}
		for _, v := range array(m[key]) {
			appendRow(v, kind)
		}
	}
	for _, v := range array(m["items"]) {
		o := object(v)
		if media := object(o["media"]); media != nil {
			copy := map[string]any{}
			for k, v := range media {
				copy[k] = v
			}
			if person := object(o["person"]); person != nil {
				copy["matched_people"] = []any{person}
			}
			if d := first(o, "release_date"); d != "" {
				copy["release_date"] = d
			}
			appendRow(copy, first(media, "type", "mediatype"))
		} else {
			appendRow(o, first(o, "type", "mediatype"))
		}
	}
	pg := object(m["pagination"])
	next := first(pg, "next_cursor")
	if next == "" {
		next = first(m, "next_cursor")
	}
	more, _ := pg["has_more"].(bool)
	if b, ok := m["has_more"].(bool); ok {
		more = b
	}
	if next != "" {
		more = true
	}
	return rows, next, more, nil
}
func (a *App) stage(ctx context.Context, s string) {
	a.mu.Lock()
	a.syncState.Stage = s
	state := a.syncState
	a.mu.Unlock()
	b, _ := json.Marshal(state)
	_, _ = a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('sync',$1) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, b)
	a.activity(ctx, "sync", s, "running")
}
func (a *App) startSync() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.syncState.Running || a.ctx.Err() != nil {
		return false
	}
	a.syncState.Running = true
	a.syncState.Stage = "starting"
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		ctx, cancel := context.WithTimeout(a.ctx, 6*time.Hour)
		defer cancel()
		if err := a.syncOnce(ctx); err != nil {
			// A rejected lease never owns persisted state. Clear only our local
			// reservation, not another worker's running/progress state.
			a.mu.Lock()
			if a.syncState.Stage == "starting" && a.syncState.Running {
				a.syncState.Running = false
				a.syncState.Stage = "failed"
				a.syncState.LastError = providerError(err)
			}
			a.mu.Unlock()
		}
	}()
	return true
}

// syncBiographyPage avoids the unbounded UI list and its per-person match queries.
func (a *App) syncBiographyPage(ctx context.Context, after int64) ([]Actress, error) {
	rows, err := a.db.Query(ctx, `SELECT id,data,updated_at FROM actresses WHERE id>$1 ORDER BY id LIMIT 500`, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	people := []Actress{}
	for rows.Next() {
		var p Actress
		var raw []byte
		var id int64
		var updated time.Time
		if err = rows.Scan(&id, &raw, &updated); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		p.ID = id
		p.UpdatedAt = updated
		people = append(people, p)
	}
	return people, rows.Err()
}

// syncCompletion is a per-run value snapshot, never a reference to live progress.
type syncCompletion struct {
	Status  string
	Message string
}

// syncOnce preserves the error-only API for manual and existing callers.
func (a *App) syncOnce(ctx context.Context) error {
	_, err := a.syncOnceResult(ctx)
	return err
}

// syncOnceResult captures completion while this run still owns the sync lease.
// Rejected leases return a zero completion and an error without changing progress.
func (a *App) syncOnceResult(ctx context.Context) (result syncCompletion, runErr error) {
	conn, e := a.db.Acquire(ctx)
	if e != nil {
		return result, e
	}
	defer conn.Release()
	var locked bool
	if e = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(486722121)`).Scan(&locked); e != nil {
		return result, e
	}
	if !locked {
		return result, errors.New("sync already running in another process")
	}
	defer func() {
		unlock, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		_, _ = conn.Exec(unlock, `SELECT pg_advisory_unlock(486722121)`)
	}()

	summary := ""
	a.mu.Lock()
	a.syncState.Running = true
	a.syncState.LastError = ""
	a.mu.Unlock()
	a.stage(ctx, "starting")
	defer func() {
		now := time.Now().UTC()
		a.mu.Lock()
		a.syncState.Running = false
		a.syncState.LastRun = &now
		a.syncState.Stage = "idle"
		a.syncState.LastError = summary
		status, message := "ok", "Sync completed; source coverage and actual person links persisted"
		if summary != "" {
			a.syncState.Stage = "partial"
			status = "warning"
			message = summary
		}
		if runErr != nil {
			a.syncState.Stage = "failed"
			a.syncState.LastError = providerError(runErr)
			status = "error"
			message = a.syncState.LastError
		}
		result = syncCompletion{Status: status, Message: message}
		state := a.syncState
		a.mu.Unlock()
		save, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		b, _ := json.Marshal(state)
		if _, err := a.db.Exec(save, `INSERT INTO app_state(key,value) VALUES('sync',$1) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, b); err != nil {
			log.Printf("persist sync completion: %v", err)
		}
		a.activity(save, "sync", message, status)
	}()
	s, e := a.settings(ctx)
	if e != nil {
		return result, e
	}
	if s.MDBList.APIKey == "" {
		return result, errors.New("MDBList API key is not configured")
	}

	var baseline bool
	if e = a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app_state WHERE key='baseline')`).Scan(&baseline); e != nil {
		return result, e
	}
	candidates := map[int64]Release{}
	activeList := ""
	listMembers := map[string][]int64{}
	failureCount := 0
	consume := func(raw json.RawMessage, kind string) error {
		r, people, e := normalizeRelease(raw, kind)
		if e != nil {
			return errors.New("metadata discovery source record could not be parsed or lacked a literal identity; discovery incomplete")
		}
		r.Coverage = coverageWithSourceWarnings(r.Coverage, "List/catalog metadata only; full MDBList enrichment pending. List membership is not cast evidence.")
		r, e = a.saveRelease(ctx, r, people)
		if e != nil {
			return e
		}
		if len(people) > 0 {
			r, e = a.saveReleaseSource(ctx, r, people, "mdblist:matched_person")
			if e != nil {
				return e
			}
		}
		candidates[r.ID] = r
		if activeList != "" {
			listMembers[activeList] = append(listMembers[activeList], r.ID)
		}
		return nil
	}
	pageFeed := func(path string, offsetMode bool, q url.Values) error {
		cursor := ""
		offset := 0
		seen := map[string]bool{}
		for page := 1; page <= 10000; page++ {
			if e := ctx.Err(); e != nil {
				return e
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			if offsetMode {
				q.Set("offset", strconv.Itoa(offset))
			}
			raw, e := a.mdb(ctx, s, path, q)
			if e != nil {
				return e
			}
			rows, next, more, e := parseDiscovery(raw)
			if e != nil {
				return e
			}
			for _, row := range rows {
				if e = consume(row.Raw, row.Kind); e != nil {
					return e
				}
			}
			a.stage(ctx, fmt.Sprintf("Imported source page %d (%d catalog titles known)", page, len(candidates)))
			if strings.HasPrefix(path, "/catalog/") && more {
				a.activity(ctx, "coverage", "Catalog discovery limited to the first 100 recent/upcoming titles per type; configured lists are fully paginated. No complete filmography claimed.", "warning")
				return nil
			}
			if !more {
				return nil
			}
			if len(rows) == 0 {
				return errors.New("metadata pagination reports more without items")
			}
			if offsetMode {
				offset += len(rows)
				continue
			}
			if next == "" || seen[next] {
				return errors.New("metadata pagination cursor missing or repeated")
			}
			seen[next] = true
			cursor = next
		}
		return errors.New("metadata pagination safety limit reached")
	}
	a.stage(ctx, "Loading configured MDBList lists (source membership is not a cast match)")
	// Legacy encrypted settings may not have persisted policy rows yet.
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return result, err
	}
	if err = syncListPolicies(ctx, tx, s); err == nil {
		err = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	if err != nil {
		return result, err
	}
	for _, list := range configuredLists(s.MDBList) {
		if !list.Enabled {
			continue
		}
		activeList = list.ID
		if e = pageFeed("/lists/"+list.ID+"/items", false, url.Values{"limit": {"1000"}, "append_to_response": {"poster,description,genres,ratings"}}); e != nil {
			return result, e
		}
		if e = a.replaceListMembership(ctx, list.ID, listMembers[list.ID]); e != nil {
			return result, e
		}
	}
	activeList = ""
	a.stage(ctx, "Loading favorites-upcoming and explicit matched-person relationships")
	if e = pageFeed("/people/favorites/upcoming", true, url.Values{"limit": {"100"}}); e != nil {
		return result, e
	}
	// Incremental broad catalog discovery is bounded by dates, never claimed as complete filmography.
	for _, kind := range []string{"movie", "show"} {
		a.stage(ctx, "Reading recent/upcoming MDBList "+kind+" catalog")
		if e = pageFeed("/catalog/"+kind, false, url.Values{"limit": {"100"}, "released_from": {time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02")}, "released_to": {time.Now().UTC().AddDate(0, 3, 0).Format("2006-01-02")}, "sort": {"released"}, "append_to_response": {"poster,description,genres,ratings"}}); e != nil {
			return result, e
		}
	}
	// Include persisted monitored titles even if removed from source lists.
	for page := 1; ; page++ {
		saved, e := a.releases(ctx, releaseFilter{Page: page, Limit: 500})
		if e != nil {
			return result, e
		}
		for _, r := range saved.Items {
			if r.Monitored {
				candidates[r.ID] = r
			}
		}
		if page*500 >= saved.Total {
			break
		}
	}
	for _, kind := range []string{"movie", "show"} {
		ids := []string{}
		for _, r := range candidates {
			if r.Type == kind && r.TMDbID != "" && (r.MetadataFetchedAt.IsZero() || time.Since(r.MetadataFetchedAt) > 6*time.Hour) {
				ids = append(ids, r.TMDbID)
			}
		}
		for offset := 0; offset < len(ids); offset += 50 {
			end := offset + 50
			if end > len(ids) {
				end = len(ids)
			}
			a.stage(ctx, fmt.Sprintf("MDBList %s batch enrichment %d/%d", kind, end, len(ids)))
			if e = a.pace(ctx); e != nil {
				return result, e
			}
			raws, e := a.client(s).MDBBatch(ctx, "tmdb", kind, ids[offset:end])
			if e != nil {
				return result, e
			}
			requested := map[string]bool{}
			for _, id := range ids[offset:end] {
				requested[id] = true
			}
			returned := map[string]bool{}
			for _, raw := range raws {
				r, people, e := normalizeRelease(raw, kind)
				if e != nil {
					failureCount++
					continue
				}
				if !requested[r.TMDbID] {
					failureCount++
					continue
				}
				r.MetadataFetchedAt = time.Now().UTC()
				returned[r.TMDbID] = true
				source := "mdblist:cast"
				if len(people) == 0 && a.cfg.TMDBAPIKey != "" {
					people, e = a.tmdbCast(ctx, &r)
					if e != nil {
						failureCount++
					} else if len(people) > 0 {
						source = "tmdb:credits"
						r.Coverage = coverageWithSourceWarnings(r.Coverage, "Actual cast from configured optional TMDb credits fallback; MDBList title metadata.")
					}
				}
				if _, e = a.saveReleaseSource(ctx, r, people, source); e != nil {
					return result, e
				}
			}
			if len(returned) < len(requested) {
				failureCount += len(requested) - len(returned)
			}
		}
	}
	for _, r := range candidates {
		if r.TMDbID == "" && r.IMDbID != "" && (r.MetadataFetchedAt.IsZero() || time.Since(r.MetadataFetchedAt) > 6*time.Hour) {
			if _, err := a.fetchRelease(ctx, s, "imdb", r.Type, r.IMDbID); err != nil {
				failureCount++
			}
		}
	}
	a.stage(ctx, "Enriching queued or stale local person biography cards")
	var total int
	if e = a.db.QueryRow(ctx, `SELECT count(*) FROM actresses`).Scan(&total); e != nil {
		return result, e
	}
	attempted, partial, unavailable, skipped, processed := 0, 0, 0, 0, 0
	var after int64
	for {
		people, err := a.syncBiographyPage(ctx, after)
		if err != nil {
			return result, err
		}
		if len(people) == 0 {
			break
		}
		for _, p := range people {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			after = p.ID
			if strings.HasPrefix(p.MetadataStatus, "pending") || time.Since(p.UpdatedAt) > 7*24*time.Hour {
				p = a.enrichPerson(ctx, s, p)
				if _, e = a.savePersonMetadata(ctx, p); e != nil {
					return result, e
				}
				attempted++
			} else {
				skipped++
			}
			if strings.HasPrefix(p.MetadataStatus, "unavailable:") {
				unavailable++
			}
			if strings.HasPrefix(p.MetadataStatus, "partial:") || (strings.HasPrefix(p.MetadataStatus, "ok") && (strings.TrimSpace(p.Biography) == "" || strings.TrimSpace(p.Profile) == "" || strings.TrimSpace(p.Birthday) == "" || strings.TrimSpace(p.PlaceOfBirth) == "")) {
				partial++
			}
			processed++
		}
		a.stage(ctx, fmt.Sprintf("Biography cards processed %d/%d; %d attempted, %d not due; %d partial, %d unavailable (not complete filmographies)", processed, total, attempted, skipped, partial, unavailable))
	}

	if partial > 0 || unavailable > 0 || failureCount > 0 {
		summary = fmt.Sprintf("Sync processed %d/%d biography cards; %d attempted, %d not due; %d partial, %d unavailable; %d title metadata gaps. Prior data retained; no complete filmography claimed", processed, total, attempted, skipped, partial, unavailable, failureCount)
	}
	if failureCount > 0 {
		a.activity(ctx, "metadata", fmt.Sprintf("%d metadata records incomplete or unavailable; prior data retained, no cast inferred", failureCount), "warning")
		// Discovery IDs were fully persisted; omissions here are optional per-title
		// enrichment gaps, not incomplete source pagination. Baseline all known IDs
		// silently so permanently unavailable metadata cannot block the whole app.
	}
	a.stage(ctx, "Refreshing configured Sonarr/Radarr inventories (read-only)")
	if err := a.syncArr(ctx); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		a.activity(ctx, "arr", "Read-only inventory sync incomplete; prior availability retained: "+providerError(err), "warning")
	}
	a.stage(ctx, "Evaluating explicitly opted-in automation (initial baseline always silent)")
	// Discovery may take hours. Re-read global consent/config after network work.
	s, e = a.settings(ctx)
	if e != nil {
		return result, e
	}
	if e = a.automate(ctx, s, !baseline); e != nil {
		return result, e
	}
	if !baseline {
		_, e = a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('baseline','true') ON CONFLICT DO NOTHING`)
		if e != nil {
			return result, e
		}
		a.activity(ctx, "automation", "Initial baseline persisted silently; no historic acquisition or notification sent", "ok")
	}
	return result, nil
}
