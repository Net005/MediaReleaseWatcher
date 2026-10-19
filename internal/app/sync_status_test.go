package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBiographyResponseDoesNotClaimMissingFieldsComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":123,"name":"Person","avatar":"portrait"}`))
	}))
	defer srv.Close()
	a := &App{cfg: Config{MDBListBaseURL: srv.URL}}
	s := defaultSettings()
	s.MDBList.APIKey = "test"
	p := a.enrichPerson(context.Background(), s, Actress{TMDbID: "123", Name: "Person", Biography: "prior biography", Birthday: "2000-01-01", PlaceOfBirth: "prior birthplace"})
	if !strings.HasPrefix(p.MetadataStatus, "partial:") || !strings.Contains(p.MetadataStatus, "biography") || !strings.Contains(p.MetadataStatus, "birthday") {
		t.Fatalf("missing fields claimed complete: %s", p.MetadataStatus)
	}
}
func TestSyncStatusPersistsPartialBiographyProgress(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/people/1":
			calls++
			http.NotFound(w, r)
		case "/people/2":
			calls++
			w.Write([]byte(`{"id":2,"name":"B","avatar":"portrait"}`))
		default:
			w.Write([]byte(`{"items":[],"has_more":false}`))
		}
	}))
	defer srv.Close()
	a.cfg.MDBListBaseURL = srv.URL
	s := defaultSettings()
	s.MDBList.APIKey = "test"
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	for _, p := range []Actress{{TMDbID: "1", Name: "A", MetadataStatus: "pending"}, {TMDbID: "2", Name: "B", MetadataStatus: "pending"}} {
		if _, e := a.saveActress(ctx, p); e != nil {
			t.Fatal(e)
		}
	}
	if e := a.syncOnce(ctx); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatalf("404 aborted watcher processing: %d", calls)
	}
	var raw []byte
	if e := a.db.QueryRow(ctx, `SELECT value FROM app_state WHERE key='sync'`).Scan(&raw); e != nil {
		t.Fatalf("scheduled/direct sync status absent: %v", e)
	}
	var state SyncState
	if e := json.Unmarshal(raw, &state); e != nil {
		t.Fatal(e)
	}
	if state.Running || state.LastRun == nil || state.Stage != "partial" || !strings.Contains(state.LastError, "1 unavailable") || !strings.Contains(state.LastError, "1 partial") {
		t.Fatalf("untruthful final status: %+v", state)
	}
	task := Task{Type: "catalog_sync", Name: "partial sync", Frequency: "once", Date: "2026-10-19", Time: "00:00", Timezone: "UTC"}
	taskRaw, _ := json.Marshal(task)
	var id int64
	if e := a.db.QueryRow(ctx, `INSERT INTO scheduled_tasks(data,enabled) VALUES($1,true) RETURNING id`, taskRaw).Scan(&id); e != nil {
		t.Fatal(e)
	}
	claim, e := a.claimSchedule(ctx, id, true)
	if e != nil {
		t.Fatal(e)
	}
	a.executeSchedule(ctx, claim)
	var status, message string
	if e := a.db.QueryRow(ctx, `SELECT last_status,last_error FROM scheduled_tasks WHERE id=$1`, id).Scan(&status, &message); e != nil {
		t.Fatal(e)
	}
	if status != "warning" || !strings.Contains(message, "1 unavailable") {
		t.Fatalf("scheduled partial falsely successful: %s %s", status, message)
	}

	var count int
	if e := a.db.QueryRow(ctx, `SELECT count(*) FROM activity WHERE message LIKE '%2/2%'`).Scan(&count); e != nil || count == 0 {
		t.Fatalf("missing factual watcher progress: %d %v", count, e)
	}
}

