package app

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"testing"
)

func TestArtworkRequiresFullImageDecode(t *testing.T) {
	var b bytes.Buffer
	if e := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); e != nil {
		t.Fatal(e)
	}
	if _, e := imageInfo(b.Bytes()[:33]); e == nil {
		t.Fatal("truncated PNG headers accepted as valid image")
	}
	if _, e := imageInfo(b.Bytes()); e != nil {
		t.Fatal(e)
	}
}
func TestUnknownAgeAndNonfiniteScoreRemainUnknown(t *testing.T) {
	r, _, e := normalizeRelease(json.RawMessage(`{"title":"Synthetic","type":"movie","ids":{"tmdb":42},"age_rating":"unknown","score":"NaN"}`), "movie")
	if e != nil || r.AgeRating != nil || r.Score != nil {
		t.Fatalf("invalid numeric metadata became asserted fact: %+v %v", r, e)
	}
}
func TestWrappedMediaCannotChangeRequestedType(t *testing.T) {
	if _, _, e := normalizeRelease(json.RawMessage(`{"show":{"title":"Synthetic","ids":{"tmdb":42}}}`), "movie"); e == nil {
		t.Fatal("show accepted for requested movie")
	}
}
