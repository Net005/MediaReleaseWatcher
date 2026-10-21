package integrations

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSiloCoverageErrorsAreTypedAndSnapshotScoped(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("unexpected mutation")
		}
		calls++
		switch r.URL.Path {
		case "/api/v2/profiles":
			fmt.Fprint(w, `{"items":[{"id":"primary","is_primary":true}]}`)
		case "/api/v2/catalog":
			fmt.Fprint(w, `{"items":[{"content_id":"custom","type":"movie"},{"content_id":"known","type":"movie"},{"content_id":"duplicate","type":"movie"},{"content_id":"duplicate2","type":"movie"}],"page":{"has_more":false}}`)
		case "/api/v2/catalog/items/custom":
			fmt.Fprint(w, `{"content_id":"custom","type":"movie"}`)
		case "/api/v2/catalog/items/known":
			fmt.Fprint(w, `{"content_id":"known","type":"movie","tmdb_id":"42","imdb_id":"tt0000042"}`)
		case "/api/v2/catalog/items/duplicate":
			fmt.Fprint(w, `{"content_id":"duplicate","type":"movie","tmdb_id":"43"}`)
		case "/api/v2/catalog/items/duplicate2":
			fmt.Fprint(w, `{"content_id":"duplicate2","type":"movie","tmdb_id":"43"}`)
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := NewClient(Config{SiloURL: srv.URL, SiloAPIKey: "synthetic"})
	for _, tc := range []struct {
		m      Media
		reason string
	}{
		{Media{Type: "movie", TMDbID: 99}, "unidentified"},
		{Media{Type: "movie", TMDbID: 43}, "ambiguous"},
		{Media{Type: "movie", TMDbID: 42, IMDbID: "tt0000099"}, "conflicting"},
		{Media{Type: "movie", TMDbID: 43, IMDbID: "tt0000042"}, "conflicting"},
	} {
		p, err := c.SiloAvailability(context.Background(), tc.m)
		var gap *SiloCoverageError
		if !errors.As(err, &gap) || gap.Reason != tc.reason || p.Status == "available" || p.Status == "absent" {
			t.Fatalf("coverage=%+v err=%v", p, err)
		}
	}
	p, err := c.SiloAvailability(context.Background(), Media{Type: "movie", TMDbID: 42})
	if err != nil || p.Status != "available" || calls != 6 {
		t.Fatalf("exact match or cache: %+v %v calls=%d", p, err, calls)
	}
}
