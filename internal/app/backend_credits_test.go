package app

import (
	"context"
	"encoding/json"
	"media-release-watcher/internal/integrations"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostgresSupplementalCreditsVerifyIMDbIdentity(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic Person"})
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"title":"Wrong Media","type":"show","ids":{"tmdb":42,"imdb":"tt7654321"}}`))
	}))
	defer server.Close()
	a.cfg.MDBListBaseURL = server.URL
	s := defaultSettings()
	s.MDBList.APIKey = "synthetic-key"
	n, e := a.enrichTVCredits(ctx, p, s, []integrations.Credit{{IMDbID: "tt1234567", URL: "https://www.tvmaze.com/shows/42/synthetic"}})
	if e == nil || n != 0 {
		t.Fatal("mismatched show falsely linked", n, e)
	}
}
func TestPostgresTVDBOnlyCreditsUseKnownLocalMetadata(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic Person"})
	if e != nil {
		t.Fatal(e)
	}
	r, _, _ := normalizeRelease(json.RawMessage(`{"title":"Synthetic","type":"show","ids":{"tmdb":42,"tvdb":84}}`), "show")
	r, e = a.saveRelease(ctx, r, nil)
	if e != nil {
		t.Fatal(e)
	}
	n, e := a.enrichTVCredits(ctx, p, defaultSettings(), []integrations.Credit{{TVDbID: 84, URL: "https://www.tvmaze.com/shows/42/synthetic"}})
	if e != nil || n != 1 {
		t.Fatal(n, e)
	}
	r, e = a.getRelease(ctx, r.ID)
	if e != nil || len(r.Actresses) != 1 || r.Actresses[0].ID != p.ID {
		t.Fatal(r, e)
	}
}
