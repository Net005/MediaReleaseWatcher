package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresOptOutPermanentlySuppressesQueuedBacklog(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic Person", Monitored: false, Notify: false})
	if e != nil {
		t.Fatal(e)
	}
	r, _, _ := normalizeRelease(json.RawMessage(`{"title":"Synthetic","type":"movie","ids":{"tmdb":42}}`), "movie")
	r.ReleaseDate = time.Now().UTC().Format("2006-01-02")
	r, e = a.saveRelease(ctx, r, []string{p.TMDbID})
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.db.Exec(ctx, `INSERT INTO actions(release_id,kind,status,event_key) VALUES($1,'notify','pending','released')`, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	s := defaultSettings()
	s.Automation.Enabled = true
	s.Pushover.Enabled = true
	if e = a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	if e = a.automate(ctx, s, false); e != nil {
		t.Fatal(e)
	}
	var status string
	if e = a.db.QueryRow(ctx, `SELECT status FROM actions WHERE release_id=$1 AND kind='notify'`, r.ID).Scan(&status); e != nil || status != "skipped" {
		t.Fatalf("opt-out leaves replayable backlog: %q %v", status, e)
	}
}
func TestPostgresWatcherPatchPreservesRichMetadataAndOtherConsent(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic Person", Biography: "rich biography", Monitored: true, Notify: true, AutoAdd: false})
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("PATCH", "http://localhost/api/actresses/1", strings.NewReader(`{"notify":false}`))
	r.SetPathValue("id", fmt.Sprint(p.ID))
	w := httptest.NewRecorder()
	a.patchActress(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got, e := a.getActress(ctx, p.ID)
	if e != nil || got.Notify || !got.Monitored || got.AutoAdd || got.Biography != "rich biography" {
		t.Fatal(got, e)
	}
}
func TestPostgresLateWatcherDoesNotReplayAnyBaselineWindow(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	for i, d := range []string{"", time.Now().UTC().Format("2006-01-02"), time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")} {
		raw := fmt.Sprintf(`{"title":"Synthetic","type":"movie","ids":{"tmdb":%d}}`, i+1)
		r, _, _ := normalizeRelease(json.RawMessage(raw), "movie")
		r.ReleaseDate = d
		if _, e := a.saveRelease(ctx, r, []string{"123"}); e != nil {
			t.Fatal(e)
		}
	}
	if e := a.automate(ctx, defaultSettings(), true); e != nil {
		t.Fatal(e)
	}
	if _, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic Person", Monitored: true, Notify: true, AutoAdd: true}); e != nil {
		t.Fatal(e)
	}
	s := defaultSettings()
	s.Automation.Enabled = true
	s.Radarr.Enabled = true
	s.Pushover.Enabled = true
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	if e := a.automate(ctx, s, false); e != nil {
		t.Fatal(e)
	}
	var n int
	if e := a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE status='baseline'`).Scan(&n); e != nil || n != 6 {
		t.Fatalf("baseline replayed after late import: %d %v", n, e)
	}
}
