package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func scheduleTime(t *testing.T, v string) time.Time {
	t.Helper()
	n, err := time.Parse(time.RFC3339, v)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestScheduleNextIntervalSkipsMissedRuns(t *testing.T) {
	task := Task{Type: "catalog_sync", Name: "Synthetic", Frequency: "interval", Date: "2026-01-01", Time: "12:00", Timezone: "UTC", IntervalMinutes: 60}
	next, err := nextScheduleRun(task, scheduleTime(t, "2026-01-03T12:37:00Z"))
	if err != nil || next == nil || !next.Equal(scheduleTime(t, "2026-01-03T13:00:00Z")) {
		t.Fatalf("next=%v error=%v", next, err)
	}
}

func TestScheduleCalendarRecurrencesAndDST(t *testing.T) {
	cases := []struct {
		name, frequency, date, clock, zone, after, want string
		weekdays                                        []int
	}{
		{name: "future once", frequency: "once", date: "2026-12-01", clock: "10:00", zone: "Europe/Amsterdam", after: "2026-10-01T00:00:00Z", want: "2026-12-01T09:00:00Z"},
		{name: "expired once", frequency: "once", date: "2026-01-01", clock: "10:00", zone: "UTC", after: "2026-01-01T10:00:00Z"},
		{name: "daily wall clock crosses spring", frequency: "daily", date: "2026-01-01", clock: "09:00", zone: "America/New_York", after: "2026-03-07T14:00:00Z", want: "2026-03-08T13:00:00Z"},
		{name: "spring gap skips nonexistent local time", frequency: "daily", date: "2026-01-01", clock: "02:30", zone: "America/New_York", after: "2026-03-07T07:30:00Z", want: "2026-03-09T06:30:00Z"},
		{name: "fall fold picks first occurrence", frequency: "daily", date: "2026-01-01", clock: "01:30", zone: "America/New_York", after: "2026-10-31T05:30:00Z", want: "2026-11-01T05:30:00Z"},
		{name: "fall fold never repeats same local date", frequency: "daily", date: "2026-01-01", clock: "01:30", zone: "America/New_York", after: "2026-11-01T05:30:00Z", want: "2026-11-02T06:30:00Z"},
		{name: "weekly Sunday", frequency: "weekly", date: "2026-01-01", clock: "09:00", zone: "Europe/Amsterdam", after: "2026-03-28T08:00:00Z", want: "2026-03-29T07:00:00Z", weekdays: []int{0}},
		{name: "future start lower bound", frequency: "daily", date: "2027-04-01", clock: "08:00", zone: "UTC", after: "2026-01-01T00:00:00Z", want: "2027-04-01T08:00:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			task := Task{Type: "catalog_sync", Name: "Synthetic", Frequency: c.frequency, Date: c.date, Time: c.clock, Timezone: c.zone, Weekdays: c.weekdays}
			got, err := nextScheduleRun(task, scheduleTime(t, c.after))
			if err != nil {
				t.Fatal(err)
			}
			if c.want == "" {
				if got != nil {
					t.Fatalf("expired once: %v", got)
				}
				return
			}
			if got == nil || !got.Equal(scheduleTime(t, c.want)) {
				t.Fatalf("got %v want %s", got, c.want)
			}
		})
	}
}

func schedulerApp(t *testing.T) *App {
	t.Helper()
	return schedulerAppDatabase(t, "postgres://hermes@127.0.0.1:55473/mrw_schedule_test?sslmode=disable")
}

func schedulerAppDatabase(t *testing.T, url string) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	pc, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("scheduler_%d", time.Now().UnixNano())
	admin, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	// Advisory locks are database-scoped, not search_path/schema-scoped. Parent
	// full-suite runs and focused worker runs share this disposable database.
	// Serialize fixtures so their distinct catalogues cannot contend for the same
	// runtime job/presence lock IDs and report false overlap failures.
	adminConn, err := admin.Acquire(ctx)
	if err != nil {
		admin.Close()
		cancel()
		t.Fatal(err)
	}
	if _, err = adminConn.Exec(ctx, `SELECT pg_advisory_lock(486722199)`); err != nil {
		adminConn.Release()
		admin.Close()
		cancel()
		t.Fatal(err)
	}
	if _, err = adminConn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		adminConn.Release()
		admin.Close()
		cancel()
		t.Fatal(err)
	}
	pc.MaxConns = 12 // Match the application pool, including reserved execution capacity.
	pc.ConnConfig.RuntimeParams["search_path"] = schema
	pc.ConnConfig.RuntimeParams["timezone"] = "UTC"
	db, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := newCipher("synthetic-scheduler-test-secret-at-least-32-characters")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{db: db, cipher: cipher, ctx: ctx, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		a.wg.Wait()
		db.Close()
		_, _ = adminConn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_, _ = adminConn.Exec(context.Background(), `SELECT pg_advisory_unlock(486722199)`)
		adminConn.Release()
		admin.Close()
	})
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func schedulerRequest(a *App, method, path, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	a.registerScheduleRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}
func schedulerCreate(t *testing.T, a *App, kind string) Task {
	t.Helper()
	rec := schedulerRequest(a, "POST", "/api/tasks", `{"type":"`+kind+`","name":"Synthetic","enabled":true,"frequency":"interval","date":"2026-01-01","time":"12:00","timezone":"UTC","interval_minutes":60}`)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var task Task
	if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if task.ID == 0 || task.NextRun == nil {
		t.Fatalf("task=%+v", task)
	}
	return task
}

