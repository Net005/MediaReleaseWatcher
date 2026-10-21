package integrations

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSiloRateLimitedPagedCatalogPublishesOneCompleteSnapshot(t *testing.T) {
	calls := map[string]int{}
	var rejected time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected mutation %s", r.Method)
		}
		key := r.URL.RequestURI()
		calls[key]++
		switch r.URL.Path {
		case "/api/v2/profiles":
			fmt.Fprint(w, `{"items":[{"id":"primary","is_primary":true}]}`)
		case "/api/v2/catalog":
			if r.URL.Query().Get("cursor") == "next" {
				if calls[key] == 1 {
					rejected = time.Now()
					w.Header().Set("Retry-After", "1")
					http.Error(w, "private-key-body", 429)
					return
				}
				if time.Since(rejected) < time.Second {
					t.Error("retried before Retry-After")
				}
				fmt.Fprint(w, `{"items":[{"content_id":"second","type":"movie"}],"page":{"has_more":false}}`)
			} else {
				fmt.Fprint(w, `{"items":[{"content_id":"first","type":"movie"}],"page":{"has_more":true,"next_cursor":"next"}}`)
			}
		case "/api/v2/catalog/items/first":
			fmt.Fprint(w, `{"content_id":"first","type":"movie","tmdb_id":"41"}`)
		case "/api/v2/catalog/items/second":
			fmt.Fprint(w, `{"content_id":"second","type":"movie","tmdb_id":"42"}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := NewClient(Config{SiloURL: srv.URL, SiloAPIKey: "synthetic-key"})
	got, err := c.SiloAvailability(context.Background(), Media{Type: "movie", TMDbID: 42})
	if err != nil || got.Status != "available" {
		t.Fatalf("rate-limited scan failed: %+v %v", got, err)
	}
	snapshot := c.siloCache
	if snapshot == nil || len(snapshot.items) != 2 || snapshot.at.IsZero() {
		t.Fatal("incomplete snapshot published")
	}
	for _, id := range []int{41, 42, 43} {
		got, err = c.SiloAvailability(context.Background(), Media{Type: "movie", TMDbID: id})
		if err != nil || (id == 43 && got.Status != "absent") || (id != 43 && got.Status != "available") {
			t.Fatalf("coverage: %+v %v", got, err)
		}
	}
	total := 0
	for _, n := range calls {
		total += n
	}
	if total != 6 || c.siloCache != snapshot {
		t.Fatalf("snapshot not reused: requests=%d", total)
	}
}

func TestSiloCooldownDoesNotStarveIndependentEndpoint(t *testing.T) {
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer limited.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{}`)
	}))
	defer healthy.Close()
	c := NewClient(Config{SiloURL: limited.URL, SiloAPIKey: "synthetic-key"})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var out any
	if err := c.siloGet(ctx, "/api/v2/catalog", nil, "", &out); err == nil {
		t.Fatal("expected limited endpoint to time out")
	}
	other := NewClient(Config{SiloURL: healthy.URL, SiloAPIKey: "another-key"})
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := other.siloGet(ctx, "/api/v2/catalog", nil, "", &out); err != nil {
		t.Fatalf("independent endpoint inherited cooldown: %v", err)
	}
}

func TestSiloCooldownSharedAcrossNormalizedEndpointClients(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	for i, base := range []string{srv.URL, srv.URL + "/"} {
		c := NewClient(Config{SiloURL: base, SiloAPIKey: fmt.Sprintf("synthetic-key-%d", i)})
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		var out any
		err := c.siloGet(ctx, "/api/v2/catalog", nil, "", &out)
		cancel()
		if err == nil || calls.Load() != 1 {
			t.Fatalf("client %d bypassed shared cooldown: requests=%d err=%v", i, calls.Load(), err)
		}
	}
}

func TestSiloGETPacingSharedAcrossClients(t *testing.T) {
	var previous time.Time
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		if !previous.IsZero() && now.Sub(previous) < 90*time.Millisecond {
			t.Errorf("Silo GET burst: gap %s", now.Sub(previous))
		}
		previous = now
		calls++
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	for range 3 {
		c := NewClient(Config{SiloURL: srv.URL, SiloAPIKey: "synthetic-key"})
		var out any
		if err := c.siloGet(context.Background(), "/api/v2/catalog", nil, "", &out); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("requests=%d", calls)
	}
}

