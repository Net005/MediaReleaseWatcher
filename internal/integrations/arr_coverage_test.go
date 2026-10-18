package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArrInventorySonarrCompleteCoverageIsExplicitAndSeparateFromReady(t *testing.T) {
	for _, tc := range []struct {
		name, statistics string
		wantCount        *int
		wantFiles        int
		wantComplete     bool
	}{
		{"complete", `{"episodeFileCount":2,"episodeCount":2}`, coverageCount(2), 2, true},
		{"more files than episodes", `{"episodeFileCount":3,"episodeCount":2}`, coverageCount(2), 3, true},
		{"partial", `{"episodeFileCount":1,"episodeCount":2}`, coverageCount(2), 1, false},
		{"omitted", `{"episodeFileCount":1}`, nil, 1, false},
		{"null", `{"episodeFileCount":1,"episodeCount":null}`, nil, 1, false},
		{"zero", `{"episodeFileCount":1,"episodeCount":0}`, coverageCount(0), 1, false},
		{"negative", `{"episodeFileCount":1,"episodeCount":-1}`, coverageCount(-1), 1, false},
		{"total is not coverage denominator", `{"episodeFileCount":1,"totalEpisodeCount":1}`, nil, 1, false},
		{"future episodes do not replace known count", `{"episodeFileCount":2,"episodeCount":2,"totalEpisodeCount":10}`, coverageCount(2), 2, true},
		{"no files", `{"episodeFileCount":0,"episodeCount":2}`, coverageCount(2), 0, false},
		{"empty", `{}`, nil, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v3/series" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				fmt.Fprintf(w, `[{"id":7,"tvdbId":142,"statistics":%s}]`, tc.statistics)
			}))
			defer srv.Close()
			c := NewClient(Config{SonarrURL: srv.URL, SonarrAPIKey: "synthetic-coverage-key"})
			inventory, err := c.ArrInventory(context.Background(), "sonarr")
			if err != nil {
				t.Fatal(err)
			}
			count := inventory.items[0].Statistics.EpisodeCount
			if (count == nil) != (tc.wantCount == nil) || count != nil && *count != *tc.wantCount {
				t.Fatalf("episodeCount omission/value lost: got %v, want %v", count, tc.wantCount)
			}
			presence, err := inventory.Match(Media{Type: "show", TVDbID: 142})
			if err != nil || presence.ID != 7 || presence.Ready != (tc.wantFiles > 0) || presence.FileCount != tc.wantFiles || presence.CompleteCoverage != tc.wantComplete {
				t.Fatalf("presence=%+v err=%v; files=%d complete=%t", presence, err, tc.wantFiles, tc.wantComplete)
			}
			raw, err := json.Marshal(presence)
			if err != nil || strings.Contains(strings.ToLower(string(raw)), "coverage") {
				t.Fatalf("transient coverage leaked into serialized UI state: %s %v", raw, err)
			}
		})
	}
}

func coverageCount(v int) *int { return &v }
