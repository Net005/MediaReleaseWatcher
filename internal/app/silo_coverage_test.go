package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSiloCoverageOutcomeNeverDowngradesFatalErrors(t *testing.T) {
	warning := &siloSyncWarning{Available: 137, Absent: 937, Unknown: 1750, Unidentified: 1749, Conflicting: 1}
	for _, tc := range []struct {
		err    error
		status string
	}{
		{nil, "ok"},
		{warning, "warning"},
		{errors.New("silo returned HTTP 429"), "error"},
		{errors.New("silo pagination incomplete"), "error"},
		{errors.Join(warning, context.Canceled), "error"},
	} {
		status, message := presenceSyncOutcome(tc.err)
		if status != tc.status {
			t.Fatalf("outcome %v: %s %s", tc.err, status, message)
		}
		if status == "warning" && !strings.Contains(message, "137 available, 937 absent, 1750 unknown") {
			t.Fatalf("aggregate warning missing: %s", message)
		}
	}
}

func TestSiloCoverageSnapshotWarningAndCachedEvidence(t *testing.T) {
	a := testApp(t)
	var catalog atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("unexpected mutation")
		}
		switch r.URL.Path {
		case "/api/v2/profiles":
			fmt.Fprint(w, `{"items":[{"id":"primary","is_primary":true}]}`)
		case "/api/v2/catalog":
			catalog.Add(1)
			fmt.Fprint(w, `{"items":[{"content_id":"custom","type":"movie"},{"content_id":"known","type":"movie"},{"content_id":"conflict","type":"movie"}],"page":{"has_more":false}}`)
		case "/api/v2/catalog/items/custom":
			fmt.Fprint(w, `{"content_id":"custom","type":"movie"}`)
		case "/api/v2/catalog/items/known":
			fmt.Fprint(w, `{"content_id":"known","type":"movie","tmdb_id":"42","imdb_id":"tt0000042"}`)
		case "/api/v2/catalog/items/conflict":
			// Match TMDb but disagree on IMDb without duplicating local identities.
			fmt.Fprint(w, `{"content_id":"conflict","type":"movie","tmdb_id":"43","imdb_id":"tt0000043"}`)
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := defaultSettings()
	s.Silo = SiloSettings{URL: srv.URL, APIKey: "synthetic", Enabled: true}
	if err := a.saveSettings(a.ctx, s); err != nil {
		t.Fatal(err)
	}
	_, err := a.db.Exec(a.ctx, `INSERT INTO releases(canonical_key,media_type,tmdb_id,imdb_id,data) VALUES
 ('exact','movie','42','tt0000042','{}'),
 ('unknown','movie','99','','{}'),
 ('conflict','movie','43','tt0000099','{}'),
 ('cached','movie','100','','{"local_availability":{"status":"available","url":"https://silo.test/item/old","checked_at":"2026-01-02T03:04:05Z"}}')`)
	if err != nil {
		t.Fatal(err)
	}
	err = a.syncSilo(a.ctx)
	var warning *siloSyncWarning
	if !errors.As(err, &warning) {
		t.Fatalf("complete catalog with coverage gaps must be typed warning: %v", err)
	}
	if warning.Available != 1 || warning.Absent != 0 || warning.Unknown != 3 || warning.Conflicting != 1 || warning.Unidentified != 2 {
		t.Fatalf("unsafe counts: %+v", warning)
	}
	if catalog.Load() != 1 {
		t.Fatalf("catalog scans=%d", catalog.Load())
	}
	for _, tc := range []struct{ key, status string }{{"exact", "available"}, {"unknown", "unknown"}, {"conflict", "unknown"}, {"cached", "available"}} {
		var status string
		if err := a.db.QueryRow(a.ctx, `SELECT data->'local_availability'->>'status' FROM releases WHERE canonical_key=$1`, tc.key).Scan(&status); err != nil || status != tc.status {
			t.Fatalf("%s=%s %v", tc.key, status, err)
		}
	}
	var at, diagnostic string
	if err := a.db.QueryRow(a.ctx, `SELECT data->'local_availability'->>'checked_at',data->'local_availability'->>'last_check_status' FROM releases WHERE canonical_key='cached'`).Scan(&at, &diagnostic); err != nil || at != "2026-01-02T03:04:05Z" || diagnostic != "warning" {
		t.Fatalf("cached evidence: %s %s %v", at, diagnostic, err)
	}
	if !a.startPresenceSync("silo", a.syncSilo) {
		t.Fatal("launch rejected")
	}
	a.wg.Wait()
	var status, message string
	if err := a.db.QueryRow(a.ctx, `SELECT status,message FROM activity WHERE kind='silo' ORDER BY id DESC LIMIT 1`).Scan(&status, &message); err != nil || status != "warning" || !strings.Contains(message, "1 available, 0 absent, 3 unknown") {
		t.Fatalf("manual outcome: %s %s %v", status, message, err)
	}
	task := schedulerCreate(t, a, "silo_sync")
	claim, err := a.claimSchedule(a.ctx, task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	a.executeSchedule(a.ctx, claim)
	if err := a.db.QueryRow(a.ctx, `SELECT last_status,last_error FROM scheduled_tasks WHERE id=$1`, task.ID).Scan(&status, &message); err != nil || status != "warning" || !strings.Contains(message, "1 available, 0 absent, 3 unknown") {
		t.Fatalf("scheduled outcome: %s %s %v", status, message, err)
	}
}

func TestSiloCoverageTransportFailureIsFatal(t *testing.T) {
	a := testApp(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }))
	defer srv.Close()
	s := defaultSettings()
	s.Silo = SiloSettings{URL: srv.URL, APIKey: "synthetic", Enabled: true}
	if err := a.saveSettings(a.ctx, s); err != nil {
		t.Fatal(err)
	}
	schedulerRelease(t, a, "movie", 42, 0, false)
	err := a.syncSilo(context.Background())
	var warning *siloSyncWarning
	if err == nil || errors.As(err, &warning) {
		t.Fatalf("429 classified as coverage: %v", err)
	}
}
