package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"media-release-watcher/internal/integrations"
	_ "time/tzdata" // IANA rules are available in minimal deployment images too.
)

type Task struct {
	ID                  int64      `json:"id"`
	Type                string     `json:"type"`
	Name                string     `json:"name"`
	Enabled             bool       `json:"enabled"`
	Frequency           string     `json:"frequency"`
	Date                string     `json:"date"`
	Time                string     `json:"time"`
	Timezone            string     `json:"timezone"`
	IntervalMinutes     int        `json:"interval_minutes"`
	Weekdays            []int      `json:"weekdays"`
	AllowDownloadSearch bool       `json:"allow_download_search"`
	NextRun             *time.Time `json:"next_run"`
	LastRun             *time.Time `json:"last_run"`
	LastStatus          string     `json:"last_status"`
	LastError           string     `json:"last_error"`
}

func validateTask(t Task) error {
	switch t.Type {
	case "catalog_sync", "silo_sync", "arr_sync", "search_missing":
	default:
		return fmt.Errorf("invalid task type")
	}
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 200 {
		return fmt.Errorf("name must contain 1..200 bytes")
	}
	switch t.Frequency {
	case "once", "interval", "daily", "weekly":
	default:
		return fmt.Errorf("invalid frequency")
	}
	date, err := time.Parse("2006-01-02", t.Date)
	if err != nil || date.Format("2006-01-02") != t.Date || date.Year() > 9998 {
		return fmt.Errorf("date must be YYYY-MM-DD")
	}
	clock, err := time.Parse("15:04", t.Time)
	if err != nil || clock.Format("15:04") != t.Time {
		return fmt.Errorf("time must be HH:MM")
	}
	if t.Timezone == "" || t.Timezone == "Local" {
		return fmt.Errorf("timezone must be an IANA zone")
	}
	loc, err := time.LoadLocation(t.Timezone)
	if err != nil {
		return fmt.Errorf("invalid IANA timezone")
	}
	if t.Frequency == "interval" && (t.IntervalMinutes < 1 || t.IntervalMinutes > 525600) {
		return fmt.Errorf("interval_minutes must be 1..525600")
	}
	seen := map[int]bool{}
	for _, day := range t.Weekdays {
		if day < 0 || day > 6 || seen[day] {
			return fmt.Errorf("weekdays must be unique numbers 0..6")
		}
		seen[day] = true
	}
	if t.Frequency == "weekly" && len(seen) == 0 {
		return fmt.Errorf("weekly tasks require weekdays")
	}
	if t.Type == "search_missing" && t.Enabled && !t.AllowDownloadSearch {
		return fmt.Errorf("search_missing requires allow_download_search")
	}
	if (t.Frequency == "once" || t.Frequency == "interval") && scheduleWallTime(date, clock, loc) == nil {
		return fmt.Errorf("date/time does not exist in timezone (DST gap)")
	}
	return nil
}

// A repeated wall-clock time runs only at its first occurrence. A gap has no
// occurrence, so recurring calendar tasks skip that date rather than shifting.
func scheduleWallTime(date, clock time.Time, loc *time.Location) *time.Time {
	wall := time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), 0, 0, time.UTC)
	guess := time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), 0, 0, loc)
	var first *time.Time
	offsets := map[int]bool{}
	for _, hours := range []int{-48, -24, 0, 24, 48} {
		_, offset := guess.Add(time.Duration(hours) * time.Hour).Zone()
		offsets[offset] = true
	}
	for offset := range offsets {
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(loc)
		if local.Format("2006-01-02 15:04") == wall.Format("2006-01-02 15:04") && (first == nil || candidate.Before(*first)) {
			v := candidate
			first = &v
		}
	}
	return first
}

