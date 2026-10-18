package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func syntheticSyncServer(t *testing.T, partial *atomic.Bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	batches := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/lists/1/items":
			if r.URL.Query().Get("cursor") == "next" {
				fmt.Fprint(w, `{"shows":[{"id":43,"title":"Synthetic Show"}],"pagination":{"has_more":false}}`)
			} else {
				fmt.Fprint(w, `{"movies":[{"id":42,"title":"Synthetic Movie"},{"id":44,"title":"Synthetic Other Movie"},{"imdb_id":"tt1234567","title":"IMDb-only"}],"pagination":{"next_cursor":"next"}}`)
			}
		case "/people/favorites/upcoming":
			fmt.Fprint(w, `{"items":[{"person":{"tmdb":123},"media":{"id":42,"title":"Synthetic Movie","type":"movie"},"release_date":"2027-01-02"}],"has_more":false}`)
		case "/catalog/movie", "/catalog/show":
			fmt.Fprint(w, `{"items":[],"has_more":false}`)
		case "/tmdb/movie", "/tmdb/show":
			batches.Add(1)
			var req struct {
				IDs []int `json:"ids"`
			}
			if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
				t.Error(e)
			}
			rows := []map[string]any{}
			kind := strings.TrimPrefix(r.URL.Path, "/tmdb/")
			for i := len(req.IDs) - 1; i >= 0; i-- {
				id := req.IDs[i]
				if partial.Load() && id == 44 {
					continue
				}
				rows = append(rows, map[string]any{"title": fmt.Sprintf("Enriched %d", id), "type": kind, "ids": map[string]int{"tmdb": id}, "description": "Rich synopsis", "genres": []map[string]string{{"title": "Drama"}}})
			}
			_ = json.NewEncoder(w).Encode(rows)
		case "/imdb/movie/tt1234567":
			fmt.Fprint(w, `{"title":"Enriched IMDb-only","type":"movie","ids":{"imdb":"tt1234567"},"description":"Rich IMDb synopsis"}`)
		case "/people/123":
			fmt.Fprint(w, `{"tmdb":123,"name":"Synthetic Person","biography":"Biography"}`)
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	return server, batches
}
func TestPostgresSyncPaginatedFeedsReorderedNumericBatchesAndBaseline(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var partial atomic.Bool
	server, batches := syntheticSyncServer(t, &partial)
	defer server.Close()
	a.cfg.MDBListBaseURL = server.URL
	s := defaultSettings()
	s.MDBList.APIKey = "synthetic-key"
	s.MDBList.ListIDs = "1"
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	p, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic Person", Monitored: true, MetadataStatus: "pending"})
	if e != nil {
		t.Fatal(e)
	}
	if e = a.syncOnce(ctx); e != nil {
		t.Fatal(e)
	}
	page, e := a.releases(ctx, releaseFilter{Limit: 500})
	if e != nil || page.Total != 4 {
		t.Fatalf("full feed not persisted: %d %v", page.Total, e)
	}
	for _, r := range page.Items {
		if r.Description == "" {
			t.Errorf("unenriched title: %+v", r)
		}
		if r.TMDbID == "42" && (len(r.Actresses) != 1 || r.Actresses[0].ID != p.ID || r.ReleaseDate != "2027-01-02") {
			t.Fatalf("favorites relationship/date lost: %+v", r)
		}
	}
	var n int
	if e = a.db.QueryRow(ctx, `SELECT count(DISTINCT (release_id,kind)) FROM actions WHERE status='baseline'`).Scan(&n); e != nil || n != 8 {
		t.Fatalf("baseline not complete: %d %v", n, e)
	}
	assertSilentCurrentBaseline(t, a, 4)
	if e = a.syncOnce(ctx); e != nil {
		t.Fatal(e)
	}
	if batches.Load() != 2 {
		t.Fatalf("fresh enriched metadata not cached: %d batches", batches.Load())
	}
}
func TestPostgresOmittedEnrichmentStillCommitsSilentBaseline(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var partial atomic.Bool
	partial.Store(true)
	server, _ := syntheticSyncServer(t, &partial)
	defer server.Close()
	a.cfg.MDBListBaseURL = server.URL
	s := defaultSettings()
	s.MDBList.APIKey = "synthetic-key"
	s.MDBList.ListIDs = "1"
	if e := a.saveSettings(ctx, s); e != nil {
		t.Fatal(e)
	}
	if e := a.syncOnce(ctx); e != nil {
		t.Fatalf("complete discovery with omitted enrichment must establish silent baseline: %v", e)
	}
	var exists bool
	if e := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app_state WHERE key='baseline')`).Scan(&exists); e != nil || !exists {
		t.Fatalf("complete discovery baseline missing: %v %v", exists, e)
	}
	var n int
	if e := a.db.QueryRow(ctx, `SELECT count(DISTINCT (release_id,kind)) FROM actions WHERE status='baseline'`).Scan(&n); e != nil || n != 8 {
		t.Fatalf("omitted title not included in silent baseline: %d %v", n, e)
	}
	assertSilentCurrentBaseline(t, a, 4)
	if e := a.db.QueryRow(ctx, `SELECT count(*) FROM activity WHERE kind='metadata' AND status='warning'`).Scan(&n); e != nil || n == 0 {
		t.Fatal("partial metadata warning missing", e)
	}
	var coverage string
	if e := a.db.QueryRow(ctx, `SELECT data->>'coverage' FROM releases WHERE tmdb_id='44'`).Scan(&coverage); e != nil || !strings.Contains(coverage, "enrichment pending") {
		t.Fatal("omitted title coverage not retained", coverage, e)
	}
	partial.Store(false)
	if e := a.syncOnce(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestPostgresActorDetailReturnsAllActualMatches(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic Person"})
	if e != nil {
		t.Fatal(e)
	}
	// Seed through a single real SQL statement to cross the API's 500-row page cap.
	_, e = a.db.Exec(ctx, `INSERT INTO releases(canonical_key,media_type,tmdb_id,data) SELECT 'movie:tmdb:'||n,'movie',n::text,jsonb_build_object('title','Synthetic '||n,'type','movie','tmdb_id',n::text,'genres','[]'::jsonb,'ratings','[]'::jsonb,'watch_providers','[]'::jsonb) FROM generate_series(1,503)n; INSERT INTO release_people(release_id,actress_id,source) SELECT id,1,'synthetic:test' FROM releases`)
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES($1,jsonb_build_object('status','ok','checked_at',now()))`, fmt.Sprintf("person_credits:%d", p.ID))
	if e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("GET", fmt.Sprintf("http://localhost/api/actresses/%d", p.ID), nil)
	req.SetPathValue("id", fmt.Sprint(p.ID))
	w := httptest.NewRecorder()
	a.actressDetail(w, req)
	var got struct {
		Total    int       `json:"total"`
		Releases []Release `json:"releases"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &got); e != nil || w.Code != 200 || got.Total != 503 || len(got.Releases) != 503 {
		t.Fatalf("actor detail truncated: status=%d total=%d count=%d %v", w.Code, got.Total, len(got.Releases), e)
	}
}
func TestPostgresAutomationFailedAcquisitionRetriesButNotificationIsUncertain(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	_, e := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic Person", Monitored: true, AutoAdd: true, Notify: true})
	if e != nil {
		t.Fatal(e)
	}
	r, people, _ := normalizeRelease(json.RawMessage(fmt.Sprintf(`{"title":"Synthetic","type":"movie","ids":{"tmdb":42},"released":%q,"cast":[{"id":123}]}`, time.Now().UTC().Format("2006-01-02"))), "movie")
	r, e = a.saveRelease(ctx, r, people)
	if e != nil {
		t.Fatal(e)
	}
	s := defaultSettings()
	s.Automation.Enabled = true
	s.Radarr.Enabled = true
	s.Pushover.Enabled = true
	if e = a.automate(ctx, s, false); e != nil {
		t.Fatal(e)
	}
	statuses := func() map[string]string {
		rows, e := a.db.Query(ctx, `SELECT kind,status FROM actions WHERE release_id=$1`, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var kind, status string
			if e = rows.Scan(&kind, &status); e != nil {
				t.Fatal(e)
			}
			out[kind] = status
		}
		return out
	}
	got := statuses()
	if got["acquire"] != "failed" || got["notify"] != "uncertain" {
		t.Fatal(got)
	}
	if e = a.automate(ctx, s, false); e != nil {
		t.Fatal(e)
	}
	got = statuses()
	if got["notify"] != "uncertain" {
		t.Fatal("uncertain notification retried", got)
	}
}