func TestSchedulePostgresCRUDAndSeedPreservation(t *testing.T) {
	a := schedulerApp(t)
	s := defaultSettings()
	s.MDBList.SyncIntervalMinutes = 77
	if err := a.saveSettings(a.ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureSchedules(a.ctx); err != nil {
		t.Fatal(err)
	}
	rec := schedulerRequest(a, "GET", "/api/tasks", "")
	var list struct {
		Items []Task `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(list.Items) != 1 || list.Items[0].IntervalMinutes != 77 {
		t.Fatalf("seed=%s", rec.Body.String())
	}
	id := list.Items[0].ID
	path := fmt.Sprintf("/api/tasks/%d", id)
	rec = schedulerRequest(a, "PATCH", path, `{"enabled":false,"name":"My disabled job","interval_minutes":123}`)
	if rec.Code != 200 {
		t.Fatalf("patch=%d %s", rec.Code, rec.Body.String())
	}
	if err := a.ensureSchedules(a.ctx); err != nil {
		t.Fatal(err)
	}
	rec = schedulerRequest(a, "GET", "/api/tasks", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Items) != 1 || list.Items[0].Enabled || list.Items[0].Name != "My disabled job" || list.Items[0].IntervalMinutes != 123 {
		t.Fatalf("overwritten=%s", rec.Body.String())
	}
	raw, _ := migrations.ReadFile("migrations/004_schedules.sql")
	if _, err := a.db.Exec(a.ctx, string(raw)); err != nil {
		t.Fatal(err)
	}
	rec = schedulerRequest(a, "GET", "/api/tasks", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Items) != 1 || list.Items[0].Name != "My disabled job" {
		t.Fatalf("migration changed jobs: %s", rec.Body.String())
	}
	rec = schedulerRequest(a, "PATCH", path, `{"time":"25:00"}`)
	if rec.Code != 400 {
		t.Fatalf("invalid accepted %d", rec.Code)
	}
	rec = schedulerRequest(a, "DELETE", path, "")
	if rec.Code != 200 {
		t.Fatalf("delete=%d %s", rec.Code, rec.Body.String())
	}
	rec = schedulerRequest(a, "PATCH", path, `{"enabled":true}`)
	if rec.Code != 404 {
		t.Fatalf("missing=%d", rec.Code)
	}
	// The initial conversion is one-shot even after users intentionally remove all tasks.
	if err := a.ensureSchedules(a.ctx); err != nil {
		t.Fatal(err)
	}
	rec = schedulerRequest(a, "GET", "/api/tasks", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Items) != 0 {
		t.Fatalf("deleted seed recreated: %s", rec.Body.String())
	}
	schedulerCreate(t, a, "arr_sync")
}

func TestSchedulePostgresAtomicClaimsAndRecovery(t *testing.T) {
	a := schedulerApp(t)
	task := schedulerCreate(t, a, "catalog_sync")
	claim, err := a.claimSchedule(a.ctx, task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	// Another instance has no in-process state shared with this one.
	other := &App{db: a.db, cipher: a.cipher, ctx: a.ctx, cancel: a.cancel}
	if second, err := other.claimSchedule(a.ctx, task.ID, true); err != errScheduleBusy || second != nil {
		t.Fatalf("overlapping claim=%v err=%v", second, err)
	}
	rec := schedulerRequest(a, "PATCH", fmt.Sprintf("/api/tasks/%d", task.ID), `{"enabled":false}`)
	if rec.Code != 409 {
		t.Fatalf("modified running job: %d", rec.Code)
	}
	for _, request := range []struct{ method, suffix string }{{"POST", "/run"}, {"DELETE", ""}} {
		rec := schedulerRequest(other, request.method, fmt.Sprintf("/api/tasks/%d%s", task.ID, request.suffix), "")
		if rec.Code != 409 {
			t.Fatalf("running task allowed %s: %d %s", request.method, rec.Code, rec.Body.String())
		}
	}
	if err := other.ensureSchedules(a.ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := a.db.QueryRow(a.ctx, `SELECT last_status FROM scheduled_tasks WHERE id=$1`, task.ID).Scan(&status); err != nil || status != "running" {
		t.Fatalf("startup stole active claim: %s %v", status, err)
	}
	// Simulate process loss: DB releases the session lock, status survives.
	claim.release()
	if _, err := a.db.Exec(a.ctx, `UPDATE scheduled_tasks SET next_run=now()-interval '30 days' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	a.runDueSchedules(a.ctx)
	recovered, err := scanSchedule(a.db.QueryRow(a.ctx, `SELECT `+scheduleColumns+` FROM scheduled_tasks WHERE id=$1`, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.LastStatus != "error" || !strings.Contains(recovered.LastError, "interrupted") || recovered.NextRun == nil || !recovered.NextRun.After(time.Now()) {
		t.Fatalf("recovery=%+v", recovered)
	}
}

func TestSchedulePostgresClaimsReserveWorkerPoolCapacity(t *testing.T) {
	a := schedulerApp(t)
	capacity := int((a.db.Config().MaxConns - 1) / 3)
	claims := []*scheduleClaim{}
	defer func() {
		for _, claim := range claims {
			claim.release()
		}
	}()
	for i := 0; i <= capacity; i++ {
		task := schedulerCreate(t, a, "catalog_sync")
		claim, err := a.claimSchedule(a.ctx, task.ID, true)
		if claim != nil {
			claims = append(claims, claim)
		}
		if i < capacity && err != nil {
			t.Fatalf("claim %d rejected: %v", i, err)
		}
		if i == capacity && (err != errScheduleBusy || claim != nil) {
			t.Fatalf("claims can exhaust the worker pool: claim=%v err=%v", claim != nil, err)
		}
	}
}

func TestSchedulePostgresFutureOnceClaimConsumedBeforeEffects(t *testing.T) {
	a := schedulerApp(t)
	rec := schedulerRequest(a, "POST", "/api/tasks", `{"type":"catalog_sync","name":"Future once","enabled":true,"frequency":"once","date":"2099-01-01","time":"12:00","timezone":"UTC"}`)
	if rec.Code != 201 {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	var task Task
	_ = json.Unmarshal(rec.Body.Bytes(), &task)
	claim, err := a.claimSchedule(a.ctx, task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := scanSchedule(a.db.QueryRow(a.ctx, `SELECT `+scheduleColumns+` FROM scheduled_tasks WHERE id=$1`, task.ID))
	claim.release()
	if err != nil || got.NextRun != nil {
		t.Fatalf("once not consumed atomically: %+v %v", got, err)
	}
	a.runDueSchedules(a.ctx)
	got, err = scanSchedule(a.db.QueryRow(a.ctx, `SELECT `+scheduleColumns+` FROM scheduled_tasks WHERE id=$1`, task.ID))
	if err != nil || got.NextRun != nil || got.LastStatus != "error" {
		t.Fatalf("crashed once rearmed: %+v %v", got, err)
	}
}

func TestSchedulePostgresExecutionAndNoReplay(t *testing.T) {
	a := schedulerApp(t)
	task := schedulerCreate(t, a, "catalog_sync")
	if _, err := a.db.Exec(a.ctx, `UPDATE scheduled_tasks SET next_run=now()-interval '30 days' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	a.runDueSchedules(a.ctx)
	got, err := scanSchedule(a.db.QueryRow(a.ctx, `SELECT `+scheduleColumns+` FROM scheduled_tasks WHERE id=$1`, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if got.LastStatus != "error" || got.LastRun == nil || !strings.Contains(got.LastError, "MDBList API key is not configured") || got.NextRun == nil || !got.NextRun.After(time.Now()) {
		t.Fatalf("execution=%+v", got)
	}
	first := *got.LastRun
	a.runDueSchedules(a.ctx)
	got, err = scanSchedule(a.db.QueryRow(a.ctx, `SELECT `+scheduleColumns+` FROM scheduled_tasks WHERE id=$1`, task.ID))
	if err != nil || !got.LastRun.Equal(first) {
		t.Fatalf("replayed: %+v err=%v", got, err)
	}
	// An explicit run-now is accepted even for a disabled task, but never overlaps.
	path := fmt.Sprintf("/api/tasks/%d", task.ID)
	if rec := schedulerRequest(a, "PATCH", path, `{"enabled":false,"frequency":"once"}`); rec.Code != 200 {
		t.Fatalf("patch=%s", rec.Body.String())
	}
	rec := schedulerRequest(a, "POST", path+"/run", "")
	if rec.Code != 202 {
		t.Fatalf("run=%d %s", rec.Code, rec.Body.String())
	}
	a.wg.Wait()
	got, err = scanSchedule(a.db.QueryRow(a.ctx, `SELECT `+scheduleColumns+` FROM scheduled_tasks WHERE id=$1`, task.ID))
	if err != nil || got.LastStatus != "error" || got.NextRun != nil || got.Enabled {
		t.Fatalf("once result=%+v err=%v", got, err)
	}
}

func TestSchedulePostgresActualCatalogueSuccess(t *testing.T) {
	a := schedulerApp(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" {
			t.Errorf("unexpected write %s", r.Method)
		}
		switch r.URL.Path {
		case "/people/favorites/upcoming", "/catalog/movie", "/catalog/show":
			fmt.Fprint(w, `{"items":[],"has_more":false}`)
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	a.cfg.MDBListBaseURL = srv.URL
	s := defaultSettings()
	s.MDBList.APIKey = "synthetic-scheduler-key"
	if err := a.saveSettings(a.ctx, s); err != nil {
		t.Fatal(err)
	}
	task := schedulerCreate(t, a, "catalog_sync")
	if _, err := a.db.Exec(a.ctx, `UPDATE scheduled_tasks SET next_run=now()-interval '1 minute' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	a.runDueSchedules(a.ctx)
	got, err := scanSchedule(a.db.QueryRow(a.ctx, `SELECT `+scheduleColumns+` FROM scheduled_tasks WHERE id=$1`, task.ID))
	if err != nil || got.LastStatus != "ok" || got.LastError != "" || calls.Load() != 3 {
		t.Fatalf("catalog result=%+v calls=%d err=%v", got, calls.Load(), err)
	}
	var baseline bool
	if err := a.db.QueryRow(a.ctx, `SELECT EXISTS(SELECT 1 FROM app_state WHERE key='baseline')`).Scan(&baseline); err != nil || !baseline {
		t.Fatalf("actual pipeline not executed: %v %v", baseline, err)
	}
}

func schedulerRelease(t *testing.T, a *App, kind string, tmdb, tvdb int, monitored bool) Release {
	t.Helper()
	metadata := map[string]any{"title": fmt.Sprintf("Synthetic %d", tmdb), "type": kind, "tmdbid": tmdb}
	if tvdb > 0 {
		metadata["tvdbid"] = tvdb
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := normalizeRelease(raw, kind)
	if err != nil {
		t.Fatal(err)
	}
	r, err = a.saveRelease(a.ctx, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(a.ctx, `UPDATE releases SET monitored=$2,monitoring_override=$2 WHERE id=$1`, r.ID, monitored); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestSchedulePostgresSearchConsentAndBoundedExecution(t *testing.T) {
	a := schedulerApp(t)
	var writes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			writes.Add(1)
			if r.URL.Path != "/api/v3/command" {
				t.Errorf("unexpected write %s", r.URL.Path)
				w.WriteHeader(400)
				return
			}
			var payload struct {
				Name     string `json:"name"`
				MovieIDs []int  `json:"movieIds"`
				SeriesID int    `json:"seriesId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if payload.Name != "MoviesSearch" || len(payload.MovieIDs) != 1 || payload.MovieIDs[0] != 7 || payload.SeriesID != 0 {
				t.Errorf("unbounded search %+v", payload)
			}
			fmt.Fprint(w, `{"id":101,"name":"MoviesSearch"}`)
			return
		}
		switch r.URL.Path {
		case "/api/v3/movie":
			fmt.Fprint(w, `[{"id":7,"tmdbId":42,"monitored":true,"hasFile":false},{"id":8,"tmdbId":43,"monitored":true,"hasFile":true,"movieFile":{"id":3,"size":100}},{"id":9,"tmdbId":44,"monitored":true,"hasFile":false},{"id":10,"tmdbId":999,"monitored":true,"hasFile":false}]`)
		case "/api/v3/movie/7":
			fmt.Fprint(w, `{"id":7,"tmdbId":42,"monitored":true,"hasFile":false}`)
		case "/api/v3/command/101":
			fmt.Fprint(w, `{"id":101,"name":"MoviesSearch","body":{"name":"MoviesSearch","movieIds":[7]}}`)
		default:
			t.Errorf("unexpected read %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	schedulerRelease(t, a, "movie", 42, 0, true)
	schedulerRelease(t, a, "movie", 43, 0, true)
	schedulerRelease(t, a, "movie", 44, 0, false)
	s := defaultSettings()
	s.Radarr = ArrSettings{URL: srv.URL, APIKey: "synthetic-key", Enabled: true, Search: true}
	if err := a.saveSettings(a.ctx, s); err != nil {
		t.Fatal(err)
	}
	task := Task{Type: "search_missing", AllowDownloadSearch: true}
	if err := a.searchMissing(a.ctx, task); err == nil || writes.Load() != 0 {
		t.Fatalf("global consent ignored: %v", err)
	}
	s.Automation.Enabled = true
	if err := a.saveSettings(a.ctx, s); err != nil {
		t.Fatal(err)
	}
	task.AllowDownloadSearch = false
	if err := a.searchMissing(a.ctx, task); err == nil || writes.Load() != 0 {
		t.Fatalf("task consent ignored: %v", err)
	}
	task.AllowDownloadSearch = true
	s.Radarr.Enabled = false
	if err := a.saveSettings(a.ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := a.searchMissing(a.ctx, task); err == nil || writes.Load() != 0 {
		t.Fatalf("integration consent ignored: %v", err)
	}
	s.Radarr.Enabled = true
	s.Radarr.Search = false
	if err := a.saveSettings(a.ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := a.searchMissing(a.ctx, task); err == nil || writes.Load() != 0 {
		t.Fatalf("integration search consent ignored: %v", err)
	}
	s.Radarr.Search = true
	if err := a.saveSettings(a.ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := a.searchMissing(a.ctx, task); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 {
		t.Fatalf("search writes=%d", writes.Load())
	}
	// Exercise the persisted task/run/status path as well as its execution helper.
	rec := schedulerRequest(a, "POST", "/api/tasks", `{"type":"search_missing","name":"Explicit search","enabled":true,"allow_download_search":true,"frequency":"interval","date":"2026-01-01","time":"12:00","timezone":"UTC","interval_minutes":60}`)
	if rec.Code != 201 {
		t.Fatalf("create search: %d %s", rec.Code, rec.Body.String())
	}
	var saved Task
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if rec = schedulerRequest(a, "POST", fmt.Sprintf("/api/tasks/%d/run", saved.ID), ""); rec.Code != 202 {
		t.Fatalf("run search: %d %s", rec.Code, rec.Body.String())
	}
	a.wg.Wait()
	got, err := scanSchedule(a.db.QueryRow(a.ctx, `SELECT `+scheduleColumns+` FROM scheduled_tasks WHERE id=$1`, saved.ID))
	if err != nil || got.LastStatus != "ok" || got.LastRun == nil || writes.Load() != 2 {
		t.Fatalf("persisted search: %+v writes=%d err=%v", got, writes.Load(), err)
	}
}

func TestSchedulePostgresSearchPartialSeries(t *testing.T) {
	for _, statistics := range []string{`{"episodeFileCount":1,"episodeCount":2}`, `{"episodeFileCount":1}`} {
		t.Run(statistics, func(t *testing.T) {
			a := schedulerAppDatabase(t, "postgres://hermes@127.0.0.1:55473/mrw_arr_review_fix?sslmode=disable")
			var writes atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/v3/series":
					fmt.Fprintf(w, `[{"id":7,"tvdbId":142,"monitored":true,"statistics":%s}]`, statistics)
				case r.Method == "GET" && r.URL.Path == "/api/v3/series/7":
					fmt.Fprintf(w, `{"id":7,"tvdbId":142,"monitored":true,"statistics":%s}`, statistics)
				case r.Method == "POST" && r.URL.Path == "/api/v3/command":
					var payload struct {
						Name     string `json:"name"`
						SeriesID int    `json:"seriesId"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Name != "SeriesSearch" || payload.SeriesID != 7 {
						t.Errorf("incorrect search: %+v %v", payload, err)
					}
					writes.Add(1)
					fmt.Fprint(w, `{"id":101}`)
				case r.Method == "GET" && r.URL.Path == "/api/v3/command/101":
					fmt.Fprint(w, `{"id":101,"name":"SeriesSearch","body":{"name":"SeriesSearch","seriesId":7}}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer srv.Close()
			r := schedulerRelease(t, a, "show", 42, 142, true)
			s := defaultSettings()
			s.Automation.Enabled = true
			s.Sonarr = ArrSettings{URL: srv.URL, APIKey: "synthetic-key", Enabled: true, Search: true}
			if err := a.saveSettings(a.ctx, s); err != nil {
				t.Fatal(err)
			}
			if err := a.syncArr(a.ctx); err != nil {
				t.Fatal(err)
			}
			got, err := a.getRelease(a.ctx, r.ID)
			if err != nil || got.Acquisition["sonarr_ready"] != "true" {
				t.Fatalf("has-files UI evidence changed: %+v %v", got.Acquisition, err)
			}
			if err := a.searchMissing(a.ctx, Task{Type: "search_missing", AllowDownloadSearch: true}); err != nil {
				t.Fatal(err)
			}
			if writes.Load() != 1 {
				t.Fatalf("partial/unknown series requires one SeriesSearch, got %d", writes.Load())
			}
		})
	}
}

func TestSchedulePostgresSearchLostSharedLease(t *testing.T) {
	for _, tc := range []struct {
		name, path          string
		waitForCancellation bool
	}{
		{"inventory pre-POST fence", "/api/v3/movie", false},
		{"identity pre-POST fence", "/api/v3/movie/7", false},
		{"blocked inventory cancellation", "/api/v3/movie", true},
		{"blocked identity cancellation", "/api/v3/movie/7", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := schedulerAppDatabase(t, "postgres://hermes@127.0.0.1:55473/mrw_arr_review_fix?sslmode=disable")
			ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
			defer cancel()
			// A distinct instance/pool, but the same application schema/database.
			db, err := pgxpool.NewWithConfig(ctx, a.db.Config())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			b := &App{db: db, cipher: a.cipher, ctx: ctx}
			schedulerRelease(t, a, "movie", 42, 0, true)
			entered, unblock := make(chan struct{}), make(chan struct{})
			canceledHTTP := make(chan struct{})
			var blocked atomic.Bool
			var writes atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && r.URL.Path == tc.path && blocked.CompareAndSwap(false, true) {
					close(entered)
					select {
					case <-unblock:
					case <-r.Context().Done():
						close(canceledHTTP)
						return
					}
				}
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/v3/movie":
					fmt.Fprint(w, `[{"id":7,"tmdbId":42,"monitored":true,"hasFile":false}]`)
				case r.Method == "GET" && r.URL.Path == "/api/v3/movie/7":
					fmt.Fprint(w, `{"id":7,"tmdbId":42,"monitored":true,"hasFile":false}`)
				case r.Method == "POST" && r.URL.Path == "/api/v3/command":
					writes.Add(1)
					fmt.Fprint(w, `{"id":101}`)
				case r.Method == "GET" && r.URL.Path == "/api/v3/command/101":
					fmt.Fprint(w, `{"id":101,"name":"MoviesSearch","body":{"name":"MoviesSearch","movieIds":[7]}}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer srv.Close()
			var unblockOnce sync.Once
			releaseHTTP := func() { unblockOnce.Do(func() { close(unblock) }) }
			defer releaseHTTP()
			s := defaultSettings()
			s.Automation.Enabled = true
			s.Radarr = ArrSettings{URL: srv.URL, APIKey: "synthetic-key", Enabled: true, Search: true}
			if err := a.saveSettings(ctx, s); err != nil {
				t.Fatal(err)
			}
			createClaim := func(app *App) *scheduleClaim {
				t.Helper()
				rec := schedulerRequest(app, "POST", "/api/tasks", `{"type":"search_missing","name":"Lease regression","enabled":true,"allow_download_search":true,"frequency":"once","date":"2027-01-01","time":"12:00","timezone":"UTC"}`)
				if rec.Code != 201 {
					t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
				}
				var task Task
				if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
					t.Fatal(err)
				}
				claim, err := app.claimSchedule(ctx, task.ID, true)
				if err != nil {
					t.Fatal(err)
				}
				return claim
			}
			first, second := createClaim(a), createClaim(b)
			firstDone := make(chan struct{})
			go func() { defer close(firstDone); a.executeSchedule(ctx, first) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("first request never blocked")
			}
			// Never terminate arbitrary PostgreSQL sessions. Only this disposable
			// database's exact one-bigint shared search lock holder is eligible.
			var terminated bool
			var leasePID int32
			if err := b.db.QueryRow(ctx, `SELECT pid FROM pg_locks WHERE locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND current_database()='mrw_arr_review_fix' AND classid=0 AND objid=486722127 AND objsubid=1 AND granted`).Scan(&leasePID); err != nil {
				t.Fatal(err)
			}
			err = b.db.QueryRow(ctx, `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND current_database()='mrw_arr_review_fix' AND classid=0 AND objid=486722127 AND objsubid=1 AND granted`, leasePID).Scan(&terminated)
			if err != nil || !terminated {
				t.Fatalf("terminate shared lease: %t %v", terminated, err)
			}
			for {
				var held bool
				if err := b.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND classid=0 AND objid=486722127 AND objsubid=1 AND granted)`, leasePID).Scan(&held); err != nil {
					t.Fatal(err)
				}
				if !held {
					break
				}
				select {
				case <-time.After(10 * time.Millisecond):
				case <-ctx.Done():
					t.Fatal("terminated lease was not released")
				}
			}
			secondDone := make(chan struct{})
			go func() { defer close(secondDone); b.executeSchedule(ctx, second) }()
			select {
			case <-secondDone:
			case <-ctx.Done():
				t.Fatal("second instance could not finish after lease loss")
			}
			if writes.Load() != 1 {
				t.Fatalf("second instance search writes=%d", writes.Load())
			}
			if tc.waitForCancellation {
				select {
				case <-canceledHTTP:
				case <-time.After(7 * time.Second):
					t.Fatal("shared-lease monitor did not cancel blocked HTTP")
				}
			}
			// Fast cases release inside the heartbeat interval to exercise the
			// synchronous guard, independently of periodic cancellation.
			releaseHTTP()
			select {
			case <-firstDone:
			case <-ctx.Done():
				t.Fatal("lost-lease worker did not stop")
			}
			var firstStatus, secondStatus string
			if err := a.db.QueryRow(ctx, `SELECT last_status FROM scheduled_tasks WHERE id=$1`, first.task.ID).Scan(&firstStatus); err != nil {
				t.Fatal(err)
			}
			if err := b.db.QueryRow(ctx, `SELECT last_status FROM scheduled_tasks WHERE id=$1`, second.task.ID).Scan(&secondStatus); err != nil {
				t.Fatal(err)
			}
			if firstStatus != "error" || secondStatus != "ok" || writes.Load() != 1 {
				t.Fatalf("lost lease must fence first: first=%s second=%s POSTs=%d", firstStatus, secondStatus, writes.Load())
			}
			if a.db.Stat().AcquiredConns() != 0 || b.db.Stat().AcquiredConns() != 0 {
				t.Fatal("lease/claim leaked pool capacity")
			}
		})
	}
}

func TestSchedulePostgresSearchSafetyLimit(t *testing.T) {
	a := schedulerApp(t)
	var writes atomic.Int32
	items := []map[string]any{}
	for i := 1; i <= maxScheduleSearchCommands+1; i++ {
		schedulerRelease(t, a, "movie", 1000+i, 0, true)
		items = append(items, map[string]any{"id": i, "tmdbId": 1000 + i, "monitored": true, "hasFile": false})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v3/command" {
			var payload struct {
				Name string `json:"name"`
				IDs  []int  `json:"movieIds"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Name != "MoviesSearch" || len(payload.IDs) != 1 {
				t.Errorf("unbounded command: %+v %v", payload, err)
				w.WriteHeader(400)
				return
			}
			writes.Add(1)
			fmt.Fprintf(w, `{"id":%d}`, payload.IDs[0])
			return
		}
		if r.Method != "GET" {
			t.Errorf("unexpected write: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		if r.URL.Path == "/api/v3/movie" {
			_ = json.NewEncoder(w).Encode(items)
			return
		}
		var id int
		if n, _ := fmt.Sscanf(r.URL.Path, "/api/v3/movie/%d", &id); n == 1 {
			fmt.Fprintf(w, `{"id":%d,"tmdbId":%d,"monitored":true}`, id, 1000+id)
			return
		}
		if n, _ := fmt.Sscanf(r.URL.Path, "/api/v3/command/%d", &id); n == 1 {
			fmt.Fprintf(w, `{"id":%d,"name":"MoviesSearch","body":{"name":"MoviesSearch","movieIds":[%d]}}`, id, id)
			return
		}
		t.Errorf("unexpected read: %s", r.URL.Path)
		w.WriteHeader(404)
	}))
	defer srv.Close()
	s := defaultSettings()
	s.Automation.Enabled = true
	s.Radarr = ArrSettings{URL: srv.URL, APIKey: "synthetic-key", Enabled: true, Search: true}
	if err := a.saveSettings(a.ctx, s); err != nil {
		t.Fatal(err)
	}
	err := a.searchMissing(a.ctx, Task{AllowDownloadSearch: true})
	if err == nil || !strings.Contains(err.Error(), "safety limit") || writes.Load() != int32(maxScheduleSearchCommands) {
		t.Fatalf("safety cap: writes=%d error=%v", writes.Load(), err)
	}
}

func TestSchedulePostgresSearchRechecksMonitoringAndConsent(t *testing.T) {
	for _, revoke := range []string{"monitoring", "automatic", "search"} {
		t.Run(revoke, func(t *testing.T) {
			a := schedulerApp(t)
			r := schedulerRelease(t, a, "movie", 42, 0, true)
			s := defaultSettings()
			s.Automation.Enabled = true
			var writes atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.Method != "GET" || request.URL.Path != "/api/v3/movie" {
					writes.Add(1)
					t.Errorf("consent revoked but request continued: %s %s", request.Method, request.URL.Path)
					w.WriteHeader(400)
					return
				}
				var err error
				switch revoke {
				case "monitoring":
					_, err = a.db.Exec(a.ctx, `UPDATE releases SET monitored=false,monitoring_override=false WHERE id=$1`, r.ID)
				case "automatic":
					s.Automation.Enabled = false
					err = a.saveSettings(a.ctx, s)
				case "search":
					s.Radarr.Search = false
					err = a.saveSettings(a.ctx, s)
				}
				if err != nil {
					t.Error(err)
				}
				fmt.Fprint(w, `[{"id":7,"tmdbId":42,"monitored":true,"hasFile":false}]`)
			}))
			defer srv.Close()
			s.Radarr = ArrSettings{URL: srv.URL, APIKey: "synthetic-key", Enabled: true, Search: true}
			if err := a.saveSettings(a.ctx, s); err != nil {
				t.Fatal(err)
			}
			err := a.searchMissing(a.ctx, Task{AllowDownloadSearch: true})
			if revoke != "monitoring" && err == nil {
				t.Fatal("revoked consent reported success")
			}
			if writes.Load() != 0 {
				t.Fatalf("requests after revocation: %d", writes.Load())
			}
		})
	}
}

func TestSchedulePostgresPresenceTaskDispatch(t *testing.T) {
	for _, kind := range []string{"arr_sync", "silo_sync"} {
		t.Run(kind, func(t *testing.T) {
			a := schedulerApp(t)
			var reads atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Errorf("presence task attempted a write: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(400)
					return
				}
				reads.Add(1)
				switch r.URL.Path {
				case "/api/v3/movie":
					fmt.Fprint(w, `[{"id":7,"tmdbId":42,"hasFile":true,"movieFile":{"id":3,"size":100}}]`)
				case "/api/v2/profiles":
					fmt.Fprint(w, `{"items":[{"id":"primary","is_primary":true}]}`)
				case "/api/v2/catalog":
					fmt.Fprint(w, `{"items":[{"content_id":"local","type":"movie"}],"page":{"has_more":false}}`)
				case "/api/v2/catalog/items/local":
					fmt.Fprint(w, `{"content_id":"local","type":"movie","tmdb_id":"42"}`)

				default:
					t.Errorf("unexpected presence read: %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			s := defaultSettings()
			if kind == "arr_sync" {
				s.Radarr = ArrSettings{URL: srv.URL, APIKey: "synthetic-key"}
			} else {
				s.Silo = SiloSettings{URL: srv.URL, APIKey: "synthetic-key", Enabled: true}
			}
			if err := a.saveSettings(a.ctx, s); err != nil {
				t.Fatal(err)
			}
			r := schedulerRelease(t, a, "movie", 42, 0, false)
			task := schedulerCreate(t, a, kind)
			claim, err := a.claimSchedule(a.ctx, task.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			a.executeSchedule(a.ctx, claim)
			got, err := scanSchedule(a.db.QueryRow(a.ctx, `SELECT `+scheduleColumns+` FROM scheduled_tasks WHERE id=$1`, task.ID))
			if err != nil || got.LastStatus != "ok" || reads.Load() == 0 {
				t.Fatalf("task dispatch: %+v reads=%d err=%v", got, reads.Load(), err)
			}
			if kind == "silo_sync" {
				var status, link, checkedAt string
				if err := a.db.QueryRow(a.ctx, `SELECT data->'local_availability'->>'status',data->'local_availability'->>'url',data->'local_availability'->>'checked_at' FROM releases WHERE id=$1`, r.ID).Scan(&status, &link, &checkedAt); err != nil || status != "available" || link != srv.URL+"/item/local" || checkedAt == "" || reads.Load() != 3 {
					t.Fatalf("catalog presence not persisted: status=%s link=%s checked_at=%s reads=%d err=%v", status, link, checkedAt, reads.Load(), err)
				}
			} else {
				var ready bool
				if err := a.db.QueryRow(a.ctx, `SELECT acquisition->>'radarr_ready'='true' FROM releases WHERE id=$1`, r.ID).Scan(&ready); err != nil || !ready {
					t.Fatalf("Arr readiness not persisted: %v %v", ready, err)
				}
			}
		})
	}
}

func TestSchedulePostgresEnableConsentAndRequestValidation(t *testing.T) {
	a := schedulerApp(t)
	rec := schedulerRequest(a, "POST", "/api/tasks", `{"type":"search_missing","name":"Search disabled","enabled":false,"frequency":"daily","date":"2026-01-01","time":"12:00","timezone":"UTC"}`)
	if rec.Code != 201 {
		t.Fatalf("disabled search: %d %s", rec.Code, rec.Body.String())
	}
	var task Task
	if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/tasks/%d", task.ID)
	for _, body := range []string{`{"enabled":true}`, `{"next_run":"2099-01-01T00:00:00Z"}`, `{"enabled":null}`, `null`, `{"unknown":true}`} {
		if got := schedulerRequest(a, "PATCH", path, body); got.Code != 400 {
			t.Errorf("invalid patch %s: %d %s", body, got.Code, got.Body.String())
		}
	}
	if got := schedulerRequest(a, "POST", path+"/run", ""); got.Code != 403 {
		t.Errorf("run without task consent: %d %s", got.Code, got.Body.String())
	}
	if got := schedulerRequest(a, "PATCH", path, `{"enabled":true,"allow_download_search":true}`); got.Code != 200 {
		t.Errorf("consented enable: %d %s", got.Code, got.Body.String())
	}
	for _, method := range []string{"PATCH", "DELETE", "POST"} {
		url := "/api/tasks/invalid"
		if method == "POST" {
			url += "/run"
		}
		if got := schedulerRequest(a, method, url, `{}`); got.Code != 400 {
			t.Errorf("invalid ID: %d %s", got.Code, got.Body.String())
		}
	}
}

func TestSchedulePostgresCompletedOnceRemainsConsumedOnEnable(t *testing.T) {
	a := schedulerApp(t)
	rec := schedulerRequest(a, "POST", "/api/tasks", `{"type":"catalog_sync","name":"Once","enabled":true,"frequency":"once","date":"2099-01-01","time":"12:00","timezone":"UTC"}`)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var task Task
	_ = json.Unmarshal(rec.Body.Bytes(), &task)
	claim, err := a.claimSchedule(a.ctx, task.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	a.executeSchedule(a.ctx, claim)
	path := fmt.Sprintf("/api/tasks/%d", task.ID)
	for _, body := range []string{`{"enabled":false}`, `{"enabled":true,"name":"Still consumed"}`} {
		if got := schedulerRequest(a, "PATCH", path, body); got.Code != 200 {
			t.Fatalf("patch: %d %s", got.Code, got.Body.String())
		}
	}
	got, err := scanSchedule(a.db.QueryRow(a.ctx, `SELECT `+scheduleColumns+` FROM scheduled_tasks WHERE id=$1`, task.ID))
	if err != nil || got.NextRun != nil || !got.Enabled {
		t.Fatalf("completed once rearmed: %+v %v", got, err)
	}
}

func TestSchedulePostgresNewAndStartUsePersistedTasks(t *testing.T) {
	a := schedulerApp(t)
	disabled := schedulerCreate(t, a, "catalog_sync")
	due := schedulerCreate(t, a, "catalog_sync")
	if rec := schedulerRequest(a, "PATCH", fmt.Sprintf("/api/tasks/%d", disabled.ID), `{"enabled":false,"name":"Preserved disabled task","interval_minutes":77}`); rec.Code != 200 {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := a.db.Exec(a.ctx, `UPDATE scheduled_tasks SET next_run=now()-interval '30 days'`); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		DatabaseURL: "postgres://hermes@127.0.0.1:55473/mrw_schedule_test?sslmode=disable&search_path=" + a.db.Config().ConnConfig.RuntimeParams["search_path"],
		Secret:      "synthetic-scheduler-test-secret-at-least-32-characters",
		DataDir:     t.TempDir(),
	}
	restarted, err := New(a.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	var count int
	if err := a.db.QueryRow(a.ctx, `SELECT count(*) FROM scheduled_tasks`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("startup replaced existing jobs: count=%d err=%v", count, err)
	}
	restarted.Start()
	deadline := time.Now().Add(5 * time.Second)
	var got Task
	for {
		got, err = scanSchedule(a.db.QueryRow(a.ctx, `SELECT `+scheduleColumns+` FROM scheduled_tasks WHERE id=$1`, due.ID))
		if err != nil {
			t.Fatal(err)
		}
		if got.LastStatus == "error" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Start did not execute persisted due task: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(got.LastError, "MDBList API key is not configured") || got.NextRun == nil || !got.NextRun.After(time.Now()) {
		t.Fatalf("startup execution state: %+v", got)
	}
	got, err = scanSchedule(a.db.QueryRow(a.ctx, `SELECT `+scheduleColumns+` FROM scheduled_tasks WHERE id=$1`, disabled.ID))
	if err != nil || got.Enabled || got.Name != "Preserved disabled task" || got.IntervalMinutes != 77 || got.LastStatus != "never" {
		t.Fatalf("disabled job changed or executed at startup: %+v %v", got, err)
	}
}

func TestScheduleValidation(t *testing.T) {
	base := Task{Type: "catalog_sync", Name: "Synthetic", Frequency: "daily", Date: "2026-01-01", Time: "12:00", Timezone: "UTC"}
	cases := []struct {
		name   string
		mutate func(*Task)
	}{
		{"type", func(v *Task) { v.Type = "downloads" }},
		{"name", func(v *Task) { v.Name = " " }},
		{"frequency", func(v *Task) { v.Frequency = "hourly" }},
		{"date", func(v *Task) { v.Date = "2026-02-30" }},
		{"noncanonical date", func(v *Task) { v.Date = "2026-1-01" }},
		{"time", func(v *Task) { v.Time = "9:00" }},
		{"zone", func(v *Task) { v.Timezone = "Local" }},
		{"unknown zone", func(v *Task) { v.Timezone = "Mars/Olympus" }},
		{"interval zero", func(v *Task) { v.Frequency = "interval" }},
		{"interval overflow", func(v *Task) { v.Frequency = "interval"; v.IntervalMinutes = 2147483647 }},
		{"weekly empty", func(v *Task) { v.Frequency = "weekly" }},
		{"weekday range", func(v *Task) { v.Frequency = "weekly"; v.Weekdays = []int{7} }},
		{"weekday duplicates", func(v *Task) { v.Frequency = "weekly"; v.Weekdays = []int{1, 1} }},
		{"search consent", func(v *Task) { v.Type = "search_missing"; v.Enabled = true }},
		{"nonexistent once", func(v *Task) {
			v.Frequency = "once"
			v.Date = "2026-03-08"
			v.Time = "02:30"
			v.Timezone = "America/New_York"
		}},
	}
	if err := validateTask(base); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := base
			c.mutate(&v)
			if err := validateTask(v); err == nil {
				t.Fatal("accepted invalid task")
			}
		})
	}
}