func TestSyncStatusScheduledRunAndBoundedWatcherTraversal(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"items":[],"has_more":false}`)) }))
	defer srv.Close()
	a.cfg.MDBListBaseURL = srv.URL
	s := defaultSettings()
	s.MDBList.APIKey = "test"
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec(ctx, `INSERT INTO actresses(tmdb_id,data) SELECT n::text,jsonb_build_object('tmdb_id',n::text,'name','Person','metadata_status','ok','profile','portrait','biography','bio','birthday','2000-01-01','place_of_birth','Place') FROM generate_series(1,501) n`); e != nil {
		t.Fatal(e)
	}
	task := Task{Type: "catalog_sync", Name: "sync", Frequency: "once", Date: "2026-10-19", Time: "00:00", Timezone: "UTC"}
	raw, _ := json.Marshal(task)
	var id int64
	if e := a.db.QueryRow(ctx, `INSERT INTO scheduled_tasks(data,enabled) VALUES($1,true) RETURNING id`, raw).Scan(&id); e != nil {
		t.Fatal(e)
	}
	claim, e := a.claimSchedule(ctx, id, true)
	if e != nil {
		t.Fatal(e)
	}
	a.executeSchedule(ctx, claim)
	var state SyncState
	if e := a.db.QueryRow(ctx, `SELECT value FROM app_state WHERE key='sync'`).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	json.Unmarshal(raw, &state)
	if state.Running || state.LastRun == nil {
		t.Fatalf("scheduled status not finalized: %+v", state)
	}
	var n int
	if e := a.db.QueryRow(ctx, `SELECT count(*) FROM activity WHERE message LIKE '%501/501%' AND message LIKE '%501 not due%'`).Scan(&n); e != nil || n == 0 {
		t.Fatalf("not all watchers processed truthfully: %d %v", n, e)
	}
}

func TestSyncBiographyPageIsBounded(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if _, e := a.db.Exec(ctx, `INSERT INTO actresses(tmdb_id,data) SELECT n::text,jsonb_build_object('tmdb_id',n::text,'name','Person') FROM generate_series(1,501) n`); e != nil {
		t.Fatal(e)
	}
	page, e := a.syncBiographyPage(ctx, 0)
	if e != nil || len(page) != 500 {
		t.Fatalf("first bounded page: %d %v", len(page), e)
	}
	next, e := a.syncBiographyPage(ctx, page[len(page)-1].ID)
	if e != nil || len(next) != 1 {
		t.Fatalf("remaining watcher: %d %v", len(next), e)
	}
}

func TestSyncBiographyFailureRetainsIdentityQuarantine(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	a := &App{cfg: Config{MDBListBaseURL: srv.URL}}
	s := defaultSettings()
	s.MDBList.APIKey = "test"
	p := a.enrichPerson(context.Background(), s, Actress{TMDbID: "123", Name: "Person", MetadataStatus: "unavailable: identity mismatch; manual review"})
	if !strings.HasPrefix(p.MetadataStatus, "unavailable: identity mismatch") {
		t.Fatalf("quarantine lost on unavailable retry: %s", p.MetadataStatus)
	}
}

func TestSyncBusyDoesNotOverwriteRunningStatus(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	conn, e := a.db.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Release()
	if _, e = conn.Exec(ctx, `SELECT pg_advisory_lock(486722121)`); e != nil {
		t.Fatal(e)
	}
	defer conn.Exec(ctx, `SELECT pg_advisory_unlock(486722121)`)
	a.mu.Lock()
	a.syncState = SyncState{Running: true, Stage: "active owner"}
	a.mu.Unlock()
	s := defaultSettings()
	s.MDBList.APIKey = "test"
	if e = a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	if e = a.syncOnce(ctx); e == nil {
		t.Fatal("expected busy")
	}
	a.mu.Lock()
	state := a.syncState
	a.mu.Unlock()
	if !state.Running || state.Stage != "active owner" {
		t.Fatalf("rejected runner corrupted owner: %+v", state)
	}
}

func TestManualSyncRejectedLeaseDoesNotStayRunning(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	conn, e := a.db.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Release()
	if _, e = conn.Exec(ctx, `SELECT pg_advisory_lock(486722121)`); e != nil {
		t.Fatal(e)
	}
	defer conn.Exec(ctx, `SELECT pg_advisory_unlock(486722121)`)
	if !a.startSync() {
		t.Fatal("start refused")
	}
	a.wg.Wait()
	a.mu.Lock()
	state := a.syncState
	a.mu.Unlock()
	if state.Running || state.Stage != "failed" {
		t.Fatalf("manual rejected lease stuck running: %+v", state)
	}
}

func TestManualSyncPersistsRunningProgress(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/people/favorites/upcoming" {
			close(entered)
			<-release
		}
		w.Write([]byte(`{"items":[],"has_more":false}`))
	}))
	defer srv.Close()
	defer close(release)
	a.cfg.MDBListBaseURL = srv.URL
	s := defaultSettings()
	s.MDBList.APIKey = "test"
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	if !a.startSync() {
		t.Fatal("start refused")
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider not reached")
	}
	var raw []byte
	if e := a.db.QueryRow(ctx, `SELECT value FROM app_state WHERE key='sync'`).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var state SyncState
	json.Unmarshal(raw, &state)
	if !state.Running || !strings.Contains(state.Stage, "favorites-upcoming") {
		t.Fatalf("running progress not persisted: %+v", state)
	}
}

// The callback is a deterministic lease handoff: the first run has returned,
// then a real second run overwrites global progress before schedule completion.
func TestScheduledSyncResultSurvivesNextRunHandoff(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/people/1" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"items":[],"has_more":false}`))
	}))
	defer srv.Close()
	a.cfg.MDBListBaseURL = srv.URL
	s := defaultSettings()
	s.MDBList.APIKey = "test"
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveActress(ctx, Actress{TMDbID: "1", Name: "A", MetadataStatus: "pending"}); err != nil {
		t.Fatal(err)
	}
	task := Task{Type: "catalog_sync", Name: "handoff", Frequency: "once", Date: "2026-10-19", Time: "00:00", Timezone: "UTC"}
	raw, _ := json.Marshal(task)
	var id int64
	if err := a.db.QueryRow(ctx, `INSERT INTO scheduled_tasks(data,enabled) VALUES($1,true) RETURNING id`, raw).Scan(&id); err != nil {
		t.Fatal(err)
	}
	claim, err := a.claimSchedule(ctx, id, true)
	if err != nil {
		t.Fatal(err)
	}
	a.executeScheduleWithSync(ctx, claim, func(ctx context.Context) (syncCompletion, error) {
		result, err := a.syncOnceResult(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// Missing configuration makes the next lease owner publish failed state.
		s.MDBList.APIKey = ""
		if err := a.saveSettings(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := a.syncOnce(ctx); err == nil {
			t.Fatal("next run should fail")
		}
		a.mu.Lock()
		stage := a.syncState.Stage
		a.mu.Unlock()
		if stage != "failed" {
			t.Fatalf("handoff did not overwrite state: %s", stage)
		}
		return result, nil
	})
	var status, message string
	if err := a.db.QueryRow(ctx, `SELECT last_status,last_error FROM scheduled_tasks WHERE id=$1`, id).Scan(&status, &message); err != nil {
		t.Fatal(err)
	}
	if status != "warning" || !strings.Contains(message, "1 unavailable") {
		t.Fatalf("first run warning lost after next lease owner: %s %s", status, message)
	}
}

func TestSyncFinalStatusPersistenceFailuresAreLogged(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	// Fail only completion writes; claiming and running progress still work.
	_, err := a.db.Exec(ctx, `
CREATE FUNCTION reject_sync_completion() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME = 'app_state' THEN
  IF NEW.key = 'sync' AND NEW.value->>'running' = 'false' THEN
   RAISE EXCEPTION 'completion write rejected';
  END IF;
 ELSE
  IF NEW.last_status <> 'running' THEN
   RAISE EXCEPTION 'completion write rejected';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER reject_sync_completion BEFORE INSERT OR UPDATE ON app_state FOR EACH ROW EXECUTE FUNCTION reject_sync_completion();
CREATE TRIGGER reject_schedule_completion BEFORE UPDATE ON scheduled_tasks FOR EACH ROW EXECUTE FUNCTION reject_sync_completion();`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := a.db.Exec(ctx, `DROP TRIGGER reject_sync_completion ON app_state; DROP TRIGGER reject_schedule_completion ON scheduled_tasks; DROP FUNCTION reject_sync_completion()`)
		if err != nil {
			t.Error(err)
		}
	})
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	task := Task{Type: "catalog_sync", Name: "failed persistence", Frequency: "once", Date: "2026-10-19", Time: "00:00", Timezone: "UTC"}
	raw, _ := json.Marshal(task)
	var id int64
	if err := a.db.QueryRow(ctx, `INSERT INTO scheduled_tasks(data,enabled) VALUES($1,true) RETURNING id`, raw).Scan(&id); err != nil {
		t.Fatal(err)
	}
	claim, err := a.claimSchedule(ctx, id, true)
	if err != nil {
		t.Fatal(err)
	}
	a.executeSchedule(ctx, claim)
	for _, want := range []string{"persist sync completion", "persist scheduled task completion"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing %q diagnostic: %s", want, logs.String())
		}
	}
}

func TestSyncStatusPersistsFailure(t *testing.T) {
	a := testApp(t)
	before := time.Now()
	if e := a.syncOnce(context.Background()); e == nil {
		t.Fatal("expected missing API key failure")
	}
	var raw []byte
	if e := a.db.QueryRow(context.Background(), `SELECT value FROM app_state WHERE key='sync'`).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var state SyncState
	json.Unmarshal(raw, &state)
	if state.Running || state.Stage != "failed" || state.LastError == "" || state.LastRun == nil || state.LastRun.Before(before) {
		t.Fatalf("failure not persisted: %+v", state)
	}
}
