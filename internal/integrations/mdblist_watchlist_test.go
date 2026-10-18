package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestMDBWatchlistSetMovieFreshReadback(t *testing.T) {
	calls := []string{}
	present := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method == "POST" {
			raw, _ := io.ReadAll(r.Body)
			var body, want any
			_ = json.Unmarshal(raw, &body)
			_ = json.Unmarshal([]byte(`{"movies":[{"ids":{"tmdb":278,"imdb":"tt0111161"}}]}`), &want)
			if !reflect.DeepEqual(body, want) {
				t.Errorf("body %s", raw)
			}
			present = true
			fmt.Fprint(w, `{"added":1,"existing":0,"not_found":0}`)
			return
		}
		if present {
			fmt.Fprint(w, `{"movies":[{"ids":{"tmdb":278,"imdb":"tt0111161"}}]}`)
		} else {
			fmt.Fprint(w, `{"movies":[]}`)
		}
	}))
	defer srv.Close()
	c := NewClient(Config{MDBListAPIKey: "synthetic", MDBListBaseURL: srv.URL})
	got, err := c.MDBSetWatchlist(context.Background(), MDBWatchlistMedia{Type: "movie", TMDbID: "278", IMDbID: "tt0111161"}, true)
	if err != nil || got.Status != "present" || !got.Verified {
		t.Fatalf("got %+v err=%v", got, err)
	}
	want := []string{"GET /watchlist/items", "POST /watchlist/items/add", "GET /watchlist/items"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatal(calls)
	}
}

func TestMDBWatchlistPaginationAndStrictCompletion(t *testing.T) {
	tests := []struct {
		name, first, header, second string
		want                        string
		calls                       int
	}{
		{"show second page", `{"shows":[],"pagination":{"next_cursor":"page 2"}}`, "true", `{"shows":[{"id":278,"mediatype":"show"}],"pagination":{"next_cursor":null}}`, "present", 2},
		{"full absence", `{"shows":[{"id":99}],"pagination":{"next_cursor":"next"}}`, "true", `{"shows":[],"pagination":{"next_cursor":null}}`, "absent", 2},
		{"header continuation lacks cursor", `{"shows":[]}`, "true", "", "unknown", 1},
		{"header conflicts cursor", `{"shows":[],"pagination":{"next_cursor":"next"}}`, "false", "", "unknown", 1},
		{"malformed header", `{"shows":[]}`, "maybe", "", "unknown", 1},
		{"body continuation lacks cursor", `{"shows":[],"pagination":{"has_more":true}}`, "", "", "unknown", 1},
		{"repeated cursor", `{"shows":[],"pagination":{"next_cursor":"next"}}`, "true", `{"shows":[],"pagination":{"next_cursor":"next"}}`, "unknown", 2},
		{"bad pagination", `{"shows":[],"pagination":[]}`, "", "", "unknown", 1},
		{"case-folded pagination", `{"shows":[],"Pagination":{"next_cursor":"next"}}`, "", "", "unknown", 1},
		{"case-folded cursor", `{"shows":[],"pagination":{"Next_Cursor":"next"}}`, "", "", "unknown", 1},
		{"numeric cursor", `{"shows":[],"pagination":{"next_cursor":12}}`, "", "", "unknown", 1},
		{"invalid boolean", `{"shows":[],"has_more":"false"}`, "", "", "unknown", 1},
		{"conflicting cursor sources", `{"shows":[],"next_cursor":"x","pagination":{"next_cursor":"y"}}`, "", "", "unknown", 1},
		{"conflicting completion sources", `{"shows":[],"has_more":false,"pagination":{"has_more":true}}`, "", "", "unknown", 1},
		{"later page fails after target found", `{"shows":[{"id":278}],"next_cursor":"next"}`, "true", `{"shows":[],"error":"remote"}`, "unknown", 2},
		{"missing bucket", `{"error":"private secret"}`, "", "", "unknown", 1},
		{"null bucket", `{"shows":null}`, "", "", "unknown", 1},
		{"row without identity", `{"shows":[{"title":"Synthetic"}]}`, "", "", "unknown", 1},
		{"wrong media", `{"shows":[{"id":278,"mediatype":"movie"}]}`, "", "", "unknown", 1},
		{"opposite bucket", `{"shows":[],"movies":[{"id":278}]}`, "", "", "unknown", 1},
		{"conflicting aliases", `{"shows":[{"id":278,"ids":{"tmdb":279}}]}`, "", "", "unknown", 1},
		{"no comparable ID", `{"shows":[{"imdb_id":"tt0111161"}]}`, "", "", "unknown", 1},
		{"duplicate target", `{"shows":[{"id":278},{"id":278}]}`, "", "", "unknown", 1},
		{"error envelope with bucket", `{"shows":[],"error":"secret"}`, "", "", "unknown", 1},
		{"top-level cursor", `{"shows":[],"next_cursor":"next"}`, "", `{"shows":[{"ids":{"tmdb":278}}]}`, "present", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					if tt.header != "" {
						w.Header().Set("X-Has-More", tt.header)
					}
					fmt.Fprint(w, tt.first)
				} else {
					if r.URL.Query().Get("cursor") == "" {
						t.Error("missing cursor")
					}
					fmt.Fprint(w, tt.second)
				}
			}))
			defer srv.Close()
			got, err := NewClient(Config{MDBListAPIKey: "synthetic", MDBListBaseURL: srv.URL}).MDBWatchlistContains(context.Background(), MDBWatchlistMedia{Type: "show", TMDbID: "278"})
			if got.Status != tt.want || calls != tt.calls || (err != nil) != (tt.want == "unknown") || (got.InWatchlist == nil) != (tt.want == "unknown") {
				t.Fatalf("got %+v err=%v calls=%d", got, err, calls)
			}
		})
	}
}

