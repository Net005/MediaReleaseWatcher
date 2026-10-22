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

func TestExistingCreditCatchupRefreshesCompleteAfter24Hours(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var creditsCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "combined_credits"):
			creditsCalls.Add(1)
			fmt.Fprint(w, `{"id":140335,"cast":[{"id":999002,"media_type":"tv"}]}`)
		case r.Method == "POST":
			fmt.Fprint(w, `[{"title":"New verified show","type":"show","ids":{"tmdb":999002}}]`)
		default:
			fmt.Fprint(w, `{"id":140335,"name":"Kim You-jung"}`)
		}
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	a.cfg.MDBListBaseURL = srv.URL
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	s.MDBList.APIKey = "synthetic"
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveActress(ctx, Actress{TMDbID: "140335", Name: "Kim You-jung"}); err != nil {
		t.Fatal(err)
	}
	cache := tmdbCreditCache{Fetched: time.Now().Add(-23 * time.Hour), Complete: true}
	raw, _ := json.Marshal(cache)
	if _, err := a.db.Exec(ctx, `DELETE FROM app_state WHERE key LIKE 'credit_catchup:%'; UPDATE scheduled_tasks SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('tmdb:actor:140335',$1)`, raw); err != nil {
		t.Fatal(err)
	}
	// Start with a fresh complete cache: both startup and subsequent wakes are no-ops.
	a.Start()
	a.wakeCreditCatchup()
	time.Sleep(100 * time.Millisecond)
	if creditsCalls.Load() != 0 {
		t.Fatal("fresh complete cache fetched")
	}
	cache.Fetched = time.Now().Add(-25 * time.Hour)
	raw, _ = json.Marshal(cache)
	if _, err := a.db.Exec(ctx, `UPDATE app_state SET value=$1 WHERE key='tmdb:actor:140335'`, raw); err != nil {
		t.Fatal(err)
	}
	progress, err := a.tmdbCreditProgress(ctx, time.Now())
	if err != nil || progress.ReadyActors != 1 || progress.PendingActors != 1 {
		t.Fatalf("expired complete coverage not ready: %+v %v", progress, err)
	}
	a.wakeCreditCatchup()
	waitCatchupCount(t, a, `SELECT count(*) FROM releases WHERE tmdb_id='999002' AND media_type='show'`, 1)
	waitCatchupCount(t, a, `SELECT count(*) FROM app_state WHERE key LIKE 'credit_catchup:%'`, 0)
	if creditsCalls.Load() != 1 {
		t.Fatalf("refresh requests = %d", creditsCalls.Load())
	}
	a.wakeCreditCatchup()
	time.Sleep(100 * time.Millisecond)
	if creditsCalls.Load() != 1 {
		t.Fatal("completed refresh fetched again")
	}
}

// The historical cache has already resolved its first movie batch. The final
// show must be discovered without another combined-credits request or full sync.
func TestExistingCreditCatchupCachedKimBeyondFirstBatch(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var creditsCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "combined_credits"):
			creditsCalls.Add(1)
			t.Error("fresh cached filmography fetched again")
			fmt.Fprint(w, `{"id":140335,"cast":[]}`)
		case r.Method == "POST":
			fmt.Fprint(w, `[{"title":"100 Days of Deception","type":"show","ids":{"tmdb":999001},"released":"2027-01-01"}]`)
		default:
			fmt.Fprint(w, `{"id":140335,"name":"Kim You-jung"}`)
		}
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	a.cfg.MDBListBaseURL = srv.URL
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	s.MDBList.APIKey = "synthetic"
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	p, err := a.saveActress(ctx, Actress{TMDbID: "140335", Name: "Kim You-jung", Group: "Keep", Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	cache := tmdbCreditCache{Fetched: time.Now().UTC(), Next: 10}
	for i := 1; i <= 24; i++ {
		cache.Credits = append(cache.Credits, tmdbCredit{ID: fmt.Sprint(i), Kind: "movie", Resolved: i <= 10})
		if _, err := a.saveRelease(ctx, Release{Type: "movie", TMDbID: fmt.Sprint(i), Title: "Existing movie"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	cache.Credits = append(cache.Credits, tmdbCredit{ID: "999001", Kind: "show"})
	raw, _ := json.Marshal(cache)
	if _, err = a.db.Exec(ctx, `DELETE FROM app_state WHERE key LIKE 'credit_catchup:%'; UPDATE scheduled_tasks SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('tmdb:actor:140335',$1)`, raw); err != nil {
		t.Fatal(err)
	}
	a.Start()
	waitCatchupCount(t, a, `SELECT count(*) FROM releases WHERE tmdb_id='999001' AND media_type='show'`, 1)
	waitCatchupCount(t, a, `SELECT count(*) FROM app_state WHERE key LIKE 'credit_catchup:%'`, 0)
	waitCatchupCount(t, a, `SELECT count(*) FROM release_people rp JOIN releases r ON r.id=rp.release_id WHERE r.tmdb_id='999001' AND r.media_type='show'`, 1)
	waitCatchupCount(t, a, `SELECT count(*) FROM releases WHERE tmdb_id='999001' AND COALESCE((data->>'monitored')::boolean,false)`, 0)
	got, err := a.getActress(ctx, p.ID)
	if err != nil || got.Group != "Keep" || !got.Notify {
		t.Fatalf("preferences changed: %+v %v", got, err)
	}
	if creditsCalls.Load() != 0 {
		t.Fatal("fresh cache refetched")
	}
}
