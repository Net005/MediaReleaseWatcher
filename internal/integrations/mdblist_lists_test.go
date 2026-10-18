package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMDBPersonalListReadRoutes(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Query().Get("apikey") != "synthetic-key" {
			t.Fatal("unsafe list request")
		}
		switch r.URL.Path {
		case "/lists/user":
			fmt.Fprint(w, `[{"id":42,"name":"Example"}]`)
		case "/lists/42":
			fmt.Fprint(w, `{"id":42,"name":"Example"}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := NewClient(Config{MDBListBaseURL: srv.URL, MDBListAPIKey: "synthetic-key"})
	for _, path := range []string{"/lists/user", "/lists/42"} {
		if _, e := c.MDBGet(context.Background(), path, nil); e != nil {
			t.Fatalf("%s: %v", path, e)
		}
	}
	for _, path := range []string{"/lists/0", "/lists/042", "/lists/-1", "/lists/user/42", "/lists/user/", "/lists/42/delete", "/lists/42%2Fitems", "/lists/42?write=true"} {
		if _, e := c.MDBGet(context.Background(), path, nil); e == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	if calls != 2 {
		t.Fatalf("rejected paths reached server: %d", calls)
	}
}
