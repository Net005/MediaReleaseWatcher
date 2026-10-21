package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"media-release-watcher/internal/integrations"
)

func TestArrCoverageResolvesExternalIDsBeforeMatching(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	tmdbCalls, arrCalls := 0, 0
	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tmdbCalls++
		if r.Method != "GET" || r.URL.Path != "/tv/1399/external_ids" {
			t.Errorf("unexpected lookup %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"id":1399,"tvdb_id":121361,"imdb_id":null}`)
	}))
	defer tmdb.Close()
	a.cfg.TMDBBaseURL = tmdb.URL
	arr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrCalls++
		if r.Method != "GET" || r.URL.Path != "/api/v3/series" {
			t.Errorf("unsafe manager request %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `[{"id":7,"tvdbId":121361}]`)
	}))
	defer arr.Close()
	s := defaultSettings()
	s.Sonarr = ArrSettings{URL: arr.URL, APIKey: "synthetic"}
	s.TMDB = TMDBSettings{Enabled: true, APIKey: "synthetic"}
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	r, err := a.saveRelease(ctx, Release{Type: "show", TMDbID: "1399", Title: "Synthetic"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.syncArr(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := a.getRelease(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TVDbID != "121361" || got.Acquisition["sonarr_status"] != "present" || tmdbCalls != 1 || arrCalls != 1 {
		t.Fatalf("unresolved presence %+v calls=%d/%d", got, tmdbCalls, arrCalls)
	}
}

func TestArrCoverageAmbiguousAndConflictingRemainUnknown(t *testing.T) {
	for _, tc := range []struct{ name, inventory string }{
		{"ambiguous", `[{"id":1,"tvdbId":100},{"id":2,"tvdbId":100}]`},
		{"conflicting", `[{"id":1,"tvdbId":100,"imdbId":"tt0000200"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			ctx := context.Background()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("write")
				}
				fmt.Fprint(w, tc.inventory)
			}))
			defer srv.Close()
			s := defaultSettings()
			s.Sonarr = ArrSettings{URL: srv.URL, APIKey: "synthetic"}
			if err := a.saveSettings(ctx, s); err != nil {
				t.Fatal(err)
			}
			r, _, err := normalizeRelease(json.RawMessage(`{"title":"Synthetic","type":"show","tmdbid":42,"tvdbid":100,"imdb_id":"tt0000100"}`), "show")
			if err != nil {
				t.Fatal(err)
			}
			r, err = a.saveRelease(ctx, r, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = a.syncArr(ctx)
			if status, _ := presenceSyncOutcome(err); status != "warning" {
				t.Fatalf("coverage classified fatal: %v", err)
			}
			got, e := a.getRelease(ctx, r.ID)
			if e != nil {
				t.Fatal(e)
			}
			if got.Acquisition["sonarr_status"] != "unknown" {
				t.Fatal(got.Acquisition)
			}
			if status, _ := presenceSyncOutcome(errors.Join(err, errors.New("fatal"))); status != "error" {
				t.Fatal("joined fatal downgraded")
			}
		})
	}
}

func TestArrCoverageMissingIdentityIsPartialReadOnly(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	calls := 0
	broken := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/series" {
			t.Errorf("unsafe request %s %s", r.Method, r.URL.Path)
		}
		if broken {
			http.Error(w, "private upstream failure", 502)
			return
		}
		fmt.Fprint(w, `[{"id":7,"tvdbId":100,"title":"Missing native identity","statistics":{"episodeFileCount":2}}]`)
	}))
	defer srv.Close()
	s := defaultSettings()
	s.Sonarr = ArrSettings{URL: srv.URL, APIKey: "synthetic"}
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	var releases []Release
	for _, raw := range []string{
		`{"title":"Missing native identity","type":"show","tmdbid":100}`,
		`{"title":"Exact","type":"show","tmdbid":101,"tvdbid":100}`,
		`{"title":"Absent","type":"show","tmdbid":102,"tvdbid":200}`,
	} {
		r, _, err := normalizeRelease(json.RawMessage(raw), "show")
		if err != nil {
			t.Fatal(err)
		}
		r, err = a.saveRelease(ctx, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, r)
	}
	// A fresh known read must clear a previous generic failure.
	if err := a.saveArrPresence(ctx, releases[1], "sonarr", integrations.ArrPresence{}, errors.New("old generic error")); err != nil {
		t.Fatal(err)
	}
	err := a.syncArr(ctx)
	status, message := presenceSyncOutcome(err)
	if err == nil || status != "warning" || !strings.Contains(message, "1 present, 1 absent, 1 unknown") {
		t.Fatalf("want standalone coverage warning, got %s %q %v", status, message, err)
	}
	if calls != 1 {
		t.Fatalf("inventory calls=%d", calls)
	}
	read := func(i int) map[string]string {
		t.Helper()
		r, e := a.getRelease(ctx, releases[i].ID)
		if e != nil {
			t.Fatal(e)
		}
		return r.Acquisition
	}
	if got := read(0); got["sonarr_status"] != "unknown" || got["sonarr_last_check_status"] != "warning" || got["sonarr_checked_at"] != "" || got["sonarr_error"] != "" || got["sonarr_warning"] == "" {
		t.Fatalf("missing identity falsely verified: %v", got)
	}
	if got := read(1); got["sonarr_status"] != "present" || got["sonarr_error"] != "" {
		t.Fatalf("exact match %v", got)
	}
	if got := read(2); got["sonarr_status"] != "absent" {
		t.Fatalf("absent %v", got)
	}
	if err := a.saveArrPresence(ctx, releases[0], "sonarr", integrations.ArrPresence{ID: 99, Ready: true}, nil); err != nil {
		t.Fatal(err)
	}
	prior := read(0)
	if status, _ := presenceSyncOutcome(a.syncArr(ctx)); status != "warning" {
		t.Fatal(status)
	}
	got := read(0)
	if got["sonarr_status"] != "present" || got["sonarr_checked_at"] != prior["sonarr_checked_at"] || got["sonarr_id"] != "99" || got["sonarr_last_check_status"] != "warning" {
		t.Fatalf("cached evidence lost %v", got)
	}
	broken = true
	if status, _ := presenceSyncOutcome(a.syncArr(ctx)); status != "error" {
		t.Fatalf("transport downgraded: %s", status)
	}
}
