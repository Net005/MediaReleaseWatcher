package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewCreditCatchupVerifiedTitleSilentBaseline(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var writes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "combined_credits"):
			fmt.Fprint(w, `{"id":939137,"cast":[{"id":311648,"media_type":"tv"}],"crew":[{"id":99,"media_type":"movie"}]}`)
		case strings.HasPrefix(r.URL.Path, "/person/") || strings.HasPrefix(r.URL.Path, "/people/"):
			fmt.Fprint(w, `{"id":939137,"name":"Nam Ji-hyun"}`)
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/tmdb/show"):
			fmt.Fprint(w, `[{"title":"Make Me Tingle","type":"show","ids":{"tmdb":311648,"imdb":"tt47661704","tvdb":472908},"released":"2026-10-15"}]`)
		default:
			writes.Add(1)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	a.cfg.MDBListBaseURL = srv.URL
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	s.MDBList.APIKey = "synthetic"
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(ctx, `UPDATE scheduled_tasks SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	a.Start()
	w := httptest.NewRecorder()
	a.addActress(w, httptest.NewRequest("POST", "/api/actresses", strings.NewReader(`{"tmdb_id":"939137","name":"Nam Ji-hyun","monitored":true,"auto_add":true,"notify":true,"group":"Keep"}`)))
	if w.Code != 201 {
		t.Fatalf("new watch %d %s", w.Code, w.Body.String())
	}
	var p Actress
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	waitCatchupCount(t, a, `SELECT count(*) FROM release_people`, 1)
	waitCatchupCount(t, a, `SELECT count(*) FROM app_state WHERE key LIKE 'credit_catchup:%'`, 0)
	releases, err := a.releases(ctx, releaseFilter{ActressID: p.ID, Limit: 36})
	if err != nil || len(releases.Items) != 1 {
		t.Fatalf("missing verified title: %+v %v", releases, err)
	}
	got := releases.Items[0]
	if got.Title != "Make Me Tingle" || got.Monitored || got.IMDbID != "tt47661704" || got.TVDbID != "472908" || got.ReleaseDate != "2026-10-15" {
		t.Fatalf("bad title: %+v", got)
	}
	var n int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM activity WHERE kind='credits' AND status='ok'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("catch-up activity %d %v", n, err)
	}
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE status='baseline'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("baseline %d %v", n, err)
	}
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM app_state WHERE key='sync' OR key LIKE 'credit_catchup:%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("global state overwritten or queue retained %d %v", n, err)
	}
	person, err := a.getActress(ctx, p.ID)
	if err != nil || person.Group != "Keep" || !person.AutoAdd || !person.Notify {
		t.Fatalf("flags changed %+v %v", person, err)
	}
	if writes.Load() != 0 {
		t.Fatalf("unexpected provider writes: %d", writes.Load())
	}
	var cache tmdbCreditCache
	var raw []byte
	if err = a.db.QueryRow(ctx, `SELECT value FROM app_state WHERE key='tmdb:actor:939137'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &cache); err != nil || !cache.Complete {
		t.Fatalf("incomplete cache %+v %v", cache, err)
	}
}

