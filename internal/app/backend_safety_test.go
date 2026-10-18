package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresMetadataRefreshDoesNotOverwriteWatcherPreferences(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic Person", Group: "old", Monitored: true})
	if e != nil {
		t.Fatal(e)
	}
	stale := p
	p.Group = "new"
	p.Notify = true
	p.AutoAdd = true
	p.Monitored = false
	if _, e = a.saveActress(ctx, p); e != nil {
		t.Fatal(e)
	}
	stale.Biography = "enriched"
	if _, e = a.savePersonMetadata(ctx, stale); e != nil {
		t.Fatal(e)
	}
	got, e := a.getActress(ctx, p.ID)
	if e != nil || got.Group != "new" || !got.Notify || !got.AutoAdd || got.Monitored || got.Biography != "enriched" {
		t.Fatalf("background enrichment overwrote preferences: %+v %v", got, e)
	}
}
func TestPostgresOldMetadataSerializesListsAsArrays(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	_, e := a.db.Exec(ctx, `INSERT INTO releases(canonical_key,media_type,tmdb_id,data) VALUES('movie:tmdb:42','movie','42','{"title":"Synthetic","type":"movie","tmdb_id":"42"}')`)
	if e != nil {
		t.Fatal(e)
	}
	r, e := a.getRelease(ctx, 1)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(r)
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	for _, key := range []string{"genres", "ratings", "watch_providers", "actresses", "source_links"} {
		if _, ok := got[key].([]any); !ok {
			t.Errorf("%s was not array: %s", key, b)
		}
	}
}
func TestReadJSONRequiresObject(t *testing.T) {
	for _, body := range []string{"null", "[]", "42", "\"text\""} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "http://localhost/api/settings", strings.NewReader(body))
		var out Settings
		if readJSON(w, r, &out) {
			t.Errorf("accepted non-object: %s", body)
		}
	}
}
func TestPostgresSupersededUpcomingActionCannotNotifyAtRelease(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	_, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic Person", Monitored: true, Notify: true})
	if e != nil {
		t.Fatal(e)
	}
	r, people, _ := normalizeRelease(json.RawMessage(`{"title":"Synthetic","type":"movie","ids":{"tmdb":42},"cast":[{"id":123}]}`), "movie")
	r.ReleaseDate = time.Now().UTC().Format("2006-01-02")
	r, e = a.saveRelease(ctx, r, people)
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.db.Exec(ctx, `INSERT INTO actions(release_id,kind,status,event_key) VALUES($1,'notify','pending','upcoming'),($1,'notify','done','released')`, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	s := defaultSettings()
	s.Automation.Enabled = true
	s.Pushover.Enabled = true
	if e = a.automate(ctx, s, false); e != nil {
		t.Fatal(e)
	}
	var status string
	if e = a.db.QueryRow(ctx, `SELECT status FROM actions WHERE release_id=$1 AND event_key='upcoming'`, r.ID).Scan(&status); e != nil || status != "superseded" {
		t.Fatalf("stale notification attempted: %q %v", status, e)
	}
}
