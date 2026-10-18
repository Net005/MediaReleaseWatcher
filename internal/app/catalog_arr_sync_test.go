package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPostgresFullCatalogSyncRefreshesConfiguredDisabledArrReadOnly(t *testing.T) {
	a := policyApp(t)
	ctx := context.Background()
	var inventoryCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected external write: %s %s", r.Method, r.URL.Path)
			http.Error(w, "no writes", 400)
			return
		}
		switch r.URL.Path {
		case "/people/favorites/upcoming", "/catalog/movie", "/catalog/show":
			fmt.Fprint(w, `{"items":[],"has_more":false}`)
		case "/api/v3/movie":
			inventoryCalls.Add(1)
			if r.Header.Get("X-Api-Key") != "synthetic" {
				t.Error("missing synthetic Arr credential")
			}
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a.cfg.MDBListBaseURL = srv.URL
	s := defaultSettings()
	s.MDBList.APIKey = "synthetic"
	s.Radarr = ArrSettings{URL: srv.URL, APIKey: "synthetic", Enabled: false}
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := a.syncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if inventoryCalls.Load() != 1 {
		t.Fatalf("full catalog sync did not refresh configured Arr: %d", inventoryCalls.Load())
	}
}

func assertSilentCurrentBaseline(t *testing.T, a *App, releases int) {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE status<>'baseline'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("initial sync left actionable history: %d %v", n, err)
	}
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM actions a JOIN releases r ON r.id=a.release_id WHERE a.status='baseline' AND a.event_key=CASE WHEN `+upcomingReleasePredicate+` THEN 'upcoming' WHEN nullif(r.data->>'release_date','') IS NOT NULL THEN 'released' ELSE 'discovered' END`).Scan(&n); err != nil || n != releases*2 {
		t.Fatalf("current event baseline incomplete: %d %v", n, err)
	}
}
