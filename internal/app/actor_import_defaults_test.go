package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostgresImportDefaultsAndEntryOverrides(t *testing.T) {
	a := policyApp(t)
	ctx := context.Background()
	original, err := a.saveActress(ctx, Actress{TMDbID: "120", Name: "Keep identity", Group: "old", Monitored: false})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"group":"Asian","monitored":true,"auto_add":true,"notify":true,"entries":[{"actor_id":"120","name":"Ignored"},{"actor_id":"121","name":"Default"},{"actor_id":"122","name":"Overrides","group":"Other","monitored":false,"auto_add":false,"notify":false},{"actor_id":"121","name":"Ignored duplicate","auto_add":false,"notify":false},{"actor_id":"123","name":"Default"}]}`
	w := httptest.NewRecorder()
	a.importActresses(w, httptest.NewRequest("POST", "/api/actresses/import", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct{ Added, Skipped int }
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Added != 3 || response.Skipped != 2 {
		t.Fatal(response, err)
	}
	got, err := a.getActress(ctx, original.ID)
	if err != nil || got.Name != "Keep identity" || got.Group != "old" || got.Monitored || got.AutoAdd || got.Notify {
		t.Fatalf("defaults changed existing opt-outs: %+v %v", got, err)
	}
	people, err := a.actresses(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range people {
		switch p.TMDbID {
		case "121", "123":
			if p.Group != "Asian" || !p.Monitored || !p.AutoAdd || !p.Notify {
				t.Fatalf("defaults/duplicate/same-name identities: %+v", p)
			}
		case "122":
			if p.Group != "Other" || p.Monitored || p.AutoAdd || p.Notify {
				t.Fatalf("explicit overrides: %+v", p)
			}
		}
	}
	if len(people) != 4 {
		t.Fatal(people)
	}
}

func TestPostgresImportOmittedFlagsAndAddDefaults(t *testing.T) {
	a := policyApp(t)
	ctx := context.Background()
	w := httptest.NewRecorder()
	a.importActresses(w, httptest.NewRequest("POST", "/api/actresses/import", strings.NewReader(`{"entries":[{"actor_id":"125","name":"Imported"}]}`)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.addActress(w, httptest.NewRequest("POST", "/api/actresses", strings.NewReader(`{"tmdb_id":"126","name":"Added"}`)))
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	people, err := a.actresses(ctx, "", "")
	if err != nil || len(people) != 2 {
		t.Fatal(people, err)
	}
	for _, p := range people {
		if !p.Monitored || p.AutoAdd || p.Notify {
			t.Fatalf("expected monitored=true, automation=false: %+v", p)
		}
	}
}

func TestPostgresActressesUpcomingOnlyVerifiedFutureMatches(t *testing.T) {
	a := policyApp(t)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		_, err := a.saveActress(ctx, Actress{TMDbID: fmt.Sprint(130 + i), Name: fmt.Sprintf("Person %d", i), Group: "Asian", Monitored: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	dates := []string{time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02"), time.Now().UTC().Format("2006-01-02"), "2000-01-01", "", "2099-01-01"}
	for i, date := range dates {
		r := policyRelease(t, a, fmt.Sprint(201+i))
		r.ReleaseDate = date
		if _, err := a.saveRelease(ctx, r, []string{fmt.Sprint(131 + i)}); err != nil {
			t.Fatal(err)
		}
	}
	// Even a stale link cannot establish a verified match after identity quarantine.
	if _, err := a.db.Exec(ctx, `UPDATE actresses SET data=data||'{"metadata_status":"unavailable: identity mismatch; manual review"}'::jsonb WHERE tmdb_id='135'`); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"?upcoming_only=true", "?upcoming_only=true&group=Asian&q=Person", "?upcoming_only=true&group=Other", "?upcoming_only=false"} {
		w := httptest.NewRecorder()
		a.listActresses(w, httptest.NewRequest("GET", "/api/actresses"+query, nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var response struct {
			Items []Actress
			Total int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		expected := 1
		if strings.Contains(query, "group=Other") {
			expected = 0
		}
		if strings.Contains(query, "=false") {
			expected = 5
		}
		if response.Total != expected || len(response.Items) != expected {
			t.Fatalf("%s: %+v", query, response)
		}
		if expected == 1 && (response.Items[0].TMDbID != "131" || response.Items[0].UpcomingCount != 1) {
			t.Fatal(response)
		}
	}
}
