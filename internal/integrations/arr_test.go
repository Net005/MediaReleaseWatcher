package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type arrFixture struct {
	server           *httptest.Server
	kind             string
	term             string
	media            Media
	options          AddOptions
	existing         []map[string]any
	candidates       []map[string]any
	lookupStatus     int
	lookupRaw        string
	lookup, verified map[string]any
	posted           map[string]any
	writes           atomic.Int32
	reads            atomic.Int32
}

func newArrFixture(t *testing.T, kind string) *arrFixture {
	t.Helper()
	f := &arrFixture{kind: kind, existing: []map[string]any{}, options: AddOptions{QualityProfileID: 3, RootFolderPath: "/library", Monitored: true, Tags: []int{2}, MinimumAvailability: "released"}}
	f.media = Media{Type: "movie", TMDbID: 42, IMDbID: "tt1234567"}
	resource := "movie"
	term := "tmdb:42"
	f.lookup = map[string]any{"title": "Synthetic Movie", "titleSlug": "synthetic-movie-42", "tmdbId": float64(42), "imdbId": "tt1234567", "year": float64(2025), "images": []any{}, "originalLanguage": map[string]any{"id": float64(1), "name": "English"}}
	if kind == "sonarr" {
		f.media = Media{Type: "show", TVDbID: 42, IMDbID: "tt1234567"}
		resource = "series"
		term = "tvdb:42"
		f.lookup = map[string]any{"title": "Synthetic Series", "titleSlug": "synthetic-series-42", "tvdbId": float64(42), "imdbId": "tt1234567", "year": float64(2025), "images": []any{}, "seriesType": "standard", "seasons": []any{map[string]any{"seasonNumber": float64(1), "monitored": false}}}
	}
	f.verified = map[string]any{"id": float64(71), "tmdbId": float64(42), "tvdbId": float64(42), "imdbId": "tt1234567", "titleSlug": f.lookup["titleSlug"], "qualityProfileId": float64(3), "path": "/library/Synthetic", "monitored": true, "tags": []any{float64(2)}, "minimumAvailability": "released", "seasons": []any{map[string]any{"seasonNumber": float64(1), "monitored": true}}}
	f.term = term
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "synthetic-arr-secret" {
			t.Error("missing X-Api-Key")
		}
		if r.URL.Query().Get("apikey") != "" {
			t.Error("API key leaked into query")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /base/api/v3/rootfolder":
			json.NewEncoder(w).Encode([]RootFolder{{ID: 1, Path: "/library"}})
		case "GET /base/api/v3/qualityprofile":
			json.NewEncoder(w).Encode([]QualityProfile{{ID: 3, Name: "Synthetic HD"}})
		case "GET /base/api/v3/" + resource:
			json.NewEncoder(w).Encode(f.existing)
		case "GET /base/api/v3/" + resource + "/lookup":
			if r.URL.Query().Get("term") != f.term {
				t.Errorf("term: %q", r.URL.Query().Get("term"))
			}
			if f.lookupStatus != 0 {
				w.WriteHeader(f.lookupStatus)
			}
			if f.lookupRaw != "" {
				w.Write([]byte(f.lookupRaw))
				return
			}
			if f.candidates != nil {
				json.NewEncoder(w).Encode(f.candidates)
			} else {
				json.NewEncoder(w).Encode([]map[string]any{f.lookup})
			}
		case "POST /base/api/v3/" + resource:
			f.writes.Add(1)
			if err := json.NewDecoder(r.Body).Decode(&f.posted); err != nil {
				t.Error(err)
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 71})
		case "GET /base/api/v3/" + resource + "/71":
			f.reads.Add(1)
			json.NewEncoder(w).Encode(f.verified)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 404)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *arrFixture) client() *Client {
	return NewClient(Config{SonarrURL: f.server.URL + "/base", SonarrAPIKey: "synthetic-arr-secret", RadarrURL: f.server.URL + "/base", RadarrAPIKey: "synthetic-arr-secret"})
}

func TestArrOptionsContract(t *testing.T) {
	for _, kind := range []string{"sonarr", "radarr"} {
		t.Run(kind, func(t *testing.T) {
			f := newArrFixture(t, kind)
			got, err := f.client().ArrOptions(context.Background(), kind)
			if err != nil || len(got.RootFolders) != 1 || len(got.QualityProfiles) != 1 {
				t.Fatalf("options: %+v %v", got, err)
			}
		})
	}
}

