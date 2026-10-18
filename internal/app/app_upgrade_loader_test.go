package app

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestPostgresUpgradeLoaderResetsCatalogueOnlyOnce(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if _, err := a.db.Exec(ctx, `DELETE FROM schema_migrations WHERE version>=3; INSERT INTO account(id,password_hash) VALUES(1,'synthetic-hash'); INSERT INTO sessions(token_hash,expires_at) VALUES('synthetic-session',now()+interval '1 day')`); err != nil {
		t.Fatal(err)
	}
	s := defaultSettings()
	s.MDBList.APIKey = "synthetic-upgrade-key"
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic watch", Monitored: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveRelease(ctx, Release{Title: "Old discovered title", Type: "movie", TMDbID: "42", Raw: []byte(`{"id":42}`)}, nil); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DatabaseURL: os.Getenv("TEST_DATABASE_URL"), Secret: "0123456789abcdef0123456789abcdef", DataDir: t.TempDir()}
	upgraded, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var count int
	if err = upgraded.db.QueryRow(ctx, `SELECT count(*) FROM releases`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("one-time reset did not clear old catalogue: %d %v", count, err)
	}
	for _, table := range []string{"account", "sessions", "settings", "actresses"} {
		if err = upgraded.db.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("upgrade changed preserved %s: %d %v", table, count, err)
		}
	}
	saved, err := upgraded.saveRelease(ctx, Release{Title: "New user title", Type: "movie", TMDbID: "43", ReleaseDate: time.Now().UTC().Format("2006-01-02"), Raw: []byte(`{"id":43}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	got, err := restarted.getRelease(ctx, saved.ID)
	if err != nil || got.Title != "New user title" {
		t.Fatalf("restart repeated destructive reset: %+v %v", got, err)
	}
	if err = restarted.db.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version=3`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("one-time reset marker missing: %d %v", count, err)
	}
}

func TestPostgresWrongKeyDoesNotResetCatalogue(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	s := defaultSettings()
	s.MDBList.APIKey = "synthetic-preserved-key"
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(ctx, `DELETE FROM schema_migrations WHERE version=3`); err != nil {
		t.Fatal(err)
	}
	saved, err := a.saveRelease(ctx, Release{Title: "Preserve until configuration is valid", Type: "movie", TMDbID: "45", Raw: []byte(`{"id":45}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{DatabaseURL: os.Getenv("TEST_DATABASE_URL"), Secret: "abcdef0123456789abcdef0123456789", DataDir: t.TempDir()}
	broken, err := New(ctx, cfg)
	if broken != nil {
		broken.Close()
	}
	if err == nil {
		t.Fatal("startup accepted wrong encryption key")
	}
	got, err := a.getRelease(ctx, saved.ID)
	if err != nil || got.Title != saved.Title {
		t.Fatalf("bad configuration ran destructive migration: %+v %v", got, err)
	}
	var count int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version=3`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("bad configuration committed reset marker: %d %v", count, err)
	}
}
