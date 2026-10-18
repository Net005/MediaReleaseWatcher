package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSchedulePostgresSearchSeriesCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, statistics string
		wantSearches     int32
	}{
		{"complete", `{"episodeFileCount":2,"episodeCount":2}`, 0},
		{"partial", `{"episodeFileCount":1,"episodeCount":2}`, 1},
		{"unknown", `{"episodeFileCount":1}`, 1},
		{"null count", `{"episodeFileCount":1,"episodeCount":null}`, 1},
		{"zero count", `{"episodeFileCount":1,"episodeCount":0}`, 1},
		{"negative count", `{"episodeFileCount":1,"episodeCount":-1}`, 1},
		{"only total count", `{"episodeFileCount":1,"totalEpisodeCount":1}`, 1},
		{"more files", `{"episodeFileCount":3,"episodeCount":2}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := schedulerApp(t)
			var searches atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/v3/series":
					fmt.Fprintf(w, `[{"id":7,"tvdbId":142,"monitored":true,"statistics":%s}]`, tc.statistics)
				case r.Method == "GET" && r.URL.Path == "/api/v3/series/7":
					fmt.Fprintf(w, `{"id":7,"tvdbId":142,"monitored":true,"statistics":%s}`, tc.statistics)
				case r.Method == "POST" && r.URL.Path == "/api/v3/command":
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					if len(payload) != 2 || payload["name"] != "SeriesSearch" || payload["seriesId"] != float64(7) {
						t.Errorf("search must target only exact series: %+v", payload)
					}
					searches.Add(1)
					fmt.Fprint(w, `{"id":101}`)
				case r.Method == "GET" && r.URL.Path == "/api/v3/command/101":
					fmt.Fprint(w, `{"id":101,"name":"SeriesSearch","body":{"name":"SeriesSearch","seriesId":7}}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer srv.Close()
			r := schedulerRelease(t, a, "show", 42, 142, true)
			// Distinct catalogue entries sharing an exact series still search once.
			schedulerRelease(t, a, "show", 43, 142, true)
			s := defaultSettings()
			s.Automation.Enabled = true
			s.Sonarr = ArrSettings{URL: srv.URL, APIKey: "synthetic-coverage-key", Enabled: true, Search: true}
			if err := a.saveSettings(a.ctx, s); err != nil {
				t.Fatal(err)
			}
			if err := a.syncArr(a.ctx); err != nil {
				t.Fatal(err)
			}
			got, err := a.getRelease(a.ctx, r.ID)
			if err != nil || got.Acquisition["sonarr_ready"] != "true" {
				t.Fatalf("has-files UI evidence changed: %+v %v", got.Acquisition, err)
			}
			if err := a.searchMissing(a.ctx, Task{Type: "search_missing", AllowDownloadSearch: true}); err != nil {
				t.Fatal(err)
			}
			if searches.Load() != tc.wantSearches {
				t.Fatalf("SeriesSearch POSTs=%d, want %d", searches.Load(), tc.wantSearches)
			}
		})
	}
}

func TestSchedulePostgresSeriesCoverageKeepsConsentAndIdentityGates(t *testing.T) {
	for _, statistics := range []string{`{"episodeFileCount":1,"episodeCount":2}`, `{"episodeFileCount":1}`} {
		for _, gate := range []string{"task", "automation", "integration", "search", "url", "key", "catalogue monitoring", "manager monitoring", "identity"} {
			t.Run(statistics+"/"+gate, func(t *testing.T) {
				a := schedulerApp(t)
				var searches atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == "GET" && r.URL.Path == "/api/v3/series":
						fmt.Fprintf(w, `[{"id":7,"tvdbId":142,"monitored":true,"statistics":%s}]`, statistics)
					case r.Method == "GET" && r.URL.Path == "/api/v3/series/7":
						tvdbID := 142
						if gate == "identity" {
							tvdbID = 143
						}
						fmt.Fprintf(w, `{"id":7,"tvdbId":%d,"monitored":%t,"statistics":%s}`, tvdbID, gate != "manager monitoring", statistics)
					case r.Method == "POST" && r.URL.Path == "/api/v3/command":
						searches.Add(1)
						w.WriteHeader(http.StatusBadRequest)
					default:
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusBadRequest)
					}
				}))
				defer srv.Close()
				schedulerRelease(t, a, "show", 42, 142, gate != "catalogue monitoring")
				s := defaultSettings()
				s.Automation.Enabled = gate != "automation"
				s.Sonarr = ArrSettings{URL: srv.URL, APIKey: "synthetic-coverage-key", Enabled: gate != "integration", Search: gate != "search"}
				if gate == "url" {
					s.Sonarr.URL = ""
				}
				if gate == "key" {
					s.Sonarr.APIKey = ""
				}
				if err := a.saveSettings(a.ctx, s); err != nil {
					t.Fatal(err)
				}
				err := a.searchMissing(a.ctx, Task{Type: "search_missing", AllowDownloadSearch: gate != "task"})
				if (err != nil) != (gate != "catalogue monitoring") || searches.Load() != 0 {
					t.Fatalf("gate %s: err=%v SeriesSearch POSTs=%d", gate, err, searches.Load())
				}
			})
		}
	}
}
