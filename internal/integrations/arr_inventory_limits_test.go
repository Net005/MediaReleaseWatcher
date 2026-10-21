package integrations

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestArrInventoryLimitRouteScope(t *testing.T) {
	payload := `"` + strings.Repeat("x", maxResponseBytes) + `"`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, payload) }))
	defer srv.Close()
	c := NewClient(Config{})
	for _, tc := range []struct {
		kind, method, path string
		q                  url.Values
		body               any
	}{
		{"radarr", "GET", "movie/1", nil, nil}, {"radarr", "GET", "movie/lookup", nil, nil},
		{"sonarr", "GET", "series/1", nil, nil}, {"sonarr", "GET", "series/lookup", nil, nil},
		{"radarr", "GET", "rootfolder", nil, nil}, {"sonarr", "GET", "qualityprofile", nil, nil},
		{"radarr", "POST", "movie", nil, map[string]int{"id": 1}}, {"sonarr", "POST", "series", nil, map[string]int{"id": 1}},
		{"radarr", "PUT", "movie", nil, nil}, {"sonarr", "DELETE", "series", nil, nil},
		{"radarr", "GET", "movie", url.Values{"tmdbId": {"1"}}, nil},
		{"sonarr", "GET", "series", nil, map[string]int{"id": 1}},
		{"other", "GET", "movie", nil, nil}, {"radarr", "GET", "series", nil, nil},
	} {
		t.Run(tc.kind+tc.method+tc.path+tc.q.Encode(), func(t *testing.T) {
			var out any
			err := c.arrRequest(context.Background(), arrService{kind: tc.kind, base: srv.URL, key: "secret"}, tc.method, tc.path, tc.q, tc.body, &out)
			if err == nil || !strings.Contains(err.Error(), "response exceeds size limit") || out != nil {
				t.Fatalf("scope escaped: %v %v", out, err)
			}
		})
	}
	for _, method := range []string{"GET", "POST"} {
		raw, err := c.request(context.Background(), "unrelated", method, srv.URL, "secret", "", nil)
		if raw != nil || err == nil || !strings.Contains(err.Error(), "exceeds size limit") {
			t.Fatalf("default %s: %v", method, err)
		}
	}
}

type inventoryRoundTrip func(*http.Request) (*http.Response, error)

func (f inventoryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type inventoryReader struct {
	remaining, read int
	closed          bool
}

func (r *inventoryReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > r.remaining {
		n = r.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = '!'
	}
	r.remaining -= n
	r.read += n
	return n, nil
}
func (r *inventoryReader) Close() error { r.closed = true; return nil }

func TestArrInventoryOversizeBoundBeforeDecode(t *testing.T) {
	for _, kind := range []string{"radarr", "sonarr"} {
		t.Run(kind, func(t *testing.T) {
			reader := &inventoryReader{remaining: maxArrInventoryBytes + 1024}
			c := NewClient(Config{RadarrURL: "http://example.invalid", RadarrAPIKey: "secret", SonarrURL: "http://example.invalid", SonarrAPIKey: "secret"})
			c.http.Transport = inventoryRoundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: reader, Header: make(http.Header)}, nil
			})
			inv, err := c.ArrInventory(context.Background(), kind)
			if inv != nil || err == nil || err.Error() != kind+" response exceeds size limit" {
				t.Fatalf("expected size before invalid JSON: %v %v", inv, err)
			}
			if reader.read != maxArrInventoryBytes+1 || !reader.closed {
				t.Fatalf("unbounded/unclosed: %+v", reader)
			}
		})
	}
}

func TestArrLargeInventoryFailClosed(t *testing.T) {
	for _, mode := range []string{"malformed", "truncated", "timeout", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "timeout":
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				case "redirect":
					http.Redirect(w, r, "http://secret.invalid/key", 302)
				case "truncated":
					w.Header().Set("Content-Length", fmt.Sprint(16<<20))
					fmt.Fprint(w, `[{"id":1,"overview":"`+strings.Repeat("x", (8<<20)+1))
				default:
					fmt.Fprint(w, `[{"id":1,"overview":"`+strings.Repeat("x", (8<<20)+1))
				}
			}))
			defer srv.Close()
			for _, kind := range []string{"radarr", "sonarr"} {
				c := NewClient(Config{RadarrURL: srv.URL, RadarrAPIKey: "secret", SonarrURL: srv.URL, SonarrAPIKey: "secret"})
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				if mode != "timeout" {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
				}
				inv, err := c.ArrInventory(ctx, kind)
				cancel()
				if err == nil || inv != nil {
					t.Fatalf("accepted %s: %v", mode, inv)
				}
				if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), srv.URL) {
					t.Fatalf("unsafe error: %v", err)
				}
			}
		})
	}
}

func TestRequestHeadersLimitContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret" || r.Header.Get("X-Test") != "kept" {
			t.Error("lost headers")
		}
		fmt.Fprint(w, `[123]`)
	}))
	defer srv.Close()
	c := NewClient(Config{})
	for _, limit := range []int{4, 5} {
		raw, err := c.requestHeadersLimit(context.Background(), "test", "GET", srv.URL, "secret", "", nil, http.Header{"X-Test": {"kept"}}, limit)
		if limit == 4 && (err == nil || raw != nil) {
			t.Fatal("accepted limit+1")
		}
		if limit == 5 && (err != nil || string(raw) != `[123]`) {
			t.Fatalf("rejected exact bound: %s %v", raw, err)
		}
	}
}
