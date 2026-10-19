package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type monitorFixture struct {
	kind, resource, command string
	media                   Media
	options                 AddOptions
	item                    map[string]any
	put                     map[string]any
	puts, posts, gets       int
	putStatus               int
	readback                func(map[string]any)
	commandBad              bool
	readAt                  func(int, map[string]any) int
	commandStatus           int
}

func newMonitorFixture(t *testing.T, kind string) (*monitorFixture, *Client) {
	t.Helper()
	f := &monitorFixture{kind: kind, resource: "movie", command: "MoviesSearch", media: Media{Type: "movie", TMDbID: 42, IMDbID: "tt1234567"}, options: AddOptions{QualityProfileID: 3, RootFolderPath: "/library", Monitored: true, Search: true}}
	f.options.RestoreMonitoring = true
	f.item = map[string]any{"id": float64(71), "tmdbId": float64(42), "imdbId": "tt1234567", "titleSlug": "synthetic-42", "monitored": false, "qualityProfileId": float64(8), "rootFolderPath": "/keep", "path": "/keep/Synthetic", "tags": []any{float64(7)}, "minimumAvailability": "announced", "unknownField": map[string]any{"preserve": true}, "largeInteger": json.Number("9007199254740993")}
	if kind == "sonarr" {
		f.resource, f.command = "series", "SeriesSearch"
		f.media = Media{Type: "show", TVDbID: 42, IMDbID: "tt1234567"}
		f.item["tvdbId"] = float64(42)
		f.item["monitorNewItems"] = "none"
		f.item["seasons"] = []any{map[string]any{"seasonNumber": float64(0), "monitored": false, "statistics": map[string]any{"keep": true}}, map[string]any{"seasonNumber": float64(1), "monitored": false}, map[string]any{"seasonNumber": float64(2), "monitored": false}}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v3/rootfolder":
			json.NewEncoder(w).Encode([]RootFolder{{ID: 1, Path: "/library"}})
		case "GET /api/v3/qualityprofile":
			json.NewEncoder(w).Encode([]QualityProfile{{ID: 3, Name: "selected"}})
		case "GET /api/v3/" + f.resource:
			json.NewEncoder(w).Encode([]any{f.item})
		case "GET /api/v3/" + f.resource + "/71":
			f.gets++
			if f.readAt != nil {
				if status := f.readAt(f.gets, f.item); status != 0 {
					http.Error(w, "private remote body", status)
					return
				}
			}
			if f.puts > 0 && f.readback != nil {
				f.readback(f.item)
			}
			json.NewEncoder(w).Encode(f.item)
		case "PUT /api/v3/" + f.resource + "/71":
			f.puts++
			d := json.NewDecoder(r.Body)
			d.UseNumber()
			if err := d.Decode(&f.put); err != nil {
				t.Error(err)
			}
			if f.putStatus != 0 {
				http.Error(w, "private remote body", f.putStatus)
				return
			}
			// Decode again as ordinary JSON values to emulate the server's resource.
			raw, _ := json.Marshal(f.put)
			json.Unmarshal(raw, &f.item)
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(71)
		case "POST /api/v3/command":
			f.posts++
			if f.commandStatus != 0 {
				http.Error(w, "private remote body", f.commandStatus)
				return
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			want := map[string]any{"name": f.command, "movieIds": []any{float64(71)}}
			if kind == "sonarr" {
				want = map[string]any{"name": f.command, "seriesId": float64(71)}
			}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("unbounded command: %#v", body)
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 99})
		case "GET /api/v3/command/99":
			scope := 71
			if f.commandBad {
				scope = 72
			}
			body := map[string]any{"name": f.command, "movieIds": []int{scope}}
			if kind == "sonarr" {
				body = map[string]any{"name": f.command, "seriesId": scope}
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 99, "name": f.command, "status": "queued", "body": body})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 404)
		}
	}))
	t.Cleanup(srv.Close)
	return f, NewClient(Config{RadarrURL: srv.URL, RadarrAPIKey: "synthetic", SonarrURL: srv.URL, SonarrAPIKey: "synthetic"})
}

func TestArrManualRestoreFullyMonitoredAndNoSearch(t *testing.T) {
	for _, kind := range []string{"radarr", "sonarr"} {
		for _, search := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/search=%t", kind, search), func(t *testing.T) {
				f, c := newMonitorFixture(t, kind)
				f.item["monitored"] = true
				f.options.Search = search
				if kind == "sonarr" {
					for _, s := range f.item["seasons"].([]any) {
						s.(map[string]any)["monitored"] = true
					}
				}
				got, err := c.Add(context.Background(), kind, f.media, f.options)
				wantPosts := 0
				if search {
					wantPosts = 1
				}
				if err != nil || !got.AlreadyExists || got.MonitoredRestored || got.SearchStarted != search || f.puts != 0 || f.posts != wantPosts {
					t.Fatalf("result=%+v err=%v puts=%d posts=%d", got, err, f.puts, f.posts)
				}
			})
		}
	}
}

