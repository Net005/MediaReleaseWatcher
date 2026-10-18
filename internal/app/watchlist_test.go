package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"media-release-watcher/internal/integrations"
)

func watchlistApp(t *testing.T, provider http.Handler) (*App, *httptest.Server, *http.Cookie) {
	t.Helper()
	a := testApp(t)
	remote := httptest.NewServer(provider)
	t.Cleanup(remote.Close)
	a.cfg.MDBListBaseURL = remote.URL
	s := defaultSettings()
	s.MDBList.APIKey = "synthetic-watchlist-key"
	if err := a.saveSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	if _, err := a.db.Exec(context.Background(), `INSERT INTO sessions(token_hash,expires_at) VALUES($1, now()+interval '1 hour')`, tokenHash(token)); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a.Handler())
	t.Cleanup(server.Close)
	return a, server, &http.Cookie{Name: "mrw_session", Value: token}
}
func watchlistRelease(t *testing.T, a *App, kind, tmdb, imdb string) int64 {
	t.Helper()
	var id int64
	// Column identity is authoritative; deliberately stale JSON must never choose
	// another title/provider/type for an account mutation.
	err := a.db.QueryRow(context.Background(), `INSERT INTO releases(canonical_key,media_type,tmdb_id,imdb_id,data) VALUES($1||':'||$2||':'||$3,$1,$2,$3,'{"type":"movie","tmdb_id":"999","imdb_id":"tt9999999"}') RETURNING id`, kind, tmdb, imdb).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func watchlistHTTP(t *testing.T, server *httptest.Server, cookie *http.Cookie, method, path, body, origin string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, raw
}
func TestWatchlistHTTPAuthOriginAndMissingRelease(t *testing.T) {
	calls := 0
	a, server, cookie := watchlistApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "must not be called", 500) }))
	id := watchlistRelease(t, a, "movie", "278", "")
	path := fmt.Sprintf("/api/releases/%d/watchlist", id)
	for _, tt := range []struct {
		name, method, path, origin string
		auth                       bool
		want                       int
	}{
		{"anonymous read", "GET", path, "", false, 401},
		{"anonymous write", "POST", path, "", false, 401},
		{"cross-origin write", "POST", path, "https://attacker.invalid", true, 403},
		{"opaque origin", "POST", path, "null", true, 403},
		{"bad local ID", "GET", "/api/releases/01/watchlist", "", true, 400},
		{"missing local release", "POST", "/api/releases/99999/watchlist", server.URL, true, 404},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var c *http.Cookie
			if tt.auth {
				c = cookie
			}
			status, raw := watchlistHTTP(t, server, c, tt.method, tt.path, `{"in_watchlist":true}`, tt.origin)
			if status != tt.want || calls != 0 {
				t.Fatalf("%d %s calls=%d", status, raw, calls)
			}
		})
	}
}

func TestWatchlistHTTPFailureNeverClaimsVerified(t *testing.T) {
	for _, scenario := range []string{"read-error", "readback-error", "readback-mismatch", "pagination-incomplete", "identity-conflict"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			a, server, cookie := watchlistApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method == "POST" {
					fmt.Fprint(w, `{"added":1}`)
					return
				}
				if scenario == "read-error" || (scenario == "readback-error" && calls == 3) {
					http.Error(w, "synthetic-watchlist-key private remote body", 503)
					return
				}
				if scenario == "pagination-incomplete" {
					w.Header().Set("X-Has-More", "true")
					fmt.Fprint(w, `{"movies":[]}`)
					return
				}
				if scenario == "identity-conflict" {
					fmt.Fprint(w, `{"movies":[{"id":278,"imdb_id":"tt9999999"}]}`)
					return
				}
				w.Header().Set("X-Has-More", "false")
				fmt.Fprint(w, `{"movies":[]}`)
			}))
			id := watchlistRelease(t, a, "movie", "278", "tt0111161")
			status, raw := watchlistHTTP(t, server, cookie, "POST", fmt.Sprintf("/api/releases/%d/watchlist", id), `{"in_watchlist":true}`, server.URL)
			var got integrations.MDBWatchlistResult
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if status != 502 || got.Verified || got.Error == "" || got.CheckedAt.IsZero() || strings.Contains(string(raw), "synthetic-watchlist-key") || strings.Contains(string(raw), "private remote body") {
				t.Fatalf("unverified: %d %s", status, raw)
			}
			if scenario == "readback-mismatch" {
				if got.Status != "absent" || got.InWatchlist == nil || *got.InWatchlist {
					t.Fatal(string(raw))
				}
			} else if got.Status != "unknown" || got.InWatchlist != nil {
				t.Fatal(string(raw))
			}
			wantCalls := 1
			if scenario == "readback-error" || scenario == "readback-mismatch" {
				wantCalls = 3
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d want=%d", calls, wantCalls)
			}
		})
	}
}