// nextScheduleRun returns the first occurrence strictly after after, never a backlog.
func nextScheduleRun(task Task, after time.Time) (*time.Time, error) {
	if err := validateTask(task); err != nil {
		return nil, err
	}
	loc, _ := time.LoadLocation(task.Timezone)
	date, _ := time.Parse("2006-01-02", task.Date)
	clock, _ := time.Parse("15:04", task.Time)
	if task.Frequency == "once" || task.Frequency == "interval" {
		start := *scheduleWallTime(date, clock, loc)
		if task.Frequency == "once" {
			if start.After(after) {
				return &start, nil
			}
			return nil, nil
		}
		step := time.Duration(task.IntervalMinutes) * time.Minute
		next := start
		if !next.After(after) {
			// Use a remainder relative to now: no duration multiplication overflow and
			// even years of downtime produce only one future occurrence.
			elapsed := after.Unix() - start.Unix()
			next = after.Truncate(time.Second).Add(time.Duration(int64(step/time.Second)-elapsed%int64(step/time.Second)) * time.Second)
		}
		return &next, nil
	}
	localAfter := after.In(loc)
	current := time.Date(localAfter.Year(), localAfter.Month(), localAfter.Day(), 0, 0, 0, 0, time.UTC)
	if current.Before(date) {
		current = date
	}
	for i := 0; i < 370; i++ {
		if task.Frequency == "daily" || scheduleWeekday(task.Weekdays, int(current.Weekday())) {
			next := scheduleWallTime(current, clock, loc)
			if next != nil && next.After(after) {
				return next, nil
			}
		}
		current = current.AddDate(0, 0, 1)
	}
	return nil, fmt.Errorf("no calendar occurrence within one year")
}
func scheduleWeekday(days []int, day int) bool {
	for _, d := range days {
		if d == day {
			return true
		}
	}
	return false
}

const scheduleColumns = `id,data,enabled,next_run,last_run,last_status,last_error`

