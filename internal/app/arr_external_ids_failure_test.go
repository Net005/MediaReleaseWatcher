package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestArrExternalIDsPersistenceFailureIsFatal(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var lookups atomic.Int32
	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/tv/1399/external_ids" {
			t.Errorf("unexpected lookup %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"id":1399,"imdb_id":"tt0944947","tvdb_id":null}`)
	}))
	defer tmdb.Close()
	a.cfg.TMDBBaseURL = tmdb.URL
	arr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/series" {
			t.Errorf("unsafe manager request %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `[{"id":7,"imdbId":"tt0944947"}]`)
	}))
	defer arr.Close()
	s := defaultSettings()
	s.Sonarr = ArrSettings{URL: arr.URL, APIKey: "synthetic-arr"}
	s.TMDB = TMDBSettings{Enabled: true, APIKey: "synthetic-tmdb"}
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	r, err := a.saveRelease(ctx, Release{Type: "show", TMDbID: "1399", Title: "Synthetic"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Reject only identity changes: acquisition/presence writes must remain usable.
	_, err = a.db.Exec(ctx, `CREATE FUNCTION arr_test_reject_imdb_change() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic IMDb persistence failure'; END $$;
 CREATE TRIGGER arr_test_reject_imdb_change BEFORE UPDATE ON releases FOR EACH ROW WHEN (OLD.imdb_id IS DISTINCT FROM NEW.imdb_id) EXECUTE FUNCTION arr_test_reject_imdb_change()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := a.db.Exec(context.Background(), `DROP TRIGGER IF EXISTS arr_test_reject_imdb_change ON releases; DROP FUNCTION IF EXISTS arr_test_reject_imdb_change()`); err != nil {
			t.Error(err)
		}
	})
	err = a.syncArr(ctx)
	if lookups.Load() != 1 {
		t.Fatalf("expected verified TMDb lookup, got %d", lookups.Load())
	}
	if status, _ := presenceSyncOutcome(err); status != "error" {
		t.Errorf("identity database failure must be fatal, got %s: %v", status, err)
	}
	var externalErr *showExternalIDsError
	if !errors.As(err, &externalErr) || externalErr.Code != "persistence_failed" {
		t.Errorf("expected typed persistence_failed, got %v", err)
	}
	got, readErr := a.getRelease(ctx, r.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got.IMDbID != "" {
		t.Errorf("failed identity persisted: %s", got.IMDbID)
	}
	p := got.Acquisition
	if p["sonarr_status"] != "error" || p["sonarr_last_check_status"] != "error" || p["sonarr_error"] == "" || p["sonarr_warning"] != "" || p["sonarr_checked_at"] != "" {
		t.Errorf("database failure must persist error, never false absence or coverage warning: %v", p)
	}
}
