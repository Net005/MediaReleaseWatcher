package app

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func testApp(t *testing.T) *App {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; PostgreSQL integration test")
	}
	a, e := New(context.Background(), Config{DatabaseURL: dsn, Secret: "0123456789abcdef0123456789abcdef", DataDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	_, e = a.db.Exec(context.Background(), `TRUNCATE release_people, releases, actresses, activity, actions, sessions, account, settings, app_state, mdblist_lists, scheduled_tasks RESTART IDENTITY CASCADE`)
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func TestPostgresUpsertMatchesOnlyActualCast(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Person", Monitored: true})
	if e != nil {
		t.Fatal(e)
	}
	r, people, e := normalizeRelease(json.RawMessage(`{"title":"Person","type":"movie","tmdbid":42,"cast":[{"id":123}]}`), "movie")
	if e != nil {
		t.Fatal(e)
	}
	r, e = a.saveRelease(ctx, r, people)
	if e != nil {
		t.Fatal(e)
	}
	same, e := a.saveRelease(ctx, r, people)
	if e != nil || same.ID != r.ID {
		t.Fatal("non-idempotent", e)
	}
	releases, e := a.releases(ctx, releaseFilter{ActressID: p.ID, Limit: 36})
	if e != nil || len(releases.Items) != 1 {
		t.Fatalf("match %+v %v", releases, e)
	}
	got, e := a.getRelease(ctx, r.ID)
	if e != nil || len(got.Actresses) != 1 {
		t.Fatal(got, e)
	}
	_, e = a.db.Exec(ctx, `DELETE FROM actresses WHERE id=$1`, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	got, e = a.getRelease(ctx, r.ID)
	if e != nil || len(got.Actresses) != 0 {
		t.Fatal("cascade", e)
	}
}
func TestPostgresSettingsSecretsNeverReadable(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	s := defaultSettings()
	s.MDBList.APIKey = "test-credential"
	s.Sonarr.URL = "http://private-host:8989"
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	var b []byte
	if e := a.db.QueryRow(ctx, `SELECT encrypted FROM settings WHERE id=1`).Scan(&b); e != nil {
		t.Fatal(e)
	}
	if string(b) == "test-credential" {
		t.Fatal("plaintext")
	}
	got, e := a.settings(ctx)
	if e != nil || got.MDBList.APIKey != s.MDBList.APIKey {
		t.Fatal(got, e)
	}
	b, _ = json.Marshal(got.public())
	if string(b) == "" || containsBytes(b, "test-credential") {
		t.Fatal("leaked secret")
	}
}
func containsBytes(b []byte, s string) bool {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return true
		}
	}
	return false
}
func TestPostgresReleaseFilterBoundary(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	r, _, _ := normalizeRelease(json.RawMessage(`{"title":"Test","type":"movie","tmdbid":42}`), "movie")
	r.ReleaseDate = time.Now().UTC().Format("2006-01-02")
	if _, e := a.saveRelease(ctx, r, nil); e != nil {
		t.Fatal(e)
	}
	for _, w := range []string{"current", "all"} {
		x, e := a.releases(ctx, releaseFilter{Window: w, Limit: 36})
		if e != nil || x.Total != 1 {
			t.Fatal(w, x, e)
		}
	}
}