func TestNewCreditCatchupStartupRepairsOnlyUncached(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "combined_credits") {
			fmt.Fprint(w, `{"id":939137,"cast":[]}`)
		} else {
			fmt.Fprint(w, `{"id":939137,"name":"Nam Ji-hyun"}`)
		}
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"939137", "2"} {
		if _, err := a.saveActress(ctx, Actress{TMDbID: id, Name: "Nam Ji-hyun"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.db.Exec(ctx, `DELETE FROM app_state WHERE key LIKE 'credit_catchup:%'; INSERT INTO app_state(key,value) VALUES('tmdb:actor:2','{"complete":false,"next_retry":"2099-01-01T00:00:00Z"}'); UPDATE scheduled_tasks SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	a.Start()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var done bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app_state WHERE key='tmdb:actor:939137' AND value->>'complete'='true')`).Scan(&done); err != nil {
			t.Fatal(err)
		}
		if done {
			var n int
			if err := a.db.QueryRow(ctx, `SELECT count(*) FROM app_state WHERE key='credit_catchup:2'`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("older incomplete actor queued: %d %v", n, err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("startup did not repair uncached watch independently of full sync")
}

func TestNewCreditCatchupBulkImportWakesAndContinues(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/person/"), "/")[0]
		if strings.HasSuffix(r.URL.Path, "combined_credits") {
			cast := []any{}
			if id != "100" {
				for i := 1; i <= 12; i++ {
					cast = append(cast, map[string]any{"id": i, "media_type": "movie"})
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"id": json.Number(id), "cast": cast})
			return
		}
		fmt.Fprintf(w, `{"id":%s,"name":"Person %s"}`, id, id)
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(ctx, `UPDATE scheduled_tasks SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveActress(ctx, Actress{TMDbID: "100", Name: "Person 100"}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 12; i++ {
		if _, err := a.saveRelease(ctx, Release{Type: "movie", TMDbID: fmt.Sprint(i), Title: "Local"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	a.Start()
	waitCatchupCount(t, a, `SELECT count(*) FROM app_state WHERE key='tmdb:actor:100' AND value->>'complete'='true'`, 1)
	entries := []map[string]string{}
	for i := 101; i <= 105; i++ {
		entries = append(entries, map[string]string{"actor_id": fmt.Sprint(i), "name": fmt.Sprintf("Person %d", i)})
	}
	body, _ := json.Marshal(map[string]any{"entries": entries})
	w := httptest.NewRecorder()
	a.importActresses(w, httptest.NewRequest("POST", "/api/actresses/import", strings.NewReader(string(body))))
	if w.Code != 200 {
		t.Fatalf("import %d %s", w.Code, w.Body.String())
	}
	waitCatchupCount(t, a, `SELECT count(*) FROM release_people`, 60)
	waitCatchupCount(t, a, `SELECT count(*) FROM app_state WHERE key LIKE 'credit_catchup:%'`, 0)
	w = httptest.NewRecorder()
	a.addActress(w, httptest.NewRequest("POST", "/api/actresses", strings.NewReader(`{"tmdb_id":"106","name":"Person 106","monitored":false}`)))
	if w.Code != 201 {
		t.Fatalf("single add %d %s", w.Code, w.Body.String())
	}
	waitCatchupCount(t, a, `SELECT count(*) FROM release_people`, 72)
}

func waitCatchupCount(t *testing.T, a *App, query string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	n := -1
	for time.Now().Before(deadline) {
		if err := a.db.QueryRow(context.Background(), query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("catch-up count = %d, want %d: %s", n, want, query)
}

func TestNewCreditCatchupPassBudgetAndScopedContinuation(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/person/")
		fmt.Fprintf(w, `{"id":%s,"name":"Person"}`, id)
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	ids := []int64{}
	for i := 1; i <= 6; i++ {
		p, err := a.saveActress(ctx, Actress{TMDbID: fmt.Sprint(i), Name: "Person"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
		credits := []tmdbCredit{}
		for j := 1; j <= 12; j++ {
			credits = append(credits, tmdbCredit{ID: fmt.Sprint(j), Kind: "movie"})
		}
		raw, _ := json.Marshal(tmdbCreditCache{Fetched: time.Now(), Credits: credits})
		if _, err = a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES($1,$2)`, "tmdb:actor:"+fmt.Sprint(i), raw); err != nil {
			t.Fatal(err)
		}
	}
	// Actor six is an older cached watch, outside this catch-up request.
	if _, err := a.db.Exec(ctx, `DELETE FROM app_state WHERE key='credit_catchup:'||$1::bigint::text`, ids[5]); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 12; i++ {
		if _, err := a.saveRelease(ctx, Release{Type: "movie", TMDbID: fmt.Sprint(i), Title: "Local"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	more, err := a.runCreditCatchupPass(ctx, s)
	if err != nil || !more {
		t.Fatalf("first pass %v %v", more, err)
	}
	waitCatchupCount(t, a, `SELECT count(*) FROM release_people`, 40)
	for pass := 0; more && pass < 8; pass++ {
		more, err = a.runCreditCatchupPass(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
	}
	if more {
		t.Fatal("continuation did not complete")
	}
	waitCatchupCount(t, a, `SELECT count(*) FROM release_people`, 60)
	waitCatchupCount(t, a, `SELECT count(*) FROM app_state WHERE key='tmdb:actor:6' AND value->>'attempted'<>'0001-01-01T00:00:00Z'`, 0)
}

func TestNewCreditCatchupBusyDisabledDeletedAndDeferral(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method == "POST" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, `{"id":1,"name":"Person"}`)
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	a.cfg.MDBListBaseURL = srv.URL
	p, err := a.saveActress(ctx, Actress{TMDbID: "1", Name: "Person"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(tmdbCreditCache{Fetched: time.Now(), Credits: []tmdbCredit{{ID: "42", Kind: "movie"}}})
	if _, err = a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('tmdb:actor:1',$1)`, raw); err != nil {
		t.Fatal(err)
	}
	s := defaultSettings()
	s.TMDB.Enabled = false
	s.TMDB.APIKey = "synthetic"
	s.MDBList.APIKey = "synthetic"
	if more, err := a.runCreditCatchupPass(ctx, s); err != nil || more {
		t.Fatalf("disabled %v %v", more, err)
	}
	s.TMDB.Enabled = true
	held, err := a.db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = held.Exec(ctx, `SELECT pg_advisory_lock(486722121)`); err != nil {
		held.Release()
		t.Fatal(err)
	}
	more, err := a.runCreditCatchupPass(ctx, s)
	_, unlockErr := held.Exec(ctx, `SELECT pg_advisory_unlock(486722121)`)
	held.Release()
	if err != nil || more || unlockErr != nil {
		t.Fatalf("busy %v %v %v", more, err, unlockErr)
	}
	if calls.Load() != 0 {
		t.Fatal("busy/disabled performed provider work")
	}
	waitCatchupCount(t, a, `SELECT count(*) FROM app_state WHERE key LIKE 'credit_catchup:%'`, 1)
	if more, err = a.runCreditCatchupPass(ctx, s); err != nil || more {
		t.Fatalf("deferral %v %v", more, err)
	}
	before := calls.Load()
	if before != 2 {
		t.Fatalf("provider work: %d", before)
	}
	for i := 0; i < 3; i++ {
		if more, err = a.runCreditCatchupPass(ctx, s); err != nil || more {
			t.Fatalf("cooldown %v %v", more, err)
		}
	}
	if calls.Load() != before {
		t.Fatal("hammered deferred provider")
	}
	if _, err = a.db.Exec(ctx, `DELETE FROM actresses WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.runCreditCatchupPass(ctx, s); err != nil {
		t.Fatal(err)
	}
	waitCatchupCount(t, a, `SELECT count(*) FROM actresses`, 0)
	waitCatchupCount(t, a, `SELECT count(*) FROM app_state WHERE key LIKE 'credit_catchup:%'`, 0)
	if calls.Load() != before {
		t.Fatal("deleted watch requested credits")
	}
}

func TestNewCreditCatchupRestartRetainsPendingAndGlobalState(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if _, err := a.saveActress(ctx, Actress{TMDbID: "1", Name: "Person"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveRelease(ctx, Release{Type: "movie", TMDbID: "42", Title: "Local"}, nil); err != nil {
		t.Fatal(err)
	}
	cache := tmdbCreditCache{Fetched: time.Now(), Credits: []tmdbCredit{{ID: "42", Kind: "movie"}}}
	raw, _ := json.Marshal(cache)
	if _, err := a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('tmdb:actor:1',$1),('sync','{"stage":"failed","last_error":"keep me"}')`, raw); err != nil {
		t.Fatal(err)
	}
	b, err := New(ctx, a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{"id":1,"name":"Person"}`) }))
	defer srv.Close()
	b.cfg.TMDBBaseURL = srv.URL
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	if more, err := b.runCreditCatchupPass(ctx, s); err != nil || more {
		t.Fatalf("restart %v %v", more, err)
	}
	waitCatchupCount(t, b, `SELECT count(*) FROM release_people`, 1)
	var state string
	if err = b.db.QueryRow(ctx, `SELECT value->>'last_error' FROM app_state WHERE key='sync'`).Scan(&state); err != nil || state != "keep me" {
		t.Fatalf("global state %q %v", state, err)
	}
	if b.syncState.Stage != "failed" || calls.Load() != 1 {
		t.Fatalf("restart state %+v, requests %d", b.syncState, calls.Load())
	}
}

func TestNewCreditCatchupCacheAndPersistenceErrorsRetainQueue(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if _, err := a.saveActress(ctx, Actress{TMDbID: "1", Name: "Person"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('tmdb:actor:1','{"credits":"invalid"}')`); err != nil {
		t.Fatal(err)
	}
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	if _, err := a.runCreditCatchupPass(ctx, s); err == nil {
		t.Fatal("invalid cache silently accepted")
	}
	waitCatchupCount(t, a, `SELECT count(*) FROM app_state WHERE key LIKE 'credit_catchup:%'`, 1)
	if _, err := a.db.Exec(ctx, `DELETE FROM app_state WHERE key='tmdb:actor:1';
 CREATE FUNCTION reject_catchup_cache() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.key LIKE 'tmdb:actor:%' THEN RAISE EXCEPTION 'synthetic cache persistence failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_catchup_cache BEFORE INSERT OR UPDATE ON app_state FOR EACH ROW EXECUTE FUNCTION reject_catchup_cache()`); err != nil {
		t.Fatal(err)
	}
	defer a.db.Exec(ctx, `DROP TRIGGER reject_catchup_cache ON app_state; DROP FUNCTION reject_catchup_cache()`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "combined_credits") {
			fmt.Fprint(w, `{"id":1,"cast":[]}`)
		} else {
			fmt.Fprint(w, `{"id":1,"name":"Person"}`)
		}
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	if _, err := a.runCreditCatchupPass(ctx, s); err == nil {
		t.Fatal("persistence failure reported success")
	}
	waitCatchupCount(t, a, `SELECT count(*) FROM app_state WHERE key LIKE 'credit_catchup:%'`, 1)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := a.runCreditCatchupPass(canceled, s); err == nil {
		t.Fatal("cancellation reported success")
	}
}

func TestNewCreditCatchupShutdownCancelsProvider(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	a.cfg.TMDBBaseURL = srv.URL
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(ctx, `UPDATE scheduled_tasks SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveActress(ctx, Actress{TMDbID: "1", Name: "Person"}); err != nil {
		t.Fatal(err)
	}
	a.Start()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}
	done := make(chan struct{})
	go func() { a.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown hung on credit provider")
	}
	// Start/wake after Close must not Add to a WaitGroup being waited on.
	a.Start()
	a.wakeCreditCatchup()
}

func TestNewCreditCatchupRejectsActualCompetingFullSync(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "combined_credits") {
			fmt.Fprint(w, `{"id":1,"cast":[]}`)
			return
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `{"id":1,"name":"Person"}`)
	}))
	defer srv.Close()
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	defer close(release)
	a.cfg.TMDBBaseURL = srv.URL
	if _, err := a.saveActress(ctx, Actress{TMDbID: "1", Name: "Person"}); err != nil {
		t.Fatal(err)
	}
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	done := make(chan error, 1)
	go func() { _, err := a.runCreditCatchupPass(work, s); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("catch-up did not reach provider")
	}
	if err := a.syncOnce(ctx); err == nil || !strings.Contains(err.Error(), "sync already running") {
		t.Fatalf("full sync not rejected: %v", err)
	}
	waitCatchupCount(t, a, `SELECT count(*) FROM app_state WHERE key='sync'`, 0)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled catch-up reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("catch-up did not cancel")
	}
}

func TestNewCreditCatchupDurableNewOnly(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, err := a.saveActress(ctx, Actress{TMDbID: "939137", Name: "Nam Ji-hyun", Group: "Keep", Monitored: false})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM app_state WHERE key='credit_catchup:'||$1::bigint::text`, p.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("new watch not durably queued: %d %v", n, err)
	}
	if _, err = a.db.Exec(ctx, `DELETE FROM app_state WHERE key='credit_catchup:'||$1::bigint::text`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.saveActress(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM app_state WHERE key='credit_catchup:'||$1::bigint::text`, p.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("existing watch requeued: %d %v", n, err)
	}
}