func scanSchedule(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var raw []byte
	var enabled bool
	var next, last *time.Time
	var status, message string
	err := row.Scan(&t.ID, &raw, &enabled, &next, &last, &status, &message)
	if err != nil {
		return t, err
	}
	id := t.ID
	if err = json.Unmarshal(raw, &t); err != nil {
		return t, err
	}
	t.ID = id
	t.Enabled = enabled
	t.NextRun = next
	t.LastRun = last
	t.LastStatus = status
	t.LastError = message
	if t.Weekdays == nil {
		t.Weekdays = []int{}
	}
	return t, nil
}
func (a *App) ensureSchedules(ctx context.Context) error {
	s, err := a.settings(ctx)
	if err != nil {
		return err
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(486722124)`); err != nil {
		return err
	}
	var seeded bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app_state WHERE key='schedules_seeded')`).Scan(&seeded); err != nil {
		return err
	}
	if !seeded {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM scheduled_tasks)`).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			now := time.Now().UTC()
			interval := s.MDBList.SyncIntervalMinutes
			if interval < 1 || interval > 525600 {
				interval = 360
			}
			task := Task{Type: "catalog_sync", Name: "Catalogue sync", Enabled: true, Frequency: "interval", Date: now.Format("2006-01-02"), Time: now.Format("15:04"), Timezone: "UTC", IntervalMinutes: interval, Weekdays: []int{}}
			next, e := nextScheduleRun(task, now)
			if e != nil {
				return e
			}
			raw, _ := json.Marshal(task)
			if _, err = tx.Exec(ctx, `INSERT INTO scheduled_tasks(data,enabled,next_run) VALUES($1,true,$2)`, raw, next); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO app_state(key,value) VALUES('schedules_seeded','true') ON CONFLICT DO NOTHING`); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (a *App) registerScheduleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tasks", a.listSchedules)
	mux.HandleFunc("POST /api/tasks", a.createSchedule)
	mux.HandleFunc("PATCH /api/tasks/{id}", a.patchSchedule)
	mux.HandleFunc("DELETE /api/tasks/{id}", a.deleteSchedule)
	mux.HandleFunc("POST /api/tasks/{id}/run", a.runScheduleNow)
}
func (a *App) listSchedules(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `SELECT `+scheduleColumns+` FROM scheduled_tasks ORDER BY id`)
	if err != nil {
		fail(w, 500, "cannot read tasks")
		return
	}
	defer rows.Close()
	items := []Task{}
	for rows.Next() {
		task, err := scanSchedule(rows)
		if err != nil {
			fail(w, 500, "cannot read task")
			return
		}
		items = append(items, task)
	}
	if rows.Err() != nil {
		fail(w, 500, "cannot read tasks")
		return
	}
	respond(w, 200, map[string]any{"items": items})
}
func scheduleInput(w http.ResponseWriter, r *http.Request, task *Task) bool {
	var fields map[string]json.RawMessage
	if !readJSON(w, r, &fields) {
		return false
	}
	for key := range fields {
		switch key {
		case "type", "name", "enabled", "frequency", "date", "time", "timezone", "interval_minutes", "weekdays", "allow_download_search":
		default:
			fail(w, 400, "unknown or read-only task field: "+key)
			return false
		}
		if string(fields[key]) == "null" {
			fail(w, 400, "task fields cannot be null")
			return false
		}
	}
	raw, _ := json.Marshal(fields)
	if err := json.Unmarshal(raw, task); err != nil {
		fail(w, 400, "invalid task fields")
		return false
	}
	if err := validateTask(*task); err != nil {
		fail(w, 400, err.Error())
		return false
	}
	return true
}
func (a *App) createSchedule(w http.ResponseWriter, r *http.Request) {
	task := Task{Weekdays: []int{}}
	if !scheduleInput(w, r, &task) {
		return
	}
	next, err := nextScheduleRun(task, time.Now().UTC())
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	raw, _ := json.Marshal(task)
	task, err = scanSchedule(a.db.QueryRow(r.Context(), `INSERT INTO scheduled_tasks(data,enabled,next_run) VALUES($1,$2,$3) RETURNING `+scheduleColumns, raw, task.Enabled, next))
	if err != nil {
		fail(w, 500, "cannot create task")
		return
	}
	respond(w, 201, task)
}
func scheduleID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		fail(w, 400, "invalid task ID")
		return 0, false
	}
	return id, true
}
func scheduleReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 404, "task not found")
	} else {
		fail(w, 500, "cannot read task")
	}
}
func (a *App) patchSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := scheduleID(w, r)
	if !ok {
		return
	}
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		fail(w, 500, "cannot update task")
		return
	}
	defer tx.Rollback(r.Context())
	task, err := scanSchedule(tx.QueryRow(r.Context(), `SELECT `+scheduleColumns+` FROM scheduled_tasks WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		scheduleReadError(w, err)
		return
	}
	if task.LastStatus == "running" {
		fail(w, 409, "task is running")
		return
	}
	original := task
	if !scheduleInput(w, r, &task) {
		return
	}
	// Name/consent/enable-only changes preserve the existing cadence. In particular,
	// disabling and re-enabling a completed once task must not replay it.
	scheduleChanged := task.Frequency != original.Frequency || task.Date != original.Date || task.Time != original.Time || task.Timezone != original.Timezone || task.IntervalMinutes != original.IntervalMinutes || !equalScheduleDays(task.Weekdays, original.Weekdays)
	next := task.NextRun
	if scheduleChanged || (!original.Enabled && task.Enabled && task.Frequency != "once") {
		next, err = nextScheduleRun(task, time.Now().UTC())
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	raw, _ := json.Marshal(task)
	task, err = scanSchedule(tx.QueryRow(r.Context(), `UPDATE scheduled_tasks SET data=$2,enabled=$3,next_run=$4,updated_at=now() WHERE id=$1 RETURNING `+scheduleColumns, id, raw, task.Enabled, next))
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		fail(w, 500, "cannot update task")
		return
	}
	respond(w, 200, task)
}
func equalScheduleDays(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var errScheduleBusy = errors.New("task already running or not due")
var errScheduleRecovered = errors.New("interrupted task recovered without replay; retry an explicit run if desired")
var errScheduleConsent = errors.New("search_missing requires explicit allow_download_search")

type scheduleClaim struct {
	task Task
	conn *pgxpool.Conn
	slot int
}

func (c *scheduleClaim) release() {
	if c.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var unlocked bool
	if c.slot >= 0 {
		if err := c.conn.QueryRow(ctx, `SELECT pg_advisory_unlock(486722128,$1::integer)`, c.slot).Scan(&unlocked); err != nil || !unlocked {
			_ = c.conn.Conn().Close(ctx)
		}
	}
	if err := c.conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1::bigint)`, -c.task.ID).Scan(&unlocked); err != nil || !unlocked {
		// Never put a session with uncertain lock ownership back into the pool.
		_ = c.conn.Conn().Close(ctx)
	}
	c.conn.Release()
	c.conn = nil
}
func (a *App) claimSchedule(ctx context.Context, id int64, manual bool) (*scheduleClaim, error) {
	conn, err := a.db.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	claim := &scheduleClaim{task: Task{ID: id}, conn: conn, slot: -1}
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::bigint)`, -id).Scan(&locked); err != nil {
		conn.Release()
		return nil, err
	}
	if !locked {
		conn.Release()
		return nil, errScheduleBusy
	}
	keep := false
	defer func() {
		if !keep {
			claim.release()
		}
	}()
	// A running task keeps this session plus its provider lease and needs spare
	// query capacity. Bound concurrent claims across instances so run-now floods
	// cannot consume the entire pool and deadlock every worker (or App.Close).
	capacity := int((a.db.Config().MaxConns - 1) / 3)
	for slot := 0; slot < capacity; slot++ {
		if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(486722128,$1::integer)`, slot).Scan(&locked); err != nil {
			return nil, err
		}
		if locked {
			claim.slot = slot
			break
		}
	}
	if claim.slot < 0 {
		return nil, errScheduleBusy
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	task, err := scanSchedule(tx.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM scheduled_tasks WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if task.LastStatus == "running" {
		// A session lock cannot expire while its owner is alive. Acquiring it with a
		// persisted running status proves loss of the old owner. Never replay an
		// uncertain external action after a crash, including one-shot tasks.
		next, e := nextScheduleRun(task, now)
		if e != nil || task.Frequency == "once" {
			next = nil
		}
		_, err = tx.Exec(ctx, `UPDATE scheduled_tasks SET last_status='error',last_error='Previous execution interrupted; not replayed automatically',next_run=$2,updated_at=now() WHERE id=$1`, id, next)
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			return nil, err
		}
		return nil, errScheduleRecovered
	}
	if !manual && (!task.Enabled || task.NextRun == nil || task.NextRun.After(now)) {
		return nil, errScheduleBusy
	}
	if task.Type == "search_missing" && !task.AllowDownloadSearch {
		return nil, errScheduleConsent
	}
	next, err := nextScheduleRun(task, now)
	if err != nil {
		return nil, err
	}
	if task.Frequency == "once" {
		next = nil
	}
	_, err = tx.Exec(ctx, `UPDATE scheduled_tasks SET next_run=$2,last_run=$3,last_status='running',last_error='',updated_at=now() WHERE id=$1`, id, next, now)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	task.NextRun = next
	task.LastRun = &now
	task.LastStatus = "running"
	task.LastError = ""
	claim.task = task
	keep = true
	return claim, nil
}
func (a *App) runScheduleNow(w http.ResponseWriter, r *http.Request) {
	id, ok := scheduleID(w, r)
	if !ok {
		return
	}
	if a.ctx.Err() != nil {
		fail(w, 503, "application is shutting down")
		return
	}
	claimCtx, cancelClaim := context.WithTimeout(r.Context(), 5*time.Second)
	stop := context.AfterFunc(a.ctx, cancelClaim)
	defer stop()
	defer cancelClaim()
	claim, err := a.claimSchedule(claimCtx, id, true)
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			fail(w, 404, "task not found")
		case errors.Is(err, errScheduleBusy), errors.Is(err, errScheduleRecovered):
			fail(w, 409, err.Error())
		case errors.Is(err, errScheduleConsent):
			fail(w, 403, err.Error())
		default:
			fail(w, 500, "cannot claim task")
		}
		return
	}
	// Synchronize Add with Close, but never hold the lifecycle mutex over DB I/O.
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		claim.release()
		fail(w, 503, "application is shutting down")
		return
	}
	a.wg.Add(1)
	go func() { defer a.wg.Done(); a.executeSchedule(a.ctx, claim) }()
	respond(w, 202, map[string]bool{"ok": true})
}
func (a *App) runDueSchedules(ctx context.Context) {
	rows, err := a.db.Query(ctx, `SELECT id FROM scheduled_tasks WHERE last_status='running' OR (enabled AND next_run<=now()) ORDER BY next_run NULLS FIRST,id LIMIT 32`)
	if err != nil {
		return
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		claim, err := a.claimSchedule(ctx, id, false)
		if err != nil {
			continue
		}
		a.executeSchedule(ctx, claim)
	}
}
func (a *App) executeSchedule(parent context.Context, claim *scheduleClaim) {
	a.executeScheduleWithSync(parent, claim, a.syncOnceResult)
}

