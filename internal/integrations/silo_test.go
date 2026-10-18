package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Library presence is an exact catalog identity match, not a disk-health check.
// No administrator, versions, probe, playback or session routes are needed.
func TestSiloCatalogPresenceUsesExactProviderIDsWithoutAdminAccess(t *testing.T) {
	for _, tc := range []struct {
		name, detail string
		media        Media
	}{
		{"tmdb-movie", `{"content_id":"local","type":"movie","tmdb_id":"42"}`, Media{Type: "movie", TMDbID: 42}},
		{"imdb-movie", `{"content_id":"local","type":"movie","imdb_id":"tt0000042"}`, Media{Type: "movie", IMDbID: "tt0000042"}},
		{"tvdb-show", `{"content_id":"local","type":"series","tvdb_id":"42"}`, Media{Type: "show", TVDbID: 42}},
		{"all-show", `{"content_id":"local","type":"series","tmdb_id":"9","imdb_id":"tt0000042","tvdb_id":"42"}`, Media{Type: "show", TMDbID: 9, IMDbID: "tt0000042", TVDbID: 42}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := map[string]int{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic-key" {
					t.Errorf("unsafe request %s", r.Method)
					w.WriteHeader(http.StatusForbidden)
					return
				}
				calls[r.URL.Path]++
				switch r.URL.Path {
				case "/api/v2/profiles":
					fmt.Fprint(w, `{"items":[{"id":"primary","is_primary":true}]}`)
				case "/api/v2/catalog":
					kind := "movie"
					if tc.media.Type == "show" {
						kind = "series"
					}
					fmt.Fprintf(w, `{"items":[{"content_id":"local","type":%q}],"page":{"has_more":false}}`, kind)
				case "/api/v2/catalog/items/local":
					fmt.Fprint(w, tc.detail)
				default:
					t.Errorf("unexpected route %s; catalog presence must not require administrator or playback access", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			c := NewClient(Config{SiloURL: srv.URL, SiloAPIKey: "synthetic-key"})
			for range 2 {
				got, err := c.SiloAvailability(context.Background(), tc.media)
				if err != nil || got.Status != "available" || got.URL != srv.URL+"/item/local" || got.CheckedAt.IsZero() || got.Error != "" {
					t.Fatalf("exact catalog match must be in Silo: %+v err=%v", got, err)
				}
			}
			if calls["/api/v2/catalog"] != 1 || calls["/api/v2/profiles"] != 1 || calls["/api/v2/catalog/items/local"] != 1 || len(calls) != 3 {
				t.Fatalf("completed inventory not reused: %+v", calls)
			}
		})
	}
}

func TestSiloMalformedCatalogTypeNeverBecomesAbsence(t *testing.T) {
	for _, kind := range []string{"", "invalid", "Movie", " movie ", "video"} {
		for _, paginated := range []bool{false, true} {
			t.Run(fmt.Sprintf("type=%q/paginated=%t", kind, paginated), func(t *testing.T) {
				catalogCalls := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet {
						t.Errorf("unexpected mutation %s", r.Method)
					}
					switch r.URL.Path {
					case "/api/v2/profiles":
						fmt.Fprint(w, `{"items":[{"id":"primary","is_primary":true}]}`)
					case "/api/v2/catalog":
						catalogCalls++
						if paginated && r.URL.Query().Get("cursor") == "" {
							fmt.Fprint(w, `{"items":[{"content_id":"good","type":"movie"}],"page":{"has_more":true,"next_cursor":"next"}}`)
						} else if kind == "" {
							// Exact reviewer reproducer: do not invent a fallback type.
							fmt.Fprint(w, `{"items":[{"content_id":"local"}],"page":{"has_more":false}}`)
						} else {
							fmt.Fprintf(w, `{"items":[{"content_id":"local","type":%q}],"page":{"has_more":false}}`, kind)
						}
					case "/api/v2/catalog/items/good":
						fmt.Fprint(w, `{"content_id":"good","type":"movie","tmdb_id":"99"}`)
					default:
						t.Errorf("unexpected route %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer srv.Close()
				c := NewClient(Config{SiloURL: srv.URL, SiloAPIKey: "synthetic-key"})
				for range 2 {
					got, err := c.SiloAvailability(context.Background(), Media{Type: "movie", TMDbID: 42})
					if err == nil || got.Status != "error" || got.URL != "" {
						t.Fatalf("malformed row became absence: %+v err=%v", got, err)
					}
					if c.siloCache != nil {
						t.Fatal("malformed catalog published as complete snapshot")
					}
				}
				wantCalls := 2
				if paginated {
					wantCalls = 4
				}
				if catalogCalls != wantCalls {
					t.Fatalf("catalog calls=%d want=%d", catalogCalls, wantCalls)
				}
			})
		}
	}
}

// Every failure must remain an error, never turn an incomplete snapshot into absence.
func TestSiloIncompleteAndAmbiguousEvidenceFailsClosed(t *testing.T) {
	for _, scenario := range []string{"missing-identity", "multiple-primary", "detail-mismatch", "invalid-identity", "invalid-tvdb", "duplicate-match", "conflicting-match", "tvdb-conflict", "missing-page", "missing-items", "catalog-cycle", "catalog-overlap", "detail-error", "unsafe-content"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" {
					t.Errorf("unexpected mutation %s", r.Method)
				}
				switch r.URL.Path {
				case "/api/v2/profiles":
					if scenario == "multiple-primary" {
						fmt.Fprint(w, `{"items":[{"id":"one","is_primary":true},{"id":"two","is_primary":true}]}`)
					} else {
						fmt.Fprint(w, `{"items":[{"id":"one","is_primary":true}]}`)
					}
				case "/api/v2/catalog":
					switch scenario {
					case "missing-page":
						fmt.Fprint(w, `{"items":[]}`)
					case "missing-items":
						fmt.Fprint(w, `{"page":{"has_more":false}}`)
					case "catalog-cycle":
						fmt.Fprint(w, `{"items":[],"page":{"has_more":true,"next_cursor":"loop"}}`)
					case "catalog-overlap":
						fmt.Fprint(w, `{"items":[{"content_id":"local","type":"movie"}],"page":{"has_more":true,"next_cursor":"loop"}}`)
					case "duplicate-match", "conflicting-match":
						fmt.Fprint(w, `{"items":[{"content_id":"local","type":"movie"},{"content_id":"other","type":"movie"}],"page":{"has_more":false}}`)
					case "unsafe-content":
						fmt.Fprint(w, `{"items":[{"content_id":"../local","type":"movie"}],"page":{"has_more":false}}`)
					default:
						fmt.Fprint(w, `{"items":[{"content_id":"local","type":"movie"}],"page":{"has_more":false}}`)
					}
				case "/api/v2/catalog/items/local", "/api/v2/catalog/items/other":
					id := strings.TrimPrefix(r.URL.Path, "/api/v2/catalog/items/")
					switch scenario {
					case "missing-identity":
						fmt.Fprintf(w, `{"content_id":%q,"type":"movie"}`, id)
					case "detail-mismatch":
						fmt.Fprint(w, `{"content_id":"changed","type":"movie","tmdb_id":"42"}`)
					case "invalid-identity":
						fmt.Fprintf(w, `{"content_id":%q,"type":"movie","tmdb_id":"042"}`, id)
					case "invalid-tvdb":
						fmt.Fprintf(w, `{"content_id":%q,"type":"movie","tvdb_id":"042"}`, id)
					case "tvdb-conflict":
						fmt.Fprintf(w, `{"content_id":%q,"type":"movie","tmdb_id":"42","tvdb_id":"43"}`, id)
					case "conflicting-match":
						fmt.Fprintf(w, `{"content_id":%q,"type":"movie","tmdb_id":"42","imdb_id":"tt9999999"}`, id)
					case "detail-error":
						http.Error(w, "synthetic private body", 500)
					default:
						fmt.Fprintf(w, `{"content_id":%q,"type":"movie","tmdb_id":"42","imdb_id":"tt0000042"}`, id)
					}

				default:
					t.Errorf("unexpected route %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			c := NewClient(Config{SiloURL: srv.URL, SiloAPIKey: "synthetic-key"})
			got, err := c.SiloAvailability(context.Background(), Media{Type: "movie", TMDbID: 42, IMDbID: "tt0000042", TVDbID: 42})
			if err == nil || got.Status != "error" || strings.Contains(got.Error, "synthetic private body") {
				t.Fatalf("incomplete evidence: %+v err=%v requests=%d", got, err, calls)
			}
		})
	}
}

func TestSiloSnapshotExpiryAndFailedRefreshNeverReuseStaleEvidence(t *testing.T) {
	fail := false
	catalogCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "private", 503)
			return
		}
		switch r.URL.Path {
		case "/api/v2/profiles":
			fmt.Fprint(w, `{"items":[{"id":"primary","is_primary":true}]}`)
		case "/api/v2/catalog":
			catalogCalls++
			fmt.Fprint(w, `{"items":[],"page":{"has_more":false}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := NewClient(Config{SiloURL: srv.URL, SiloAPIKey: "synthetic-key"})
	m := Media{Type: "movie", TMDbID: 42}
	if got, e := c.SiloAvailability(context.Background(), m); e != nil || got.Status != "absent" {
		t.Fatal(got, e)
	}
	c.siloCache.at = time.Now().Add(-siloCacheTTL - time.Second)
	fail = true
	if got, e := c.SiloAvailability(context.Background(), m); e == nil || got.Status != "error" {
		t.Fatal("stale evidence reused", got, e)
	}
	fail = false
	if got, e := c.SiloAvailability(context.Background(), m); e != nil || got.Status != "absent" {
		t.Fatal(got, e)
	}
	if catalogCalls != 2 {
		t.Fatalf("refresh calls %d", catalogCalls)
	}
}

func TestSiloInputValidationAndSnapshotIsolation(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "private", 403) }))
	defer srv.Close()
	c := NewClient(Config{SiloURL: srv.URL, SiloAPIKey: "synthetic-key"})
	for _, m := range []Media{{Type: "movie", TMDbID: -1}, {Type: "show", TVDbID: -1}, {Type: "show", IMDbID: "tt42"}, {Type: "other", TMDbID: 42}} {
		if got, e := c.SiloAvailability(context.Background(), m); e == nil || got.Status != "error" {
			t.Fatal(got, e)
		}
	}
	if got, e := c.SiloAvailability(context.Background(), Media{Type: "movie", Title: "Exact title"}); e != nil || got.Status != "unknown" {
		t.Fatal(got, e)
	}
	if calls != 0 {
		t.Fatalf("invalid or missing IDs triggered %d requests", calls)
	}
	c.siloCache = &siloSnapshot{at: time.Now(), items: []siloItem{{ContentID: "local", Type: "movie", TMDbID: "42"}}}
	other := NewClient(Config{SiloURL: srv.URL, SiloAPIKey: "other-synthetic-key"})
	if got, e := other.SiloAvailability(context.Background(), Media{Type: "movie", TMDbID: 42}); e == nil || got.Status != "error" {
		t.Fatal("credential-scoped snapshot leaked", got, e)
	}
}

func TestSiloExactIdentityUsesCompletePaginatedCatalog(t *testing.T) {
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Errorf("unsafe request %s", r.Method)
			w.WriteHeader(403)
			return
		}
		calls[r.URL.Path]++
		switch r.URL.Path {
		case "/api/v2/profiles":
			fmt.Fprint(w, `{"items":[{"id":"primary","is_primary":true}]}`)
		case "/api/v2/catalog":
			if r.Header.Get("X-Profile-Id") != "primary" {
				t.Error("profile missing")
			}
			if r.URL.Query().Get("cursor") == "next" {
				fmt.Fprint(w, `{"items":[{"content_id":"metadata","type":"movie"}],"page":{"has_more":false}}`)
			} else {
				fmt.Fprint(w, `{"items":[{"content_id":"local","type":"movie"},{"content_id":"book","type":"ebook"},{"content_id":"season","type":"season"}],"page":{"has_more":true,"next_cursor":"next"}}`)
			}
		case "/api/v2/catalog/items/local":
			fmt.Fprint(w, `{"content_id":"local","type":"movie","tmdb_id":"42","imdb_id":"tt0000042"}`)
		case "/api/v2/catalog/items/metadata":
			fmt.Fprint(w, `{"content_id":"metadata","type":"movie","tmdb_id":"43"}`)

		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := NewClient(Config{SiloURL: server.URL, SiloAPIKey: "synthetic-key"})
	for _, tc := range []struct {
		media  Media
		status string
	}{
		{Media{Type: "movie", TMDbID: 42, IMDbID: "tt0000042"}, "available"},
		{Media{Type: "movie", TMDbID: 43}, "available"},
		{Media{Type: "movie", TVDbID: 42}, "absent"},
		{Media{Type: "movie", TMDbID: 44, Title: "local"}, "absent"},
		{Media{Type: "show", TMDbID: 42}, "absent"},
		{Media{Type: "movie", Title: "local"}, "unknown"},
	} {
		got, err := c.SiloAvailability(context.Background(), tc.media)
		if (err != nil) != (tc.status == "error") || got.Status != tc.status {
			t.Fatalf("media %+v: %+v %v", tc.media, got, err)
		}
		if tc.status == "available" {
			id := "local"
			if tc.media.TMDbID == 43 {
				id = "metadata"
			}
			if got.URL != server.URL+"/item/"+id || got.CheckedAt.IsZero() {
				t.Fatalf("catalog presence lacks a safe link: %+v", got)
			}
		} else if got.URL != "" {
			t.Fatalf("non-matching catalog result has a link %q", got.URL)
		}
	}
	if calls["/api/v2/catalog"] != 2 || calls["/api/v2/catalog/items/local"] != 1 {
		t.Fatalf("snapshot not cached: %+v", calls)
	}
}
