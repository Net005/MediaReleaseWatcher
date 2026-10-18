package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestPostgresReleaseAPIPreservesLocalAvailabilityAcrossMetadataRefresh(t *testing.T) {
	a := policyApp(t)
	ctx := context.Background()
	r := policyRelease(t, a, "42")
	if _, err := a.db.Exec(ctx, `UPDATE releases SET data=data||'{"local_availability":{"status":"available","url":"http://synthetic/item/local","checked_at":"2026-10-18T12:00:00Z","last_check_status":"error","error":"retry later"}}'::jsonb WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	read := func() {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/releases/1", nil)
		req.SetPathValue("id", fmt.Sprint(r.ID))
		a.releaseDetail(w, req)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var got struct {
			LocalAvailability map[string]any `json:"local_availability"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.LocalAvailability["status"] != "available" || got.LocalAvailability["url"] != "http://synthetic/item/local" || got.LocalAvailability["last_check_status"] != "error" {
			t.Fatalf("availability hidden or overwritten: %+v", got)
		}
	}
	read()
	if _, err := a.saveRelease(ctx, Release{Title: "Refreshed", Type: "movie", TMDbID: "42", Raw: []byte(`{"id":42}`)}, nil); err != nil {
		t.Fatal(err)
	}
	read()
}
