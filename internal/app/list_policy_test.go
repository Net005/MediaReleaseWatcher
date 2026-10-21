package app

import (
	"context"
	"testing"
)

func TestPostgresActorOptinMonitoringAndActivationBaseline(t *testing.T) {
	a := policyApp(t)
	ctx := context.Background()
	p, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	r := upcomingPolicyRelease(t, a, "42")
	r, err = a.saveRelease(ctx, r, []string{"123"})
	if err != nil || r.Monitored {
		t.Fatal("monitor-only actor opted in", r, err)
	}
	p.AutoAdd = true
	p.Notify = true
	if _, err = a.saveActress(ctx, p); err != nil {
		t.Fatal(err)
	}
	r, err = a.getRelease(ctx, r.ID)
	if err != nil || !r.Monitored || r.MonitoringOrigin != "actress" {
		t.Fatal(r, err)
	}
	var count int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE release_id=$1 AND status='baseline'`, r.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	n := upcomingPolicyRelease(t, a, "43")
	n, err = a.saveRelease(ctx, n, []string{"123"})
	if err != nil || !n.Monitored {
		t.Fatal(n, err)
	}
	_, err = a.db.Exec(ctx, `UPDATE releases SET monitored=false,monitoring_override=false WHERE id=$1`, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	n, err = a.saveRelease(ctx, n, []string{"123"})
	if err != nil || n.Monitored {
		t.Fatal(n, err)
	}
}

func TestPostgresListPolicyMembershipAndManualOverrides(t *testing.T) {
	a := policyApp(t)
	ctx := context.Background()
	s := defaultSettings()
	s.MDBList.ListIDs = "11,22"
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	r := upcomingPolicyRelease(t, a, "42")
	other := upcomingPolicyRelease(t, a, "43")
	if r.Monitored {
		t.Fatal("discovery automatically monitored")
	}
	if err := a.replaceListMembership(ctx, "11", []int64{r.ID}); err != nil {
		t.Fatal(err)
	}
	p, err := a.releases(ctx, releaseFilter{ListID: "11"})
	if err != nil || p.Total != 1 || p.Items[0].ID != r.ID {
		t.Fatal(p, err)
	}
	s.MDBList.Lists = []MDBListPolicy{{ID: "11", Enabled: true, Monitored: true, AutoAdd: true}, {ID: "22", Enabled: true}}
	if err = a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	r, err = a.getRelease(ctx, r.ID)
	if err != nil || !r.Monitored || r.MonitoringOrigin != "list" || len(r.Lists) != 1 {
		t.Fatal(r, err)
	}
	_, err = a.db.Exec(ctx, `UPDATE releases SET monitored=false,monitoring_override=false WHERE id=$1`, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	r, err = a.getRelease(ctx, r.ID)
	if err != nil || r.Monitored || r.MonitoringOrigin != "manual" {
		t.Fatal(r, err)
	}
	other, err = a.getRelease(ctx, other.ID)
	if err != nil || other.Monitored {
		t.Fatal(other, err)
	}
	if err = a.replaceListMembership(ctx, "11", nil); err != nil {
		t.Fatal(err)
	}
	p, err = a.releases(ctx, releaseFilter{ListID: "11"})
	if err != nil || p.Total != 0 {
		t.Fatal(p, err)
	}
}
