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

func TestCreditRetryPostgresPersistsDeferralAndFreesActorSlots(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method == "POST" {
			fmt.Fprint(w, `[]`)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/person/")
		fmt.Fprintf(w, `{"id":%s,"name":"Person"}`, id)
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL, a.cfg.MDBListBaseURL = srv.URL, srv.URL
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	s.MDBList.APIKey = "synthetic"
	for i := 1; i <= 5; i++ {
		id := fmt.Sprint(i)
		if _, err := a.saveActress(ctx, Actress{TMDbID: id, Name: "Person"}); err != nil {
			t.Fatal(err)
		}
		cache := tmdbCreditCache{Fetched: time.Now(), Credits: []tmdbCredit{{ID: "42", Kind: "movie"}}}
		b, _ := json.Marshal(cache)
		if _, err := a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES($1,$2)`, "tmdb:actor:"+id, b); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := a.syncTMDBCredits(ctx, s); err != nil || n != 5 {
		t.Fatalf("first pending %d: %v", n, err)
	}
	var b []byte
	if err := a.db.QueryRow(ctx, `SELECT value FROM app_state WHERE key='tmdb:actor:1'`).Scan(&b); err != nil {
		t.Fatal(err)
	}
	var c tmdbCreditCache
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if c.Credits[0].Attempts != 1 || !c.Credits[0].NextRetry.After(time.Now()) || c.Complete {
		t.Fatalf("missing not durably deferred: %+v", c)
	}
	before := calls.Load()
	if _, err := a.syncTMDBCredits(ctx, s); err != nil {
		t.Fatal(err)
	}
	if delta := calls.Load() - before; delta != 2 {
		t.Fatalf("deferred actors occupied slots: %d requests, want 2 for actor 5", delta)
	}
	progress, err := a.tmdbCreditProgress(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if progress.PendingActors != 5 || progress.DeferredCredits != 5 || progress.PendingCredits != 0 || progress.ProviderBlockedCredits != 5 || progress.ErrorActors != 5 {
		t.Fatalf("inaccurate cached progress: %+v", progress)
	}
}

func TestCreditRetryPostgresDeferredTitleUsesExistingPrimary(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if _, err := a.saveActress(ctx, Actress{TMDbID: "1", Name: "Person"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveRelease(ctx, Release{TMDbID: "42", Type: "movie", Title: "Primary"}, nil); err != nil {
		t.Fatal(err)
	}
	c := tmdbCreditCache{Fetched: time.Now(), Credits: []tmdbCredit{{ID: "42", Kind: "movie", Attempts: 4, NextRetry: time.Now().Add(24 * time.Hour), Error: "MDBList title metadata unavailable"}}}
	b, _ := json.Marshal(c)
	if _, err := a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('tmdb:actor:1',$1)`, b); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("existing primary must not need MDBList")
		}
		fmt.Fprint(w, `{"id":1,"name":"Person"}`)
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	a.cfg.MDBListBaseURL = srv.URL
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	before, err := a.tmdbCreditProgress(ctx, time.Now())
	if err != nil || before.ReadyActors != 1 || before.PendingCredits != 1 || before.DeferredCredits != 0 {
		t.Fatalf("existing primary incorrectly reported deferred: %+v %v", before, err)
	}
	if pending, err := a.syncTMDBCredits(ctx, s); err != nil || pending != 0 {
		t.Fatalf("existing primary not eligible: %d %v", pending, err)
	}
	var links, baselines int
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM release_people`).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE status='baseline'`).Scan(&baselines); err != nil {
		t.Fatal(err)
	}
	if links != 1 || baselines != 0 {
		t.Fatalf("existing primary linking/baseline changed: links=%d baselines=%d", links, baselines)
	}
}

func TestCreditRetryPostgresCachedProgressDoesNotInventFailures(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	now := time.Now()
	caches := []tmdbCreditCache{
		{},
		{Fetched: now, Credits: []tmdbCredit{{ID: "1", Kind: "movie"}, {ID: "2", Kind: "show", Resolved: true}}},
		{Fetched: now, Credits: []tmdbCredit{{ID: "3", Kind: "movie", Attempts: 1, Error: "MDBList title metadata unavailable", NextRetry: now.Add(time.Hour)}}},
		{Error: "TMDb person unavailable", NextRetry: now.Add(time.Hour)},
		{Fetched: now, Complete: true, Error: "obsolete title failure"},
	}
	for i, c := range caches {
		id := fmt.Sprint(i + 1)
		if _, err := a.saveActress(ctx, Actress{TMDbID: id, Name: "Person"}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			continue
		}
		b, _ := json.Marshal(c)
		if _, err := a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES($1,$2)`, "tmdb:actor:"+id, b); err != nil {
			t.Fatal(err)
		}
	}
	p, err := a.tmdbCreditProgress(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if p.PendingActors != 4 || p.ErrorActors != 2 || p.PendingCredits != 1 || p.DeferredCredits != 1 || p.ProviderBlockedCredits != 1 || p.UnattemptedCredits != 1 || p.UnattemptedActors != 1 || p.DeferredActors != 2 || p.ReadyActors != 2 {
		t.Fatalf("unattempted, blocked, resolved coverage conflated: %+v", p)
	}
}

func TestCreditRetryPostgresIdentityErrorsBackOff(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"id":1,"name":"Different"}`)
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	if _, err := a.saveActress(ctx, Actress{TMDbID: "1", Name: "Person"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := a.syncTMDBCredits(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("identity error hammered %d times", calls.Load())
	}
	p, err := a.tmdbCreditProgress(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if p.PendingActors != 1 || p.ErrorActors != 1 || p.ProviderBlockedActors != 1 {
		t.Fatalf("identity failure not distinct: %+v", p)
	}
}
