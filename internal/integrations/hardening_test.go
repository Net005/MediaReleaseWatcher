package integrations

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMDBBatchValidatesIdentityNotResponsePosition(t *testing.T) {
	for _, body := range []string{`[{"id":99,"ids":{"tmdb":99},"type":"movie"}]`, `[{"id":42,"ids":{"tmdb":42},"type":"show"}]`, `[{"id":42,"type":"movie"}]`, `[{"ids":{"tmdb":42},"type":"movie"},{"ids":{"tmdb":42},"type":"movie"}]`} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer s.Close()
			c := NewClient(Config{MDBListAPIKey: "secret", MDBListBaseURL: s.URL})
			if _, err := c.MDBBatch(context.Background(), "tmdb", "movie", []string{"42"}); err == nil {
				t.Fatal("accepted unexpected batch identity/type")
			}
		})
	}
}

func TestArrConflictingExistingIdentityNeverPosts(t *testing.T) {
	f := newArrFixture(t, "radarr")
	item := map[string]any{"id": float64(71), "tmdbId": float64(42), "imdbId": "tt9999999"}
	f.existing = []map[string]any{item}
	if _, err := f.client().Add(context.Background(), "radarr", f.media, f.options); err == nil || f.writes.Load() != 0 {
		t.Fatalf("conflicting existing identity not rejected: %v writes=%d", err, f.writes.Load())
	}
}

func TestRequestRejectsCredentialLeakingRedirects(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1); w.Write([]byte(`{}`)) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/?apikey=synthetic-secret", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c := NewClient(Config{MDBListAPIKey: "synthetic-secret", MDBListBaseURL: source.URL})
	_, err := c.MDBGet(context.Background(), "/user", nil)
	if err == nil || strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), destination.URL) || destinationCalls.Load() != 0 {
		t.Fatalf("redirect unsafe: %v destination requests=%d", err, destinationCalls.Load())
	}
}

func TestResponseSizeAndJSONAreBounded(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", (8<<20)+1), `{"broken":`, "null"} {
		t.Run("bounded", func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
			defer s.Close()
			c := NewClient(Config{MDBListAPIKey: "secret", MDBListBaseURL: s.URL})
			if body == "null" {
				if _, err := c.MDBBatch(context.Background(), "tmdb", "movie", []string{"42"}); err == nil {
					t.Error("accepted null batch")
				}
			} else if _, err := c.MDBGet(context.Background(), "/user", nil); err == nil {
				t.Error("accepted unsafe response")
			}
		})
	}
}

func TestCanceledAndTransportFailuresAreSanitized(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	c := NewClient(Config{MDBListAPIKey: "secret-in-query", MDBListBaseURL: s.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.MDBGet(ctx, "/user", nil); err == nil || strings.Contains(err.Error(), "secret-in-query") || strings.Contains(err.Error(), s.URL) {
		t.Fatalf("timeout: %v", err)
	}
	s.Close()
	if _, err := c.MDBGet(context.Background(), "/user", nil); err == nil || strings.Contains(err.Error(), "secret-in-query") || strings.Contains(err.Error(), s.URL) {
		t.Fatalf("transport error: %v", err)
	}
	if c.http.Timeout <= 0 {
		t.Fatal("no request timeout")
	}
}

func TestEndpointConfigurationRejectsEmbeddedSecretsAndTraversal(t *testing.T) {
	for _, base := range []string{"http://user:password@localhost", "http://localhost?apikey=secret", "http://localhost#fragment", "file:///private", "http://localhost/a/../private", "http://localhost/%2e%2e"} {
		c := NewClient(Config{MDBListAPIKey: "secret", MDBListBaseURL: base})
		if _, err := c.MDBGet(context.Background(), "/user", nil); err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "password") {
			t.Fatalf("config unsafe: %v", err)
		}
	}
}

func TestRetryAfterParsingAndCooldownExpires(t *testing.T) {
	if retryAfter("0") < time.Second || retryAfter("-1") < time.Second || retryAfter("9999999999999999999999") < time.Second || retryAfter(time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)) < time.Second {
		t.Error("unsafe retry default")
	}
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte(`{}`)) }))
	defer s.Close()
	c := NewClient(Config{MDBListAPIKey: "secret", MDBListBaseURL: s.URL})
	c.cooldown["MDBList"] = time.Now().Add(-time.Second)
	if _, err := c.MDBGet(context.Background(), "/user", nil); err != nil || calls.Load() != 1 {
		t.Fatalf("cooldown didn't expire: %v", err)
	}
}

func TestHTTPErrorDoesNotIncludeBodyOrURL(t *testing.T) {
	err := (&HTTPError{Service: "synthetic", StatusCode: 401}).Error()
	if err != "synthetic returned HTTP 401" {
		t.Fatal(err)
	}
	e := &HTTPError{Service: "synthetic", StatusCode: 429, RetryAfter: time.Minute}
	var target *HTTPError
	if !errors.As(e, &target) {
		t.Fatal("not typed")
	}
}

func TestRequestSizeAndBadMethodAreSanitized(t *testing.T) {
	c := NewClient(Config{})
	if _, err := c.request(context.Background(), "Synthetic", http.MethodPost, "http://unused.invalid", "", "", bytes.Repeat([]byte("x"), (6<<20)+1)); err == nil {
		t.Error("unbounded request")
	}
	if _, err := c.request(context.Background(), "Synthetic", "bad\nmethod", "http://unused.invalid/?secret=not-in-error", "", "", nil); err == nil || strings.Contains(err.Error(), "not-in-error") {
		t.Fatalf("bad request leak: %v", err)
	}
	if _, err := c.MDBGet(context.Background(), "/user", url.Values{"query": {strings.Repeat("x", 17<<10)}}); err == nil {
		t.Error("accepted oversized query")
	}
}
