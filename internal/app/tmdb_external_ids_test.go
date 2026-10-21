package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func externalIDsFixture(t *testing.T, body string, during func(*App, Release)) (*App, Settings, Release, *int) {
	t.Helper()
	a := testApp(t)
	r := Release{Type: "show", TMDbID: "1399", Title: "Preserve title", Description: "rich metadata", Monitored: true}
	var err error
	r, err = a.saveRelease(context.Background(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(context.Background(), `UPDATE releases SET monitored=true WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	r, err = a.getRelease(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	calls := new(int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		*calls++
		if q.URL.Path != "/tv/1399/external_ids" {
			t.Errorf("path %s", q.URL.Path)
		}
		if during != nil {
			during(a, r)
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	a.cfg.TMDBBaseURL = server.URL
	return a, Settings{TMDB: TMDBSettings{Enabled: true, APIKey: "synthetic-private-key"}}, r, calls
}
func TestExternalIDsPreservesConcurrentRichFields(t *testing.T) {
	ctx := context.Background()
	a, s, r, _ := externalIDsFixture(t, `{"id":1399,"imdb_id":"tt0944947","tvdb_id":121361}`, func(a *App, r Release) {
		_, e := a.db.Exec(ctx, `UPDATE releases SET data=data||'{"unknown_future_field":{"nested":[1,2,3]},"title":"new title","description":"new overview","genres":["Drama"],"source_links":["https://example.invalid/proof"]}',monitored=false,acquisition='{"sonarr":"existing","custom":"preserve"}' WHERE id=$1`, r.ID)
		if e != nil {
			t.Error(e)
		}
	})
	got, e := a.resolveShowExternalIDs(ctx, s, r)
	if e != nil || got.Title != "new title" || got.Description != "new overview" || got.Monitored || got.Acquisition["custom"] != "preserve" {
		t.Fatal("rich fields lost", got, e)
	}
	var raw []byte
	if e = a.db.QueryRow(ctx, `SELECT data FROM releases WHERE id=$1`, r.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var data map[string]any
	if e = json.Unmarshal(raw, &data); e != nil || data["unknown_future_field"] == nil {
		t.Fatal("unknown JSON field lost", string(raw), e)
	}
	var state string
	if e = a.db.QueryRow(ctx, `SELECT value->>'status' FROM app_state WHERE key LIKE 'tmdb_external_ids:%'`).Scan(&state); e != nil || state != "resolved" {
		t.Fatal("missing success proof", state, e)
	}
}
func TestExternalIDsConflictsAndRaces(t *testing.T) {
	for _, kind := range []string{"duplicate-imdb", "duplicate-tvdb", "deleted", "changed-imdb", "changed-tvdb", "changed-tmdb", "changed-type"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			during := func(a *App, r Release) {
				var e error
				switch kind {
				case "deleted":
					_, e = a.db.Exec(ctx, `DELETE FROM releases WHERE id=$1`, r.ID)
				case "changed-imdb":
					_, e = a.db.Exec(ctx, `UPDATE releases SET imdb_id='tt0000001',data=data||'{"imdb_id":"tt0000001"}' WHERE id=$1`, r.ID)
				case "changed-tvdb":
					_, e = a.db.Exec(ctx, `UPDATE releases SET data=data||'{"tvdb_id":"77"}' WHERE id=$1`, r.ID)
				case "changed-tmdb":
					_, e = a.db.Exec(ctx, `UPDATE releases SET tmdb_id='88',data=data||'{"tmdb_id":"88"}' WHERE id=$1`, r.ID)
				case "changed-type":
					_, e = a.db.Exec(ctx, `UPDATE releases SET media_type='movie',data=data||'{"type":"movie"}' WHERE id=$1`, r.ID)
				case "duplicate-imdb":
					_, e = a.db.Exec(ctx, `INSERT INTO releases(canonical_key,media_type,imdb_id,data) VALUES('conflict','show','tt0944947','{"imdb_id":"tt0944947"}')`)
				case "duplicate-tvdb":
					_, e = a.db.Exec(ctx, `INSERT INTO releases(canonical_key,media_type,data) VALUES('conflict','show','{"tvdb_id":"121361"}')`)
				}
				if e != nil {
					t.Error(e)
				}
			}
			a, s, r, _ := externalIDsFixture(t, `{"id":1399,"imdb_id":"tt0944947","tvdb_id":121361}`, during)
			got, e := a.resolveShowExternalIDs(ctx, s, r)
			if e == nil || got.IMDbID != "" || got.TVDbID != "" {
				t.Fatalf("race/conflict accepted %+v %v", got, e)
			}
			var n int
			err := a.db.QueryRow(ctx, `SELECT count(*) FROM releases WHERE id=$1 AND (imdb_id='tt0944947' OR data->>'tvdb_id'='121361')`, r.ID).Scan(&n)
			if err != nil || n != 0 {
				t.Fatal("unsafe persistence", n, err)
			}
			if kind == "deleted" {
				a.db.QueryRow(ctx, `SELECT count(*) FROM releases WHERE id=$1`, r.ID).Scan(&n)
				if n != 0 {
					t.Fatal("resurrected")
				}
			}
		})
	}
}
func TestExternalIDsNegativeCacheAndProof(t *testing.T) {
	for _, body := range []string{`{"id":1399}`, `{"id":999,"imdb_id":"tt0944947"}`, `invalid synthetic-private-key`} {
		t.Run(body, func(t *testing.T) {
			a, s, r, calls := externalIDsFixture(t, body, nil)
			ctx := context.Background()
			for i := 0; i < 2; i++ {
				_, e := a.resolveShowExternalIDs(ctx, s, r)
				if e == nil || strings.Contains(e.Error(), s.TMDB.APIKey) {
					t.Fatalf("unsafe/missing error %v", e)
				}
			}
			if *calls != 1 {
				t.Fatalf("negative cache calls=%d", *calls)
			}
			var state string
			err := a.db.QueryRow(ctx, `SELECT value::text FROM app_state WHERE key LIKE 'tmdb_external_ids:%'`).Scan(&state)
			if err != nil || !strings.Contains(state, "https://www.themoviedb.org/tv/1399") || !strings.Contains(state, "checked_at") || strings.Contains(state, s.TMDB.APIKey) {
				t.Fatalf("unsafe/missing proof %q %v", state, err)
			}
			s.TMDB.APIKey = "replacement-synthetic-key"
			a.resolveShowExternalIDs(ctx, s, r)
			if *calls != 2 {
				t.Fatal("new configuration did not retry")
			}
			_, err = a.db.Exec(ctx, `UPDATE app_state SET value=jsonb_set(value,'{retry_after}',to_jsonb($1::text)) WHERE key LIKE 'tmdb_external_ids:%'`, time.Now().Add(-25*time.Hour).UTC().Format(time.RFC3339Nano))
			if err != nil {
				t.Fatal(err)
			}
			a.resolveShowExternalIDs(ctx, s, r)
			if *calls != 3 {
				t.Fatal("expired attempt did not retry")
			}
		})
	}
}
func TestExternalIDsRejectUnverified(t *testing.T) {
	for _, body := range []string{`{"id":1400,"imdb_id":"tt0944947"}`, `{"id":"1399","tvdb_id":121361}`, `{"id":1399,"imdb_id":" tt0944947","tvdb_id":121361}`, `{"id":1399,"tvdb_id":-1}`, `{"id":1399,"tvdb_id":"121361"}`, `{"id":1399,"tvdb_id":1.5}`, `{"id":1399,"imdb_id":"tt1234567890123"}`, `{"id":1399,"imdb_id":true}`, `{"id":1399,"imdb_id":null,"tvdb_id":null}`} {
		t.Run(body, func(t *testing.T) {
			a, s, r, _ := externalIDsFixture(t, body, nil)
			got, err := a.resolveShowExternalIDs(context.Background(), s, r)
			if err == nil || got.IMDbID != "" || got.TVDbID != "" {
				t.Fatalf("accepted invalid response %+v %v", got, err)
			}
			saved, e := a.getRelease(context.Background(), r.ID)
			if e != nil || saved.IMDbID != "" || saved.TVDbID != "" {
				t.Fatal("mutated invalid", saved, e)
			}
		})
	}
}
func TestExternalIDsPartialValid(t *testing.T) {
	for _, body := range []string{`{"id":1399,"imdb_id":null,"tvdb_id":121361}`, `{"id":1399,"imdb_id":"tt0944947","tvdb_id":null}`} {
		t.Run(body, func(t *testing.T) {
			a, s, r, _ := externalIDsFixture(t, body, nil)
			got, e := a.resolveShowExternalIDs(context.Background(), s, r)
			if e != nil || got.IMDbID == "" && got.TVDbID == "" {
				t.Fatal(got, e)
			}
		})
	}
}
func TestExternalIDsEligibility(t *testing.T) {
	for _, kind := range []string{"disabled", "no-key", "invalid", "existing-imdb", "existing-tvdb", "movie"} {
		t.Run(kind, func(t *testing.T) {
			a, s, r, calls := externalIDsFixture(t, `{"id":1399,"imdb_id":"tt0944947","tvdb_id":121361}`, nil)
			switch kind {
			case "disabled":
				s.TMDB.Enabled = false
			case "no-key":
				s.TMDB.APIKey = ""
			case "invalid":
				r.TMDbID = " 1399"
			case "existing-imdb":
				r.IMDbID = "tt0000001"
			case "existing-tvdb":
				r.TVDbID = "99"
			case "movie":
				r.Type = "movie"
			}
			got, _ := a.resolveShowExternalIDs(context.Background(), s, r)
			if *calls != 0 || got.IMDbID != r.IMDbID || got.TVDbID != r.TVDbID {
				t.Fatalf("eligibility mutated/called %+v %d", got, *calls)
			}
			var n int
			a.db.QueryRow(context.Background(), `SELECT count(*) FROM app_state WHERE key LIKE 'tmdb_external_ids:%'`).Scan(&n)
			if n != 0 {
				t.Fatal("ineligible lookup changed state")
			}
		})
	}
}
func TestExternalIDsEnrichPreserves(t *testing.T) {
	a, s, r, calls := externalIDsFixture(t, `{"id":1399,"imdb_id":"tt0944947","tvdb_id":121361}`, nil)
	got, err := a.resolveShowExternalIDs(context.Background(), s, r)
	if err != nil || got.IMDbID != "tt0944947" || got.TVDbID != "121361" {
		t.Fatalf("got %+v error %v", got, err)
	}
	saved, err := a.getRelease(context.Background(), r.ID)
	if err != nil || saved.Title != r.Title || saved.Description != r.Description || !saved.Monitored || saved.IMDbID != got.IMDbID || saved.TVDbID != got.TVDbID {
		t.Fatalf("preservation/readback %+v %v", saved, err)
	}
	if *calls != 1 {
		t.Fatal(*calls)
	}
}
