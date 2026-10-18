package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"media-release-watcher/internal/integrations"
)

func TestSiloPresenceWriteRejectsChangedTVDbIdentity(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	r := Release{Type: "show", TVDbID: "42"}
	if e := a.db.QueryRow(ctx, `INSERT INTO releases(canonical_key,media_type,data) VALUES('show:tvdb:42','show','{"tvdb_id":"42"}') RETURNING id`).Scan(&r.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec(ctx, `UPDATE releases SET data='{"tvdb_id":"43"}' WHERE id=$1`, r.ID); e != nil {
		t.Fatal(e)
	}
	if e := a.saveSiloPresence(ctx, r, integrations.LocalAvailability{Status: "available", URL: "https://silo.test/item/local", CheckedAt: time.Now().UTC()}, nil); e != nil {
		t.Fatal(e)
	}
	var stale bool
	if e := a.db.QueryRow(ctx, `SELECT data ? 'local_availability' FROM releases WHERE id=$1`, r.ID).Scan(&stale); e != nil || stale {
		t.Fatalf("changed TVDb identity received stale presence: stale=%t err=%v", stale, e)
	}
}

func TestSiloSyncManyReleasesUsesOneSnapshot(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.Method != "GET" {
			t.Errorf("unexpected mutation %s", r.Method)
		}
		switch r.URL.Path {
		case "/base/api/v2/profiles":
			fmt.Fprint(w, `{"items":[{"id":"primary","is_primary":true}]}`)
		case "/base/api/v2/catalog":
			if r.URL.Query().Get("cursor") == "next" {
				fmt.Fprint(w, `{"items":[{"content_id":"metadata","type":"movie"},{"content_id":"series:synthetic","type":"series"}],"page":{"has_more":false}}`)
			} else {
				fmt.Fprint(w, `{"items":[{"content_id":"movie:synthetic","type":"movie"}],"page":{"has_more":true,"next_cursor":"next"}}`)
			}
		case "/base/api/v2/catalog/items/movie:synthetic":
			fmt.Fprint(w, `{"content_id":"movie:synthetic","type":"movie","tmdb_id":"42"}`)
		case "/base/api/v2/catalog/items/metadata":
			fmt.Fprint(w, `{"content_id":"metadata","type":"movie","tmdb_id":"43"}`)
		case "/base/api/v2/catalog/items/series:synthetic":
			fmt.Fprint(w, `{"content_id":"series:synthetic","type":"series","tvdb_id":"42"}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := defaultSettings()
	s.Silo = SiloSettings{URL: srv.URL + "/base", APIKey: "synthetic-key", Enabled: true}
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec(ctx, `INSERT INTO releases(canonical_key,media_type,tmdb_id,data) SELECT 'movie:tmdb:'||i,'movie',i::text,jsonb_build_object('title','Synthetic','type','movie','tmdb_id',i::text) FROM generate_series(1,80) i`); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec(ctx, `INSERT INTO releases(canonical_key,media_type,data) VALUES('show:tvdb:42','show','{"tvdb_id":"42"}')`); e != nil {
		t.Fatal(e)
	}
	if e := a.syncSilo(ctx); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		status string
		want   int
	}{{"available", 3}, {"absent", 78}} {
		var n int
		if e := a.db.QueryRow(ctx, `SELECT count(*) FROM releases WHERE data->'local_availability'->>'status'=$1`, tc.status).Scan(&n); e != nil || n != tc.want {
			t.Fatalf("%s=%d %v", tc.status, n, e)
		}
	}
	var link, checkedAt string
	if e := a.db.QueryRow(ctx, `SELECT data->'local_availability'->>'url',data->'local_availability'->>'checked_at' FROM releases WHERE tmdb_id='42'`).Scan(&link, &checkedAt); e != nil || link != srv.URL+"/base/item/movie:synthetic" || checkedAt == "" {
		t.Fatal("catalog presence must have a safe URL and check timestamp", link, checkedAt, e)
	}
	if e := a.db.QueryRow(ctx, `SELECT data->'local_availability'->>'url' FROM releases WHERE canonical_key='show:tvdb:42'`).Scan(&link); e != nil || link != srv.URL+"/base/item/series:synthetic" {
		t.Fatal("TVDb-only show did not persist exact catalog presence", link, e)
	}
	if calls["/base/api/v2/catalog"] != 2 || calls["/base/api/v2/profiles"] != 1 || calls["/base/api/v2/catalog/items/movie:synthetic"] != 1 || calls["/base/api/v2/catalog/items/series:synthetic"] != 1 || len(calls) != 5 {
		t.Fatalf("snapshot not reused %+v", calls)
	}
}

func TestSiloSyncMissingAndMalformedIdentityNeverAbsent(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	s := defaultSettings()
	s.Silo = SiloSettings{URL: "http://127.0.0.1:1", APIKey: "synthetic-key", Enabled: true}
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec(ctx, `INSERT INTO releases(canonical_key,media_type,tmdb_id,data) VALUES('synthetic:missing','movie','','{"title":"Missing"}'),('synthetic:malformed','movie','042','{"title":"Malformed"}')`); e != nil {
		t.Fatal(e)
	}
	if e := a.syncSilo(ctx); e == nil {
		t.Fatal("malformed identity not reported")
	}
	for _, tc := range []struct{ key, status string }{{"synthetic:missing", "unknown"}, {"synthetic:malformed", "error"}} {
		var got string
		if e := a.db.QueryRow(ctx, `SELECT data->'local_availability'->>'status' FROM releases WHERE canonical_key=$1`, tc.key).Scan(&got); e != nil || got != tc.status {
			t.Fatalf("%s=%s %v", tc.key, got, e)
		}
	}
}

func TestSiloSyncCatalogPresenceRetainsPriorEvidenceOnAPIFailure(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	mode := "available"
	catalogCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Errorf("unsafe Silo request")
			w.WriteHeader(400)
			return
		}
		if mode == "error" {
			http.Error(w, "remote response must not leak", 503)
			return
		}
		switch r.URL.Path {
		case "/api/v2/profiles":
			fmt.Fprint(w, `{"items":[{"id":"primary","is_primary":true}]}`)
		case "/api/v2/catalog":
			catalogCalls++
			if mode == "absent" {
				fmt.Fprint(w, `{"items":[],"page":{"has_more":false}}`)
			} else {
				fmt.Fprint(w, `{"items":[{"content_id":"local","type":"movie"}],"page":{"has_more":false}}`)
			}
		case "/api/v2/catalog/items/local":
			fmt.Fprint(w, `{"content_id":"local","type":"movie","tmdb_id":"42","imdb_id":"tt0000042"}`)

		default:
			t.Errorf("unexpected Silo path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := defaultSettings()
	s.Silo = SiloSettings{URL: srv.URL, APIKey: "synthetic-key", Enabled: true}
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	r, _, e := normalizeRelease(json.RawMessage(`{"title":"Example","type":"movie","tmdbid":42,"imdb_id":"tt0000042"}`), "movie")
	if e != nil {
		t.Fatal(e)
	}
	r, e = a.saveRelease(ctx, r, nil)
	if e != nil {
		t.Fatal(e)
	}
	read := func() map[string]any {
		t.Helper()
		var raw []byte
		if e := a.db.QueryRow(ctx, `SELECT data->'local_availability' FROM releases WHERE id=$1`, r.ID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var result map[string]any
		if e := json.Unmarshal(raw, &result); e != nil {
			t.Fatal(e)
		}
		return result
	}
	if e = a.syncSilo(ctx); e != nil {
		t.Fatal(e)
	}
	got := read()
	if got["status"] != "available" || got["url"] != srv.URL+"/item/local" || got["error"] != nil {
		t.Fatalf("catalog presence not reported: %+v", got)
	}
	presentAt := got["checked_at"]
	mode = "error"
	if e = a.syncSilo(ctx); e == nil {
		t.Fatal("API failure reported success")
	}
	got = read()
	if got["status"] != "available" || got["url"] != srv.URL+"/item/local" || got["checked_at"] != presentAt || got["last_check_status"] != "error" || got["last_attempt_at"] == nil || got["error"] == nil {
		t.Fatalf("catalog presence lost on API failure: %+v", got)
	}
	mode = "available"
	if e = a.syncSilo(ctx); e != nil {
		t.Fatal(e)
	}
	got = read()
	if got["status"] != "available" || got["error"] != nil || got["last_check_status"] != nil || got["last_attempt_at"] != nil {
		t.Fatalf("successful catalog refresh retained failure diagnostics: %+v", got)
	}
	// Existing catalog status/URL/timestamp must survive a provider failure.
	prior := map[string]any{"status": "available", "url": srv.URL + "/item/local", "checked_at": "2026-01-02T03:04:05Z"}
	priorJSON, e := json.Marshal(prior)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.db.Exec(ctx, `UPDATE releases SET data=jsonb_set(data,'{local_availability}',$2::jsonb) WHERE id=$1`, r.ID, priorJSON); e != nil {
		t.Fatal(e)
	}
	mode = "error"
	if e = a.syncSilo(ctx); e == nil {
		t.Fatal("failure reported success")
	}
	got = read()
	if got["status"] != prior["status"] || got["url"] != prior["url"] || got["checked_at"] != prior["checked_at"] || got["last_check_status"] != "error" || got["error"] == nil {
		t.Fatalf("cached presence lost: %+v", got)
	}
	mode = "absent"
	if e = a.syncSilo(ctx); e != nil {
		t.Fatal(e)
	}
	got = read()
	if got["status"] != "absent" || got["url"] != "" || got["error"] != nil {
		t.Fatalf("verified absent not applied: %+v", got)
	}
	absentAt := got["checked_at"]
	mode = "error"
	if e = a.syncSilo(ctx); e == nil {
		t.Fatal("API failure reported success")
	}
	got = read()
	if got["status"] != "absent" || got["url"] != "" || got["checked_at"] != absentAt || got["last_check_status"] != "error" {
		t.Fatalf("absent evidence lost on API failure: %+v", got)
	}
	if catalogCalls != 3 {
		t.Fatalf("expected fresh snapshot each run, got %d", catalogCalls)
	}
}