func TestArrAddLookupExplicitOptionsAndExactReadback(t *testing.T) {
	for _, kind := range []string{"sonarr", "radarr"} {
		for _, search := range []bool{false, true} {
			name := kind + "-no-search"
			if search {
				name = kind + "-search"
			}
			t.Run(name, func(t *testing.T) {
				f := newArrFixture(t, kind)
				f.options.Search = search
				got, err := f.client().Add(context.Background(), kind, f.media, f.options)
				if err != nil || got.ID != 71 || got.AlreadyExists || !strings.HasPrefix(got.URL, f.server.URL+"/base/") {
					t.Fatalf("add: %+v %v", got, err)
				}
				if f.writes.Load() != 1 || f.reads.Load() != 1 {
					t.Fatal("add was not posted and read back exactly once")
				}
				p := f.posted
				for k, want := range map[string]any{"title": f.lookup["title"], "titleSlug": f.lookup["titleSlug"], "qualityProfileId": float64(3), "rootFolderPath": "/library", "monitored": true, "tags": []any{float64(2)}} {
					if !reflect.DeepEqual(p[k], want) {
						t.Errorf("payload %s: got %#v, want %#v", k, p[k], want)
					}
				}
				if _, ok := p["id"]; ok {
					t.Error("forwarded lookup ID")
				}
				options, ok := p["addOptions"].(map[string]any)
				if !ok {
					t.Fatal("addOptions missing")
				}
				if kind == "radarr" {
					if options["searchForMovie"] != search || p["minimumAvailability"] != "released" || p["originalLanguage"] == nil {
						t.Errorf("radarr add: %#v", p)
					}
				} else {
					if options["searchForMissingEpisodes"] != search || options["searchForCutoffUnmetEpisodes"] != false || p["seasonFolder"] != true {
						t.Errorf("sonarr options: %#v", p)
					}
					seasons, ok := p["seasons"].([]any)
					if !ok || len(seasons) != 1 || seasons[0].(map[string]any)["monitored"] != true {
						t.Errorf("seasons: %#v", p["seasons"])
					}
				}
			})
		}
	}
}

func TestArrAlreadyExistsUsesExactReadbackWithoutPost(t *testing.T) {
	for _, kind := range []string{"sonarr", "radarr"} {
		t.Run(kind, func(t *testing.T) {
			f := newArrFixture(t, kind)
			f.existing = []map[string]any{f.verified}
			got, err := f.client().Add(context.Background(), kind, f.media, f.options)
			if err != nil || !got.AlreadyExists || got.ID != 71 || f.writes.Load() != 0 || f.reads.Load() != 1 {
				t.Fatalf("dedupe: %+v %v, writes=%d reads=%d", got, err, f.writes.Load(), f.reads.Load())
			}
		})
	}
}

func TestArrRejectsReadbackMismatch(t *testing.T) {
	for _, kind := range []string{"sonarr", "radarr"} {
		for _, field := range []string{"id", "identity", "profile", "root", "monitored", "tags", "availability", "seasons"} {
			if kind == "radarr" && field == "seasons" || kind == "sonarr" && field == "availability" {
				continue
			}
			t.Run(kind+"/"+field, func(t *testing.T) {
				f := newArrFixture(t, kind)
				switch field {
				case "id":
					f.verified["id"] = float64(72)
				case "identity":
					f.verified["tmdbId"] = float64(999)
					f.verified["tvdbId"] = float64(999)
				case "profile":
					f.verified["qualityProfileId"] = float64(9)
				case "root":
					f.verified["path"] = "/library-other/Synthetic"
				case "monitored":
					f.verified["monitored"] = false
				case "tags":
					f.verified["tags"] = []any{}
				case "availability":
					f.verified["minimumAvailability"] = "announced"
				case "seasons":
					f.verified["seasons"] = []any{map[string]any{"seasonNumber": float64(1), "monitored": false}}
				}
				got, err := f.client().Add(context.Background(), kind, f.media, f.options)
				if err == nil || got.ID != 0 || f.writes.Load() != 1 || f.reads.Load() != 1 {
					t.Fatalf("mismatch claimed success: %+v %v", got, err)
				}
			})
		}
	}
}