func TestArrManualRestoreNoSearchAndLegacyConsent(t *testing.T) {
	for _, kind := range []string{"radarr", "sonarr"} {
		for _, restore := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restore=%t", kind, restore), func(t *testing.T) {
				f, c := newMonitorFixture(t, kind)
				f.options.RestoreMonitoring = restore
				f.options.Search = !restore
				got, err := c.Add(context.Background(), kind, f.media, f.options)
				wantPuts := 0
				if restore {
					wantPuts = 1
				}
				if err != nil || !got.AlreadyExists || got.MonitoredRestored != restore || got.SearchStarted || f.puts != wantPuts || f.posts != 0 {
					t.Fatalf("result=%+v err=%v puts=%d posts=%d", got, err, f.puts, f.posts)
				}
			})
		}
	}
}

func TestArrManualRestoreUnknownStateNeverWrites(t *testing.T) {
	for _, kind := range []string{"radarr", "sonarr"} {
		scenarios := []string{"missing-monitored", "null-monitored", "initial-read-conflict", "initial-read-wrong-id"}
		if kind == "sonarr" {
			scenarios = append(scenarios, "missing-seasons", "null-seasons", "missing-season-monitored", "null-season-monitored", "missing-season-number", "duplicate-season", "negative-season")
		}
		for _, scenario := range scenarios {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				f, c := newMonitorFixture(t, kind)
				f.readAt = func(n int, item map[string]any) int {
					switch scenario {
					case "missing-monitored":
						delete(item, "monitored")
					case "null-monitored":
						item["monitored"] = nil
					case "initial-read-conflict":
						item["imdbId"] = "tt9999999"
					case "initial-read-wrong-id":
						item["id"] = float64(72)
					case "missing-seasons":
						delete(item, "seasons")
					case "null-seasons":
						item["seasons"] = nil
					case "missing-season-monitored":
						delete(item["seasons"].([]any)[0].(map[string]any), "monitored")
					case "null-season-monitored":
						item["seasons"].([]any)[0].(map[string]any)["monitored"] = nil
					case "missing-season-number":
						delete(item["seasons"].([]any)[0].(map[string]any), "seasonNumber")
					case "duplicate-season":
						item["seasons"].([]any)[1].(map[string]any)["seasonNumber"] = float64(0)
					case "negative-season":
						item["seasons"].([]any)[0].(map[string]any)["seasonNumber"] = float64(-1)
					}
					return 0
				}
				got, err := c.Add(context.Background(), kind, f.media, f.options)
				if err == nil || got.ID != 0 || got.MonitoredRestored || got.SearchStarted || f.puts != 0 || f.posts != 0 {
					t.Fatalf("unsafe result=%+v err=%v puts=%d posts=%d", got, err, f.puts, f.posts)
				}
			})
		}
	}
}

func TestArrManualRestoreFailedVerificationPreventsSearch(t *testing.T) {
	for _, kind := range []string{"radarr", "sonarr"} {
		scenarios := []string{"put-error", "put-conflict", "readback-error", "readback-id", "readback-native", "readback-imdb", "readback-unmonitored", "final-search-read-conflict"}
		if kind == "sonarr" {
			scenarios = append(scenarios, "readback-season-false", "readback-season-missing", "readback-season-number-missing", "readback-seasons-null", "final-search-read-season-false")
		}
		for _, scenario := range scenarios {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				f, c := newMonitorFixture(t, kind)
				if scenario == "put-error" {
					f.putStatus = 503
				}
				if scenario == "put-conflict" {
					f.putStatus = 409
				}
				f.readAt = func(n int, item map[string]any) int {
					if n < 2 {
						return 0
					}
					switch scenario {
					case "readback-error":
						return 503
					case "readback-id":
						item["id"] = float64(72)
					case "readback-native":
						item["tmdbId"] = float64(99)
						item["tvdbId"] = float64(99)
					case "readback-imdb":
						item["imdbId"] = "tt9999999"
					case "readback-unmonitored":
						item["monitored"] = false
					case "readback-season-false":
						item["seasons"].([]any)[0].(map[string]any)["monitored"] = false
					case "readback-season-missing":
						item["seasons"] = item["seasons"].([]any)[1:]
					case "readback-season-number-missing":
						delete(item["seasons"].([]any)[0].(map[string]any), "seasonNumber")
					case "readback-seasons-null":
						item["seasons"] = nil
					case "final-search-read-conflict":
						if n == 3 {
							item["imdbId"] = "tt9999999"
						}
					case "final-search-read-season-false":
						if n == 3 {
							item["seasons"].([]any)[0].(map[string]any)["monitored"] = false
						}
					}
					return 0
				}
				got, err := c.Add(context.Background(), kind, f.media, f.options)
				if err == nil || got.ID != 0 || got.SearchStarted || got.MonitoredRestored || f.puts != 1 || f.posts != 0 {
					t.Fatalf("unsafe result=%+v err=%v puts=%d posts=%d", got, err, f.puts, f.posts)
				}
				if strings.Contains(err.Error(), "private remote body") {
					t.Fatal("remote body leaked")
				}
			})
		}
	}
}

