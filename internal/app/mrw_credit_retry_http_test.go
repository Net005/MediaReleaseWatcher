package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCreditRetryTMDBRespectsRetryAfter(t *testing.T) {
	var calls atomic.Int32
	var first time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("unsafe retry method")
		}
		if calls.Add(1) == 1 {
			first = time.Now()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		fmt.Fprint(w, `{"id":1}`)
	}))
	defer srv.Close()
	a := &App{cfg: Config{TMDBBaseURL: srv.URL}}
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "private-secret"
	_, err := a.tmdbGet(context.Background(), s, "/person/1")
	if err != nil || calls.Load() != 2 {
		t.Fatalf("transient rate limit not retried: calls=%d err=%v", calls.Load(), err)
	}
	if time.Since(first) < time.Second {
		t.Fatal("retried before Retry-After")
	}
}

func TestCreditRetryTMDBLongRetryAfterIsDeferredWithoutHammering(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "86400")
		w.WriteHeader(429)
		fmt.Fprint(w, "private-secret")
	}))
	defer srv.Close()
	a := &App{cfg: Config{TMDBBaseURL: srv.URL}}
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "private-secret"
	start := time.Now()
	_, err := a.tmdbGet(context.Background(), s, "/person/1")
	if err == nil || calls.Load() != 1 || strings.Contains(err.Error(), "private-secret") {
		t.Fatalf("unsafe retry or secret leak: calls=%d err=%v", calls.Load(), err)
	}
	if retry := tmdbRetryAt(err, start); retry.Before(start.Add(23 * time.Hour)) {
		t.Fatalf("Retry-After lost: %s", retry)
	}
}

func TestCreditRetryTMDBTransientGETBounded(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer srv.Close()
	a := &App{cfg: Config{TMDBBaseURL: srv.URL}}
	s := defaultSettings()
	s.TMDB.Enabled = true
	s.TMDB.APIKey = "synthetic"
	if _, err := a.tmdbGet(context.Background(), s, "/person/1"); err == nil || calls.Load() != 3 {
		t.Fatalf("GET retries not bounded: %d, %v", calls.Load(), err)
	}
}