func TestWatchlistHTTPRejectsAmbiguousDesiredState(t *testing.T) {
	calls := 0
	a, server, cookie := watchlistApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, `{"movies":[]}`) }))
	id := watchlistRelease(t, a, "movie", "278", "")
	for _, body := range []string{`{}`, `{"in_watchlist":null}`, `{"in_watchlist":"false"}`, `[]`, `{"in_watchlist":true} {}`, `{"in_watchlist":true,"in_watchlist":false}`, `{"In_Watchlist":true}`, `{"in_watchlist":true,"tmdb_id":"999"}`} {
		status, raw := watchlistHTTP(t, server, cookie, "POST", fmt.Sprintf("/api/releases/%d/watchlist", id), body, server.URL)
		if status != 400 || calls != 0 {
			t.Errorf("body=%s status=%d result=%s calls=%d", body, status, raw, calls)
		}
	}
}

func TestWatchlistHTTPPostVerifiesFreshReadback(t *testing.T) {
	for _, kind := range []string{"movie", "show"} {
		t.Run(kind, func(t *testing.T) {
			present := false
			var calls []string
			a, server, cookie := watchlistApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.Method == "POST" {
					raw, _ := io.ReadAll(r.Body)
					want := fmt.Sprintf(`{"%ss":[{"ids":{"imdb":"tt0111161","tmdb":278}}]}`, kind)
					if strings.TrimSpace(string(raw)) != want {
						t.Errorf("wrong exact write payload: %s", raw)
					}
					if r.URL.Path != "/watchlist/items/add" && r.URL.Path != "/watchlist/items/remove" {
						t.Errorf("wrong mutation: %s", r.URL.Path)
					}
					present = r.URL.Path == "/watchlist/items/add"
					fmt.Fprint(w, `{"added":1}`)
					return
				}
				w.Header().Set("X-Has-More", "false")
				if present {
					fmt.Fprintf(w, `{"%ss":[{"id":278,"imdb_id":"tt0111161"}]}`, kind)
				} else {
					fmt.Fprintf(w, `{"%ss":[]}`, kind)
				}
			}))
			id := watchlistRelease(t, a, kind, "278", "tt0111161")
			path := fmt.Sprintf("/api/releases/%d/watchlist", id)
			status, raw := watchlistHTTP(t, server, cookie, "POST", path, `{"in_watchlist":true}`, server.URL)
			var got integrations.MDBWatchlistResult
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if status != 200 || !got.Verified || got.Status != "present" || len(calls) != 3 {
				t.Fatalf("status=%d result=%s calls=%v", status, raw, calls)
			}
			if strings.Join(calls, ",") != "GET /watchlist/items,POST /watchlist/items/add,GET /watchlist/items" {
				t.Fatal(calls)
			}
			// Explicit desired state, not a toggle: a second add only reads.
			status, raw = watchlistHTTP(t, server, cookie, "POST", path, `{"in_watchlist":true}`, server.URL)
			if status != 200 || len(calls) != 4 {
				t.Fatalf("non-idempotent: %d %s %v", status, raw, calls)
			}
			status, raw = watchlistHTTP(t, server, cookie, "POST", path, `{"in_watchlist":false}`, server.URL)
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if status != 200 || got.Status != "absent" || got.InWatchlist == nil || *got.InWatchlist || !got.Verified || len(calls) != 7 || calls[5] != "POST /watchlist/items/remove" {
				t.Fatalf("remove not verified: %d %s %v", status, raw, calls)
			}
			status, raw = watchlistHTTP(t, server, cookie, "POST", path, `{"in_watchlist":false}`, server.URL)
			if status != 200 || len(calls) != 8 {
				t.Fatalf("remove not idempotent: %d %s %v", status, raw, calls)
			}
			var actions int
			if err := a.db.QueryRow(context.Background(), `SELECT count(*) FROM actions WHERE release_id=$1`, id).Scan(&actions); err != nil || actions != 0 {
				t.Fatalf("watchlist triggered acquisition actions: %d %v", actions, err)
			}
		})
	}
}

func TestWatchlistHTTPGetUsesLiteralColumnIdentity(t *testing.T) {
	calls := 0
	a, server, cookie := watchlistApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/watchlist/items" || r.URL.Query().Get("mediatype") != "show" || r.URL.Query().Get("apikey") != "synthetic-watchlist-key" {
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("X-Has-More", "false")
		fmt.Fprint(w, `{"shows":[{"ids":{"tmdb":278,"imdb":"tt0111161"}}]}`)
	}))
	id := watchlistRelease(t, a, "show", "278", "tt0111161")
	status, raw := watchlistHTTP(t, server, cookie, "GET", fmt.Sprintf("/api/releases/%d/watchlist", id), "", "")
	var got integrations.MDBWatchlistResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if status != 200 || got.Status != "present" || got.InWatchlist == nil || !*got.InWatchlist || got.CheckedAt.IsZero() || calls != 1 {
		t.Fatalf("status=%d result=%s calls=%d", status, raw, calls)
	}
}
