package app

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestPostgresDeleteActorRecomputesDerivedMonitoringPreservesManual(t *testing.T) {
	a := policyApp(t)
	ctx := context.Background()
	p, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic", Monitored: true, AutoAdd: true})
	if err != nil {
		t.Fatal(err)
	}
	r := policyRelease(t, a, "42")
	if _, err = a.saveRelease(ctx, r, []string{"123"}); err != nil {
		t.Fatal(err)
	}
	manual := policyRelease(t, a, "43")
	if _, err = a.saveRelease(ctx, manual, []string{"123"}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(ctx, `UPDATE releases SET monitoring_override=true WHERE id=$1`, manual.ID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("DELETE", "/api/actresses/1", nil)
	req.SetPathValue("id", fmt.Sprint(p.ID))
	w := httptest.NewRecorder()
	a.deleteActress(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got, err := a.getRelease(ctx, r.ID)
	if err != nil || got.Monitored || got.MonitoringOrigin != "discovery" {
		t.Fatalf("removed actor left derived monitoring: %+v %v", got, err)
	}
	got, err = a.getRelease(ctx, manual.ID)
	if err != nil || !got.Monitored || got.MonitoringOrigin != "manual" {
		t.Fatalf("removed actor changed manual monitoring: %+v %v", got, err)
	}
}
