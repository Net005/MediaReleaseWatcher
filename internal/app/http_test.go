package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPAuthenticationAndOrigin(t *testing.T) {
	a := testApp(t)
	h := a.Handler()
	request := func(method, path, body string, c *http.Cookie, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if c != nil {
			r.AddCookie(c)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/releases", "", nil, ""); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("POST", "/api/setup", `{"password":"test-password-123"}`, nil, "https://evil.test"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := request("POST", "/api/setup", `{"password":"test-password-123"}`, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	c := cookies[0]
	if w := request("POST", "/api/setup", `{"password":"another-password"}`, nil, ""); w.Code != 409 {
		t.Fatal(w.Code)
	}
	if w := request("GET", "/api/releases", "", c, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/api/dashboard", "", c, "")
	var d map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &d); e != nil || d["featured"] == nil || d["activity"] == nil {
		t.Fatal(w.Body.String())
	}
	if w := request("POST", "/api/logout", `{}`, c, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := request("GET", "/api/releases", "", c, ""); w.Code != 401 {
		t.Fatal("session survives logout")
	}
	var n int
	if e := a.db.QueryRow(context.Background(), `SELECT count(*) FROM sessions`).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}
func TestSettingsBlankSecretPreserved(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	s := defaultSettings()
	s.MDBList.APIKey = "test-credential"
	a.saveSettings(ctx, s)
	h := a.Handler()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "http://localhost/api/setup", strings.NewReader(`{"password":"test-password-123"}`))
	h.ServeHTTP(w, r)
	c := w.Result().Cookies()[0]
	r = httptest.NewRequest("PUT", "http://localhost/api/settings", strings.NewReader(`{"mdblist":{"api_key":"","list_ids":"","sync_interval_minutes":60}}`))
	r.AddCookie(c)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got, e := a.settings(ctx)
	if e != nil || got.MDBList.APIKey != "test-credential" {
		t.Fatal(got, e)
	}
}