func TestSiloPersistent429RetainsLastSnapshot(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet {
			t.Errorf("unexpected mutation %s", r.Method)
		}
		switch r.URL.Path {
		case "/api/v2/profiles":
			fmt.Fprint(w, `{"items":[{"id":"primary","is_primary":true}]}`)
		case "/api/v2/catalog":
			fmt.Fprint(w, `{"items":[{"content_id":"first","type":"movie"},{"content_id":"second","type":"movie"}],"page":{"has_more":false}}`)
		case "/api/v2/catalog/items/first":
			fmt.Fprint(w, `{"content_id":"first","type":"movie","tmdb_id":"41"}`)
		case "/api/v2/catalog/items/second":
			w.Header().Set("Retry-After", "1")
			http.Error(w, "private-body synthetic-key", http.StatusTooManyRequests)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := NewClient(Config{SiloURL: srv.URL, SiloAPIKey: "synthetic-key"})
	old := &siloSnapshot{at: time.Now().Add(-siloCacheTTL - time.Second), items: []siloItem{{ContentID: "old", Type: "movie", TMDbID: "42"}}}
	c.siloCache = old
	start := time.Now()
	got, err := c.SiloAvailability(context.Background(), Media{Type: "movie", TMDbID: 42})
	var limited *HTTPError
	if !errors.As(err, &limited) || limited.StatusCode != 429 || calls != 6 || time.Since(start) > 5*time.Second {
		t.Fatalf("unbounded or wrong error: calls=%d result=%+v err=%v", calls, got, err)
	}
	if got.Status != "error" || got.URL != "" || c.siloCache != old || len(old.items) != 1 {
		t.Fatal("failed refresh changed verified snapshot or reused stale evidence")
	}
	for _, secret := range []string{"private-body", "synthetic-key", srv.URL} {
		if strings.Contains(got.Error, secret) {
			t.Fatal("unsafe error")
		}
	}
}

func TestSiloRetryWaitCancellationAndDeadline(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			rejected := make(chan struct{}, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "3600")
				w.WriteHeader(429)
				rejected <- struct{}{}
			}))
			defer srv.Close()
			c := NewClient(Config{SiloURL: srv.URL, SiloAPIKey: "synthetic-key"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 1200*time.Millisecond)
				defer cancel()
			}
			done := make(chan error, 1)
			go func() { var out any; done <- c.siloGet(ctx, "/api/v2/catalog", nil, "", &out) }()
			select {
			case <-rejected:
			case <-time.After(3 * time.Second):
				t.Fatal("no first request")
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || calls.Load() != 1 || strings.Contains(err.Error(), "synthetic-key") {
					t.Fatalf("canceled wait: calls=%d err=%v", calls.Load(), err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("wait ignored caller cancellation/deadline")
			}
		})
	}
}

func TestSiloDetailRetryHonorsHTTPDate(t *testing.T) {
	calls := 0
	var retryAt time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			retryAt = time.Now().Add(2 * time.Second).Truncate(time.Second)
			w.Header().Set("Retry-After", retryAt.UTC().Format(http.TimeFormat))
			w.WriteHeader(429)
			return
		}
		if time.Now().Before(retryAt) {
			t.Error("HTTP-date retry was early")
		}
		fmt.Fprint(w, `{"content_id":"local","type":"movie","tmdb_id":"42"}`)
	}))
	defer srv.Close()
	c := NewClient(Config{SiloURL: srv.URL, SiloAPIKey: "synthetic-key"})
	var detail siloItem
	if err := c.siloGet(context.Background(), "/api/v2/catalog/items/local", nil, "primary", &detail); err != nil || calls != 2 || detail.TMDbID != "42" {
		t.Fatalf("detail retry: %+v calls=%d err=%v", detail, calls, err)
	}
}

func TestSiloRetryBudgetWithoutCallerDeadline(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	c := NewClient(Config{SiloURL: srv.URL, SiloAPIKey: "synthetic-key"})
	start := time.Now()
	var out any
	err := c.siloGet(context.Background(), "/api/v2/catalog", nil, "", &out)
	elapsed := time.Since(start)
	if err == nil || calls != 1 || elapsed < 9*time.Second || elapsed > 12*time.Second {
		t.Fatalf("unbounded retry budget or early retry: calls=%d elapsed=%s err=%v", calls, elapsed, err)
	}
}

func TestSiloSharedGateWaitCanBeCanceled(t *testing.T) {
	// Hold the same gate as an in-flight GET; cancellation must not need its token.
	gate, release := acquireSiloReadGate("http://example.invalid")
	defer release()
	gate.token <- struct{}{}
	defer func() { <-gate.token }()
	c := NewClient(Config{SiloURL: "http://example.invalid", SiloAPIKey: "synthetic-key"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	var out any
	if err := c.siloGet(ctx, "/api/v2/catalog", nil, "", &out); err == nil || time.Since(start) > time.Second {
		t.Fatalf("shared gate ignored cancellation: %v", err)
	}
}

func TestSiloGETDoesNotRetryNonRateLimitFailure(t *testing.T) {
	for _, status := range []int{401, 403, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				http.Error(w, "private-body synthetic-key", status)
			}))
			defer srv.Close()
			c := NewClient(Config{SiloURL: srv.URL, SiloAPIKey: "synthetic-key"})
			var out any
			err := c.siloGet(context.Background(), "/api/v2/catalog", nil, "", &out)
			if err == nil || calls != 1 || strings.Contains(err.Error(), "private-body") || strings.Contains(err.Error(), "synthetic-key") || strings.Contains(err.Error(), srv.URL) {
				t.Fatalf("unsafe or retried non-429: calls=%d err=%v", calls, err)
			}
		})
	}
}