func (a *App) executeScheduleWithSync(parent context.Context, claim *scheduleClaim, syncRun func(context.Context) (syncCompletion, error)) {
	defer claim.release()
	ctx, cancel := context.WithTimeout(parent, 6*time.Hour)
	defer cancel()
	// Probe the lock-owning session while work runs; loss of DB ownership cancels
	// bounded provider HTTP requests instead of letting an unfenced worker proceed.
	heartbeatDone := make(chan struct{})
	heartbeatStop := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				probe, done := context.WithTimeout(ctx, 5*time.Second)
				_, err := claim.conn.Exec(probe, `SELECT 1`)
				done()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	var runErr error
	var completion syncCompletion
	switch claim.task.Type {
	case "catalog_sync":
		completion, runErr = syncRun(ctx)
	case "silo_sync":
		runErr = a.syncSilo(ctx)
	case "arr_sync":
		runErr = a.syncArr(ctx)
	case "search_missing":
		runErr = a.searchMissing(ctx, claim.task)
	default:
		runErr = fmt.Errorf("invalid task execution type")
	}
	close(heartbeatStop)
	<-heartbeatDone
	status, message := "ok", ""
	if claim.task.Type == "catalog_sync" && runErr == nil && completion.Status == "warning" {
		status, message = completion.Status, completion.Message
	}
	if runErr != nil {
		status, message = presenceSyncOutcome(runErr)
	}
	// Compute from completion, not the old due time: slow jobs and downtime cannot
	// trigger a burst of catch-up runs. Once is consumed before any side effect.
	next, nextErr := nextScheduleRun(claim.task, time.Now().UTC())
	if nextErr != nil {
		next = nil
		status = "error"
		message = "Task recurrence is invalid"
	}
	if claim.task.Frequency == "once" {
		next = nil
	}
	save, done := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer done()
	if _, err := claim.conn.Exec(save, `UPDATE scheduled_tasks SET last_status=$2,last_error=$3,next_run=$4,updated_at=now() WHERE id=$1`, claim.task.ID, status, message, next); err != nil {
		log.Printf("persist scheduled task completion (task %d): %v", claim.task.ID, err)
	}
}