func TestMDBWatchlistRemoveAndIdempotentAbsence(t *testing.T) {
	for _, kind := range []string{"movie", "show"} {
		t.Run(kind, func(t *testing.T) {
			present := true
			calls := []string{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.Method == "POST" {
					if r.URL.Path != "/watchlist/items/remove" {
						t.Errorf("wrong endpoint %s", r.URL.Path)
					}
					raw, _ := io.ReadAll(r.Body)
					if string(raw) != fmt.Sprintf(`{"%ss":[{"ids":{"imdb":"tt0111161"}}]}`, kind) {
						t.Errorf("wrong provider/type body %s", raw)
					}
					present = false
					fmt.Fprint(w, `{"removed":1}`)
					return
				}
				w.Header().Set("X-Has-More", "false")
				if present {
					fmt.Fprintf(w, `{"%ss":[{"imdb_id":"tt0111161"}]}`, kind)
				} else {
					fmt.Fprintf(w, `{"%ss":[]}`, kind)
				}
			}))
			defer srv.Close()
			c := NewClient(Config{MDBListAPIKey: "synthetic", MDBListBaseURL: srv.URL})
			media := MDBWatchlistMedia{Type: kind, IMDbID: "tt0111161"}
			for i := 0; i < 2; i++ {
				got, err := c.MDBSetWatchlist(context.Background(), media, false)
				if err != nil || got.Status != "absent" || got.InWatchlist == nil || *got.InWatchlist || !got.Verified {
					t.Fatalf("%+v %v", got, err)
				}
			}
			want := []string{"GET /watchlist/items", "POST /watchlist/items/remove", "GET /watchlist/items", "GET /watchlist/items"}
			if !reflect.DeepEqual(calls, want) {
				t.Fatal(calls)
			}
		})
	}
}

func TestMDBWatchlistLiteralIDsFailClosedBeforeHTTP(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, `{"movies":[]}`) }))
	defer srv.Close()
	c := NewClient(Config{MDBListAPIKey: "synthetic", MDBListBaseURL: srv.URL})
	for _, media := range []MDBWatchlistMedia{
		{Type: "tv", TMDbID: "278"}, {Type: "Movie", TMDbID: "278"}, {Type: "movie"},
		{Type: "movie", TMDbID: "0278"}, {Type: "movie", TMDbID: "278 "}, {Type: "movie", TMDbID: "-278"},
		{Type: "movie", TMDbID: "1e2"}, {Type: "movie", TMDbID: "999999999999999999999999999"},
		{Type: "movie", IMDbID: "0111161"}, {Type: "movie", IMDbID: "tt0111161 "}, {Type: "movie", TMDbID: "278", IMDbID: "ttbad"},
	} {
		got, err := c.MDBSetWatchlist(context.Background(), media, true)
		if err == nil || got.Status != "unknown" || got.InWatchlist != nil || got.Verified || calls != 0 {
			t.Fatalf("invalid literal accepted: %+v %+v %v calls=%d", media, got, err, calls)
		}
	}
}

