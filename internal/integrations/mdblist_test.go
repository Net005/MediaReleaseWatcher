package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMDBGetUsesConfiguredKeyAndPreservesCallerQuery(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/people/42" {
			t.Errorf("unexpected route: %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("apikey") != "synthetic-mdblist-secret" {
			t.Error("missing configured query API key")
		}
		if r.URL.Query().Get("lang") != "en" {
			t.Error("query missing")
		}
		w.Write([]byte(`{"id":42,"name":"Synthetic Person"}`))
	}))
	defer s.Close()
	c := NewClient(Config{MDBListAPIKey: "synthetic-mdblist-secret", MDBListBaseURL: s.URL + "/api"})
	q := url.Values{"lang": {"en"}, "apikey": {"caller-cannot-override"}}
	got, err := c.MDBGet(context.Background(), "/people/42", q)
	if err != nil || !json.Valid(got) {
		t.Fatalf("get: %s %v", got, err)
	}
	if q.Get("apikey") != "caller-cannot-override" {
		t.Error("mutated caller values")
	}
}

func TestMDBGetRejectsUnsafeOrUnsupportedPaths(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte(`{}`)) }))
	defer s.Close()
	c := NewClient(Config{MDBListAPIKey: "secret", MDBListBaseURL: s.URL})
	for _, p := range []string{"https://other.invalid/user", "//other.invalid/user", "/user?apikey=bad", "/people/../user", "/people/%34%32", "/people/0042", "/people/42/favorite", "/people/search", "/lists/2/items/extra", "/tmdb/movie/1/extra", "/tmdb/movie/0", "/imdb/movie/tt123bad", "/user#fragment"} {
		if _, err := c.MDBGet(context.Background(), p, nil); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("unsafe paths made %d requests", calls.Load())
	}
}

func TestMDBBatchReadOnlyNumericContract(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/tmdb/movie" || r.URL.Query().Get("apikey") != "batch-secret" {
			t.Error("incorrect batch request")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("content type")
		}
		var body struct {
			IDs    []int    `json:"ids"`
			Append []string `json:"append_to_response"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.IDs) != 2 || body.IDs[0] != 42 || body.IDs[1] != 43 || len(body.Append) != 1 || body.Append[0] != "keyword" {
			t.Errorf("body: %+v", body)
		}
		w.Write([]byte(`[{"id":43,"ids":{"tmdb":43},"type":"movie"},{"id":42,"ids":{"tmdb":42},"type":"movie"}]`))
	}))
	defer s.Close()
	c := NewClient(Config{MDBListAPIKey: "batch-secret", MDBListBaseURL: s.URL})
	got, err := c.MDBBatch(context.Background(), "tmdb", "movie", []string{"42", "43"})
	if err != nil || len(got) != 2 {
		t.Fatalf("batch: %v %v", got, err)
	}
	for _, tt := range []struct {
		provider, kind string
		ids            []string
	}{{"tmdb", "movie", nil}, {"tmdb", "movie", []string{"042"}}, {"tmdb", "movie", []string{"0"}}, {"imdb", "movie", []string{"tt1234567"}}, {"tmdb", "../user", []string{"42"}}} {
		if _, err := c.MDBBatch(context.Background(), tt.provider, tt.kind, tt.ids); err == nil {
			t.Errorf("accepted invalid batch %+v", tt)
		}
	}
}

func TestMDBHTTPFailuresDoNotLeakSecrets(t *testing.T) {
	for _, code := range []int{401, 403, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				w.Write([]byte("synthetic-secret " + r.URL.String()))
			}))
			defer s.Close()
			c := NewClient(Config{MDBListAPIKey: "synthetic-secret", MDBListBaseURL: s.URL})
			_, err := c.MDBGet(context.Background(), "/user", nil)
			if err == nil || strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), s.URL) {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}

func TestRateLimitHonorsRetryAfterWithoutRetryLoop(t *testing.T) {
	for _, header := range []string{"120", time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat), "invalid"} {
		t.Run(header, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", header)
				w.WriteHeader(429)
			}))
			defer s.Close()
			c := NewClient(Config{MDBListAPIKey: "secret", MDBListBaseURL: s.URL})
			for i := 0; i < 2; i++ {
				_, err := c.MDBGet(context.Background(), "/user", nil)
				var rate *HTTPError
				if !errors.As(err, &rate) || rate.StatusCode != 429 || rate.RetryAfter <= 0 {
					t.Fatalf("rate error: %v", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("cooldown failed: %d requests", calls.Load())
			}
		})
	}
}

func TestMDBMissingConfig(t *testing.T) {
	if _, err := NewClient(Config{}).MDBGet(context.Background(), "/user", nil); err == nil {
		t.Fatal("missing key accepted")
	}
}
