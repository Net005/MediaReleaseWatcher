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

func TestArrSearchOneVerifiedItemAndCommandReadback(t *testing.T) {
	for _, kind := range []string{"radarr", "sonarr"} {
		for _, scenario := range []string{"ok", "invalid-id", "missing-identity", "identity-conflict", "unmonitored", "wrong-item", "command-error", "missing-command", "readback-error", "wrong-command", "wrong-scope"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				posts, reads := 0, 0
				resource, name := "movie", "MoviesSearch"
				m := Media{Type: "movie", TMDbID: 42, IMDbID: "tt0000042"}
				if kind == "sonarr" {
					resource, name = "series", "SeriesSearch"
					m = Media{Type: "show", TVDbID: 42, IMDbID: "tt0000042"}
				}
				id := 7
				if scenario == "invalid-id" {
					id = 0
				}
				if scenario == "missing-identity" {
					m.TMDbID = 0
					m.TVDbID = 0
					m.IMDbID = ""
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-Api-Key") != "synthetic-key" {
						t.Error("missing auth")
					}
					switch r.Method + " " + r.URL.Path {
					case "GET /api/v3/" + resource + "/7":
						native := 42
						if scenario == "identity-conflict" {
							native = 43
						}
						itemID := 7
						if scenario == "wrong-item" {
							itemID = 8
						}
						fmt.Fprintf(w, `{"id":%d,"tmdbId":%d,"tvdbId":%d,"imdbId":"tt0000042","monitored":%t}`, itemID, native, native, scenario != "unmonitored")
					case "POST /api/v3/command":
						posts++
						var body map[string]any
						if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
							t.Error(e)
						}
						if body["name"] != name {
							t.Errorf("wrong command: %+v", body)
						}
						if kind == "radarr" {
							ids, ok := body["movieIds"].([]any)
							if !ok || len(ids) != 1 || ids[0] != float64(7) || len(body) != 2 {
								t.Errorf("unbounded payload %+v", body)
							}
						} else if body["seriesId"] != float64(7) || len(body) != 2 {
							t.Errorf("unbounded payload %+v", body)
						}
						if scenario == "command-error" {
							http.Error(w, "private remote text", 503)
							return
						}
						if scenario == "missing-command" {
							fmt.Fprint(w, `{}`)
							return
						}
						fmt.Fprintf(w, `{"id":99,"name":%q}`, name)
					case "GET /api/v3/command/99":
						reads++
						if scenario == "readback-error" {
							http.Error(w, "private remote text", 503)
							return
						}
						commandName := name
						if scenario == "wrong-command" {
							commandName = "MissingMoviesSearch"
						}
						scope := 7
						if scenario == "wrong-scope" {
							scope = 8
						}
						fmt.Fprintf(w, `{"id":99,"name":%q,"body":{"name":%q,"movieIds":[%d],"seriesId":%d}}`, commandName, commandName, scope, scope)
					default:
						t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
						w.WriteHeader(404)
					}
				}))
				defer srv.Close()
				c := NewClient(Config{RadarrURL: srv.URL, RadarrAPIKey: "synthetic-key", SonarrURL: srv.URL, SonarrAPIKey: "synthetic-key"})
				e := c.ArrSearch(context.Background(), kind, id, m)
				if (e == nil) != (scenario == "ok") {
					t.Fatalf("error=%v posts=%d reads=%d", e, posts, reads)
				}
				if e != nil && strings.Contains(e.Error(), "private remote text") {
					t.Fatal("remote body leaked")
				}
				if scenario == "ok" && (posts != 1 || reads != 1) {
					t.Fatalf("not read back: posts=%d reads=%d", posts, reads)
				}
				switch scenario {
				case "invalid-id", "missing-identity", "identity-conflict", "unmonitored", "wrong-item":
					if posts != 0 {
						t.Fatal("unsafe search submitted")
					}
				}
				if posts > 1 {
					t.Fatal("uncertain command retried")
				}
			})
		}
	}
}
