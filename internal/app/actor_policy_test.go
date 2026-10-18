package app

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresImportExplicitActorFlagsUpdateExactIDOnly(t *testing.T) {
	a := policyApp(t)
	ctx := context.Background()
	p, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Original", Group: "original", Monitored: true, Notify: false})
	if err != nil {
		t.Fatal(err)
	}
	r := policyRelease(t, a, "42")
	if _, err = a.saveRelease(ctx, r, []string{"123"}); err != nil {
		t.Fatal(err)
	}
	send := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		a.importActresses(w, httptest.NewRequest("POST", "/api/actresses/import", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	send(`{"entries":[{"actor_id":"123","name":"Do not replace","group":"ignored","auto_add":true,"notify":true},{"actor_id":"123","name":"Duplicate"}]}`)
	got, err := a.getActress(ctx, p.ID)
	if err != nil || !got.AutoAdd || !got.Notify || !got.Monitored || got.Name != "Original" || got.Group != "original" {
		t.Fatal(got, err)
	}
	send(`{"entries":[{"actor_id":"123","name":"Ignored","auto_add":false}]}`)
	got, err = a.getActress(ctx, p.ID)
	if err != nil || got.AutoAdd || !got.Notify {
		t.Fatal(got, err)
	}
	var n int
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM actresses`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err = a.db.QueryRow(ctx, `SELECT count(*) FROM actions WHERE release_id=$1 AND status='baseline'`, r.ID).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
}
