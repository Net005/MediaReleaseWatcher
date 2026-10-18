package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

var malformedDiscoveryEnvelopes = map[string]string{
	"unknown":              `{"error":"synthetic provider failure"}`,
	"missing_collection":   `{"pagination":{"has_more":false}}`,
	"movies_object":        `{"movies":{}}`,
	"items_string":         `{"items":"invalid"}`,
	"shows_null":           `{"shows":null}`,
	"pagination_array":     `{"items":[],"pagination":[]}`,
	"pagination_null":      `{"items":[],"pagination":null}`,
	"root_more_string":     `{"items":[],"has_more":"false"}`,
	"nested_more_number":   `{"items":[],"pagination":{"has_more":0}}`,
	"root_cursor_number":   `{"items":[],"next_cursor":12}`,
	"nested_cursor_object": `{"items":[],"pagination":{"next_cursor":{}}}`,
	"conflicting_more":     `{"items":[],"has_more":false,"pagination":{"has_more":true}}`,
	"conflicting_cursor":   `{"items":[],"next_cursor":"root","pagination":{"next_cursor":"nested"}}`,
}

func TestDiscoveryEnvelopeFailsClosed(t *testing.T) {
	for name, raw := range malformedDiscoveryEnvelopes {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := parseDiscovery(json.RawMessage(raw)); err == nil {
				t.Fatal("malformed discovery envelope accepted as complete")
			}
		})
	}
}

func TestRecognizedEmptyDiscoveryAndNullableTerminalCursor(t *testing.T) {
	for _, raw := range []string{`{"items":[]}`, `{"movies":[],"shows":[],"pagination":{"next_cursor":null,"has_more":false}}`, `{"items":[],"has_more":false,"next_cursor":null}`} {
		rows, cursor, more, err := parseDiscovery(json.RawMessage(raw))
		if err != nil || len(rows) != 0 || cursor != "" || more {
			t.Fatalf("valid empty terminal page rejected: %s %v", raw, err)
		}
	}
}

func TestPostgresMalformedDiscoveryEnvelopeNeverCommitsBaseline(t *testing.T) {
	for name, raw := range malformedDiscoveryEnvelopes {
		t.Run(name, func(t *testing.T) {
			a := testApp(t)
			ctx := context.Background()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/lists/1/items" {
					fmt.Fprint(w, raw)
				} else {
					fmt.Fprint(w, `{"items":[]}`)
				}
			}))
			defer server.Close()
			a.cfg.MDBListBaseURL = server.URL
			s := defaultSettings()
			s.MDBList.APIKey = "synthetic-key"
			s.MDBList.ListIDs = "1"
			if err := a.saveSettings(ctx, s); err != nil {
				t.Fatal(err)
			}
			if err := a.syncOnce(ctx); err == nil {
				t.Fatal("invalid discovery treated as completed sync")
			}
			var exists bool
			if err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app_state WHERE key='baseline')`).Scan(&exists); err != nil || exists {
				t.Fatalf("unsafe baseline persisted: %v %v", exists, err)
			}
		})
	}
}