func TestArrManualRestoreUncertainSearchNeverClaimsSuccessOrRetries(t *testing.T) {
	for _, kind := range []string{"radarr", "sonarr"} {
		for _, scenario := range []string{"post-error", "command-scope"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				f, c := newMonitorFixture(t, kind)
				if scenario == "post-error" {
					f.commandStatus = 503
				} else {
					f.commandBad = true
				}
				got, err := c.Add(context.Background(), kind, f.media, f.options)
				if err == nil || got.ID != 0 || got.SearchStarted || got.MonitoredRestored || f.puts != 1 || f.posts != 1 {
					t.Fatalf("unsafe result=%+v err=%v puts=%d posts=%d", got, err, f.puts, f.posts)
				}
			})
		}
	}
}

func TestArrManualRestoreIMDbOnlyPinsNativeIdentity(t *testing.T) {
	for _, kind := range []string{"radarr", "sonarr"} {
		for _, finalRead := range []int{2, 3} {
			t.Run(fmt.Sprintf("%s/read=%d", kind, finalRead), func(t *testing.T) {
				f, c := newMonitorFixture(t, kind)
				f.media.TMDbID, f.media.TVDbID = 0, 0
				f.readAt = func(n int, item map[string]any) int {
					if n == finalRead {
						if kind == "radarr" {
							item["tmdbId"] = float64(999)
						} else {
							item["tvdbId"] = float64(999)
						}
					}
					return 0
				}
				got, err := c.Add(context.Background(), kind, f.media, f.options)
				if err == nil || got.ID != 0 || got.MonitoredRestored || got.SearchStarted || f.puts != 1 || f.posts != 0 {
					t.Fatalf("result=%+v err=%v puts=%d posts=%d", got, err, f.puts, f.posts)
				}
			})
		}
	}
}

func TestArrManualRestoreMovie(t *testing.T) {
	f, c := newMonitorFixture(t, "radarr")
	before, err := json.Marshal(f.item)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(before)))
	decoder.UseNumber()
	if err := decoder.Decode(&want); err != nil {
		t.Fatal(err)
	}
	want["monitored"] = true
	got, err := c.Add(context.Background(), f.kind, f.media, f.options)
	if err != nil || got.ID != 71 || !got.AlreadyExists || !got.MonitoredRestored || !got.SearchStarted || f.puts != 1 || f.posts != 1 {
		t.Fatalf("restore: %+v err=%v puts=%d posts=%d", got, err, f.puts, f.posts)
	}
	if !reflect.DeepEqual(f.put, want) {
		t.Fatalf("movie resource not preserved: got=%#v want=%#v", f.put, want)
	}
	if f.gets != 3 {
		t.Fatalf("expected initial, readback and final pre-search exact GETs; got %d", f.gets)
	}
}

func TestArrManualRestoreSeriesAllSeasonsPreservesResource(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unmonitored", true: "mixed"}[mixed], func(t *testing.T) {
			f, c := newMonitorFixture(t, "sonarr")
			if mixed {
				f.item["monitored"] = true
				f.item["seasons"].([]any)[1].(map[string]any)["monitored"] = true
			}
			before, _ := json.Marshal(f.item)
			got, err := c.Add(context.Background(), f.kind, f.media, f.options)
			if err != nil || !got.MonitoredRestored || !got.SearchStarted || !got.AlreadyExists || f.puts != 1 || f.posts != 1 {
				t.Fatalf("restore: %+v err=%v puts=%d posts=%d", got, err, f.puts, f.posts)
			}
			var want map[string]any
			d := json.NewDecoder(strings.NewReader(string(before)))
			d.UseNumber()
			d.Decode(&want)
			want["monitored"] = true
			for _, s := range want["seasons"].([]any) {
				s.(map[string]any)["monitored"] = true
			}
			if !reflect.DeepEqual(f.put, want) {
				t.Errorf("resource was not preserved\n got: %#v\nwant: %#v", f.put, want)
			}
		})
	}
}
