package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPSettingsOriginRejectsSchemeMismatch(t *testing.T) {
	a := testApp(t)
	h := a.Handler()
	r := httptest.NewRequest("POST", "http://localhost/api/setup", strings.NewReader(`{"password":"test-password-123"}`))
	r.Header.Set("Origin", "https://localhost")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-scheme write accepted: %d", w.Code)
	}
}
func TestPostgresCreditsRetainsSourceLinksAndLateImport(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	r, _, _ := normalizeRelease(json.RawMessage(`{"title":"Synthetic show","type":"show","tmdbid":42}`), "show")
	r.SourceLinks = append(r.SourceLinks, "https://www.tvmaze.com/shows/42/synthetic")
	r, e := a.saveReleaseSource(ctx, r, []string{"123"}, "tvmaze:castcredits:https://www.tvmaze.com/shows/42/synthetic")
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Actresses) != 0 {
		t.Fatal("invented watcher")
	}
	p, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic person"})
	if e != nil {
		t.Fatal(e)
	}
	r, e = a.getRelease(ctx, r.ID)
	if e != nil || len(r.Actresses) != 1 || r.Actresses[0].ID != p.ID || len(r.SourceLinks) != 2 {
		t.Fatal(r, e)
	}
}
