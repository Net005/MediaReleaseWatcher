package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSiloKnownMatchSurvivesUnmatchedCatalogueEntries(t *testing.T) {
	for _, otherType := range []string{"movie", "series", "manga"} {
		t.Run(otherType, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Errorf("unexpected write")
					w.WriteHeader(405)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/api/v2/profiles":
					json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"id": "primary", "is_primary": true}}})
				case r.URL.Path == "/api/v2/catalog":
					json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"content_id": "unmatched", "type": otherType}, map[string]any{"content_id": "known", "type": "movie"}}, "page": map[string]any{"has_more": false}})
				case strings.HasSuffix(r.URL.Path, "/unmatched"):
					json.NewEncoder(w).Encode(map[string]any{"content_id": "unmatched", "type": otherType, "title": "Unmatched fixture", "genres": []string{}, "keywords": []string{}, "status": "unmatched"})
				case strings.HasSuffix(r.URL.Path, "/known"):
					json.NewEncoder(w).Encode(map[string]any{"content_id": "known", "type": "movie", "tmdb_id": "42", "imdb_id": "tt1234567", "title": "Known fixture", "genres": []string{}, "keywords": []string{}, "status": "matched"})
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			client := NewClient(Config{SiloURL: server.URL, SiloAPIKey: "synthetic-key"})
			found, err := client.SiloAvailability(context.Background(), Media{Type: "movie", TMDbID: 42, IMDbID: "tt1234567"})
			if err != nil || found.Status != "available" || found.URL != server.URL+"/item/known" {
				t.Fatalf("known exact match blocked by unrelated unmatched item: %+v %v", found, err)
			}
			missing, err := client.SiloAvailability(context.Background(), Media{Type: "movie", TMDbID: 43})
			if otherType == "movie" {
				if missing.Status == "absent" || missing.Status == "available" {
					t.Fatalf("unidentified movie must not prove absence: %+v %v", missing, err)
				}
			} else if err != nil || missing.Status != "absent" {
				t.Fatalf("unidentified series must not block a complete movie miss: %+v %v", missing, err)
			}
		})
	}
}
