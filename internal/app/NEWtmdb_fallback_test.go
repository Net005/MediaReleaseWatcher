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

func TestTMDBPersonFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":123,"name":"Canonical","also_known_as":["Alias"],"biography":"Fallback","birthday":"2000-01-01","profile_path":"/portrait.jpg"}`))
	}))
	defer srv.Close()
	a := &App{cfg: Config{TMDBBaseURL: srv.URL}}
	s := Settings{}
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "key"
	p := Actress{TMDbID: "123", Name: "Alias", Biography: "Primary"}
	got, err := a.tmdbPerson(context.Background(), s, p)
	if err != nil || got.Name != "Alias" || got.Biography != "Primary" || got.Birthday != "2000-01-01" || got.Profile == "" {
		t.Fatalf("fallback %+v %v", got, err)
	}
	p.Name = "Wrong"
	if _, err = a.tmdbPerson(context.Background(), s, p); err == nil {
		t.Fatal("accepted wrong identity")
	}
	p.Name = "Alias"
	p.MetadataStatus = "unavailable: identity mismatch"
	if _, err = a.tmdbPerson(context.Background(), s, p); err == nil {
		t.Fatal("cleared quarantine")
	}
}

func TestTMDBCreditsCastOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":123,"cast":[{"id":42,"media_type":"movie"},{"id":43,"media_type":"tv"},{"id":42,"media_type":"movie"}],"crew":[{"id":99,"media_type":"movie"}]}`))
	}))
	defer srv.Close()
	a := &App{cfg: Config{TMDBBaseURL: srv.URL}}
	s := Settings{}
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "key"
	credits, err := a.tmdbPersonCredits(context.Background(), s, "123")
	if err != nil || len(credits) != 2 || credits[0].ID != "42" || credits[1].Kind != "show" {
		t.Fatalf("credits %+v %v", credits, err)
	}
}

func TestTMDBEnrichPersonPrimaryFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/people/") {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"id":123,"name":"Person","biography":"TMDb biography"}`))
	}))
	defer srv.Close()
	a := &App{cfg: Config{TMDBBaseURL: srv.URL, MDBListBaseURL: srv.URL}}
	s := Settings{}
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "key"
	s.MDBList.APIKey = "mdb"
	p := a.enrichPerson(context.Background(), s, Actress{TMDbID: "123", Name: "Person"})
	if p.Biography != "TMDb biography" {
		t.Fatalf("fallback not wired %+v", p)
	}
}

