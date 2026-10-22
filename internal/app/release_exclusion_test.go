package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPostgresVerifiedExclusionCreditResolution(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if _, e := a.saveActress(ctx, Actress{TMDbID: "710", Name: "Actor"}); e != nil {
		t.Fatal(e)
	}
	c := tmdbCreditCache{Fetched: time.Now(), Credits: []tmdbCredit{{ID: "713", Kind: "show", Attempts: 3, NextRetry: time.Now().Add(time.Hour), Error: "old failure"}}}
	raw, _ := json.Marshal(c)
	if _, e := a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('tmdb:actor:710',$1),('release_exclusion:show:713','{"canonical_tmdb_id":"711"}')`, raw); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/person/710" {
			t.Errorf("excluded credit requested provider: %s", r.URL.Path)
		}
		w.Write([]byte(`{"id":710,"name":"Actor"}`))
	}))
	defer srv.Close()
	a.cfg.TMDBBaseURL = srv.URL
	a.cfg.MDBListBaseURL = srv.URL
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	if n, e := a.syncTMDBCredits(ctx, s); e != nil || n != 0 {
		t.Fatalf("excluded credit pending %d %v", n, e)
	}
	if e := a.db.QueryRow(ctx, `SELECT value FROM app_state WHERE key='tmdb:actor:710'`).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	c = tmdbCreditCache{}
	if e := json.Unmarshal(raw, &c); e != nil {
		t.Fatal(e)
	}
	if !c.Complete || !c.Credits[0].Resolved || c.Credits[0].Error != "" || !c.Credits[0].NextRetry.IsZero() {
		t.Fatalf("excluded credit retry loop %+v", c)
	}
	progress, e := a.tmdbCreditProgress(ctx, time.Now())
	if e != nil || progress.PendingCredits != 0 || progress.DeferredCredits != 0 || progress.ErrorActors != 0 {
		t.Fatalf("progress %+v %v", progress, e)
	}
}

func TestPostgresVerifiedExclusionScheduledSearch(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	bad, e := a.saveRelease(ctx, Release{Type: "show", TMDbID: "713", TVDbID: "714", Title: "Companion"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.db.Exec(ctx, `UPDATE releases SET monitored=true,monitoring_override=true WHERE id=$1;`, bad.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('release_exclusion:show:713','{}')`); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("excluded scheduled search reached manager: %s", r.URL.Path)
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	s := defaultSettings()
	s.Automation.Enabled = true
	s.Sonarr.Enabled = true
	s.Sonarr.Search = true
	s.Sonarr.URL = srv.URL
	s.Sonarr.APIKey = "synthetic"
	if e = a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	if _, e = a.db.Exec(ctx, `UPDATE releases SET monitored=true WHERE id=$1`, bad.ID); e != nil {
		t.Fatal(e)
	}
	if e = a.searchMissing(ctx, Task{AllowDownloadSearch: true}); e != nil {
		t.Fatal(e)
	}
}

func TestPostgresVerifiedExclusionGuards(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	bad, e := a.saveRelease(ctx, Release{Type: "show", TMDbID: "713", Title: "Companion"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('release_exclusion:show:713','{"canonical_tmdb_id":"711","reason":"verified duplicate special"}')`)
	if e != nil {
		t.Fatal(e)
	}
	for _, handler := range []func(http.ResponseWriter, *http.Request){a.releaseDetail, a.patchRelease, a.acquireRelease, a.releaseCast} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"monitored":true,"target":"sonarr"}`))
		req.SetPathValue("id", strconv.FormatInt(bad.ID, 10))
		w := httptest.NewRecorder()
		handler(w, req)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "711") {
			t.Fatalf("expected exclusion with canonical reference, got %d %s", w.Code, w.Body.String())
		}
	}
	_, e = a.acquire(ctx, bad, defaultSettings(), "sonarr", false, 0, "")
	if e == nil || !strings.Contains(e.Error(), "excluded") {
		t.Fatalf("direct acquisition not excluded: %v", e)
	}
	actor, err := a.saveActress(ctx, Actress{TMDbID: "710", Name: "Actor"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.saveRelease(ctx, bad, []string{"710"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"scope":"all"}`))
	req.SetPathValue("id", strconv.FormatInt(actor.ID, 10))
	w := httptest.NewRecorder()
	a.acquireActress(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"results":[]`) {
		t.Fatalf("bulk did not exclude %d %s", w.Code, w.Body.String())
	}
	if e = a.automate(ctx, defaultSettings(), false); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE release_id=$1`, bad.ID).Scan(&n); e != nil || n != 0 {
		t.Fatalf("excluded automation created actions %d %v", n, e)
	}
	if _, e = a.db.Exec(ctx, `INSERT INTO actions(release_id,kind,status,event_key) VALUES($1,'acquire','failed','discovered'),($1,'notify','pending','discovered')`, bad.ID); e != nil {
		t.Fatal(e)
	}
	s := defaultSettings()
	s.Automation.Enabled = true
	if e = a.automate(ctx, s, false); e != nil {
		t.Fatal(e)
	}
	if e = a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE release_id=$1 AND status='skipped'`, bad.ID).Scan(&n); e != nil || n != 2 {
		t.Fatalf("queued exclusions not retired %d %v", n, e)
	}
}

