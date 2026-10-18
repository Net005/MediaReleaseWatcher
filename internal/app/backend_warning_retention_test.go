package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresDiscoveryPreservesNonIMDbIdentifierWarning(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lists/1/items":
			fmt.Fprint(w, `{"movies":[{"id":42,"mediatype":"movie","title":"Synthetic placeholder fixture","imdb_id":"tr9001"}]}`)
		case "/tmdb/movie":
			fmt.Fprint(w, `[]`)
		default:
			fmt.Fprint(w, `{"items":[]}`)
		}
	}))
	defer server.Close()
	a.cfg.MDBListBaseURL = server.URL
	s := defaultSettings()
	s.MDBList.APIKey = "synthetic-key"
	s.MDBList.ListIDs = "1"
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := a.syncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := a.releases(ctx, releaseFilter{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("missing imported fixture: %v", err)
	}
	r := page.Items[0]
	if !strings.Contains(r.Coverage, "non-IMDb") {
		t.Fatalf("discovery replaced the identifier warning: %s", r.Coverage)
	}
	if r.IMDbID != "" || r.TMDbID != "42" {
		t.Fatalf("source identity was rewritten: %+v", r)
	}
	if !strings.Contains(string(r.Raw), "tr9001") {
		t.Fatal("source placeholder not retained in raw metadata")
	}
}
