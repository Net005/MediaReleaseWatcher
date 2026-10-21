package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresCatalogArrOutcome(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprintf("transport_failure_%v", broken), func(t *testing.T) {
			a := policyApp(t)
			ctx := context.Background()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("external write: %s", r.Method)
				}
				switch r.URL.Path {
				case "/people/favorites/upcoming", "/catalog/movie", "/catalog/show":
					fmt.Fprint(w, `{"items":[],"has_more":false}`)
				case "/api/v3/series":
					if broken {
						http.Error(w, "secret-provider-body", http.StatusBadGateway)
						return
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
			s.Sonarr = ArrSettings{URL: srv.URL, APIKey: "synthetic", Enabled: false}
			if err := a.saveSettings(ctx, s); err != nil {
				t.Fatal(err)
			}
			release, err := a.saveRelease(ctx, Release{Type: "show", TMDbID: "100", Title: "Missing native identity", MetadataFetchedAt: time.Now().UTC(), Acquisition: map[string]string{"sonarr_status": "present", "sonarr_id": "7"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			// Seed durable verified evidence, not saveRelease's caller-supplied
			// acquisition fields (which are intentionally normalized).
			if _, err := a.db.Exec(ctx, `UPDATE releases SET acquisition=acquisition||'{"sonarr_status":"present","sonarr_id":"7"}'::jsonb WHERE id=$1`, release.ID); err != nil {
				t.Fatal(err)
			}
			if err := a.syncOnce(ctx); err != nil {
				t.Fatalf("optional inventory outcome failed catalog: %v", err)
			}
			var status, message string
			if err := a.db.QueryRow(ctx, `SELECT status,message FROM activity WHERE kind='arr' ORDER BY id DESC LIMIT 1`).Scan(&status, &message); err != nil {
				t.Fatal(err)
			}
			if broken {
				if status != "error" || !strings.Contains(message, "prior availability retained") || strings.Contains(message, "secret-provider-body") || strings.Contains(message, srv.URL) {
					t.Fatalf("unsafe or misclassified transport activity: %s %q", status, message)
				}
			} else {
				if status != "warning" || !strings.Contains(message, "Sonarr 0 present, 0 absent, 1 unknown") || strings.Contains(message, "failed") || strings.Contains(message, "incomplete") {
					t.Fatalf("coverage details lost: %s %q", status, message)
				}
			}
			got, err := a.getRelease(ctx, release.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Acquisition["sonarr_status"] != "present" || got.Acquisition["sonarr_id"] != "7" {
				t.Fatalf("prior presence discarded: %+v", got.Acquisition)
			}
		})
	}
}