// search_missing is a bounded search of existing, exact-ID-matched manager
// records, never an add or a manager-wide missing/cutoff search. The additional
// lease prevents different search tasks and app instances searching concurrently.
const maxScheduleSearchCommands = 100

func scheduleSearchManager(s Settings, kind string) (ArrSettings, error) {
	cfg := s.Radarr
	if kind == "sonarr" {
		cfg = s.Sonarr
	}
	if !s.Automation.Enabled {
		return cfg, errors.New("automatic actions must be explicitly enabled for search_missing")
	}
	if !cfg.Enabled || !cfg.Search || cfg.URL == "" || cfg.APIKey == "" {
		return cfg, fmt.Errorf("%s must be configured with acquisition and search explicitly enabled", kind)
	}
	return cfg, nil
}

func (a *App) searchMissing(ctx context.Context, task Task) error {
	if !task.AllowDownloadSearch {
		return errScheduleConsent
	}
	s, err := a.settings(ctx)
	if err != nil {
		return err
	}
	if !s.Automation.Enabled {
		return errors.New("automatic actions must be explicitly enabled for search_missing")
	}
	_, radarrErr := scheduleSearchManager(s, "radarr")
	_, sonarrErr := scheduleSearchManager(s, "sonarr")
	if radarrErr != nil && sonarrErr != nil {
		return errors.New("search_missing requires a configured, enabled integration with search explicitly enabled")
	}
	lease, err := a.searchPresenceLease(ctx)
	if err != nil {
		return err
	}
	defer lease.release()
	ctx = lease.ctx
	// Materialize and close the rows before acquiring any further pool connection.
	// Scope is the app's monitored catalogue, not the manager's whole inventory.
	rows, err := a.db.Query(ctx, `SELECT id,media_type,tmdb_id,imdb_id,COALESCE(data->>'tvdb_id','') FROM releases r WHERE monitored AND `+visibleReleasePredicate+` ORDER BY id`)
	if err != nil {
		return err
	}
	releases := []Release{}
	for rows.Next() {
		var r Release
		if err = rows.Scan(&r.ID, &r.Type, &r.TMDbID, &r.IMDbID, &r.TVDbID); err != nil {
			break
		}
		releases = append(releases, r)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	client := a.client(s)
	commands := 0
	for _, manager := range []struct {
		kind, mediaType string
	}{{"radarr", "movie"}, {"sonarr", "show"}} {
		cfg, managerErr := scheduleSearchManager(s, manager.kind)
		if managerErr != nil {
			continue
		}
		var inventory *integrations.ArrInventory
		searched := map[int]bool{}
		for _, r := range releases {
			if r.Type != manager.mediaType {
				continue
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			media, err := presenceMedia(r)
			if err != nil {
				return err // Fail closed on invalid literal identities.
			}
			if inventory == nil {
				inventory, err = client.ArrInventory(ctx, manager.kind)
				if err != nil {
					return err
				}
			}
			presence, err := inventory.Match(media)
			if err != nil {
				return err
			}
			// Ready is has-files UI evidence, not complete series coverage. Without
			// proven coverage a Sonarr series may still have missing episodes.
			if presence.ID <= 0 || (manager.kind == "radarr" && presence.Ready) || (manager.kind == "sonarr" && presence.CompleteCoverage) || searched[presence.ID] {
				continue
			}
			if commands >= maxScheduleSearchCommands {
				return fmt.Errorf("search_missing stopped at its safety limit of %d item searches", maxScheduleSearchCommands)
			}
			// Consent and monitoring can be revoked while inventory is loading. Check
			// immediately before each external write, keeping the matched endpoint.
			current, err := a.settings(ctx)
			if err != nil {
				return err
			}
			currentCfg, err := scheduleSearchManager(current, manager.kind)
			if err != nil {
				return err
			}
			if currentCfg.URL != cfg.URL || currentCfg.APIKey != cfg.APIKey {
				return errors.New("integration configuration changed during search_missing; rerun to rematch identities")
			}
			var monitored bool
			if err = a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM releases r WHERE id=$1 AND monitored AND `+visibleReleasePredicate+` AND media_type=$2 AND tmdb_id=$3 AND imdb_id=$4 AND COALESCE(data->>'tvdb_id','')=$5)`, r.ID, r.Type, r.TMDbID, r.IMDbID, r.TVDbID).Scan(&monitored); err != nil {
				return err
			}
			if !monitored {
				continue
			}
			if err = client.ArrSearchGuarded(ctx, manager.kind, presence.ID, media, lease.check); err != nil {
				// Never retry an ambiguous command POST in this execution.
				return err
			}
			searched[presence.ID] = true
			commands++
		}
	}
	return nil
}

func (a *App) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := scheduleID(w, r)
	if !ok {
		return
	}
	result, err := a.db.Exec(r.Context(), `DELETE FROM scheduled_tasks WHERE id=$1 AND last_status<>'running'`, id)
	if err != nil {
		fail(w, 500, "cannot delete task")
		return
	}
	if result.RowsAffected() == 0 {
		var exists bool
		if err = a.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM scheduled_tasks WHERE id=$1)`, id).Scan(&exists); err != nil {
			fail(w, 500, "cannot read task")
			return
		}
		if exists {
			fail(w, 409, "task is running")
		} else {
			fail(w, 404, "task not found")
		}
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