func TestPostgresVerifiedExclusionVisibility(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, e := a.saveActress(ctx, Actress{TMDbID: "710", Name: "Actor", Monitored: true})
	if e != nil {
		t.Fatal(e)
	}
	parent, e := a.saveRelease(ctx, Release{Type: "show", TMDbID: "711", IMDbID: "tt711", TVDbID: "712", Title: "Same title", ReleaseDate: "2099-01-01"}, []string{"710"})
	if e != nil {
		t.Fatal(e)
	}
	bad, e := a.saveRelease(ctx, Release{Type: "show", TMDbID: "713", Title: "Same title", ReleaseDate: "2099-01-01"}, []string{"710"})
	if e != nil {
		t.Fatal(e)
	}
	page, e := a.releases(ctx, releaseFilter{})
	if e != nil || page.Total != 2 {
		t.Fatalf("distinct identity must remain: %+v %v", page, e)
	}
	_, e = a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('release_exclusion:show:713','{"reason":"verified duplicate special","canonical_tmdb_id":"711","season":0,"episode":1,"source_urls":["https://example.test/proof"]}')`)
	if e != nil {
		t.Fatal(e)
	}
	page, e = a.releases(ctx, releaseFilter{})
	if e != nil || page.Total != 1 || page.Items[0].ID != parent.ID {
		t.Fatalf("excluded catalog: %+v %v", page, e)
	}
	personReleases, e := a.releasesForPerson(ctx, "710")
	if e != nil || len(personReleases) != 1 {
		t.Fatalf("person releases %+v %v", personReleases, e)
	}
	matched, e := a.allMatched(ctx, p.ID)
	if e != nil || len(matched) != 1 {
		t.Fatalf("matched %+v %v", matched, e)
	}
	gotP, e := a.getActress(ctx, p.ID)
	if e != nil || gotP.ReleaseCount != 1 || gotP.UpcomingCount != 1 {
		t.Fatalf("counts %+v %v", gotP, e)
	}
	if _, e = a.db.Exec(ctx, `UPDATE releases SET monitored=false,monitoring_override=false,acquisition='{"sonarr":"already_exists","sonarr_id":"1234","sonarr_ready":"true","sonarr_file_count":"4"}',data=data||'{"local_availability":{"status":"available","url":"https://example.test/local"},"source_links":["https://example.test/canonical"]}'::jsonb WHERE id=$1`, parent.ID); e != nil {
		t.Fatal(e)
	}
	before, e := a.getRelease(ctx, parent.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.db.Exec(ctx, `UPDATE releases SET monitoring_override=true WHERE id=$1`, bad.ID)
	if e != nil {
		t.Fatal(e)
	}
	refreshed, e := a.saveRelease(ctx, bad, []string{"710"})
	if e != nil {
		t.Fatal(e)
	}
	if refreshed.Monitored || refreshed.TMDbID != "713" || refreshed.IMDbID != "" || refreshed.TVDbID != "" {
		t.Fatalf("refresh resurrected or replaced identity %+v", refreshed)
	}
	after, e := a.getRelease(ctx, parent.ID)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("canonical mutated %v", e)
	}
	restarted, err := New(ctx, Config{DatabaseURL: os.Getenv("TEST_DATABASE_URL"), Secret: "0123456789abcdef0123456789abcdef", DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	page, e = restarted.releases(ctx, releaseFilter{})
	if e != nil || page.Total != 1 {
		t.Fatalf("restart resurrected %+v %v", page, e)
	}
	page, e = a.releases(ctx, releaseFilter{})
	if e != nil || page.Total != 1 {
		t.Fatalf("refresh visibility %+v %v", page, e)
	}
}
