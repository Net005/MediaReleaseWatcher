package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMDBListNonIMDbPlaceholderUsesIndependentTMDbIdentity(t *testing.T) {
	raw := json.RawMessage(`{"id":101,"mediatype":"movie","title":"Synthetic provider fixture","imdb_id":"tr9001","ids":{"tmdb":101,"imdb":"tr9001","mdblist":"fixture"}}`)
	r, _, err := normalizeRelease(raw, "movie")
	if err != nil {
		t.Fatalf("valid independent TMDb identity must survive a non-IMDb placeholder: %v", err)
	}
	if r.TMDbID != "101" || r.IMDbID != "" {
		t.Fatalf("IMDb placeholder must not be rewritten or accepted as IMDb: %+v", r)
	}
	if !strings.Contains(string(r.Raw), `"tr9001"`) {
		t.Fatal("literal source placeholder was not preserved in raw metadata")
	}
	if !strings.Contains(r.Coverage, "non-IMDb") {
		t.Fatal("missing explicit source identifier coverage warning")
	}
}

func TestNonIMDbPlaceholderDoesNotValidateMissingOrMalformedCanonicalIdentity(t *testing.T) {
	for _, raw := range []string{
		`{"mediatype":"movie","title":"Synthetic","imdb_id":"tr9001"}`,
		`{"mediatype":"movie","title":"Synthetic","tmdb_id":"00101","imdb_id":"tr9001"}`,
		`{"mediatype":"movie","title":"Synthetic","tmdb_id":"101","imdb_id":"tt123x"}`,
		`{"mediatype":"movie","title":"Synthetic","tmdb_id":"101","imdb_id":"tr9x"}`,
	} {
		if _, _, err := normalizeRelease(json.RawMessage(raw), "movie"); err == nil {
			t.Fatalf("invalid source token must not be silently repaired: %s", raw)
		}
	}
}
