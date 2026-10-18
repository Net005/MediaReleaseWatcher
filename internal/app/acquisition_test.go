package app

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAcquireRefusesIncompatibleTargetWithoutExternalWrite(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	r, _, _ := normalizeRelease(json.RawMessage(`{"title":"Test","tmdbid":42,"type":"movie"}`), "movie")
	r, _ = a.saveRelease(ctx, r, nil)
	if _, e := a.acquire(ctx, r, Settings{}, "sonarr", false, 0, ""); e == nil {
		t.Fatal("movie accepted by sonarr")
	}
	if _, e := a.acquire(ctx, r, Settings{}, "radarr", false, 0, ""); e == nil {
		t.Fatal("unconfigured integration accepted")
	}
}
func TestAutomationDisabledDurablySuppressesHistoricalBackfill(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, _ := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Person", Monitored: true, AutoAdd: true, Notify: true})
	r, ids, _ := normalizeRelease(json.RawMessage(`{"title":"Test","tmdbid":42,"type":"movie","released":"2020-01-01","cast":[{"id":123}]}`), "movie")
	r, _ = a.saveRelease(ctx, r, ids)
	if e := a.automate(ctx, defaultSettings(), false); e != nil {
		t.Fatal(e)
	}
	var n int
	if e := a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE release_id=$1 AND status='skipped'`, r.ID).Scan(&n); e != nil || n != 2 {
		t.Fatal(n, e, p.ID)
	}
}
