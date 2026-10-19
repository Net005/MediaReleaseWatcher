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
)

type noTVMazeTransport struct {
	fallback http.RoundTripper
	calls    atomic.Int32
}

func (tr *noTVMazeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Hostname(), "tvmaze.com") {
		tr.calls.Add(1)
		return nil, fmt.Errorf("retired provider must not be requested")
	}
	return tr.fallback.RoundTrip(req)
}

func TestCurrentPersonMetadataStatusPreservesBiographyWarnings(t *testing.T) {
	for _, tc := range []struct{ status, want string }{
		{"", "Unknown / not enriched"},
		{"Supplemental TVMaze credits unavailable", "Unknown / not enriched"},
		{"unavailable: identity mismatch | Supplemental TVMaze credits unavailable", "unavailable: identity mismatch"},
		{"ok: MDBList biography | Supplemental TVMaze credits unavailable: old error", "ok: MDBList biography"},
	} {
		if got := currentPersonMetadataStatus(tc.status); got != tc.want {
			t.Errorf("status %q: got %q want %q", tc.status, got, tc.want)
		}
	}
}

func TestPostgresDetailNeverRequestsTVMazeAndRetainsVerifiedLinks(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic Person", Biography: "MDBList biography", MetadataStatus: "ok: MDBList biography | Supplemental TVMaze credits unavailable: retired failure; no inferred matches."})
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := normalizeRelease(json.RawMessage(`{"title":"Verified historical title","type":"show","ids":{"tmdb":42,"imdb":"tt1234567"}}`), "show")
	if err != nil {
		t.Fatal(err)
	}
	const source = "https://www.tvmaze.com/shows/42/synthetic"
	r.SourceLinks = append(r.SourceLinks, source)
	r, err = a.saveReleaseSource(ctx, r, []string{p.TMDbID}, "tvmaze:castcredits:"+source)
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("person_credits:%d", p.ID)
	_, err = a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES($1,'{"status":"error","checked_at":"2000-01-01T00:00:00Z","source":"TVMaze","legacy":"preserve"}')`, key)
	if err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	trap := &noTVMazeTransport{fallback: old}
	http.DefaultTransport = trap
	defer func() { http.DefaultTransport = old }()
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/actresses/%d", p.ID), nil)
	req.SetPathValue("id", fmt.Sprint(p.ID))
	w := httptest.NewRecorder()
	a.actressDetail(w, req)
	a.wg.Wait()
	if trap.calls.Load() != 0 {
		t.Fatalf("detail issued %d retired TVMaze requests", trap.calls.Load())
	}
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var got struct {
		Actress  Actress   `json:"actress"`
		Releases []Release `json:"releases"`
		Total    int       `json:"total"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(got.Actress.MetadataStatus), "tvmaze") {
		t.Fatalf("retired error displayed as current coverage: %q", got.Actress.MetadataStatus)
	}
	if got.Actress.Biography != "MDBList biography" || got.Total != 1 || len(got.Releases) != 1 || got.Releases[0].ID != r.ID || !strings.Contains(strings.Join(got.Releases[0].SourceLinks, " "), source) {
		t.Fatalf("historical verified match lost: %+v", got)
	}
	var legacy string
	if err = a.db.QueryRow(ctx, `SELECT value->>'legacy' FROM app_state WHERE key=$1`, key).Scan(&legacy); err != nil || legacy != "preserve" {
		t.Fatalf("detail overwrote historical state or queued dead job: %q %v", legacy, err)
	}
	var provenance string
	if err = a.db.QueryRow(ctx, `SELECT source FROM release_cast WHERE release_id=$1 AND tmdb_id=$2`, r.ID, p.TMDbID).Scan(&provenance); err != nil || provenance != "tvmaze:castcredits:"+source {
		t.Fatal("historical provenance lost", provenance, err)
	}
	stored, err := a.getActress(ctx, p.ID)
	if err != nil || stored.MetadataStatus != p.MetadataStatus {
		t.Fatal("read-only detail deleted historical diagnostics", stored.MetadataStatus, err)
	}
	// The cast-person view can refresh MDBList biography but cannot initiate
	// supplemental credits, create watches, or erase verified cached cast links.
	mdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/people/123" {
			t.Errorf("unexpected MDBList route: %s", req.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":123,"name":"Synthetic Person","biography":"MDBList biography"}`))
	}))
	defer mdb.Close()
	a.cfg.MDBListBaseURL = mdb.URL
	s := defaultSettings()
	s.MDBList.APIKey = "synthetic-key"
	if err = a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("GET", "/api/people/123", nil)
	req.SetPathValue("id", "123")
	w = httptest.NewRecorder()
	a.personDetail(w, req)
	a.wg.Wait()
	var personGot struct {
		Person   Actress   `json:"person"`
		Releases []Release `json:"releases"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &personGot); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if trap.calls.Load() != 0 || personGot.Person.Biography != "MDBList biography" || len(personGot.Releases) != 1 || personGot.Releases[0].ID != r.ID {
		t.Fatalf("cast-person detail requested retired provider or lost verified data: requests=%d response=%+v", trap.calls.Load(), personGot)
	}
}
