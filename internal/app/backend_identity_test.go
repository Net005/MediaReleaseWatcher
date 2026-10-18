package app

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPostgresIdentityMismatchQuarantinesActualIDMatches(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Supplied Person", Monitored: true, AutoAdd: true})
	if e != nil {
		t.Fatal(e)
	}
	r, ids, _ := normalizeRelease(json.RawMessage(`{"title":"Synthetic","type":"movie","ids":{"tmdb":42},"cast":[{"id":123}]}`), "movie")
	r, e = a.saveRelease(ctx, r, ids)
	if e != nil {
		t.Fatal(e)
	}
	p.MetadataStatus = "unavailable: identity mismatch; supplied name and literal ID retained for manual review"
	if _, e = a.savePersonMetadata(ctx, p); e != nil {
		t.Fatal(e)
	}
	r, e = a.getRelease(ctx, r.ID)
	if e != nil || len(r.Actresses) != 0 {
		t.Fatal("wrong imported identity remains linked", r, e)
	}
	r, e = a.saveRelease(ctx, r, ids)
	if e != nil || len(r.Actresses) != 0 {
		t.Fatal("quarantined identity relinked", r, e)
	}
}
