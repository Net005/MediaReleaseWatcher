package integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArrReadbackRequiresMonitoringPresenceAndConsistentRoot(t *testing.T) {
	for _, issue := range []string{"absent-monitoring", "root-path-conflict", "path-traversal"} {
		t.Run(issue, func(t *testing.T) {
			f := newArrFixture(t, "radarr")
			switch issue {
			case "absent-monitoring":
				f.options.Monitored = false
				delete(f.verified, "monitored")
			case "root-path-conflict":
				f.verified["rootFolderPath"] = "/library"
				f.verified["path"] = "/other/Synthetic"
			case "path-traversal":
				f.verified["path"] = "/library/../other/Synthetic"
			}
			if got, err := f.client().Add(context.Background(), "radarr", f.media, f.options); err == nil || got.ID != 0 {
				t.Fatalf("unverified option state accepted: %+v %v", got, err)
			}
		})
	}
}

func TestArrIMDbFallbackAndUnmonitoredExplicitFalse(t *testing.T) {
	for _, kind := range []string{"sonarr", "radarr"} {
		t.Run(kind, func(t *testing.T) {
			f := newArrFixture(t, kind)
			f.media.TMDbID = 0
			f.media.TVDbID = 0
			f.term = "imdb:tt1234567"
			f.options.Monitored = false
			f.options.MinimumAvailability = ""
			f.verified["monitored"] = false
			f.verified["seasons"] = []any{map[string]any{"seasonNumber": float64(1), "monitored": false}}
			got, err := f.client().Add(context.Background(), kind, f.media, f.options)
			if err != nil || got.ID != 71 || f.posted["monitored"] != false {
				t.Fatalf("IMDb fallback: %+v %v", got, err)
			}
			if f.posted["addOptions"].(map[string]any)["monitor"] != "none" {
				t.Error("monitoring not explicitly disabled")
			}
		})
	}
}

func TestArrExistingReadbackMismatchNeverClaimsSuccess(t *testing.T) {
	f := newArrFixture(t, "radarr")
	f.existing = []map[string]any{{"id": float64(71), "tmdbId": float64(42), "imdbId": "tt1234567"}}
	f.verified["tmdbId"] = float64(999)
	if got, err := f.client().Add(context.Background(), "radarr", f.media, f.options); err == nil || got.AlreadyExists || f.writes.Load() != 0 {
		t.Fatalf("bad existing readback: %+v %v", got, err)
	}
}

func TestArrAndPushoverHTTPErrorsSanitized(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte("secret-arr-key secret-push-token secret-push-user " + r.URL.String()))
	}))
	defer s.Close()
	c := NewClient(Config{RadarrURL: s.URL, RadarrAPIKey: "secret-arr-key", PushoverURL: s.URL, PushoverToken: "secret-push-token", PushoverUser: "secret-push-user"})
	_, arrErr := c.ArrOptions(context.Background(), "radarr")
	pushErr := c.Notify(context.Background(), Notification{Message: "Synthetic"})
	for _, err := range []error{arrErr, pushErr} {
		if err == nil || strings.Contains(err.Error(), "secret-") || strings.Contains(err.Error(), s.URL) {
			t.Fatalf("credential leakage: %v", err)
		}
	}
}
