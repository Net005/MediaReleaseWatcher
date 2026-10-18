package app

import (
	"bytes"
	"context"
	"os"
	"testing"
)

func TestPostgresStartupSeedsAndPreservesSchedules(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if _, err := a.db.Exec(ctx, `DELETE FROM scheduled_tasks`); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DatabaseURL: os.Getenv("TEST_DATABASE_URL"), Secret: "0123456789abcdef0123456789abcdef", DataDir: t.TempDir()}
	booted, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer booted.Close()
	var count int
	if err = booted.db.QueryRow(ctx, `SELECT count(*) FROM scheduled_tasks`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("startup must seed one schedule, got %d: %v", count, err)
	}
	if _, err = booted.db.Exec(ctx, `UPDATE scheduled_tasks SET enabled=false,next_run=NULL,data=jsonb_set(data,'{name}','"User-edited schedule"'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	var before, after []byte
	if err = booted.db.QueryRow(ctx, `SELECT data FROM scheduled_tasks`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	var enabled bool
	if err = restarted.db.QueryRow(ctx, `SELECT data,enabled FROM scheduled_tasks`).Scan(&after, &enabled); err != nil {
		t.Fatal(err)
	}
	if enabled || !bytes.Equal(before, after) {
		t.Fatalf("restart changed disabled user schedule: enabled=%v before=%s after=%s", enabled, before, after)
	}
	if err = restarted.db.QueryRow(ctx, `SELECT count(*) FROM scheduled_tasks`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("restart duplicated schedule: %d %v", count, err)
	}
}
