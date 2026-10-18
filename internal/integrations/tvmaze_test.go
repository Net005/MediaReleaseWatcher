package integrations

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func tvmazeClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c := NewClient(Config{MDBListAPIKey: "must-not-send-mdblist-key", SonarrAPIKey: "must-not-send-arr-key"})
	c.tvmazeBaseURL = s.URL
	c.tvmazeGate = newRequestGate()
	return c
}
func TestTVMazeCreditsExactNameDOBAndShowAttribution(t *testing.T) {
	var timesMu sync.Mutex
	var starts []time.Time
	c := tvmazeClient(t, func(w http.ResponseWriter, r *http.Request) {
		timesMu.Lock()
		starts = append(starts, time.Now())
		timesMu.Unlock()
		if r.Method != "GET" || r.Header.Get("X-Api-Key") != "" || r.URL.Query().Get("apikey") != "" || r.Header.Get("Authorization") != "" {
			t.Error("TVMaze must be unauthenticated GET only")
		}
		switch r.URL.Path {
		case "/search/people":
			if r.URL.Query().Get("q") != "Synthetic Actor" {
				t.Error("search name changed")
			}
			w.Write([]byte(`[{"score":99,"person":{"id":12,"name":"Synthetico Actor","birthday":"1990-01-02"}},{"person":{"id":11,"name":"Synthetic Actor","birthday":"1980-01-02"}},{"person":{"id":10,"name":"Synthetic Actor","birthday":"1990-01-02"}}]`))
		case "/people/10/castcredits":
			if r.URL.Query().Get("embed") != "show" {
				t.Error("show embed required")
			}
			w.Write([]byte(`[{"_embedded":{"show":{"id":42,"name":"Synthetic Show","url":"https://www.tvmaze.com/shows/42/synthetic-show","premiered":"2021-05-06","externals":{"thetvdb":987,"imdb":"tt1234567"}}}},{"_embedded":{"show":{"id":42,"name":"Synthetic Show","url":"https://www.tvmaze.com/shows/42/synthetic-show","externals":{"thetvdb":987,"imdb":"tt1234567"}}}}]`))
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	got, err := c.TVMazeCredits(context.Background(), "Synthetic Actor", "1990-01-02")
	want := []Credit{{Title: "Synthetic Show", IMDbID: "tt1234567", TVDbID: 987, Year: 2021, URL: "https://www.tvmaze.com/shows/42/synthetic-show"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("credits: %+v %v", got, err)
	}
	timesMu.Lock()
	defer timesMu.Unlock()
	if len(starts) != 2 || starts[1].Sub(starts[0]) < 950*time.Millisecond {
		t.Fatalf("TVMaze requests not limited to one per second: %v", starts)
	}
}

func TestTVMazeCreditsRejectsFuzzyAmbiguousOrWrongDOB(t *testing.T) {
	for _, body := range []string{`[{"person":{"id":1,"name":"Synthetic Actors","birthday":"1990-01-02"}}]`, `[{"person":{"id":1,"name":"Synthetic Actor","birthday":"1990-01-02"}},{"person":{"id":2,"name":"Synthetic Actor","birthday":"1990-01-02"}}]`, `[{"person":{"id":1,"name":"Synthetic Actor","birthday":"1991-01-02"}}]`, `[{"person":{"id":1,"name":"Synthetic Actor","birthday":null}}]`} {
		t.Run(body, func(t *testing.T) {
			var calls atomic.Int32
			c := tvmazeClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/search/people" {
					t.Error("must not fetch credits for unmatched identity")
				}
				w.Write([]byte(body))
			})
			got, err := c.TVMazeCredits(context.Background(), "Synthetic Actor", "1990-01-02")
			if err == nil || len(got) != 0 || calls.Load() != 1 {
				t.Fatalf("accepted unsafe identity: %+v %v requests=%d", got, err, calls.Load())
			}
		})
	}
}

func TestTVMazeHyphenAlternateStillRequiresExactIdentity(t *testing.T) {
	c := tvmazeClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/people":
			switch r.URL.Query().Get("q") {
			case "Synthetic-Name Actor":
				w.Write([]byte(`[{"person":{"id":8,"name":"Different Person"}}]`))
			case "Synthetic Name Actor":
				w.Write([]byte(`[{"person":{"id":9,"name":"Synthetic Name Actor","birthday":"1990-01-02"}}]`))
			default:
				t.Error("unexpected name")
			}
		case "/people/9/castcredits":
			w.Write([]byte(`[]`))
		default:
			t.Error("unexpected route")
		}
	})
	got, err := c.TVMazeCredits(context.Background(), "Synthetic-Name Actor", "1990-01-02")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("hyphen-normalized identity: %+v %v", got, err)
	}
}

func TestTVMazeCreditsMissingDOBAllowsOnlyUniqueExactName(t *testing.T) {
	c := tvmazeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/people" {
			w.Write([]byte(`[{"person":{"id":9,"name":"SYNTHETIC Actor","birthday":null}}]`))
		} else {
			w.Write([]byte(`[]`))
		}
	})
	if _, err := c.TVMazeCredits(context.Background(), "Synthetic Actor", ""); err != nil {
		t.Fatal(err)
	}
}

func TestTVMazeRateLimitAndCancellation(t *testing.T) {
	var calls atomic.Int32
	c := tvmazeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
	})
	for i := 0; i < 2; i++ {
		_, err := c.TVMazeCredits(context.Background(), "Synthetic Actor", "")
		var rate *HTTPError
		if !errors.As(err, &rate) || rate.StatusCode != 429 || rate.RetryAfter <= 0 {
			t.Fatalf("rate: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("429 caused retries: %d", calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.TVMazeCredits(ctx, "Synthetic Actor", ""); err == nil {
		t.Error("accepted cancellation")
	}
}

func TestTVMazeInputValidationAndUntrustedCredits(t *testing.T) {
	c := tvmazeClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("must validate before request") })
	for _, v := range [][2]string{{"", ""}, {"---", ""}, {strings.Repeat("x", 251), ""}, {"Synthetic", "1990-99-99"}} {
		if _, err := c.TVMazeCredits(context.Background(), v[0], v[1]); err == nil {
			t.Error("accepted invalid person input")
		}
	}
	for _, show := range []string{`{"id":42,"name":"Synthetic","url":"https://evil.example/shows/42","externals":{"imdb":"tt1234567"}}`, `{"id":42,"name":"Synthetic","url":"https://www.tvmaze.com/shows/42/synthetic","externals":{"imdb":"tt1234567/evil"}}`} {
		t.Run(show, func(t *testing.T) {
			c := tvmazeClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/search/people" {
					w.Write([]byte(`[{"person":{"id":9,"name":"Synthetic Actor"}}]`))
				} else {
					w.Write([]byte(`[{"_embedded":{"show":` + show + `}}]`))
				}
			})
			if got, err := c.TVMazeCredits(context.Background(), "Synthetic Actor", ""); err == nil || len(got) != 0 {
				t.Fatalf("accepted untrusted credit: %+v %v", got, err)
			}
		})
	}
}
