package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostgresCastAndPersonDetailsUseRealMetadata(t *testing.T) {
	a := policyApp(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/people/123" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		fmt.Fprint(w, `{"id":123,"name":"Synthetic Person","biography":"Provider biography","birthday":"1980-01-02","place_of_birth":"Synthetic place"}`)
	}))
	defer srv.Close()
	a.cfg.MDBListBaseURL = srv.URL
	s := defaultSettings()
	s.MDBList.APIKey = "synthetic"
	if err := a.saveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	r, people, err := normalizeRelease(json.RawMessage(`{"title":"Synthetic","type":"movie","tmdb_id":"42","cast":[{"id":123,"name":"Synthetic Person","character":"Role","profile_path":"/real.jpg"}],"crew":[{"id":999,"name":"Not cast"}]}`), "movie")
	if err != nil {
		t.Fatal(err)
	}
	r, err = a.saveRelease(ctx, r, people)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/releases/1/cast", nil)
	req.SetPathValue("id", fmt.Sprint(r.ID))
	w := httptest.NewRecorder()
	a.releaseCast(w, req)
	var cast struct {
		Items []CastPerson `json:"items"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &cast); w.Code != 200 || err != nil || len(cast.Items) != 1 || cast.Items[0].Name != "Synthetic Person" || cast.Items[0].Character != "Role" {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	req = httptest.NewRequest("GET", "/api/people/123", nil)
	req.SetPathValue("id", "123")
	w = httptest.NewRecorder()
	a.personDetail(w, req)
	var detail struct {
		Person   Actress   `json:"person"`
		Releases []Release `json:"releases"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &detail); w.Code != 200 || err != nil || detail.Person.Biography != "Provider biography" || detail.Person.TMDbID != "123" || len(detail.Releases) != 1 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	var n int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM actresses`).Scan(&n); err != nil || n != 0 {
		t.Fatal("detail created a watcher", n, err)
	}
}
