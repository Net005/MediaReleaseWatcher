package app

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func upcomingPolicyRelease(t *testing.T, a *App, id string) Release {
	t.Helper()
	r := policyRelease(t, a, id)
	r.ReleaseDate = time.Now().UTC().AddDate(1, 0, 0).Format("2006-01-02")
	r, err := a.saveRelease(context.Background(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPostgresUpcomingMonitoringAutomaticPolicies(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	dates := []string{time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02"), time.Now().UTC().Format("2006-01-02"), "", "garbage", "2099-02-30", "2099-2-01", "2099-12-31"}
	ids := []int64{}
	for i, date := range dates {
		r, err := a.saveRelease(ctx, Release{Type: "movie", TMDbID: fmt.Sprint(100 + i), Title: "Synthetic", ReleaseDate: date}, []string{"123"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	// Existing catalogue defaults must not survive automatic recomputation.
	if _, err := a.db.Exec(ctx, `UPDATE releases SET monitored=true`); err != nil {
		t.Fatal(err)
	}
	p, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic", Group: "preserve", Monitored: true, Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		r, err := a.getRelease(ctx, id)
		if err != nil || r.Monitored != (i == len(ids)-1) {
			t.Errorf("date %q monitored=%v err=%v", dates[i], r.Monitored, err)
		}
	}
	if p.AutoAdd || !p.Notify || p.Group != "preserve" {
		t.Fatal("changed actor choices", p)
	}
	// Lists use the same date eligibility, independent of their auto-add choice.
	s := defaultSettings()
	s.MDBList.Lists = []MDBListPolicy{{ID: "11", Enabled: true, Monitored: true}}
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := a.replaceListMembership(ctx, "11", ids); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		r, err := a.getRelease(ctx, id)
		if err != nil || r.Monitored != (i == len(ids)-1) {
			t.Errorf("list date %q monitored=%v err=%v", dates[i], r.Monitored, err)
		}
	}
}
