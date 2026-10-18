package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMDBMetadataGenresAndContent(t *testing.T) {
	r, _, e := normalizeRelease(json.RawMessage(`{"title":"Synthetic","type":"movie","ids":{"tmdb":42},"genres":[{"id":1,"title":"Drama"}],"commonsense_media":{"parental_nudity":2}}`), "movie")
	if e != nil || len(r.Genres) != 1 || r.Genres[0] != "Drama" || r.ContentInfo["commonsense_media"] == nil {
		t.Fatalf("provider metadata lost: %+v %v", r, e)
	}
}

func TestRejectInvalidOrMismatchedMediaIdentity(t *testing.T) {
	for _, raw := range []string{`{"title":"Synthetic","type":"show","ids":{"tmdb":42}}`, `{"title":"Synthetic","type":"movie","ids":{"tmdb":"0042"}}`, `{"title":"Synthetic","type":"movie","ids":{"imdb":"not-an-imdb-id"}}`} {
		if _, _, e := normalizeRelease(json.RawMessage(raw), "movie"); e == nil {
			t.Errorf("accepted wrong identity: %s", raw)
		}
	}
}

func TestPersonEnrichmentCannotRenameSourceIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tmdb":123,"name":"Different Person","biography":"wrong identity"}`))
	}))
	defer server.Close()
	a := &App{cfg: Config{MDBListBaseURL: server.URL}}
	s := defaultSettings()
	s.MDBList.APIKey = "synthetic-key"
	got := a.enrichPerson(context.Background(), s, Actress{TMDbID: "123", Name: "Supplied Person", Biography: "retained", MetadataStatus: "pending"})
	if got.Name != "Supplied Person" || got.Biography != "retained" || !strings.Contains(got.MetadataStatus, "identity mismatch") {
		t.Fatalf("source identity mutated: %+v", got)
	}
}

func TestSparsePersonEnrichmentRetainsBiography(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tmdb":123,"name":"Supplied Person"}`))
	}))
	defer server.Close()
	a := &App{cfg: Config{MDBListBaseURL: server.URL}}
	s := defaultSettings()
	s.MDBList.APIKey = "synthetic-key"
	got := a.enrichPerson(context.Background(), s, Actress{TMDbID: "123", Name: "Supplied Person", Biography: "retained", Profile: "https://image.tmdb.org/profile.jpg"})
	if got.Biography != "retained" || got.Profile == "" {
		t.Fatalf("sparse biography erased rich fields: %+v", got)
	}
}

func TestDiscoverySplitListsAndPersonMedia(t *testing.T) {
	for _, raw := range []string{
		`{"movies":[{"id":42,"title":"Movie"}],"shows":[{"id":42,"title":"Show"}],"pagination":{"next_cursor":"next"}}`,
		`{"items":[{"person":{"tmdb":123,"name":"Supplied Person"},"media":{"type":"movie","ids":{"tmdb":42},"title":"Movie"},"release_date":"2027-01-02"}],"has_more":true}`,
	} {
		rows, next, more, e := parseDiscovery(json.RawMessage(raw))
		if e != nil || len(rows) == 0 || !more {
			t.Fatalf("feed parsing: %v %v", rows, e)
		}
		for i, row := range rows {
			r, people, e := normalizeRelease(row.Raw, row.Kind)
			if e != nil {
				t.Fatal(e)
			}
			if strings.Contains(raw, `"person"`) {
				if len(people) != 1 || people[0] != "123" || r.ReleaseDate != "2027-01-02" {
					t.Fatalf("lost relationship: %+v %v", r, people)
				}
			} else if i == 1 && (r.Type != "show" || next != "next") {
				t.Fatal("split type/cursor lost")
			}
		}
	}
}

func TestPostgresSparseUpdateRetainsRichMetadataAndLinks(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	rich, people, e := normalizeRelease(json.RawMessage(`{"title":"Synthetic","type":"movie","ids":{"tmdb":42,"imdb":"tt1234567","tvdb":84},"description":"Rich biography","genres":["Drama"],"ratings":[{"source":"test","value":8}],"released":"2027-01-02"}`), "movie")
	if e != nil {
		t.Fatal(e)
	}
	rich.SourceLinks = append(rich.SourceLinks, "https://www.tvmaze.com/shows/42/synthetic")
	rich, e = a.saveRelease(ctx, rich, people)
	if e != nil {
		t.Fatal(e)
	}
	sparse, _, _ := normalizeRelease(json.RawMessage(`{"title":"Synthetic updated","type":"movie","ids":{"tmdb":42}}`), "movie")
	got, e := a.saveRelease(ctx, sparse, nil)
	if e != nil {
		t.Fatal(e)
	}
	if got.ID != rich.ID || got.Title != "Synthetic updated" || got.Description != rich.Description || got.IMDbID != rich.IMDbID || got.TVDbID != rich.TVDbID || len(got.Genres) != 1 || len(got.Ratings) != 1 || len(got.SourceLinks) != 2 || got.ReleaseDate != rich.ReleaseDate {
		t.Fatalf("sparse update erased data: %+v", got)
	}
	var raw map[string]any
	if json.Unmarshal(got.Raw, &raw) != nil || raw["description"] != "Rich biography" {
		t.Fatal("original provider data lost")
	}
}

func TestPostgresBaselineIncludesUnmatchedAndUnmonitored(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	r, _, _ := normalizeRelease(json.RawMessage(`{"title":"Synthetic","type":"movie","ids":{"tmdb":42}}`), "movie")
	r, e := a.saveRelease(ctx, r, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.db.Exec(ctx, `UPDATE releases SET monitored=false WHERE id=$1`, r.ID); e != nil {
		t.Fatal(e)
	}
	if e = a.automate(ctx, defaultSettings(), true); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE release_id=$1 AND status='baseline'`, r.ID).Scan(&n); e != nil || n != 2 {
		t.Fatalf("historic titles not baselined: %d %v", n, e)
	}
}
