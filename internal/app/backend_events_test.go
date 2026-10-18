package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNotificationContainsCoverInfoWithSafeLength(t *testing.T) {
	age := 16
	r := Release{Title: "Synthetic", Type: "movie", Year: 2026, Country: "KR", ReleaseDate: "2027-01-02", Certification: "R", AgeRating: &age, Description: strings.Repeat("界", 2000), Actresses: []PersonLink{{Name: "Supplied Person"}}}
	msg := notificationMessage(r)
	if utf8.RuneCountInString(msg) > 1024 || !utf8.ValidString(msg) {
		t.Fatal("unsafe notification size")
	}
	for _, s := range []string{"2026", "KR", "2027-01-02", "Certification: R", "Suggested age: 16", "Supplied Person", "界"} {
		if !strings.Contains(msg, s) {
			t.Errorf("missing %q: %s", s, msg)
		}
	}
	unknown := notificationMessage(Release{})
	if !strings.Contains(unknown, "Certification: unknown") || !strings.Contains(unknown, "Suggested age: unknown") {
		t.Fatal(unknown)
	}
}
func TestAutomationEventTracksActualReleaseOnce(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	if releaseEvent(Release{ReleaseDate: "2026-10-19"}, now) == releaseEvent(Release{ReleaseDate: "2026-10-18"}, now) {
		t.Fatal("upcoming baseline suppresses actual release")
	}
	if releaseEvent(Release{ReleaseDate: "2026-10-17"}, now) != releaseEvent(Release{ReleaseDate: "2026-10-18"}, now) {
		t.Fatal("date correction duplicates release event")
	}
}
func TestPostgresUpcomingBaselineAllowsActualReleaseEvent(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	_, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic Person", Monitored: true, AutoAdd: true, Notify: true})
	if e != nil {
		t.Fatal(e)
	}
	raw := fmt.Sprintf(`{"title":"Synthetic","type":"movie","ids":{"tmdb":42},"released":%q,"cast":[{"id":123}]}`, time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02"))
	r, people, _ := normalizeRelease(json.RawMessage(raw), "movie")
	r, e = a.saveRelease(ctx, r, people)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.automate(ctx, defaultSettings(), true); e != nil {
		t.Fatal(e)
	}
	r.ReleaseDate = time.Now().UTC().Format("2006-01-02")
	r, e = a.saveRelease(ctx, r, nil)
	if e != nil {
		t.Fatal(e)
	}
	// Disabled actions are durably skipped; there must still be a new actual-release event.
	if e = a.automate(ctx, defaultSettings(), false); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE release_id=$1`, r.ID).Scan(&n); e != nil || n != 4 {
		t.Fatalf("release event not recorded separately: %d %v", n, e)
	}
}
