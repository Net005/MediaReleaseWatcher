package app

import (
	"context"
	"media-release-watcher/internal/integrations"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresDeletedWatcherCannotBeResurrectedByEnrichment(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic Person"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.db.Exec(ctx, `DELETE FROM actresses WHERE id=$1`, p.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = a.savePersonMetadata(ctx, p); e == nil {
		t.Fatal("deleted row unexpectedly updated")
	}
	var count int
	if e = a.db.QueryRow(ctx, `SELECT count(*) FROM actresses`).Scan(&count); e != nil || count != 0 {
		t.Fatal("deleted watcher resurrected", count, e)
	}
}
func TestPostgresFreshReverseProxySetupUsesConfiguredPublicOrigin(t *testing.T) {
	a := testApp(t)
	a.cfg.PublicURL = "https://watcher.example.test"
	h := a.Handler()
	for _, origin := range []string{"https://evil.example.test", "http://watcher.example.test", "https://watcher.example.test/path"} {
		r := httptest.NewRequest("POST", "http://internal:8080/api/setup", strings.NewReader(`{"password":"test-password-123"}`))
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Errorf("untrusted origin accepted %s: %d", origin, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "http://internal:8080/api/setup", strings.NewReader(`{"password":"test-password-123"}`))
	r.Header.Set("Origin", a.cfg.PublicURL)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 || !w.Result().Cookies()[0].Secure {
		t.Fatalf("reverse proxy bootstrap failed %d %s", w.Code, w.Body.String())
	}
}
func TestPublicURLMustBeAnOrigin(t *testing.T) {
	for _, u := range []string{"https://user:pass@example.test", "https://example.test/path", "https://example.test?key=value", "https://example.test#fragment", "//example.test"} {
		s := defaultSettings()
		s.PublicURL = u
		if validSettings(s) == nil {
			t.Errorf("accepted non-origin PUBLIC_URL %s", u)
		}
	}
}
func TestPostgresTVDBOnlyMissingMetadataIsExplicitIncomplete(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic Person"})
	if e != nil {
		t.Fatal(e)
	}
	matched, e := a.enrichTVCredits(ctx, p, defaultSettings(), []integrations.Credit{{Title: "Synthetic", TVDbID: 42, URL: "https://www.tvmaze.com/shows/42/synthetic"}})
	if e == nil || matched != 0 || !strings.Contains(e.Error(), "incomplete") {
		t.Fatalf("unsupported TVDB-only lookup cached as success %d %v", matched, e)
	}
}