func TestArrInvalidInputsNeverWrite(t *testing.T) {
	for _, field := range []string{"kind", "type", "id", "imdb", "negative-id", "profile", "root", "tags", "availability", "lookup-id", "lookup-slug"} {
		t.Run(field, func(t *testing.T) {
			f := newArrFixture(t, "radarr")
			kind := "radarr"
			switch field {
			case "kind":
				kind = "other"
			case "type":
				f.media.Type = "show"
			case "id":
				f.media.TMDbID = 0
				f.media.IMDbID = ""
			case "imdb":
				f.media.IMDbID = "tt1234567/evil"
			case "negative-id":
				f.media.TVDbID = -1
			case "profile":
				f.options.QualityProfileID = 99
			case "root":
				f.options.RootFolderPath = "/arbitrary"
			case "tags":
				f.options.Tags = []int{-1}
			case "availability":
				f.options.MinimumAvailability = "unsupported"
			case "lookup-id":
				f.lookup["tmdbId"] = float64(9)
			case "lookup-slug":
				delete(f.lookup, "titleSlug")
			}
			got, err := f.client().Add(context.Background(), kind, f.media, f.options)
			if err == nil || got.ID != 0 || f.writes.Load() != 0 {
				t.Fatalf("unsafe write: %+v %v", got, err)
			}
		})
	}
}

func TestArrAddRejectsAmbiguousAndIncompleteEvidenceBeforeWrite(t *testing.T) {
	for _, kind := range []string{"radarr", "sonarr"} {
		for _, scenario := range []string{"existing-duplicate", "lookup-duplicate", "lookup-conflict", "inventory-null", "inventory-missing-identity"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				f := newArrFixture(t, kind)
				switch scenario {
				case "existing-duplicate":
					f.existing = []map[string]any{f.verified, f.verified}
				case "lookup-duplicate":
					f.candidates = []map[string]any{f.lookup, f.lookup}
				case "lookup-conflict":
					conflict := map[string]any{}
					for k, v := range f.lookup {
						conflict[k] = v
					}
					conflict["imdbId"] = "tt9999999"
					f.candidates = []map[string]any{f.lookup, conflict}
				case "inventory-null":
					f.existing = nil
				case "inventory-missing-identity":
					f.existing = []map[string]any{{"id": float64(70), "title": "Synthetic Movie"}}
				}
				got, e := f.client().Add(context.Background(), kind, f.media, f.options)
				if e == nil || got.ID != 0 || f.writes.Load() != 0 {
					t.Fatalf("unsafe add %+v %v writes=%d", got, e, f.writes.Load())
				}
			})
		}
	}
}

func TestArrLookupErrorsAndMissingIdentityNeverWrite(t *testing.T) {
	for _, kind := range []string{"radarr", "sonarr"} {
		for _, tc := range []struct {
			name, raw string
			status    int
		}{
			{"rate-limit", "private body", 429}, {"provider-error", "private body", 503}, {"invalid-json", "not JSON", 200}, {"null", "null", 200}, {"empty", "[]", 200}, {"missing-identity", `[{"title":"Synthetic","titleSlug":"synthetic"}]`, 200},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				f := newArrFixture(t, kind)
				f.lookupStatus = tc.status
				f.lookupRaw = tc.raw
				got, e := f.client().Add(context.Background(), kind, f.media, f.options)
				if e == nil || got.ID != 0 || f.writes.Load() != 0 || strings.Contains(e.Error(), "private body") {
					t.Fatalf("unsafe lookup %+v %v writes=%d", got, e, f.writes.Load())
				}
			})
		}
	}
}

func TestArrMissingConfig(t *testing.T) {
	for _, cfg := range []Config{{}, {RadarrURL: "http://127.0.0.1"}, {RadarrAPIKey: "secret"}, {RadarrURL: "http://user:password@localhost", RadarrAPIKey: "secret"}} {
		if _, err := NewClient(cfg).ArrOptions(context.Background(), "radarr"); err == nil || strings.Contains(err.Error(), "password") {
			t.Fatalf("configuration accepted or leaked: %v", err)
		}
	}
}
