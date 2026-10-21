package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArrLargeInventoryNativeIDs(t *testing.T) {
	for _, kind := range []string{"radarr", "sonarr"} {
		t.Run(kind, func(t *testing.T) {
			payload := `[{"id":17,"tmdbId":101,"tvdbId":201,"overview":"` + strings.Repeat("x", (8<<20)+1) + `"},{"id":29,"tmdbId":102,"tvdbId":202}]`
			resource := "movie"
			media := Media{Type: "movie", TMDbID: 102}
			if kind == "sonarr" {
				resource = "series"
				media = Media{Type: "show", TVDbID: 202}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v3/"+resource {
					t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
				}
				fmt.Fprint(w, payload)
			}))
			defer srv.Close()
			c := NewClient(Config{RadarrURL: srv.URL, RadarrAPIKey: "test", SonarrURL: srv.URL, SonarrAPIKey: "test"})
			inv, err := c.ArrInventory(context.Background(), kind)
			if err != nil {
				t.Fatal(err)
			}
			if len(inv.items) != 2 || inv.items[0].ID != 17 || inv.items[1].TMDbID != 102 || inv.items[1].TVDbID != 202 {
				t.Fatalf("incomplete native identities: %+v", inv.items)
			}
			got, err := inv.Match(media)
			if err != nil || got.ID != 29 {
				t.Fatalf("last identity: %+v %v", got, err)
			}
		})
	}
}
