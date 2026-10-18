package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/url"
	"strings"
)

type SiloSettings struct {
	URL        string `json:"url"`
	Configured bool   `json:"configured"`
	APIKey     string `json:"api_key,omitempty"`
	Enabled    bool   `json:"enabled"`
}
type MDBListPolicy struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Monitored bool   `json:"monitored"`
	AutoAdd   bool   `json:"auto_add"`
}
type MDBSettings struct {
	Lists               []MDBListPolicy `json:"lists"`
	Configured          bool            `json:"configured"`
	APIKey              string          `json:"api_key,omitempty"`
	ListIDs             string          `json:"list_ids"`
	SyncIntervalMinutes int             `json:"sync_interval_minutes"`
}
type ArrSettings struct {
	URL              string `json:"url"`
	Configured       bool   `json:"configured"`
	APIKey           string `json:"api_key,omitempty"`
	QualityProfileID int    `json:"quality_profile_id"`
	RootFolder       string `json:"root_folder"`
	Enabled          bool   `json:"enabled"`
	Search           bool   `json:"search"`
}
type PushSettings struct {
	Configured bool   `json:"configured"`
	Enabled    bool   `json:"enabled"`
	Device     string `json:"device"`
	Token      string `json:"token,omitempty"`
	User       string `json:"user,omitempty"`
}
type AutomationSettings struct {
	Enabled         bool `json:"enabled"`
	NotifyFirstSync bool `json:"notify_first_sync"`
}
type Settings struct {
	Silo       SiloSettings       `json:"silo"`
	MDBList    MDBSettings        `json:"mdblist"`
	Sonarr     ArrSettings        `json:"sonarr"`
	Radarr     ArrSettings        `json:"radarr"`
	Pushover   PushSettings       `json:"pushover"`
	Automation AutomationSettings `json:"automation"`
	PublicURL  string             `json:"public_url"`
}

func defaultSettings() Settings { return Settings{MDBList: MDBSettings{SyncIntervalMinutes: 360}} }
func (s Settings) public() Settings {
	s.Silo.Configured = s.Silo.URL != "" && s.Silo.APIKey != ""
	s.Silo.APIKey = ""
	s.MDBList.Lists = configuredLists(s.MDBList)
	s.Automation.NotifyFirstSync = false // Retained compatibility field; baseline notifications are unsupported.
	s.MDBList.Configured = s.MDBList.APIKey != ""
	s.Sonarr.Configured = s.Sonarr.APIKey != "" && s.Sonarr.URL != ""
	s.Radarr.Configured = s.Radarr.APIKey != "" && s.Radarr.URL != ""
	s.Pushover.Configured = s.Pushover.Token != "" && s.Pushover.User != ""
	s.MDBList.APIKey = ""
	s.Sonarr.APIKey = ""
	s.Radarr.APIKey = ""
	s.Pushover.Token = ""
	s.Pushover.User = ""
	return s
}
func (a *App) settings(ctx context.Context) (Settings, error) {
	s := defaultSettings()
	var b []byte
	e := a.db.QueryRow(ctx, `SELECT encrypted FROM settings WHERE id=1`).Scan(&b)
	if e == pgx.ErrNoRows {
		s.MDBList.APIKey = a.cfg.MDBListAPIKey
		s.PublicURL = a.cfg.PublicURL
		return s, nil
	}
	if e != nil {
		return s, e
	}
	b, e = unseal(a.cipher, b)
	if e != nil {
		return s, errors.New("cannot decrypt settings: check APP_SECRET")
	}
	e = json.Unmarshal(b, &s)
	s.MDBList.Lists = configuredLists(s.MDBList)
	return s, e
}
func (a *App) saveSettings(ctx context.Context, s Settings) error {
	s.MDBList.Lists = configuredLists(s.MDBList)
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	b, e = seal(a.cipher, b)
	if e != nil {
		return e
	}
	tx, e := a.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = syncListPolicies(ctx, tx, s); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO settings(id,encrypted) VALUES(1,$1) ON CONFLICT(id) DO UPDATE SET encrypted=excluded.encrypted,updated_at=now()`, b); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func validPublicURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Hostname() == "" || !oneOf(u.Scheme, "http", "https") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || len(raw) > 400 {
		return errors.New("public_url must be an HTTP(S) origin without userinfo, path, query or fragment (max 400 bytes)")
	}
	return nil
}
func validSettings(s Settings) error {
	if e := validPublicURL(s.PublicURL); e != nil {
		return e
	}
	if s.Automation.NotifyFirstSync {
		return errors.New("notify_first_sync is unsupported: the initial baseline is always silent")
	}
	if s.MDBList.SyncIntervalMinutes < 5 || s.MDBList.SyncIntervalMinutes > 10080 {
		return errors.New("sync_interval_minutes must be 5..10080")
	}
	for _, id := range strings.FieldsFunc(s.MDBList.ListIDs, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if !numericID.MatchString(id) {
			return errors.New("list_ids must contain literal positive numeric IDs")
		}
	}
	seen := map[string]bool{}
	for _, list := range configuredLists(s.MDBList) {
		if !numericID.MatchString(list.ID) || seen[list.ID] || len(list.Name) > 300 {
			return errors.New("lists require unique literal positive numeric IDs and names up to 300 bytes")
		}
		seen[list.ID] = true
	}
	for _, x := range []string{s.Sonarr.URL, s.Radarr.URL, s.Silo.URL, s.PublicURL} {
		if x == "" {
			continue
		}
		u, e := url.Parse(x)
		if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("connection URLs must be HTTP(S) without userinfo, query or fragment")
		}
	}
	if s.Sonarr.QualityProfileID < 0 || s.Radarr.QualityProfileID < 0 {
		return errors.New("quality profile IDs must be positive")
	}
	return nil
}
