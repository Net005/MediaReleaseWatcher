package app

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

//go:embed web
var assets embed.FS

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if e := d.Decode(&raw); e != nil || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		fail(w, 400, "expected a JSON object within 1 MiB")
		return false
	}
	if e := json.Unmarshal(raw, v); e != nil {
		fail(w, 400, "invalid JSON or body exceeds 1 MiB")
		return false
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		fail(w, 400, "expected one JSON object")
		return false
	}
	return true
}
func writeMethod(m string) bool { return m != "GET" && m != "HEAD" && m != "OPTIONS" }
func (a *App) originAllowed(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, e := url.Parse(o)
	if e != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if strings.EqualFold(u.Host, r.Host) && u.Scheme == scheme && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == "" {
		return true
	}
	s, e := a.settings(r.Context())
	if e == nil && s.PublicURL != "" {
		p, e := url.Parse(s.PublicURL)
		return e == nil && strings.EqualFold(p.Host, u.Host) && p.Scheme == u.Scheme
	}
	return false
}
func (a *App) authenticated(r *http.Request) bool {
	c, e := r.Cookie("mrw_session")
	if e != nil || len(c.Value) != 64 {
		return false
	}
	var ok bool
	e = a.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sessions WHERE token_hash=$1 AND expires_at>now())`, tokenHash(c.Value)).Scan(&ok)
	return e == nil && ok
}
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if a.db.Ping(ctx) != nil {
			fail(w, 503, "database unavailable")
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/session", a.session)
	mux.HandleFunc("POST /api/setup", a.setup)
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)
	mux.HandleFunc("GET /api/dashboard", a.dashboard)
	mux.HandleFunc("GET /api/settings", a.getSettings)
	mux.HandleFunc("PUT /api/settings", a.putSettings)
	mux.HandleFunc("GET /api/releases", a.listReleases)
	mux.HandleFunc("GET /api/mdblist/lists", a.listMDBLists)
	mux.HandleFunc("POST /api/releases", a.addRelease)
	mux.HandleFunc("GET /api/releases/{id}", a.releaseDetail)
	mux.HandleFunc("GET /api/releases/{id}/cast", a.releaseCast)
	mux.HandleFunc("GET /api/people/{id}", a.personDetail)
	mux.HandleFunc("PATCH /api/releases/{id}", a.patchRelease)
	mux.HandleFunc("GET /api/actresses", a.listActresses)
	mux.HandleFunc("POST /api/actresses", a.addActress)
	mux.HandleFunc("POST /api/actresses/import", a.importActresses)
	mux.HandleFunc("GET /api/actresses/{id}", a.actressDetail)
	mux.HandleFunc("PATCH /api/actresses/{id}", a.patchActress)
	mux.HandleFunc("DELETE /api/actresses/{id}", a.deleteActress)
	mux.HandleFunc("GET /api/search", a.search)
	mux.HandleFunc("POST /api/sync", func(w http.ResponseWriter, r *http.Request) {
		if !a.startSync() {
			fail(w, 409, "sync already running")
			return
		}
		respond(w, 202, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/activity", func(w http.ResponseWriter, r *http.Request) {
		p := queryInt(r, "page", 1)
		limit := queryInt(r, "limit", 100)
		if limit > 500 {
			limit = 500
		}
		items, total, e := a.activities(r.Context(), limit, (p-1)*limit)
		if e != nil {
			fail(w, 500, "cannot read activity")
			return
		}
		respond(w, 200, map[string]any{"items": items, "total": total})
	})
	mux.HandleFunc("GET /api/integrations/{kind}/options", a.integrationOptions)
	mux.HandleFunc("POST /api/integrations/{kind}/test", a.testIntegration)
	mux.HandleFunc("POST /api/releases/{id}/acquire", a.acquireRelease)
	mux.HandleFunc("POST /api/actresses/{id}/acquire", a.acquireActress)
	mux.HandleFunc("GET /api/artwork", a.artwork)
	a.registerUpgradeRoutes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			fail(w, 404, "API route not found")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			fail(w, 405, "method not allowed")
			return
		}
		path := r.URL.Path
		switch path {
		case "/", "/index.html":
			path = "/index.html"
		case "/app.js", "/styles.css":
		default:
			fail(w, 404, "not found")
			return
		}
		b, e := assets.ReadFile("web" + path)
		if e != nil {
			fail(w, 404, "not found")
			return
		}
		switch path {
		case "/app.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case "/styles.css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		_, _ = w.Write(b)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https://image.tmdb.org https://images.mdblist.com https://mdblist.com data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if writeMethod(r.Method) && !a.originAllowed(r) {
			fail(w, 403, "cross-origin writes are forbidden")
			return
		}
		public := r.URL.Path == "/api/session" || r.URL.Path == "/api/setup" || r.URL.Path == "/api/login"
		if strings.HasPrefix(r.URL.Path, "/api/") && !public && !a.authenticated(r) {
			fail(w, 401, "authentication required")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *App) session(w http.ResponseWriter, r *http.Request) {
	var exists bool
	if e := a.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM account)`).Scan(&exists); e != nil {
		fail(w, 503, "database unavailable")
		return
	}
	respond(w, 200, map[string]bool{"authenticated": a.authenticated(r), "needs_setup": !exists})
}
func (a *App) setup(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	var exists bool
	if e := a.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM account)`).Scan(&exists); e != nil {
		fail(w, 503, "database unavailable")
		return
	}
	if exists {
		fail(w, 409, "setup already completed")
		return
	}
	if len(v.Password) < 12 || len(v.Password) > 72 {
		fail(w, 400, "password must be 12..72 bytes")
		return
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(v.Password), bcrypt.DefaultCost)
	if e != nil {
		fail(w, 500, "password hashing failed")
		return
	}
	tag, e := a.db.Exec(r.Context(), `INSERT INTO account(id,password_hash) VALUES(1,$1) ON CONFLICT DO NOTHING`, hash)
	if e != nil {
		fail(w, 500, "cannot save account")
		return
	}
	if tag.RowsAffected() != 1 {
		fail(w, 409, "setup already completed")
		return
	}
	a.createSession(w, r)
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		host = r.RemoteAddr
	}
	now := time.Now()
	a.rateMu.Lock()
	kept := []time.Time{}
	for _, t := range a.loginAttempts[host] {
		if now.Sub(t) < 15*time.Minute {
			kept = append(kept, t)
		}
	}
	allowed := len(kept) < 10
	if allowed {
		kept = append(kept, now)
	}
	if len(a.loginAttempts) > 10000 {
		a.loginAttempts = map[string][]time.Time{}
	}
	a.loginAttempts[host] = kept
	a.rateMu.Unlock()
	if !allowed {
		w.Header().Set("Retry-After", "900")
		fail(w, 429, "too many login attempts")
		return
	}
	var v struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	var hash []byte
	e = a.db.QueryRow(r.Context(), `SELECT password_hash FROM account WHERE id=1`).Scan(&hash)
	if e != nil || bcrypt.CompareHashAndPassword(hash, []byte(v.Password)) != nil {
		fail(w, 401, "invalid password")
		return
	}
	a.createSession(w, r)
}
func (a *App) createSession(w http.ResponseWriter, r *http.Request) {
	token, e := randomToken()
	if e != nil {
		fail(w, 500, "cannot create session")
		return
	}
	expires := time.Now().Add(30 * 24 * time.Hour)
	_, e = a.db.Exec(r.Context(), `INSERT INTO sessions(token_hash,expires_at) VALUES($1,$2)`, tokenHash(token), expires)
	if e != nil {
		fail(w, 500, "cannot persist session")
		return
	}
	_, _ = a.db.Exec(r.Context(), `DELETE FROM sessions WHERE expires_at<now()`)
	s, _ := a.settings(r.Context())
	http.SetCookie(w, &http.Cookie{Name: "mrw_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || strings.HasPrefix(s.PublicURL, "https://"), MaxAge: 30 * 24 * 3600, Expires: expires})
	respond(w, 200, map[string]bool{"authenticated": true, "needs_setup": false})
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("mrw_session"); e == nil {
		if _, e = a.db.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, tokenHash(c.Value)); e != nil {
			fail(w, 500, "cannot invalidate session")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "mrw_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	respond(w, 200, map[string]bool{"ok": true})
}
func (a *App) getSettings(w http.ResponseWriter, r *http.Request) {
	s, e := a.settings(r.Context())
	if e != nil {
		fail(w, 500, "cannot read settings")
		return
	}
	respond(w, 200, s.public())
}
func (a *App) putSettings(w http.ResponseWriter, r *http.Request) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	s, e := a.settings(r.Context())
	if e != nil {
		fail(w, 500, "cannot read settings")
		return
	}
	old := s
	if !readJSON(w, r, &s) {
		return
	}
	if s.TMDB.APIKey == "" {
		s.TMDB.APIKey = old.TMDB.APIKey
	}
	if s.MDBList.APIKey == "" {
		s.MDBList.APIKey = old.MDBList.APIKey
	}
	if s.Sonarr.APIKey == "" {
		s.Sonarr.APIKey = old.Sonarr.APIKey
	}
	if s.Radarr.APIKey == "" {
		s.Radarr.APIKey = old.Radarr.APIKey
	}
	if s.Silo.APIKey == "" {
		s.Silo.APIKey = old.Silo.APIKey
	}
	if s.Pushover.Token == "" {
		s.Pushover.Token = old.Pushover.Token
	}
	if s.Pushover.User == "" {
		s.Pushover.User = old.Pushover.User
	}
	if e = validSettings(s); e != nil {
		fail(w, 400, e.Error())
		return
	}
	if e = a.saveSettings(r.Context(), s); e != nil {
		fail(w, 500, "cannot save settings")
		return
	}
	a.activity(r.Context(), "settings", "Settings updated; credentials redacted", "ok")
	respond(w, 200, s.public())
}
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	s := r.PathValue("id")
	id, e := strconv.ParseInt(s, 10, 64)
	if e != nil || id < 1 || !numericID.MatchString(s) {
		fail(w, 400, "invalid ID")
		return 0, false
	}
	return id, true
}
func queryInt(r *http.Request, key string, def int) int {
	n, e := strconv.Atoi(r.URL.Query().Get(key))
	if e != nil || n < 1 {
		return def
	}
	return n
}
func (a *App) listReleases(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := releaseFilter{Query: q.Get("q"), Window: q.Get("window"), Type: q.Get("type"), Group: q.Get("group"), Sort: q.Get("sort"), ListID: q.Get("list_id"), Page: queryInt(r, "page", 1), Limit: queryInt(r, "limit", 36)}
	if f.ListID != "" && !numericID.MatchString(f.ListID) {
		fail(w, 400, "invalid list_id")
		return
	}
	if id := q.Get("actress_id"); id != "" {
		n, e := strconv.ParseInt(id, 10, 64)
		if e != nil || n < 1 {
			fail(w, 400, "invalid actress_id")
			return
		}
		f.ActressID = n
	}
	if !oneOf(f.Window, "", "all", "past", "current", "upcoming") || !oneOf(f.Type, "", "all", "movie", "show") || !oneOf(f.Sort, "", "release", "added", "score", "title") {
		fail(w, 400, "invalid release filter")
		return
	}
	page, e := a.releases(r.Context(), f)
	if e != nil {
		fail(w, 500, "cannot list releases")
		return
	}
	respond(w, 200, page)
}
func oneOf(s string, options ...string) bool {
	for _, o := range options {
		if s == o {
			return true
		}
	}
	return false
}
func resultError(w http.ResponseWriter, e error, what string) {
	if errors.Is(e, pgx.ErrNoRows) {
		fail(w, 404, what+" not found")
	} else {
		fail(w, 500, "cannot load "+what)
	}
}
func (a *App) releaseDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if a.rejectExcludedRelease(w, r, id) {
		return
	}
	v, e := a.getRelease(r.Context(), id)
	if e != nil {
		resultError(w, e, "release")
		return
	}
	respond(w, 200, v)
}
func (a *App) patchRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if a.rejectExcludedRelease(w, r, id) {
		return
	}
	var v struct {
		Monitored *bool `json:"monitored"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	if v.Monitored == nil {
		fail(w, 400, "monitored is required")
		return
	}
	tag, e := a.db.Exec(r.Context(), `UPDATE releases SET monitored=$2,monitoring_override=$2,updated_at=now() WHERE id=$1`, id, *v.Monitored)
	if e != nil {
		fail(w, 500, "cannot update release")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, 404, "release not found")
		return
	}
	a.releaseDetail(w, r)
}
func (a *App) listActresses(w http.ResponseWriter, r *http.Request) {
	upcoming := false
	if raw := r.URL.Query().Get("upcoming_only"); raw != "" {
		var err error
		upcoming, err = strconv.ParseBool(raw)
		if err != nil {
			fail(w, 400, "invalid upcoming_only")
			return
		}
	}
	items, e := a.actresses(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("group"), upcoming)
	if e != nil {
		fail(w, 500, "cannot list actresses")
		return
	}
	respond(w, 200, map[string]any{"items": items, "total": len(items)})
}
func (a *App) actressDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, e := a.getActress(r.Context(), id)
	if e != nil {
		resultError(w, e, "actress")
		return
	}
	p.MetadataStatus = currentPersonMetadataStatus(p.MetadataStatus)
	releases, e := a.allMatched(r.Context(), id)
	if e != nil {
		fail(w, 500, "cannot list matched releases")
		return
	}
	respond(w, 200, map[string]any{"actress": p, "releases": releases, "total": len(releases)})
}
func (a *App) patchActress(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, e := a.getActress(r.Context(), id)
	if e != nil {
		resultError(w, e, "actress")
		return
	}
	var v struct {
		Group     *string `json:"group"`
		Monitored *bool   `json:"monitored"`
		AutoAdd   *bool   `json:"auto_add"`
		Notify    *bool   `json:"notify"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	updates := map[string]any{}
	if v.Group != nil {
		updates["group"] = *v.Group
	}
	if v.Monitored != nil {
		updates["monitored"] = *v.Monitored
	}
	if v.AutoAdd != nil {
		updates["auto_add"] = *v.AutoAdd
	}
	if v.Notify != nil {
		updates["notify"] = *v.Notify
	}
	b, e := json.Marshal(updates)
	if e != nil {
		fail(w, 400, "invalid watcher preferences")
		return
	}
	tx, e := a.db.Begin(r.Context())
	if e != nil {
		fail(w, 500, "cannot update actress")
		return
	}
	defer tx.Rollback(r.Context())
	var prior []byte
	if e = tx.QueryRow(r.Context(), `SELECT data FROM actresses WHERE id=$1 FOR UPDATE`, id).Scan(&prior); e != nil {
		resultError(w, e, "actress")
		return
	}
	var before Actress
	if e = json.Unmarshal(prior, &before); e != nil {
		fail(w, 500, "cannot update actress")
		return
	}
	before.ID = id
	after := before
	if v.Monitored != nil {
		after.Monitored = *v.Monitored
	}
	if v.AutoAdd != nil {
		after.AutoAdd = *v.AutoAdd
	}
	if v.Notify != nil {
		after.Notify = *v.Notify
	}
	tag, e := tx.Exec(r.Context(), `UPDATE actresses SET data=data||$2::jsonb,updated_at=now() WHERE id=$1`, id, b)
	if e != nil {
		fail(w, 500, "cannot update actress")
		return
	}
	if tag.RowsAffected() != 1 {
		fail(w, 404, "actress not found")
		return
	}
	if e = baselineActorActivation(r.Context(), tx, before, after); e == nil {
		_, e = tx.Exec(r.Context(), recomputeMonitoring)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		fail(w, 500, "cannot update actress")
		return
	}
	p, e = a.getActress(r.Context(), id)
	if e != nil {
		resultError(w, e, "actress")
		return
	}
	respond(w, 200, p)
}
func (a *App) deleteActress(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, e := a.db.Begin(r.Context())
	if e != nil {
		fail(w, 500, "cannot delete actress")
		return
	}
	defer tx.Rollback(r.Context())
	tag, e := tx.Exec(r.Context(), `DELETE FROM actresses WHERE id=$1`, id)
	if e != nil {
		fail(w, 500, "cannot delete actress")
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, 404, "actress not found")
		return
	}
	if _, e = tx.Exec(r.Context(), recomputeMonitoring); e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		fail(w, 500, "cannot delete actress")
		return
	}
	a.activity(r.Context(), "watcher", "Removed local watcher; remote MDBList favorites unchanged", "ok")
	respond(w, 200, map[string]bool{"ok": true})
}
func (a *App) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stats := map[string]int{}
	for _, window := range []string{"all", "past", "current", "upcoming"} {
		p, e := a.releases(ctx, releaseFilter{Window: window, Limit: 1})
		if e != nil {
			fail(w, 500, "cannot read dashboard")
			return
		}
		k := window
		if k == "all" {
			k = "releases"
		}
		stats[k] = p.Total
	}
	var actresses, monitored int
	if e := a.db.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE (data->>'monitored')::boolean) FROM actresses`).Scan(&actresses, &monitored); e != nil {
		fail(w, 500, "cannot read dashboard")
		return
	}
	stats["actresses"] = actresses
	stats["monitored"] = monitored
	featured, e := a.releases(ctx, releaseFilter{Sort: "score", Limit: 8})
	if e != nil {
		fail(w, 500, "cannot read dashboard")
		return
	}
	upcoming, e := a.releases(ctx, releaseFilter{Window: "upcoming", Limit: 12})
	if e != nil {
		fail(w, 500, "cannot read dashboard")
		return
	}
	recent, e := a.releases(ctx, releaseFilter{Sort: "added", Limit: 12})
	if e != nil {
		fail(w, 500, "cannot read dashboard")
		return
	}
	activity, _, e := a.activities(ctx, 30, 0)
	if e != nil {
		fail(w, 500, "cannot read activity")
		return
	}
	a.mu.Lock()
	state := a.syncState
	a.mu.Unlock()
	respond(w, 200, map[string]any{"stats": stats, "featured": featured.Items, "upcoming": upcoming.Items, "recent": recent.Items, "activity": activity, "sync": state})
}
