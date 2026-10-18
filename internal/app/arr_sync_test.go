package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"media-release-watcher/internal/integrations"
)

func TestArrPresenceRoutesAndSiloConnectionAreReadOnly(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var mutations atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations.Add(1)
			w.WriteHeader(400)
			return
		}
		switch r.URL.Path {
		case "/api/v3/movie", "/api/v3/series":
			fmt.Fprint(w, `[]`)
		case "/api/v2/profiles":
			fmt.Fprint(w, `{"items":[{"id":"primary","is_primary":true}]}`)
		case "/api/v2/catalog":
			fmt.Fprint(w, `{"items":[],"page":{"has_more":false}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := defaultSettings()
	s.Radarr = ArrSettings{URL: srv.URL, APIKey: "synthetic-key", Enabled: false}
	s.Sonarr = s.Radarr
	s.Silo = SiloSettings{URL: srv.URL, APIKey: "synthetic-key"}
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	mux := http.NewServeMux()
	a.registerArrSyncRoutes(mux)
	a.registerSiloRoutes(mux)
	request := func(method, path string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
	}
	for _, kind := range []string{"radarr", "sonarr", "silo"} {
		request("GET", "/api/integrations/"+kind+"/test", 200)
	}
	request("POST", "/api/integrations/silo/test", 200)
	request("POST", "/api/silo/sync", 400)
	s.Silo.Enabled = true
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	request("POST", "/api/arr/sync", 202)
	request("POST", "/api/silo/sync", 202)
	a.wg.Wait()
	if mutations.Load() != 0 {
		t.Fatal("read-only routes caused external mutations")
	}
	a.cancel()
	request("POST", "/api/arr/sync", 503)
	request("POST", "/api/silo/sync", 503)
}

func TestArrSearchLeaseRequiresActualOwnershipAndSerializesProbes(t *testing.T) {
	a := schedulerApp(t)
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	lease, err := a.searchPresenceLease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.release()
	if err := lease.check(ctx); err != nil {
		t.Fatal(err)
	}
	var probes sync.WaitGroup
	for i := 0; i < 8; i++ {
		probes.Add(1)
		go func() {
			defer probes.Done()
			for lease.check(ctx) == nil {
				select {
				case <-time.After(5 * time.Millisecond):
				case <-lease.ctx.Done():
					return
				}
			}
		}()
	}
	// Exercise both the periodic monitor and synchronous checks on the same
	// non-concurrency-safe pgx connection before dropping just the lock.
	time.Sleep(1100 * time.Millisecond)
	lease.mu.Lock()
	var unlocked bool
	err = lease.conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, arrSearchLeaseKey).Scan(&unlocked)
	lease.mu.Unlock()
	if err != nil || !unlocked {
		t.Fatalf("unlock: %t %v", unlocked, err)
	}
	if err := lease.check(ctx); err == nil || lease.ctx.Err() == nil {
		t.Fatalf("live session without ownership accepted: %v", err)
	}
	probes.Wait()
	lease.release()
	lease.release() // Idempotent cleanup must return its single pool connection.
	if a.db.Stat().AcquiredConns() != 0 {
		t.Fatal("search lease leaked pool capacity")
	}
}

func TestArrPresenceDoesNotStartAfterShutdown(t *testing.T) {
	a := testApp(t)
	a.cancel()
	var calls atomic.Int32
	a.startPresenceSync("arr", func(context.Context) error { calls.Add(1); return nil })
	a.wg.Wait()
	if calls.Load() != 0 {
		t.Fatal("presence sync started after shutdown")
	}
}

func TestArrFailurePreservesEvidenceTimeAndMarksUnknown(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	r, _, e := normalizeRelease(json.RawMessage(`{"title":"Synthetic","type":"movie","tmdbid":42}`), "movie")
	if e != nil {
		t.Fatal(e)
	}
	r, e = a.saveRelease(ctx, r, nil)
	if e != nil {
		t.Fatal(e)
	}
	read := func() map[string]string {
		t.Helper()
		var raw []byte
		if e := a.db.QueryRow(ctx, `SELECT acquisition FROM releases WHERE id=$1`, r.ID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var m map[string]string
		if e := json.Unmarshal(raw, &m); e != nil {
			t.Fatal(e)
		}
		return m
	}
	if e = a.saveArrPresence(ctx, r, "radarr", integrations.ArrPresence{}, fmt.Errorf("synthetic failure")); e != nil {
		t.Fatal(e)
	}
	if got := read(); got["radarr_status"] != "error" || got["radarr_checked_at"] != "" || got["radarr_error"] == "" {
		t.Fatal("first error not explicit", got)
	}
	if e = a.saveArrPresence(ctx, r, "radarr", integrations.ArrPresence{ID: 7}, nil); e != nil {
		t.Fatal(e)
	}
	verified := read()
	if e = a.saveArrPresence(ctx, r, "radarr", integrations.ArrPresence{}, fmt.Errorf("synthetic failure")); e != nil {
		t.Fatal(e)
	}
	got := read()
	if got["radarr_status"] != "present" || got["radarr_checked_at"] != verified["radarr_checked_at"] || got["radarr_last_check_status"] != "error" || got["radarr_last_attempt_at"] == "" {
		t.Fatal("error altered evidence", got, verified)
	}
}

func TestArrPresenceSyncManyReleasesUsesOneFullInventoryPerManager(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.Method != "GET" || r.Header.Get("X-Api-Key") != "synthetic-key" {
			t.Error("unsafe inventory request")
		}
		if r.URL.Path != "/api/v3/movie" && r.URL.Path != "/api/v3/series" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		rows := []map[string]any{}
		for i := 1; i <= 75; i++ {
			rows = append(rows, map[string]any{"id": i, "tmdbId": i, "tvdbId": i + 100, "hasFile": false})
		}
		json.NewEncoder(w).Encode(rows)
	}))
	defer srv.Close()
	s := defaultSettings()
	s.Radarr = ArrSettings{URL: srv.URL, APIKey: "synthetic-key", Enabled: false}
	s.Sonarr = s.Radarr
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"movie", "show"} {
		for i := 1; i <= 75; i++ {
			raw := fmt.Sprintf(`{"title":"Synthetic %s %d","type":%q,"tmdbid":%d,"tvdbid":%d}`, kind, i, kind, i, i+100)
			r, _, e := normalizeRelease(json.RawMessage(raw), kind)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = a.saveRelease(ctx, r, nil); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e := a.syncArr(ctx); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"radarr", "sonarr"} {
		var count int
		if e := a.db.QueryRow(ctx, `SELECT count(*) FROM releases WHERE acquisition->>$1='already_exists' AND acquisition->>$2='present' AND acquisition->>$3='false'`, kind, kind+"_status", kind+"_ready").Scan(&count); e != nil || count != 75 {
			t.Fatalf("%s reconciled=%d error=%v", kind, count, e)
		}
	}
	var local int
	if e := a.db.QueryRow(ctx, `SELECT count(*) FROM releases WHERE COALESCE(data->'local_availability'->>'status','unknown')<>'unknown'`).Scan(&local); e != nil || local != 0 {
		t.Fatalf("Arr incorrectly set local files: %d %v", local, e)
	}
	if calls["/api/v3/movie"] != 1 || calls["/api/v3/series"] != 1 || len(calls) != 2 {
		t.Fatalf("not one full inventory each: %+v", calls)
	}
}

func TestArrPresenceSyncDisabledManagersReadOnlyAndPreservesErrors(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	counts := map[string]int{}
	mode := "present"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counts[r.URL.Path]++
		if r.Method != "GET" {
			t.Errorf("inventory attempted %s", r.Method)
			w.WriteHeader(400)
			return
		}
		if mode == "error" {
			http.Error(w, "do not expose remote response", 503)
			return
		}
		if mode == "absent" {
			fmt.Fprint(w, `[]`)
			return
		}
		switch r.URL.Path {
		case "/api/v3/movie":
			fmt.Fprint(w, `[{"id":7,"tmdbId":42,"imdbId":"tt0000042","hasFile":true,"movieFile":{"id":8,"size":100}}]`)
		case "/api/v3/series":
			fmt.Fprint(w, `[{"id":11,"tvdbId":100,"imdbId":"tt0000100","statistics":{"episodeFileCount":3}}]`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := defaultSettings()
	s.Radarr = ArrSettings{URL: srv.URL, APIKey: "synthetic-key", Enabled: false}
	s.Sonarr = s.Radarr
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	var releases []Release
	for _, raw := range []string{`{"title":"Movie","type":"movie","tmdbid":42,"imdb_id":"tt0000042"}`, `{"title":"Show","type":"show","tmdbid":50,"tvdbid":100,"imdb_id":"tt0000100"}`} {
		r, _, e := normalizeRelease(json.RawMessage(raw), "")
		if e != nil {
			t.Fatal(e)
		}
		r, e = a.saveRelease(ctx, r, nil)
		if e != nil {
			t.Fatal(e)
		}
		releases = append(releases, r)
	}
	if e := a.syncArr(ctx); e != nil {
		t.Fatal(e)
	}
	for i, kind := range []string{"radarr", "sonarr"} {
		r, e := a.getRelease(ctx, releases[i].ID)
		if e != nil {
			t.Fatal(e)
		}
		if r.Acquisition[kind] != "already_exists" || r.Acquisition[kind+"_id"] == "" || r.Acquisition[kind+"_ready"] != "true" {
			t.Fatalf("missing verified membership: %+v", r.Acquisition)
		}
	}
	if counts["/api/v3/movie"] != 1 || counts["/api/v3/series"] != 1 {
		t.Fatalf("not one inventory each: %+v", counts)
	}
	mode = "error"
	if e := a.syncArr(ctx); e == nil {
		t.Fatal("failed inventories reported success")
	}
	r, e := a.getRelease(ctx, releases[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	if r.Acquisition["radarr"] != "already_exists" || r.Acquisition["radarr_id"] != "7" {
		t.Fatal("failure cleared cached presence", r.Acquisition)
	}
	mode = "absent"
	if e := a.syncArr(ctx); e != nil {
		t.Fatal(e)
	}
	r, e = a.getRelease(ctx, releases[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	if r.Acquisition["radarr"] != "" || r.Acquisition["radarr_id"] != "" {
		t.Fatal("verified absence did not clear", r.Acquisition)
	}
}
