package app

import (
	"context"
	"encoding/json"
	"testing"
)

func applyPolicyMigration(t *testing.T, a *App) {
	t.Helper()
	b, err := migrations.ReadFile("migrations/003_controlled_discovery.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := a.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(context.Background(), string(b)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCatalogueResetMigrationOnlyOnce(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if err := a.saveSettings(ctx, defaultSettings()); err != nil {
		t.Fatal(err)
	}
	_, err := a.db.Exec(ctx, `DELETE FROM schema_migrations WHERE version=3;
 INSERT INTO account(id,password_hash) VALUES(1,'synthetic');
 INSERT INTO sessions(token_hash,expires_at) VALUES('synthetic',now()+interval '1 day');
 INSERT INTO actresses(tmdb_id,data) VALUES('123','{"name":"Synthetic","monitored":true}');
 INSERT INTO releases(canonical_key,media_type,tmdb_id,data) VALUES('movie:tmdb:42','movie','42','{"title":"Old"}');
 INSERT INTO release_cast(release_id,tmdb_id,source) SELECT id,'123','synthetic' FROM releases;
 INSERT INTO release_people(release_id,actress_id,source) SELECT id,1,'synthetic' FROM releases;
 INSERT INTO actions(release_id,kind,status,event_key) SELECT id,'acquire','baseline','discovered' FROM releases;
 INSERT INTO activity(kind,message,status) VALUES('sync','old release','ok');
 INSERT INTO app_state(key,value) VALUES('baseline','true'),('sync','{}'),('person_credits:1','{}'),('unrelated','true')`)
	if err != nil {
		t.Fatal(err)
	}
	applyPolicyMigration(t, a)
	for _, table := range []string{"releases", "release_cast", "release_people", "actions", "activity"} {
		var n int
		if err := a.db.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s retained stale rows: %d %v", table, n, err)
		}
	}
	for _, table := range []string{"account", "sessions", "settings", "actresses"} {
		var n int
		if err := a.db.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s not preserved: %d %v", table, n, err)
		}
	}
	var keys int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM app_state WHERE key='unrelated'`).Scan(&keys); err != nil || keys != 1 {
		t.Fatal(keys, err)
	}
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM app_state WHERE key IN ('baseline','sync','person_credits:1')`).Scan(&keys); err != nil || keys != 0 {
		t.Fatal(keys, err)
	}
	_, err = a.db.Exec(ctx, `INSERT INTO releases(canonical_key,media_type,tmdb_id,data) VALUES('movie:tmdb:43','movie','43','{"title":"New"}')`)
	if err != nil {
		t.Fatal(err)
	}
	applyPolicyMigration(t, a)
	var monitored bool
	if err = a.db.QueryRow(ctx, `SELECT monitored FROM releases WHERE tmdb_id='43'`).Scan(&monitored); err != nil || monitored {
		t.Fatalf("new title reset or monitored: %v %v", monitored, err)
	}
	var marker int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE version=3`).Scan(&marker); err != nil || marker != 1 {
		t.Fatal(marker, err)
	}
}

func policyApp(t *testing.T) *App { t.Helper(); a := testApp(t); applyPolicyMigration(t, a); return a }

func policyRelease(t *testing.T, a *App, id string) Release {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"title": "Synthetic " + id, "type": "movie", "tmdb_id": id})
	r, people, err := normalizeRelease(raw, "movie")
	if err != nil {
		t.Fatal(err)
	}
	r, err = a.saveRelease(context.Background(), r, people)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
