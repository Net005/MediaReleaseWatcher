package app

import (
	"encoding/json"
	"testing"
)

func TestSparseReleaseMerge(t *testing.T) {
	old, _, _ := normalizeRelease(json.RawMessage(`{"title":"Old","type":"movie","ids":{"tmdb":42,"imdb":"tt1234567","tvdb":84},"description":"Rich","runtime":120,"genres":["Drama"],"score":80,"commonsense_media":{"parental_nudity":2}}`), "movie")
	old.SourceLinks = append(old.SourceLinks, "https://www.tvmaze.com/shows/42/synthetic")
	incoming, _, _ := normalizeRelease(json.RawMessage(`{"title":"Updated","type":"movie","ids":{"tmdb":42},"score":0}`), "movie")
	got := mergeRelease(old, incoming)
	if got.Title != "Updated" || got.Description != "Rich" || got.Runtime != 120 || got.IMDbID != old.IMDbID || got.TVDbID != "84" || len(got.Genres) != 1 || len(got.SourceLinks) != 2 || got.Score == nil || *got.Score != 0 {
		t.Fatalf("sparse merge lost data: %+v", got)
	}
	var raw map[string]any
	_ = json.Unmarshal(got.Raw, &raw)
	if raw["description"] != "Rich" {
		t.Fatal("provider raw metadata lost")
	}
}
