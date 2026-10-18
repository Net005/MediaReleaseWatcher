package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNegativeProviderRatingSentinelsRemainUnknown(t *testing.T) {
	r, _, e := normalizeRelease(json.RawMessage(`{"title":"Synthetic","type":"movie","ids":{"tmdb":42},"score":-1,"age_rating":-1}`), "movie")
	if e != nil || r.Score != nil || r.AgeRating != nil {
		t.Fatal("negative sentinel asserted as a rating", r.Score, r.AgeRating, e)
	}
}
func TestPostgresDiscoveryFailuresNeverCommitBaseline(t *testing.T) {
	for _, mode := range []string{"source_parse", "cursor", "network"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			ctx := context.Background()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/lists/1/items" {
					fmt.Fprint(w, `{"items":[]}`)
					return
				}
				switch mode {
				case "source_parse":
					fmt.Fprint(w, `{"movies":[{"title":"Missing literal identity"}]}`)
				case "cursor":
					fmt.Fprint(w, `{"movies":[{"id":42,"title":"Synthetic"}],"pagination":{"next_cursor":"repeat","has_more":true}}`)
				case "network":
					http.Error(w, "synthetic failure", 503)
				}
			}))
			defer server.Close()
			a.cfg.MDBListBaseURL = server.URL
			s := defaultSettings()
			s.MDBList.APIKey = "synthetic-key"
			s.MDBList.ListIDs = "1"
			if e := a.saveSettings(ctx, s); e != nil {
				t.Fatal(e)
			}
			if e := a.syncOnce(ctx); e == nil {
				t.Fatal("discovery failure accepted as completed sync")
			}
			var exists bool
			if e := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app_state WHERE key='baseline')`).Scan(&exists); e != nil || exists {
				t.Fatal("failed discovery committed baseline", exists, e)
			}
		})
	}
}
