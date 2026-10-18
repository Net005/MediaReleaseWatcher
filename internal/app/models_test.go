package app

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNormalizeMetadataUsesActualCastNotName(t *testing.T) {
	r, people, err := normalizeRelease(json.RawMessage(`{"title":"Test","type":"movie","ids":{"tmdb":42,"imdb":"tt1234567"},"released":"2026-10-01","cast":[{"id":123,"name":"Person"}],"score":0,"age_rating":16,"certification":"R"}`), "movie")
	if err != nil {
		t.Fatal(err)
	}
	if r.TMDbID != "42" || r.Score == nil || *r.Score != 0 || len(people) != 1 || people[0] != "123" || r.AgeRating == nil || *r.AgeRating != 16 {
		t.Fatalf("bad normalization: %+v %v", r, people)
	}
	_, people, err = normalizeRelease(json.RawMessage(`{"title":"Test Person","type":"movie","tmdbid":42,"description":"Person stars","keywords":["Person"]}`), "movie")
	if err != nil || len(people) != 0 {
		t.Fatal("invented cast", err, people)
	}
}
func TestReleaseWindows(t *testing.T) {
	now := time.Date(2026, 10, 18, 14, 0, 0, 0, time.UTC)
	for _, x := range []struct{ date, want string }{{"2026-10-19", "upcoming"}, {"2026-10-18", "current"}, {"2026-09-18", "current"}, {"2026-09-17", "past"}, {"", "unknown"}} {
		if got := releaseWindow(x.date, now); got != x.want {
			t.Errorf("%s got %s want %s", x.date, got, x.want)
		}
	}
}
