package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFavoritesRowsUseActualPersonMediaRelationship(t *testing.T) {
	raw := json.RawMessage(`{"items":[{"person":{"tmdb":123,"name":"Person","avatar":"https://image.tmdb.org/a.jpg"},"media":{"type":"show","tmdb":42,"title":"Test"},"release_date":"2026-10-19"}],"has_more":false}`)
	rows, next, more, e := parseDiscovery(raw)
	if e != nil || next != "" || more || len(rows) != 1 {
		t.Fatal(rows, next, more, e)
	}
	r, p, e := normalizeRelease(rows[0].Raw, rows[0].Kind)
	if e != nil || r.TMDbID != "42" || r.ReleaseDate != "2026-10-19" || len(p) != 1 || p[0] != "123" {
		t.Fatal(r, p, e)
	}
}
func TestSyncBatchReorderingBaselineSilent(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	writes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/lists/1/items":
			w.Write([]byte(`{"movies":[{"id":42,"title":"First","mediatype":"movie"},{"id":43,"title":"Second","mediatype":"movie"}],"pagination":{"has_more":false}}`))
		case r.URL.Path == "/people/favorites/upcoming":
			w.Write([]byte(`{"items":[],"has_more":false}`))
		case strings.HasPrefix(r.URL.Path, "/catalog/"):
			w.Write([]byte(`{"movies":[],"shows":[],"pagination":{}}`))
		case r.Method == "POST" && strings.TrimRight(r.URL.Path, "/") == "/tmdb/movie":
			w.Write([]byte(`[{"title":"Second","type":"movie","ids":{"tmdb":43},"cast":[{"id":123}]},{"title":"First","type":"movie","ids":{"tmdb":42},"cast":[{"id":123}]}]`))
		default:
			writes++
			http.Error(w, "unexpected", 500)
		}
	}))
	defer srv.Close()
	a.cfg.MDBListBaseURL = srv.URL
	s := defaultSettings()
	s.MDBList.APIKey = "test"
	s.MDBList.ListIDs = "1"
	s.Automation.Enabled = true
	s.Pushover.Enabled = true
	s.Pushover.Token = "test"
	s.Pushover.User = "test"
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	if _, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Person", Monitored: true, AutoAdd: true, Notify: true, MetadataStatus: "ok"}); e != nil {
		t.Fatal(e)
	}
	if e := a.syncOnce(ctx); e != nil {
		t.Fatal(e)
	}
	p, e := a.releases(ctx, releaseFilter{Limit: 36})
	if e != nil || p.Total != 2 {
		t.Fatal(p, e)
	}
	for _, r := range p.Items {
		if len(r.Actresses) != 1 {
			t.Fatal("missing actual cast", r)
		}
	}
	if writes != 0 {
		t.Fatal("unexpected baseline external write", writes)
	}
	var n int
	a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE status='baseline'`).Scan(&n)
	if n != 4 {
		t.Fatal("baseline not durably suppressed", n)
	}
}
