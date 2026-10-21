package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresSyncReportsCachedCreditProgress(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var discovery, persons, batches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/people/favorites/upcoming" || strings.HasPrefix(r.URL.Path, "/catalog/"):
			discovery.Add(1)
			fmt.Fprint(w, `{"items":[],"has_more":false}`)
		case strings.HasPrefix(r.URL.Path, "/person/"):
			persons.Add(1)
			id := strings.TrimPrefix(r.URL.Path, "/person/")
			if strings.Contains(id, "/") {
				t.Errorf("report must not fetch credits: %s", r.URL.Path)
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"id":%s,"name":"Person"}`, id)
		case r.Method == http.MethodPost && r.URL.Path == "/tmdb/movie":
			batches.Add(1)
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected provider call %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a.cfg.MDBListBaseURL, a.cfg.TMDBBaseURL = srv.URL, srv.URL
	s := defaultSettings()
	s.MDBList.APIKey = "synthetic"
	s.TMDB.Enabled, s.TMDB.APIKey = true, "synthetic"
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 1; i <= 8; i++ {
		id := fmt.Sprint(i)
		if _, err := a.saveActress(ctx, Actress{TMDbID: id, Name: "Person", Biography: "Biography", Profile: "/profile.jpg", Birthday: "1990-01-01", PlaceOfBirth: "Place", MetadataStatus: "ok"}); err != nil {
			t.Fatal(err)
		}
		cache := tmdbCreditCache{Fetched: now, Credits: []tmdbCredit{{ID: fmt.Sprint(100 + i), Kind: "movie"}}}
		if i == 8 {
			cache = tmdbCreditCache{NextRetry: now.Add(time.Hour)}
		} // Queued, never attempted; not a provider failure.
		if i == 6 {
			cache.Credits[0].Attempts = 1
			cache.Credits[0].Error = "MDBList title metadata unavailable"
			cache.Credits[0].NextRetry = now.Add(time.Hour)
		}
		if i == 7 {
			cache.Credits = nil
			cache.Error = "TMDb person unavailable"
			cache.NextRetry = now.Add(time.Hour)
		}
		raw, _ := json.Marshal(cache)
		if _, err := a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES($1,$2)`, "tmdb:actor:"+id, raw); err != nil {
			t.Fatal(err)
		}
	}
	result, err := a.syncOnceResult(ctx)
	if err != nil {
		t.Fatalf("queued credit work must not fail sync: %v", err)
	}
	if result.Status != "warning" {
		t.Fatalf("queued work status = %q; want warning", result.Status)
	}
	want := "TMDb credit discovery: 8 pending actors (1 ready, 7 deferred); 1 ready credits, 5 deferred credits; 1 unattempted actors, 1 unattempted credits; 6 provider-blocked actors, 5 provider-blocked credits (overlap ready/deferred counts, not additional work); durable continuation saved for next sync; no complete filmography claimed"
	if !strings.Contains(result.Message, want) {
		t.Errorf("returned summary missing truthful cached counts:\n%s\nwant: %s", result.Message, want)
	}
	var raw []byte
	if err := a.db.QueryRow(ctx, `SELECT value FROM app_state WHERE key='sync'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var state SyncState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.Stage != "partial" || state.Running || state.LastError != result.Message {
		t.Errorf("persisted completion differs: %+v / %+v", state, result)
	}
	var message string
	if err := a.db.QueryRow(ctx, `SELECT message FROM activity WHERE kind='sync' AND status='warning' ORDER BY id DESC LIMIT 1`).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if message != result.Message {
		t.Errorf("persisted activity = %q; returned %q", message, result.Message)
	}
	if discovery.Load() != 3 || persons.Load() != 4 || batches.Load() != 4 {
		t.Errorf("reporting added provider work: discovery=%d persons=%d batches=%d", discovery.Load(), persons.Load(), batches.Load())
	}
}
