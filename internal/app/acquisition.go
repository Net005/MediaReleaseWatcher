package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"media-release-watcher/internal/integrations"
)

func (a *App) integrationOptions(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if !oneOf(kind, "sonarr", "radarr") {
		fail(w, 404, "integration not found")
		return
	}
	s, e := a.settings(r.Context())
	if e != nil {
		fail(w, 500, "cannot read settings")
		return
	}
	o, e := a.client(s).ArrOptions(r.Context(), kind)
	if e != nil {
		fail(w, 502, providerError(e))
		return
	}
	respond(w, 200, o)
}
func (a *App) testIntegration(w http.ResponseWriter, r *http.Request) {
	s, e := a.settings(r.Context())
	if e != nil {
		fail(w, 500, "cannot read settings")
		return
	}
	kind := r.PathValue("kind")
	switch kind {
	case "radarr", "sonarr":
		_, e = a.client(s).ArrOptions(r.Context(), kind)
	case "pushover":
		e = a.client(s).Notify(r.Context(), integrations.Notification{Title: "Media Release Watcher", Message: "Explicit integration test; background notifications remain opt-in.", Device: s.Pushover.Device})
	default:
		fail(w, 404, "integration not found")
		return
	}
	if e != nil {
		a.activity(r.Context(), "integration", kind+" test failed: "+providerError(e), "error")
		fail(w, 502, providerError(e))
		return
	}
	a.activity(r.Context(), "integration", kind+" test succeeded", "ok")
	respond(w, 200, map[string]bool{"ok": true})
}
func (a *App) acquire(ctx context.Context, r Release, s Settings, target string, search bool, profile int, root string) (integrations.AddResult, error) {
	if !oneOf(target, "radarr", "sonarr") {
		return integrations.AddResult{}, errors.New("target must be radarr or sonarr")
	}
	if (r.Type == "movie" && target != "radarr") || (r.Type == "show" && target != "sonarr") {
		return integrations.AddResult{}, errors.New("media type is incompatible with selected acquisition target")
	}
	cfg := s.Radarr
	if target == "sonarr" {
		cfg = s.Sonarr
	}
	if cfg.URL == "" || cfg.APIKey == "" {
		return integrations.AddResult{}, errors.New("integration is not configured")
	}
	if profile == 0 {
		profile = cfg.QualityProfileID
	}
	if root == "" {
		root = cfg.RootFolder
	}
	if profile <= 0 || root == "" {
		return integrations.AddResult{}, errors.New("valid root folder and positive quality profile ID are required")
	}
	// The adapter validates selected options against remote values and verifies exact POST readback.
	tmdb, _ := strconv.Atoi(r.TMDbID)
	tvdb, _ := strconv.Atoi(r.TVDbID)
	res, e := a.client(s).Add(ctx, target, integrations.Media{Title: r.Title, Type: r.Type, IMDbID: r.IMDbID, PosterURL: r.Poster, TMDbID: tmdb, TVDbID: tvdb, Year: r.Year}, integrations.AddOptions{QualityProfileID: profile, RootFolderPath: root, Monitored: true, Search: search, Tags: []int{}, MinimumAvailability: "released"})
	if e != nil {
		a.activity(ctx, "acquisition", fmt.Sprintf("%s: %s failed: %s", target, r.Title, providerError(e)), "error")
		return res, e
	}
	status := "added"
	if res.AlreadyExists {
		status = "already_exists"
	}
	_, e = a.db.Exec(ctx, `UPDATE releases SET acquisition=jsonb_set(acquisition,ARRAY[$2::text],to_jsonb($3::text)),updated_at=now() WHERE id=$1`, r.ID, target, status)
	if e != nil {
		return res, errors.New("remote add verified but local acquisition state could not be saved; retry is idempotent")
	}
	a.activity(ctx, "acquisition", fmt.Sprintf("%s %s: %s", target, status, r.Title), "ok")
	return res, nil
}
func (a *App) acquireRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	release, e := a.getRelease(r.Context(), id)
	if e != nil {
		resultError(w, e, "release")
		return
	}
	var v struct {
		Target           string `json:"target"`
		Search           bool   `json:"search"`
		QualityProfileID int    `json:"quality_profile_id"`
		RootFolder       string `json:"root_folder"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	if !oneOf(v.Target, "sonarr", "radarr") || (release.Type == "movie" && v.Target != "radarr") || (release.Type == "show" && v.Target != "sonarr") {
		fail(w, 400, "target must be radarr for movies or sonarr for shows")
		return
	}
	s, e := a.settings(r.Context())
	if e != nil {
		fail(w, 500, "cannot read settings")
		return
	}
	res, e := a.acquire(r.Context(), release, s, v.Target, v.Search, v.QualityProfileID, v.RootFolder)
	if e != nil {
		fail(w, 502, providerError(e))
		return
	}
	respond(w, 200, map[string]any{"ok": true, "result": res})
}

type bulkResult struct {
	ReleaseID     int64  `json:"release_id"`
	Title         string `json:"title"`
	OK            bool   `json:"ok"`
	AlreadyExists bool   `json:"already_exists"`
	Error         string `json:"error"`
}

func (a *App) acquireActress(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, e := a.getActress(r.Context(), id); e != nil {
		resultError(w, e, "actress")
		return
	}
	var v struct {
		Scope  string `json:"scope"`
		Search bool   `json:"search"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	if !oneOf(v.Scope, "all", "released", "upcoming") {
		fail(w, 400, "scope must be all, released or upcoming")
		return
	}
	items, e := a.allMatched(r.Context(), id)
	if e != nil {
		fail(w, 500, "cannot read actual matched releases")
		return
	}
	s, e := a.settings(r.Context())
	if e != nil {
		fail(w, 500, "cannot read settings")
		return
	}
	results := []bulkResult{}
	added, failed, skipped := 0, 0, 0
	for _, item := range items {
		window := releaseWindow(item.ReleaseDate, time.Now().UTC())
		if (v.Scope == "released" && (window == "upcoming" || window == "unknown")) || (v.Scope == "upcoming" && window != "upcoming") {
			skipped++
			continue
		}
		row := bulkResult{ReleaseID: item.ID, Title: item.Title}
		if e := r.Context().Err(); e != nil {
			row.Error = "request canceled"
			failed++
			results = append(results, row)
			continue
		}
		target := "radarr"
		if item.Type == "show" {
			target = "sonarr"
		}
		res, e := a.acquire(r.Context(), item, s, target, v.Search, 0, "")
		if e != nil {
			row.Error = providerError(e)
			failed++
		} else {
			row.OK = true
			row.AlreadyExists = res.AlreadyExists
			if res.AlreadyExists {
				skipped++
			} else {
				added++
			}
		}
		results = append(results, row)
	}
	respond(w, 200, map[string]any{"results": results, "added": added, "failed": failed, "skipped": skipped})
}
func (a *App) automate(ctx context.Context, s Settings, baseline bool) error {
	// Capture decisions even while disabled: enabling later never releases historic backlog.
	rows, e := a.db.Query(ctx, `SELECT r.id,bool_or(r.monitored AND COALESCE((p.data->>'monitored')::boolean,false) AND COALESCE((p.data->>'auto_add')::boolean,false)),bool_or(r.monitored AND COALESCE((p.data->>'monitored')::boolean,false) AND COALESCE((p.data->>'notify')::boolean,false)) FROM releases r LEFT JOIN release_people rp ON rp.release_id=r.id LEFT JOIN actresses p ON p.id=rp.actress_id AND COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%' GROUP BY r.id`)
	if e != nil {
		return e
	}
	type decision struct {
		id              int64
		acquire, notify bool
	}
	decisions := []decision{}
	for rows.Next() {
		var d decision
		if e = rows.Scan(&d.id, &d.acquire, &d.notify); e != nil {
			rows.Close()
			return e
		}
		decisions = append(decisions, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, d := range decisions {
		r, e := a.getRelease(ctx, d.id)
		if e != nil {
			return e
		}
		for _, kind := range []string{"acquire", "notify"} {
			status := "skipped"
			if baseline {
				status = "baseline"
			} else if s.Automation.Enabled && oneOf(releaseWindow(r.ReleaseDate, time.Now().UTC()), "current", "upcoming") && ((kind == "acquire" && d.acquire) || (kind == "notify" && d.notify && s.Pushover.Enabled)) {
				if kind == "notify" || ((r.Type == "movie" && s.Radarr.Enabled) || (r.Type == "show" && s.Sonarr.Enabled)) {
					status = "pending"
				}
			}
			_, e = a.db.Exec(ctx, `INSERT INTO actions(release_id,kind,status,event_key) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, r.ID, kind, status, releaseEvent(r, time.Now().UTC()))
			if e != nil {
				return e
			}
		}
	}
	if baseline {
		return nil
	}
	if !s.Automation.Enabled {
		_, e = a.db.Exec(ctx, `UPDATE actions SET status='skipped',error='Automation disabled; backlog suppressed',updated_at=now() WHERE status IN('pending','failed')`)
		return e
	}
	// Durable single-sync lease + atomic claims prevent concurrent processes duplicating actions.
	rows, e = a.db.Query(ctx, `SELECT id,release_id,kind,status,event_key FROM actions WHERE status IN('pending','failed') ORDER BY id LIMIT 200`)
	if e != nil {
		return e
	}
	type action struct {
		id, release         int64
		kind, status, event string
	}
	queue := []action{}
	for rows.Next() {
		var x action
		if e = rows.Scan(&x.id, &x.release, &x.kind, &x.status, &x.event); e != nil {
			rows.Close()
			return e
		}
		queue = append(queue, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, x := range queue {
		if e = ctx.Err(); e != nil {
			return e
		}
		r, e := a.getRelease(ctx, x.release)
		if e != nil {
			return e
		}
		if x.event != releaseEvent(r, time.Now().UTC()) || !oneOf(releaseWindow(r.ReleaseDate, time.Now().UTC()), "current", "upcoming") {
			if _, e = a.db.Exec(ctx, `UPDATE actions SET status='superseded',updated_at=now() WHERE id=$1 AND status=$2`, x.id, x.status); e != nil {
				return e
			}
			continue
		}
		var gate bool
		field := "auto_add"
		if x.kind == "notify" {
			field = "notify"
		}
		e = a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM release_people rp JOIN actresses p ON p.id=rp.actress_id WHERE rp.release_id=$1 AND COALESCE((p.data->>'monitored')::boolean,false) AND COALESCE((p.data->>$2)::boolean,false) AND COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%')`, r.ID, field).Scan(&gate)
		if e != nil {
			return e
		}
		skip := func() error {
			_, err := a.db.Exec(ctx, `UPDATE actions SET status='skipped',error='Consent disabled; backlog suppressed',updated_at=now() WHERE id=$1 AND status=$2`, x.id, x.status)
			return err
		}
		if !gate || !r.Monitored || (x.kind == "notify" && !s.Pushover.Enabled) {
			if e = skip(); e != nil {
				return e
			}
			continue
		}
		target := "radarr"
		cfg := s.Radarr
		if r.Type == "show" {
			target = "sonarr"
			cfg = s.Sonarr
		}
		if x.kind == "acquire" && !cfg.Enabled {
			if e = skip(); e != nil {
				return e
			}
			continue
		}
		tag, e := a.db.Exec(ctx, `UPDATE actions SET status='processing',updated_at=now() WHERE id=$1 AND status=$2`, x.id, x.status)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			continue
		}
		if x.kind == "acquire" {
			_, e = a.acquire(ctx, r, s, target, cfg.Search, 0, "")
		} else {
			// Pushover has no idempotency key. An uncertain delivery is never automatically retried.
			n := integrations.Notification{Title: truncateRunes("New matched release: "+r.Title, 250), Message: notificationMessage(r), Device: s.Pushover.Device, URLTitle: "View matched release"}
			if s.PublicURL != "" {
				n.URL = strings.TrimRight(s.PublicURL, "/") + "/#/release/" + strconv.FormatInt(r.ID, 10)
			}
			if r.Poster != "" {
				if b, _, artErr := a.fetchArtwork(ctx, r.Poster); artErr == nil {
					n.Attachment = b
					n.AttachmentName = "cover.jpg"
				} else {
					a.activity(ctx, "notification", "Cover unavailable; notification will contain title and release link only", "warning")
				}
			}
			e = a.client(s).Notify(ctx, n)
		}
		status := "done"
		errText := ""
		if e != nil {
			status = "failed"
			if x.kind == "notify" {
				status = "uncertain"
			}
			errText = providerError(e)
			a.activity(ctx, "automation", x.kind+" failed for "+r.Title+": "+errText, "error")
		} else {
			a.activity(ctx, "automation", x.kind+" completed for "+r.Title, "ok")
		}
		finish, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, saveErr := a.db.Exec(finish, `UPDATE actions SET status=$2,error=$3,updated_at=now() WHERE id=$1`, x.id, status, errText)
		done()
		if saveErr != nil {
			return saveErr
		}
	}
	// Crash recovery is safe for Arr (verified idempotent); notifications stay uncertain, not duplicated.
	_, e = a.db.Exec(ctx, `UPDATE actions SET status=CASE WHEN kind='acquire' THEN 'failed' ELSE 'uncertain' END,error='Interrupted action; acquisition may retry idempotently; notification requires manual review',updated_at=now() WHERE status='processing' AND updated_at<now()-interval '1 hour'`)
	return e
}
func personNames(p []PersonLink) string {
	names := []string{}
	for _, p := range p {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}