func TestPostgresTMDBBoundedContinuation(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	s := Settings{}
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "key"
	s.MDBList.APIKey = "mdb"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		id := parts[2]
		if strings.HasSuffix(r.URL.Path, "combined_credits") {
			cast := []any{}
			for i := 1; i <= 12; i++ {
				cast = append(cast, map[string]any{"id": i, "media_type": "movie"})
			}
			json.NewEncoder(w).Encode(map[string]any{"id": json.Number(id), "cast": cast, "crew": []any{map[string]any{"id": 99, "media_type": "movie"}}})
			return
		}
		fmt.Fprintf(w, `{"id":%s,"name":"Person %s"}`, id, id)
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	for i := 1; i <= 5; i++ {
		_, err := a.saveActress(ctx, Actress{TMDbID: fmt.Sprint(100 + i), Name: fmt.Sprintf("Person %d", 100+i), Monitored: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 12; i++ {
		_, err := a.saveRelease(ctx, Release{Type: "movie", TMDbID: fmt.Sprint(i), Title: fmt.Sprintf("Primary %d", i)}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	pending, err := a.syncTMDBCredits(ctx, s)
	if err != nil || pending == 0 {
		t.Fatalf("pending %d %v", pending, err)
	}
	var count int
	a.db.QueryRow(ctx, `SELECT count(*) FROM release_people`).Scan(&count)
	if count != 40 {
		t.Fatalf("budget got %d links", count)
	}
	var raw []byte
	if err = a.db.QueryRow(ctx, `SELECT value FROM app_state WHERE key='tmdb:actor:101'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var cache tmdbCreditCache
	if json.Unmarshal(raw, &cache) != nil || len(cache.Credits) != 12 {
		t.Fatal("lost pending identities", string(raw))
	}
	_, err = a.syncTMDBCredits(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	a.db.QueryRow(ctx, `SELECT count(*) FROM release_people`).Scan(&count)
	if count <= 40 {
		t.Fatal("continuation made no progress", count)
	}
	var actions int
	a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE status<>'baseline'`).Scan(&actions)
	if actions != 0 {
		t.Fatal("queued automation")
	}
	r, err := a.getRelease(ctx, 1)
	if err != nil || !strings.HasPrefix(r.Title, "Primary") || !strings.Contains(strings.Join(r.SourceLinks, " "), "themoviedb.org/person/") {
		t.Fatalf("metadata/provenance %+v %v", r, err)
	}
}

func TestPostgresTMDBRegularSync(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/person/123":
			w.Write([]byte(`{"id":123,"name":"Person"}`))
		case "/person/123/combined_credits":
			w.Write([]byte(`{"id":123,"cast":[]}`))
		case "/people/123":
			w.Write([]byte(`{"id":123,"name":"Person"}`))
		default:
			w.Write([]byte(`{"items":[]}`))
		}
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	a.cfg.MDBListBaseURL = srv.URL
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "key"
	s.MDBList.APIKey = "mdb"
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Person"}); err != nil {
		t.Fatal(err)
	}
	if err := a.syncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app_state WHERE key='tmdb:actor:123')`).Scan(&exists); err != nil || !exists {
		t.Fatal("regular sync missing actor discovery", err)
	}
}

func TestPostgresTMDBCachedIdentityRechecked(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Wrong"})
	if err != nil {
		t.Fatal(err)
	}
	_ = p
	r, err := a.saveRelease(ctx, Release{TMDbID: "42", Title: "Primary", Type: "movie"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := tmdbCreditCache{Fetched: time.Now(), Credits: []tmdbCredit{{ID: "42", Kind: "movie"}}}
	raw, _ := json.Marshal(c)
	if _, err = a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('tmdb:actor:123',$1)`, raw); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"id":123,"name":"Real"}`)) }))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	s := Settings{}
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "key"
	_, err = a.syncTMDBCredits(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	a.db.QueryRow(ctx, `SELECT count(*) FROM release_people WHERE release_id=$1`, r.ID).Scan(&count)
	if count != 0 {
		t.Fatal("cached verification authorized changed identity")
	}
}

func TestPostgresTMDBUnavailableCreditsDoNotStarveLaterTitles(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	_, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Person"})
	if err != nil {
		t.Fatal(err)
	}
	c := tmdbCreditCache{Fetched: time.Now()}
	for i := 1; i <= 12; i++ {
		c.Credits = append(c.Credits, tmdbCredit{ID: fmt.Sprint(i), Kind: "movie"})
	}
	raw, _ := json.Marshal(c)
	if _, err = a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('tmdb:actor:123',$1)`, raw); err != nil {
		t.Fatal(err)
	}
	for i := 11; i <= 12; i++ {
		if _, err = a.saveRelease(ctx, Release{TMDbID: fmt.Sprint(i), Title: "Primary", Type: "movie"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.Write([]byte(`[]`))
			return
		}
		w.Write([]byte(`{"id":123,"name":"Person"}`))
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	a.cfg.MDBListBaseURL = srv.URL
	s := Settings{}
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "key"
	s.MDBList.APIKey = "mdb"
	for i := 0; i < 2; i++ {
		if _, err = a.syncTMDBCredits(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	a.db.QueryRow(ctx, `SELECT count(*) FROM release_people`).Scan(&count)
	if count != 2 {
		t.Fatal("failed records starved later cached titles", count)
	}
}

func TestPostgresTMDBRecentPartialCardEligible(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	partial, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Person", Biography: "Primary", MetadataStatus: "partial: missing portrait and birthday"})
	if err != nil {
		t.Fatal(err)
	}
	complete, err := a.saveActress(ctx, Actress{TMDbID: "124", Name: "Complete", Biography: "Primary complete", Profile: "portrait", Birthday: "2000-01-01", PlaceOfBirth: "Place", MetadataStatus: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	var unnecessary atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/people/123":
			w.Write([]byte(`{"id":123,"name":"Person","biography":"Primary"}`))
		case "/people/124", "/person/124":
			unnecessary.Add(1)
			w.Write([]byte(`{"id":124,"name":"Complete"}`))
		case "/person/123":
			w.Write([]byte(`{"id":123,"name":"Person","biography":"Secondary","profile_path":"/portrait.jpg","birthday":"2000-01-01","place_of_birth":"Place"}`))
		case "/person/123/combined_credits":
			w.Write([]byte(`{"id":123,"cast":[]}`))
		default:
			w.Write([]byte(`{"items":[]}`))
		}
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	a.cfg.MDBListBaseURL = srv.URL
	// A fresh complete credit cache must not trigger biography or discovery traffic.
	cache := tmdbCreditCache{Fetched: time.Now(), Attempted: time.Now(), Complete: true}
	raw, _ := json.Marshal(cache)
	if _, err = a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('tmdb:actor:124',$1)`, raw); err != nil {
		t.Fatal(err)
	}
	s := defaultSettings()
	s.MDBList.APIKey = "mdb"
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "key"
	if err = a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err = a.syncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := a.getActress(ctx, partial.ID)
	if err != nil || got.Profile == "" || got.Birthday == "" || got.Biography != "Primary" || !strings.HasPrefix(got.MetadataStatus, "ok:") {
		t.Fatalf("recent partial not supplemented %+v %v", got, err)
	}
	got, err = a.getActress(ctx, complete.ID)
	if err != nil || got.Biography != "Primary complete" || unnecessary.Load() != 0 {
		t.Fatal("fresh complete card fetched unnecessarily", unnecessary.Load(), err)
	}
}

func TestTMDBRequestSafety(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/secret-key", 302)
		case "/large":
			w.Write([]byte(strings.Repeat("x", (8<<20)+1)))
		default:
			w.Write([]byte(`{"success":false,"status_message":"secret-key"}`))
		}
	}))
	defer srv.Close()
	a := &App{cfg: Config{TMDBBaseURL: srv.URL}}
	s := Settings{}
	if _, err := a.tmdbGet(context.Background(), s, "/authentication"); err == nil || calls.Load() != 0 {
		t.Fatal("disabled source called")
	}
	s.TMDB.Enabled = true
	if _, err := a.tmdbGet(context.Background(), s, "/authentication"); err == nil || calls.Load() != 0 {
		t.Fatal("missing key called")
	}
	s.TMDB.APIKey = "secret-key"
	for _, path := range []string{"/redirect", "/large", "/failure"} {
		if _, err := a.tmdbGet(context.Background(), s, path); err == nil || strings.Contains(err.Error(), "secret-key") {
			t.Fatal("unsafe result", err)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("followed redirect", calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.tmdbGet(ctx, s, "/authentication"); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestPostgresTMDBMovieShowMDBBatch(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	_, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Person"})
	if err != nil {
		t.Fatal(err)
	}
	var batches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/person/123":
			w.Write([]byte(`{"id":123,"name":"Person"}`))
		case "/person/123/combined_credits":
			w.Write([]byte(`{"id":123,"cast":[{"id":42,"media_type":"movie","title":"DO NOT USE"},{"id":43,"media_type":"tv","name":"DO NOT USE"}],"crew":[{"id":99,"media_type":"movie"}]}`))
		case "/tmdb/movie":
			batches.Add(1)
			w.Write([]byte(`[{"ids":{"tmdb":42},"type":"movie","title":"Primary movie"}]`))
		case "/tmdb/show":
			batches.Add(1)
			w.Write([]byte(`[{"ids":{"tmdb":43},"type":"show","title":"Primary show"}]`))
		default:
			t.Error("unexpected request", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	a.cfg.MDBListBaseURL = srv.URL
	s := Settings{}
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "key"
	s.MDBList.APIKey = "mdb"
	pending, err := a.syncTMDBCredits(ctx, s)
	if err != nil || pending != 0 || batches.Load() != 2 {
		t.Fatal("discovery", pending, batches.Load(), err)
	}
	rs, err := a.releasesForPerson(ctx, "123")
	if err != nil || len(rs) != 2 {
		t.Fatal("cast matches", rs, err)
	}
	for _, r := range rs {
		if !strings.HasPrefix(r.Title, "Primary") {
			t.Fatal("not primary metadata", r.Title)
		}
	}
}

func TestPostgresTMDBQuarantineHidesCachedPersonLinks(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if _, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Wrong", MetadataStatus: "unavailable: identity mismatch"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveReleaseSource(ctx, Release{TMDbID: "42", Title: "Title", Type: "movie"}, []string{"123"}, "tmdb:credits"); err != nil {
		t.Fatal(err)
	}
	rs, err := a.releasesForPerson(ctx, "123")
	if err != nil || len(rs) != 0 {
		t.Fatal("identity quarantine leaked person releases", len(rs), err)
	}
}

func TestPostgresTMDBPerPersonFailureStatus(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	for _, id := range []string{"123", "124"} {
		if _, err := a.saveActress(ctx, Actress{TMDbID: id, Name: "Person"}); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/person/123") {
			w.WriteHeader(429)
			return
		}
		if strings.HasSuffix(r.URL.Path, "combined_credits") {
			w.Write([]byte(`{"id":124,"cast":[]}`))
			return
		}
		w.Write([]byte(`{"id":124,"name":"Person"}`))
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	s := Settings{}
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "key"
	pending, err := a.syncTMDBCredits(ctx, s)
	if err != nil || pending != 1 {
		t.Fatal("failure stopped other people", pending, err)
	}
	var raw []byte
	if err = a.db.QueryRow(ctx, `SELECT value FROM app_state WHERE key='tmdb:actor:123'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var c tmdbCreditCache
	if json.Unmarshal(raw, &c) != nil || c.Error == "" || c.Complete {
		t.Fatal("failure not persisted truthfully", string(raw))
	}
}

// Synthetic providers only; never read configured provider credentials.
func tmdbReviewFixture(t *testing.T, a *App, title string) Settings {
	t.Helper()
	ctx := context.Background()
	if _, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Person", Monitored: true, AutoAdd: true, Notify: true}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/person/123":
			fmt.Fprint(w, `{"id":123,"name":"Person"}`)
		case "/person/123/combined_credits":
			fmt.Fprint(w, `{"id":123,"cast":[{"id":42,"media_type":"movie"}]}`)
		case "/tmdb/movie":
			fmt.Fprint(w, title)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	a.cfg.TMDBBaseURL, a.cfg.MDBListBaseURL = srv.URL, srv.URL
	s := defaultSettings()
	s.TMDB.Enabled, s.TMDB.APIKey, s.MDBList.APIKey = true, "synthetic", "synthetic"
	return s
}

func TestPostgresTMDBHistoricalBaselineAtomic(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	s := tmdbReviewFixture(t, a, `[{"ids":{"tmdb":42},"type":"movie","title":"Historical","released":"2000-01-01","cast":[{"id":123}]}]`)
	// A baseline write failure is the crash/cancellation boundary: none of the
	// release, ordinary cast or supplemental cast may have committed first.
	_, err := a.db.Exec(ctx, `CREATE FUNCTION tmdb_review_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'baseline interrupted'; END $$;
CREATE TRIGGER tmdb_review_fail BEFORE INSERT ON actions FOR EACH ROW EXECUTE FUNCTION tmdb_review_fail()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.db.Exec(ctx, `DROP TRIGGER IF EXISTS tmdb_review_fail ON actions; DROP FUNCTION IF EXISTS tmdb_review_fail()`)
	})
	if _, err = a.syncTMDBCredits(ctx, s); err == nil {
		t.Fatal("expected baseline failure")
	}
	var n int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM releases`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("historical release committed before baseline: %d %v", n, err)
	}
	for _, table := range []string{"release_cast", "release_people", "actions"} {
		if err = a.db.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partial %s: %d %v", table, n, err)
		}
	}
	if _, err = a.db.Exec(ctx, `DROP TRIGGER tmdb_review_fail ON actions; DROP FUNCTION tmdb_review_fail()`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.syncTMDBCredits(ctx, s); err != nil {
		t.Fatal(err)
	}
	// Restart with supplemental discovery disabled; baseline must already be durable.
	b, err := New(ctx, a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	s.TMDB.Enabled, s.TMDB.APIKey = false, ""
	if _, err = b.syncTMDBCredits(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err = b.automate(ctx, s, false); err != nil {
		t.Fatal(err)
	}
	if err = b.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE status='baseline' AND event_key='released'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("durable baseline: %d %v", n, err)
	}
	if err = b.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE status<>'baseline'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("restart queued historical action: %d %v", n, err)
	}
}

func TestPostgresTMDBCancellationBeforeBaselineRollsBack(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	s := tmdbReviewFixture(t, a, `[{"ids":{"tmdb":42},"type":"movie","title":"Historical","released":"2000-01-01","cast":[{"id":123}]}]`)
	lock, err := a.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err = lock.Exec(ctx, `LOCK TABLE actions IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	work, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { defer close(done); _, err := a.syncTMDBCredits(work, s); done <- err }()
	defer func() {
		cancel()
		lock.Rollback(ctx)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if err = a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%INSERT INTO actions%')`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("worker did not reach baseline boundary")
	}
	var n int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM releases`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("eligibility window before baseline commit: %d %v", n, err)
	}
	cancel()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("ignored canceled baseline")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled worker stuck")
	}
	if err = lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"releases", "release_cast", "release_people", "actions"} {
		if err = a.db.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("cancellation left %s: %d %v", table, n, err)
		}
	}
}

func TestPostgresTMDBPrimaryEligibilityPreserved(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	s := tmdbReviewFixture(t, a, `[]`)
	r, err := a.saveRelease(ctx, Release{Type: "movie", TMDbID: "42", Title: "Primary", ReleaseDate: time.Now().UTC().Format("2006-01-02"), Description: "Rich primary metadata"}, []string{"123"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(ctx, `UPDATE releases SET monitoring_override=false WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.syncTMDBCredits(ctx, s); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE release_id=$1`, r.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("primary autoeligibility suppressed: %d %v", n, err)
	}
	got, err := a.getRelease(ctx, r.ID)
	if err != nil || got.Description != r.Description || got.Monitored || got.MonitoringOrigin != "manual" {
		t.Fatalf("primary metadata/override changed: %+v %v", got, err)
	}
	if _, err = a.db.Exec(ctx, `UPDATE releases SET monitoring_override=true WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.automate(ctx, s, false); err != nil {
		t.Fatal(err)
	}
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE release_id=$1 AND status<>'baseline'`, r.ID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("primary release not eligible: %d %v", n, err)
	}
}

func TestPostgresTMDBUpcomingBaselineOnlyCurrentEvent(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	s := tmdbReviewFixture(t, a, fmt.Sprintf(`[{"ids":{"tmdb":42},"type":"movie","title":"Upcoming","released":%q}]`, time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02")))
	if _, err := a.syncTMDBCredits(ctx, s); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE event_key<>'upcoming'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("future event blanket-baselined: %d %v", n, err)
	}
	r, err := a.getRelease(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	r.ReleaseDate = time.Now().UTC().Format("2006-01-02")
	if _, err = a.saveRelease(ctx, r, nil); err != nil {
		t.Fatal(err)
	}
	if err = a.automate(ctx, s, false); err != nil {
		t.Fatal(err)
	}
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE event_key='released' AND status<>'baseline'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("genuine release ineligible: %d %v", n, err)
	}
}

func TestTMDBCreditBatchRejectsUnexpectedRawIdentities(t *testing.T) {
	valid := json.RawMessage(`{"ids":{"tmdb":42},"type":"movie","title":"Requested"}`)
	for name, raw := range map[string]string{
		"extra":                 `{"ids":{"tmdb":99},"type":"movie","title":"Extra","cast":[{"id":123}]}`,
		"wrong kind":            `{"ids":{"tmdb":43},"type":"show","title":"Wrong kind"}`,
		"missing kind":          `{"ids":{"tmdb":43},"title":"Missing kind"}`,
		"noncanonical identity": `{"ids":{"tmdb":"43"},"type":"movie","title":"String ID"}`,
		"duplicate":             string(valid),
		"conflicting aliases":   `{"ids":{"tmdb":43},"tmdbid":99,"type":"movie","title":"Conflicting identity"}`,
	} {
		t.Run(name, func(t *testing.T) {
			// Direct application-boundary fixture deliberately bypasses the already
			// hardened HTTP adapter, proving validation precedes any possible save.
			releases, cast, err := normalizeTMDBCreditBatch([]json.RawMessage{valid, json.RawMessage(raw)}, "movie", []string{"42", "43"})
			if err == nil || len(releases) != 0 || len(cast) != 0 {
				t.Fatalf("accepted invalid batch: %v %v %v", releases, cast, err)
			}
		})
	}
}

func TestTMDBCreditBatchAllowsReorderingAndMissingTitles(t *testing.T) {
	raws := []json.RawMessage{json.RawMessage(`{"ids":{"tmdb":43},"type":"movie","title":"Second"}`), json.RawMessage(`{"ids":{"tmdb":42},"type":"movie","title":"First"}`)}
	releases, _, err := normalizeTMDBCreditBatch(raws, "movie", []string{"42", "43", "44"})
	if err != nil || len(releases) != 2 || releases["42"].Title != "First" || releases["43"].Title != "Second" {
		t.Fatalf("valid batch: %v %v", releases, err)
	}
}

func TestPostgresTMDBBatchExtraWatchedTitleRejectedBeforeSave(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	s := tmdbReviewFixture(t, a, `[{"ids":{"tmdb":42},"type":"movie","title":"Requested","cast":[{"id":123}]},{"ids":{"tmdb":99},"type":"movie","title":"Outside budget","cast":[{"id":123}]}]`)
	pending, err := a.syncTMDBCredits(ctx, s)
	if err != nil || pending != 1 {
		t.Fatalf("bad batch continuation: %d %v", pending, err)
	}
	for _, table := range []string{"releases", "release_cast", "release_people", "actions"} {
		var n int
		if err = a.db.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("batch wrote %s outside validation/budget: %d %v", table, n, err)
		}
	}
}

func TestTMDBAuthentication(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
		ok     bool
	}{{`{"success":true}`, 200, true}, {`{"success":false,"status_message":"secret-key"}`, 200, false}, {`{"success":true}`, 429, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/authentication" || r.URL.Query().Get("api_key") != "secret-key" {
				t.Error("wrong auth request", r.URL.Path)
			}
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		}))
		a := &App{cfg: Config{TMDBBaseURL: srv.URL}}
		s := Settings{}
		s.TMDB.Enabled = true
		s.TMDB.APIKey = "secret-key"
		err := a.testTMDB(context.Background(), s)
		if (err == nil) != tc.ok {
			t.Errorf("auth result %v", err)
		}
		if err != nil && strings.Contains(err.Error(), "secret-key") {
			t.Fatal("credential leaked")
		}
		srv.Close()
	}
}
