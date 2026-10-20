package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestManualSyncRejectedLeasePreservesNewStartingOwner(t *testing.T) {
	a := testApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Exhaust the pool so the launched worker cannot acquire its lease until
	// another owner has published the exact stage used by the old reservation.
	held := make([]*pgxpool.Conn, 0, a.db.Config().MaxConns)
	t.Cleanup(func() {
		if len(held) > 0 {
			_, _ = held[0].Exec(context.Background(), `SELECT pg_advisory_unlock(486722121)`)
		}
		for _, c := range held {
			c.Release()
		}
		a.wg.Wait()
	})
	for i := int32(0); i < a.db.Config().MaxConns; i++ {
		c, err := a.db.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	if _, err := held[0].Exec(ctx, `SELECT pg_advisory_lock(486722121)`); err != nil {
		t.Fatal(err)
	}
	if !a.startSync() {
		t.Fatal("manual launch refused")
	}
	owner := SyncState{Running: true, Stage: "starting", LastError: "owner progress"}
	a.mu.Lock()
	a.syncState = owner
	a.mu.Unlock()
	held[len(held)-1].Release()
	held = held[:len(held)-1]
	a.wg.Wait()
	a.mu.Lock()
	got := a.syncState
	reserved := a.manualSyncRunning
	a.mu.Unlock()
	if got != owner {
		t.Fatalf("rejected launch overwrote another starting owner: %+v", got)
	}
	if reserved {
		t.Fatal("manual launch reservation leaked after rejection")
	}
}

// Hold the final persistence statement after public Running becomes false, while
// syncOnce still owns its advisory lease. No production hooks or timing sleeps
// determine entry into the handoff window.
func TestManualSyncRejectsLaunchUntilPreviousGoroutineReturns(t *testing.T) {
	a, server, cookie := watchlistApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/people/1" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"items":[],"has_more":false}`))
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := a.saveActress(ctx, Actress{TMDbID: "1", Name: "Unavailable", MetadataStatus: "pending"}); err != nil {
		t.Fatal(err)
	}
	barrier, err := a.db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = barrier.Exec(ctx, `SELECT pg_advisory_lock(486722198)`); err != nil {
		barrier.Release()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = barrier.Exec(context.Background(), `SELECT pg_advisory_unlock(486722198)`)
		barrier.Release()
		a.wg.Wait()
		_, _ = a.db.Exec(context.Background(), `DROP TRIGGER IF EXISTS manual_handoff_barrier ON app_state; DROP FUNCTION IF EXISTS manual_handoff_barrier()`)
	})
	if _, err = a.db.Exec(ctx, `CREATE FUNCTION manual_handoff_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.key='sync' AND NEW.value->>'running'='false' THEN PERFORM pg_advisory_xact_lock(486722198); END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER manual_handoff_barrier BEFORE INSERT OR UPDATE ON app_state FOR EACH ROW EXECUTE FUNCTION manual_handoff_barrier()`); err != nil {
		t.Fatal(err)
	}
	post := func() int {
		code, _ := watchlistHTTP(t, server, cookie, "POST", "/api/sync", "{}", server.URL)
		return code
	}
	if code := post(); code != http.StatusAccepted {
		t.Fatalf("first POST: %d", code)
	}
	for {
		var waiting bool
		if err = a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=486722198 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("final persistence barrier not reached")
		case <-time.After(time.Millisecond):
		}
	}
	a.mu.Lock()
	completed := a.syncState
	a.mu.Unlock()
	if completed.Running || completed.Stage != "partial" || completed.LastRun == nil {
		t.Fatalf("not at partial completion handoff: %+v", completed)
	}
	for i := 0; i < 8; i++ {
		if code := post(); code != http.StatusConflict {
			t.Fatalf("POST during final persistence/lease handoff accepted: got %d, want 409 (public running already false)", code)
		}
	}
	if _, err = barrier.Exec(ctx, `SELECT pg_advisory_unlock(486722198)`); err != nil {
		t.Fatal(err)
	}
	a.wg.Wait()
	var raw []byte
	if err = a.db.QueryRow(ctx, `SELECT value FROM app_state WHERE key='sync'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var persisted SyncState
	if err = json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	final := a.syncState
	a.mu.Unlock()
	if final.Stage != persisted.Stage || final.LastError != persisted.LastError || final.LastRun == nil || persisted.LastRun == nil || !final.LastRun.Equal(*persisted.LastRun) || final.Stage != "partial" {
		t.Fatalf("memory/DB completion diverged: memory=%+v persisted=%+v", final, persisted)
	}
	if code := post(); code != http.StatusAccepted {
		t.Fatalf("POST after full return: %d", code)
	}
	a.wg.Wait()
}
