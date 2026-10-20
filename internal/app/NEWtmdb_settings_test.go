package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTMDBSettingsDefaultsAndSecretRedaction(t *testing.T) {
	s := defaultSettings()
	b, _ := json.Marshal(s.public())
	var public map[string]json.RawMessage
	_ = json.Unmarshal(b, &public)
	var tmdb map[string]any
	_ = json.Unmarshal(public["tmdb"], &tmdb)
	if tmdb["enabled"] != true || tmdb["configured"] != false {
		t.Fatalf("missing enabled, unconfigured TMDb default: %s", b)
	}
	// Decode rather than refer to new types so RED is a behavioral failure.
	if e := json.Unmarshal([]byte(`{"tmdb":{"enabled":true,"api_key":"saved-secret","configured":false}}`), &s); e != nil {
		t.Fatal(e)
	}
	b, _ = json.Marshal(s.public())
	_ = json.Unmarshal(b, &public)
	_ = json.Unmarshal(public["tmdb"], &tmdb)
	if containsBytes(b, "saved-secret") || tmdb["configured"] != true {
		t.Fatalf("unsafe TMDb public settings: %s", b)
	}
}

func TestTMDBSettingsEncryptedRoundTrip(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	s := defaultSettings()
	_ = json.Unmarshal([]byte(`{"tmdb":{"enabled":true,"api_key":"encrypted-tmdb-secret"}}`), &s)
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	var encrypted []byte
	if e := a.db.QueryRow(ctx, `SELECT encrypted FROM settings WHERE id=1`).Scan(&encrypted); e != nil {
		t.Fatal(e)
	}
	if containsBytes(encrypted, "encrypted-tmdb-secret") {
		t.Fatal("plaintext TMDb key")
	}
	got, e := a.settings(ctx)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(got)
	if !containsBytes(b, "encrypted-tmdb-secret") {
		t.Fatal("TMDb key lost on encrypted roundtrip")
	}
}

func tmdbSettingsRequest(a *App, method, path, body string, c *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}
func TestTMDBSettingsPatchPreservesSecret(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	s := defaultSettings()
	s.TMDB.APIKey = "preserve-secret"
	s.MDBList.APIKey = "mdb-preserve"
	s.MDBList.Lists = []MDBListPolicy{{ID: "123", Enabled: true, Monitored: true}}
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	setup := tmdbSettingsRequest(a, "POST", "/api/setup", `{"password":"test-password-123"}`, nil)
	c := setup.Result().Cookies()[0]
	for _, body := range []string{`{"tmdb":{"api_key":"","enabled":false}}`, `{"public_url":"http://localhost"}`} {
		w := tmdbSettingsRequest(a, "PUT", "/api/settings", body, c)
		if w.Code != 200 || strings.Contains(w.Body.String(), "preserve-secret") {
			t.Fatal(w.Code, w.Body.String())
		}
		got, e := a.settings(ctx)
		if e != nil || got.TMDB.APIKey != "preserve-secret" || got.TMDB.Enabled || got.MDBList.APIKey != "mdb-preserve" || len(got.MDBList.Lists) != 1 || !got.MDBList.Lists[0].Monitored {
			t.Fatal("patch lost settings", e)
		}
	}
}
func TestTMDBSettingsEnvironmentFallback(t *testing.T) {
	a := testApp(t)
	a.cfg.TMDBAPIKey = "environment-secret"
	ctx := context.Background()
	s, e := a.settings(ctx)
	if e != nil || s.TMDB.APIKey != "environment-secret" || !s.TMDB.Enabled {
		t.Fatal("missing initial environment fallback", e)
	}
	s = defaultSettings()
	if e = a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	s, e = a.settings(ctx)
	if e != nil || s.TMDB.APIKey != "environment-secret" {
		t.Fatal("missing saved-blank fallback", e)
	}
	s.TMDB.Enabled = false
	s.TMDB.APIKey = ""
	if e = a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	s, e = a.settings(ctx)
	if e != nil || s.TMDB.APIKey != "" || s.TMDB.Enabled || s.public().TMDB.Configured {
		t.Fatal("disabled setting used environment fallback", e)
	}
}

func TestTMDBSettingsTestEndpointUsesSavedSecret(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int64
	var rejected atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if rejected.Load() {
			w.WriteHeader(401)
			w.Write([]byte(`saved-tmdb-key`))
			return
		}
		if r.URL.Path != "/authentication" || r.URL.Query().Get("api_key") != "saved-tmdb-key" {
			t.Error("wrong saved credentials or route")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	}))
	defer upstream.Close()
	a.cfg.TMDBBaseURL = upstream.URL
	if w := tmdbSettingsRequest(a, "POST", "/api/integrations/tmdb/test", `{}`, nil); w.Code != 401 || calls.Load() != 0 {
		t.Fatal("test endpoint bypassed authentication", w.Code)
	}
	setup := tmdbSettingsRequest(a, "POST", "/api/setup", `{"password":"test-password-123"}`, nil)
	c := setup.Result().Cookies()[0]
	if w := tmdbSettingsRequest(a, "POST", "/api/integrations/tmdb/test", `{"api_key":"unsaved-secret"}`, c); w.Code != 502 || calls.Load() != 0 || strings.Contains(w.Body.String(), "unsaved-secret") {
		t.Fatal("unconfigured test must fail safely", w.Code, w.Body.String())
	}
	s := defaultSettings()
	s.TMDB.APIKey = "saved-tmdb-key"
	if e := a.saveSettings(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	if w := tmdbSettingsRequest(a, "POST", "/api/integrations/tmdb/test", `{"api_key":"ignored-secret"}`, c); w.Code != 200 || calls.Load() != 1 || strings.Contains(w.Body.String(), "saved-tmdb-key") {
		t.Fatal("saved test failed", w.Code, w.Body.String())
	}
	rejected.Store(true)
	if w := tmdbSettingsRequest(a, "POST", "/api/integrations/tmdb/test", `{}`, c); w.Code != 502 || strings.Contains(w.Body.String(), "saved-tmdb-key") {
		t.Fatal("failed test leaked credentials", w.Code, w.Body.String())
	}
	var n int
	if e := a.db.QueryRow(context.Background(), `SELECT count(*) FROM activity WHERE message LIKE '%saved-tmdb-key%'`).Scan(&n); e != nil || n != 0 {
		t.Fatal("unsafe activity", e, n)
	}
	s.TMDB.Enabled = false
	if e := a.saveSettings(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	before := calls.Load()
	if w := tmdbSettingsRequest(a, "POST", "/api/integrations/tmdb/test", `{}`, c); w.Code != 502 || calls.Load() != before {
		t.Fatal("disabled test contacted upstream", w.Code)
	}
}
