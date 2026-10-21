package app

import (
	"context"
	"testing"
	"time"
)

func TestPostgresUpcomingMonitoringRefreshAndOverrides(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic", Monitored: true, AutoAdd: true, Group: "keep"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.saveRelease(ctx, Release{Type: "movie", TMDbID: "42", Title: "Test"}, []string{"123"})
	if err != nil || r.Monitored {
		t.Fatal(r, err)
	}
	r.ReleaseDate = "2099-01-01"
	r, err = a.saveRelease(ctx, r, []string{"123"})
	if err != nil || !r.Monitored {
		t.Fatal("date discovery", r, err)
	}
	r.ReleaseDate = time.Now().UTC().Format("2006-01-02")
	r, err = a.saveRelease(ctx, r, []string{"123"})
	if err != nil || r.Monitored {
		t.Fatal("released refresh", r, err)
	}
	// The same public source save used by backfill cannot reenable a past title.
	r, err = a.saveReleaseSource(ctx, r, []string{"123"}, "historical")
	if err != nil || r.Monitored {
		t.Fatal(r, err)
	}
	r.ReleaseDate = "2099-01-01"
	if _, err = a.db.Exec(ctx, `UPDATE releases SET monitoring_override=false WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	r, err = a.saveRelease(ctx, r, []string{"123"})
	if err != nil || r.Monitored {
		t.Fatal("false override lost", r, err)
	}
	if _, err = a.db.Exec(ctx, `UPDATE releases SET monitoring_override=true WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	r, err = a.saveRelease(ctx, r, []string{"123"})
	if err != nil || !r.Monitored {
		t.Fatal("manual true lost", r, err)
	}
	// Monitoring without actor automation remains opt-in; do not enable unrelated future titles.
	p.AutoAdd = false
	if _, err = a.saveActress(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(ctx, `UPDATE releases SET monitoring_override=NULL WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(ctx, recomputeMonitoring); err != nil {
		t.Fatal(err)
	}
	r, err = a.getRelease(ctx, r.ID)
	if err != nil || r.Monitored {
		t.Fatal("silently enabled monitor-only actor", r, err)
	}
	p.AutoAdd = true
	p.MetadataStatus = "unavailable: identity mismatch: test"
	if _, err = a.saveActress(ctx, p); err != nil {
		t.Fatal(err)
	}
	r, err = a.getRelease(ctx, r.ID)
	if err != nil || r.Monitored {
		t.Fatal("quarantine", r, err)
	}
}