func TestMDBWatchlistCredentialSafeRemoteFailures(t *testing.T) {
	for _, mode := range []string{"unauthorized", "limited", "server-error", "redirect", "invalid-json", "network", "canceled", "missing-key", "invalid-endpoint"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch mode {
				case "unauthorized":
					w.WriteHeader(401)
				case "limited":
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(429)
				case "server-error":
					w.WriteHeader(503)
				case "redirect":
					w.Header().Set("Location", "https://invalid.example/?apikey=synthetic-private-key")
					w.WriteHeader(302)
				}
				fmt.Fprint(w, "synthetic-private-key private remote response")
			}))
			defer srv.Close()
			cfg := Config{MDBListAPIKey: "synthetic-private-key", MDBListBaseURL: srv.URL}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "network" {
				srv.Close()
			}
			if mode == "canceled" {
				cancel()
			}
			if mode == "missing-key" {
				cfg.MDBListAPIKey = ""
			}
			if mode == "invalid-endpoint" {
				cfg.MDBListBaseURL = srv.URL + "?apikey=synthetic-private-key"
			}
			c := NewClient(cfg)
			got, err := c.MDBSetWatchlist(ctx, MDBWatchlistMedia{Type: "movie", TMDbID: "278"}, true)
			raw, _ := json.Marshal(got)
			if err == nil || got.Status != "unknown" || got.InWatchlist != nil || got.Verified || got.Error == "" || strings.Contains(string(raw)+fmt.Sprint(err), "synthetic-private-key") || strings.Contains(string(raw), "private remote response") {
				t.Fatalf("unsafe failure: %s %v", raw, err)
			}
			if mode == "limited" {
				_, _ = c.MDBWatchlistContains(ctx, MDBWatchlistMedia{Type: "movie", TMDbID: "278"})
				if calls != 1 {
					t.Fatalf("rate-limit cooldown bypassed: %d", calls)
				}
			}
		})
	}
}

func TestMDBWatchlistUnknownOnErrorEnvelopes(t *testing.T) {
	for _, body := range []string{`{"movies":[],"status":"error","message":"secret"}`, `{"movies":[],"success":false}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		got, err := NewClient(Config{MDBListAPIKey: "synthetic", MDBListBaseURL: srv.URL}).MDBWatchlistContains(context.Background(), MDBWatchlistMedia{Type: "movie", TMDbID: "278"})
		srv.Close()
		if err == nil || got.Status != "unknown" || got.InWatchlist != nil {
			t.Fatalf("error became absence: %s %+v %v", body, got, err)
		}
	}
}

func TestMDBWatchlistReadbackMismatchCarriesError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == "POST" {
			fmt.Fprint(w, `{"added":1}`)
			return
		}
		fmt.Fprint(w, `{"movies":[]}`)
	}))
	defer srv.Close()
	got, err := NewClient(Config{MDBListAPIKey: "synthetic", MDBListBaseURL: srv.URL}).MDBSetWatchlist(context.Background(), MDBWatchlistMedia{Type: "movie", TMDbID: "278"}, true)
	if err == nil || got.Verified || got.Error == "" || got.Status != "absent" || calls != 3 {
		t.Fatalf("unverified write: %+v %v calls=%d", got, err, calls)
	}
}

func TestMDBWatchlistRejectsAmbiguousWireIdentity(t *testing.T) {
	for _, body := range []string{
		`{"movies":[{"id":"278"}]}`,
		`{"movies":[{"ids":{"tmdb":"278"}}]}`,
		`{"movies":[{"ID":278}]}`,
		`{"movies":[{"id":278,"ID":999}]}`,
		`{"movies":[{"ids":{"TMDB":278}}]}`,
		`{"movies":[{"id":999,"id":278}]}`,
		`{"movies":[{"ids":{"tmdb":999,"tmdb":278}}]}`,
		`{"movies":[{"id":278}],"movies":[]}`,
		`{"movies":[],"pagination":{"next_cursor":"next","next_cursor":null}}`,
	} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			got, err := NewClient(Config{MDBListAPIKey: "synthetic", MDBListBaseURL: srv.URL}).MDBWatchlistContains(context.Background(), MDBWatchlistMedia{Type: "movie", TMDbID: "278"})
			if err == nil || got.Status != "unknown" || got.InWatchlist != nil {
				t.Fatalf("ambiguous wire identity accepted: %+v %v", got, err)
			}
		})
	}
}

func TestMDBWatchlistContainsExactMovie(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/watchlist/items" || r.URL.Query().Get("mediatype") != "movie" || r.URL.Query().Get("limit") != "1000" || r.URL.Query().Get("apikey") != "synthetic" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"movies":[{"id":278,"imdb_id":"tt0111161","mediatype":"movie"}],"shows":[]}`)
	}))
	defer srv.Close()
	c := NewClient(Config{MDBListAPIKey: "synthetic", MDBListBaseURL: srv.URL})
	got, err := c.MDBWatchlistContains(context.Background(), MDBWatchlistMedia{Type: "movie", TMDbID: "278", IMDbID: "tt0111161"})
	if err != nil || got.Status != "present" || got.InWatchlist == nil || !*got.InWatchlist || got.CheckedAt.IsZero() || calls != 1 {
		t.Fatalf("got %+v err=%v calls=%d", got, err, calls)
	}
}
