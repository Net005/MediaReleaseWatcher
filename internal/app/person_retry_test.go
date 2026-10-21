package app

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Minimal schema isolates these provider-boundary tests from unrelated startup
// migrations and never uses a database unless explicitly supplied by the runner.
func personRetryApp(t *testing.T) *App {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; PostgreSQL integration test")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("person_retry_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer admin.Close()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err = db.Exec(ctx, `CREATE TABLE app_state(key text PRIMARY KEY,value jsonb NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return &App{db: db}
}

func TestPostgresPersonRetryFreshPartial(t *testing.T) {
	a := personRetryApp(t)
	ctx := context.Background()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"id":123,"name":"Person"}`))
	}))
	defer srv.Close()
	a.cfg.MDBListBaseURL, a.cfg.TMDBBaseURL = srv.URL, srv.URL
	s := Settings{}
	s.MDBList.APIKey = "synthetic-mdb"
	s.TMDB.Enabled, s.TMDB.APIKey = true, "synthetic-tmdb"
	p := Actress{TMDbID: "123", Name: "Person", MetadataStatus: "partial: missing biography", UpdatedAt: time.Now()}
	got, attempted, err := a.refreshPersonBiography(ctx, s, p)
	if err != nil || !attempted || calls.Load() != 2 {
		t.Fatalf("first attempt=%v calls=%d err=%v", attempted, calls.Load(), err)
	}
	if got.Biography != "" || !strings.HasPrefix(got.MetadataStatus, "partial:") {
		t.Fatalf("missing fields claimed complete: %+v", got)
	}
	got.UpdatedAt = time.Now().Add(-30 * 24 * time.Hour)
	_, attempted, err = a.refreshPersonBiography(ctx, s, got)
	if err != nil || attempted || calls.Load() != 2 {
		t.Fatalf("repeat attempt=%v calls=%d err=%v", attempted, calls.Load(), err)
	}
}

func TestPostgresPersonRetryBackoffAndConfiguration(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			a := personRetryApp(t)
			ctx := context.Background()
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				if status == http.StatusOK {
					id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
					name := "Person"
					if id == "124" {
						name = "Other"
					}
					fmt.Fprintf(w, `{"id":%s,"name":%q}`, id, name)
				}
			}))
			defer srv.Close()
			a.cfg.MDBListBaseURL, a.cfg.TMDBBaseURL = srv.URL, srv.URL
			s := Settings{}
			s.MDBList.APIKey = "synthetic-mdb"
			p := Actress{TMDbID: "123", Name: "Person", MetadataStatus: "partial: missing biography", UpdatedAt: time.Now()}
			if _, attempted, err := a.refreshPersonBiography(ctx, s, p); err != nil || attempted || calls.Load() != 0 {
				t.Fatalf("disabled: %v %v calls=%d", attempted, err, calls.Load())
			}
			s.TMDB.Enabled, s.TMDB.APIKey = true, "synthetic-tmdb"
			p, attempted, err := a.refreshPersonBiography(ctx, s, p)
			requestsPerAttempt := calls.Load()
			if err != nil || !attempted || requestsPerAttempt < 2 {
				t.Fatalf("enabled: %v %v calls=%d", attempted, err, calls.Load())
			}
			s.TMDB.APIKey = "replacement-tmdb"
			p, attempted, err = a.refreshPersonBiography(ctx, s, p)
			if err != nil || !attempted || calls.Load() != 2*requestsPerAttempt {
				t.Fatalf("changed key: %v %v calls=%d", attempted, err, calls.Load())
			}
			var leaked bool
			if err = a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app_state WHERE key LIKE '%synthetic%' OR value::text LIKE '%synthetic%' OR key LIKE '%replacement%' OR value::text LIKE '%replacement%')`).Scan(&leaked); err != nil || leaked {
				t.Fatalf("credential persisted: %v %v", leaked, err)
			}
			age := func(d time.Duration) {
				t.Helper()
				_, err := a.db.Exec(ctx, `UPDATE app_state SET value=jsonb_build_object('attempted',$1::text)`, time.Now().Add(-d).UTC().Format(time.RFC3339Nano))
				if err != nil {
					t.Fatal(err)
				}
			}
			age(23 * time.Hour)
			// A restart and unrelated preference edits cannot reset metadata freshness.
			restarted := &App{db: a.db, cfg: a.cfg}
			p.UpdatedAt = time.Now()
			_, attempted, err = restarted.refreshPersonBiography(ctx, s, p)
			if err != nil || attempted || calls.Load() != 2*requestsPerAttempt {
				t.Fatalf("before backoff: %v %v calls=%d", attempted, err, calls.Load())
			}
			age(25 * time.Hour)
			p, attempted, err = restarted.refreshPersonBiography(ctx, s, p)
			if err != nil || !attempted || calls.Load() != 3*requestsPerAttempt {
				t.Fatalf("after backoff: %v %v calls=%d", attempted, err, calls.Load())
			}
			if p.Biography != "" || strings.HasPrefix(p.MetadataStatus, "ok") {
				t.Fatalf("empty upstream claimed complete: %+v", p)
			}
			p.TMDbID, p.Name = "124", "Other"
			_, attempted, err = restarted.refreshPersonBiography(ctx, s, p)
			if err != nil || !attempted || calls.Load() != 4*requestsPerAttempt {
				t.Fatalf("following person: %v %v calls=%d", attempted, err, calls.Load())
			}
		})
	}
}

func TestPostgresPersonRetryQuarantine(t *testing.T) {
	a := personRetryApp(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.NotFound(w, r) }))
	defer srv.Close()
	a.cfg.MDBListBaseURL, a.cfg.TMDBBaseURL = srv.URL, srv.URL
	s := Settings{}
	s.MDBList.APIKey = "synthetic-mdb"
	s.TMDB.Enabled, s.TMDB.APIKey = true, "synthetic-tmdb"
	p := Actress{TMDbID: "123", Name: "Literal", MetadataStatus: "unavailable: identity mismatch; manual review", UpdatedAt: time.Now().Add(-30 * 24 * time.Hour)}
	got, attempted, err := a.refreshPersonBiography(context.Background(), s, p)
	if err != nil || attempted || calls.Load() != 0 || got.MetadataStatus != p.MetadataStatus || got.Name != p.Name {
		t.Fatalf("quarantine changed: %+v %v %v calls=%d", got, attempted, err, calls.Load())
	}
}
