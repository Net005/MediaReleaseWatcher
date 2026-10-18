package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArrInventoryRejectsIncompleteAndAmbiguousIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		matchError bool
	}{
		{"http-error", "private response", 503, false},
		{"invalid-json", "broken", 200, false},
		{"null", "null", 200, false},
		{"object", `{"records":[]}`, 200, false},
		{"missing-identity", `[{"id":7}]`, 200, false},
		{"invalid-native", `[{"id":7,"tmdbId":-42}]`, 200, false},
		{"invalid-imdb", `[{"id":7,"tmdbId":42,"imdbId":"tt42"}]`, 200, false},
		{"duplicate-id", `[{"id":7,"tmdbId":42},{"id":7,"tmdbId":43}]`, 200, false},
		{"duplicate-provider", `[{"id":7,"tmdbId":42},{"id":8,"tmdbId":42}]`, 200, true},
		{"conflicting-provider", `[{"id":7,"tmdbId":42,"imdbId":"tt0000043"}]`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Errorf("unsafe mutation %s", r.Method)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			c := NewClient(Config{RadarrURL: srv.URL, RadarrAPIKey: "synthetic-key"})
			inv, e := c.ArrInventory(context.Background(), "radarr")
			if tc.matchError {
				if e != nil {
					t.Fatal(e)
				}
				_, e = inv.Match(Media{Type: "movie", TMDbID: 42, IMDbID: "tt0000042"})
			}
			if e == nil || strings.Contains(e.Error(), "private response") {
				t.Fatalf("unsafe evidence: %v", e)
			}
		})
	}
}

func TestArrInventorySonarrMatchesTVDbNotTMDbOrTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":7,"tvdbId":42,"tmdbId":99,"title":"Same title","statistics":{"episodeFileCount":3}}]`)
	}))
	defer srv.Close()
	c := NewClient(Config{SonarrURL: srv.URL, SonarrAPIKey: "synthetic-key"})
	inv, e := c.ArrInventory(context.Background(), "sonarr")
	if e != nil {
		t.Fatal(e)
	}
	got, e := inv.Match(Media{Type: "show", TVDbID: 42, TMDbID: 999})
	if e != nil || got.ID != 7 || got.FileCount != 3 || !got.Ready {
		t.Fatal(got, e)
	}
	got, e = inv.Match(Media{Type: "show", TVDbID: 99, Title: "Same title"})
	if e != nil || got.ID != 0 {
		t.Fatal("wrong provider used", got, e)
	}
}

func TestArrInventoryLinksStayOnConfiguredRoute(t *testing.T) {
	for _, slug := range []string{".", "..", "../../outside", "https://elsewhere.example/item", "a?secret=1", "normal-slug", "caf\u00e9"} {
		inv := &ArrInventory{kind: "radarr", base: "https://manager.example/base", items: []arrItem{{ID: 7, TMDbID: 42, TitleSlug: slug}}}
		got, e := inv.Match(Media{Type: "movie", TMDbID: 42})
		want := "https://manager.example/base/movie/7"
		if slug == "normal-slug" {
			want = "https://manager.example/base/movie/normal-slug"
		}
		if slug == "caf\u00e9" {
			want = "https://manager.example/base/movie/caf%C3%A9"
		}
		if e != nil || got.URL != want {
			t.Fatalf("slug=%q: %+v %v", slug, got, e)
		}
	}
}

func TestArrInventoryExactIdentityReadinessAndOneFetch(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/api/v3/movie" || r.Header.Get("X-Api-Key") != "synthetic-key" {
			t.Errorf("unsafe request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		fmt.Fprint(w, `[{"id":7,"tmdbId":42,"imdbId":"tt0000042","hasFile":true,"movieFile":{"id":12,"size":1024}},{"id":8,"tmdbId":43,"hasFile":false}]`)
	}))
	defer srv.Close()
	c := NewClient(Config{RadarrURL: srv.URL, RadarrAPIKey: "synthetic-key"})
	inventory, e := c.ArrInventory(context.Background(), "radarr")
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		media Media
		id    int
		ready bool
		err   bool
	}{
		{Media{Type: "movie", TMDbID: 42, IMDbID: "tt0000042"}, 7, true, false},
		{Media{Type: "movie", IMDbID: "tt0000042"}, 7, true, false},
		{Media{Type: "movie", TMDbID: 43}, 8, false, false},
		{Media{Type: "movie", TMDbID: 44, Title: "42"}, 0, false, false},
		{Media{Type: "movie", TMDbID: 43, IMDbID: "tt0000042"}, 0, false, true},
		{Media{Type: "show", TVDbID: 42}, 0, false, true},
		{Media{Type: "movie"}, 0, false, true},
	} {
		got, e := inventory.Match(tc.media)
		if (e != nil) != tc.err || got.ID != tc.id || got.Ready != tc.ready {
			t.Fatalf("%+v got %+v %v", tc.media, got, e)
		}
	}
	if calls != 1 {
		t.Fatalf("inventory fetched %d times", calls)
	}
}
