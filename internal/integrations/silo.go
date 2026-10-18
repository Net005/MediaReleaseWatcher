package integrations

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Silo's native v2 contract uses Bearer auth, an acting profile for catalog
// reads, cursor pages, and detail imdb_id/tmdb_id fields (not title matching).
// Verified against silo-plugin-metadata-stash/internal/legacyprovider/{catalog,silo}.go
// and Silo-Server/silo-server/contracts/api/v2/openapi.json.
const siloCacheTTL = 5 * time.Minute
const siloMaxPages = 1000

type LocalAvailability struct {
	Status    string    `json:"status"`
	URL       string    `json:"url"`
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

type siloItem struct {
	ContentID string `json:"content_id"`
	Type      string `json:"type"`
	IMDbID    string `json:"imdb_id"`
	TMDbID    string `json:"tmdb_id"`
	TVDbID    string `json:"tvdb_id"`
}
type siloPage struct {
	HasMore    *bool  `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}
type siloSnapshot struct {
	at           time.Time
	items        []siloItem
	unidentified map[string]bool
}

// safeSiloID accepts native opaque IDs but never lets a remote value change the
// origin, route, query, or fragment of a link/request. Unknown forms fail closed.
func safeSiloID(id string) bool {
	return id != "" && len(id) <= 512 && id != "." && id != ".." && !strings.ContainsAny(id, "/\\%?#\r\n\t ")
}
func (c *Client) siloGet(ctx context.Context, path string, q url.Values, profile string, out any) error {
	if strings.TrimSpace(c.cfg.SiloAPIKey) == "" {
		return errors.New("silo API key must be configured")
	}
	target, err := endpoint(c.cfg.SiloURL, path, q)
	if err != nil {
		return err
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+c.cfg.SiloAPIKey)
	if profile != "" {
		headers.Set("X-Profile-Id", profile)
	}
	raw, err := c.requestHeaders(ctx, "silo", http.MethodGet, target, "", "", nil, headers)
	if err != nil {
		return err
	}
	return decode(raw, out, "silo")
}
func (c *Client) siloProfile(ctx context.Context) (string, error) {
	var result struct {
		Items []struct {
			ID      string `json:"id"`
			Primary bool   `json:"is_primary"`
		} `json:"items"`
	}
	if err := c.siloGet(ctx, "/api/v2/profiles", nil, "", &result); err != nil {
		return "", err
	}
	primary := ""
	for _, p := range result.Items {
		if p.Primary {
			if !safeSiloID(p.ID) || primary != "" {
				return "", errors.New("silo primary profile is unavailable or ambiguous")
			}
			primary = p.ID
		}
	}
	if primary != "" {
		return primary, nil
	}
	if len(result.Items) == 1 && safeSiloID(result.Items[0].ID) {
		return result.Items[0].ID, nil
	}
	return "", errors.New("silo primary profile is unavailable or ambiguous")
}

// SiloTest performs GET-only authentication and catalog permission checks.
func (c *Client) SiloTest(ctx context.Context) error {
	profile, err := c.siloProfile(ctx)
	if err != nil {
		return err
	}
	var result struct {
		Items []siloItem `json:"items"`
		Page  *siloPage  `json:"page"`
	}
	if err = c.siloGet(ctx, "/api/v2/catalog", url.Values{"limit": {"1"}, "skip_total": {"true"}}, profile, &result); err != nil {
		return err
	}
	if result.Items == nil || result.Page == nil || result.Page.HasMore == nil {
		return errors.New("silo returned incomplete catalog page")
	}
	return nil
}

// nextSiloCursor requires an explicit terminal page, and detects cycles of any
// length. A partial response must never authorize an absent result.
func nextSiloCursor(page *siloPage, seen map[string]bool) (string, error) {
	if page == nil || page.HasMore == nil {
		return "", errors.New("silo returned incomplete pagination")
	}
	if !*page.HasMore {
		return "", nil
	}
	if page.NextCursor == "" || seen[page.NextCursor] {
		return "", errors.New("silo pagination did not advance")
	}
	seen[page.NextCursor] = true
	return page.NextCursor, nil
}
func (c *Client) siloInventory(ctx context.Context) (*siloSnapshot, error) {
	if c.siloCache != nil && time.Since(c.siloCache.at) < siloCacheTTL {
		return c.siloCache, nil
	}
	profile, err := c.siloProfile(ctx)
	if err != nil {
		return nil, err
	}
	snapshot := &siloSnapshot{unidentified: map[string]bool{}}
	seenCursor, seenID := map[string]bool{}, map[string]bool{}
	cursor := ""
	for n := 0; n < siloMaxPages; n++ {
		var page struct {
			Items []siloItem `json:"items"`
			Page  *siloPage  `json:"page"`
		}
		q := url.Values{"limit": {"200"}, "skip_total": {"true"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		if err = c.siloGet(ctx, "/api/v2/catalog", q, profile, &page); err != nil {
			return nil, err
		}
		if page.Items == nil {
			return nil, errors.New("silo returned incomplete catalog")
		}
		for _, row := range page.Items {
			if !safeSiloID(row.ContentID) {
				return nil, errors.New("silo returned an unsafe content ID")
			}
			if seenID[row.ContentID] {
				return nil, errors.New("silo catalog changed during pagination")
			}
			seenID[row.ContentID] = true
			switch row.Type {
			case "movie", "series":
				// Supported exact types; validate the detail identity below.
			case "season", "episode", "audiobook", "ebook", "manga", "podcast", "podcast_episode":
				// Other CatalogItem types documented by the v2 schema.
				continue
			default:
				return nil, errors.New("silo returned missing or unsupported catalog type; availability is unknown")
			}
			var detail siloItem
			if err = c.siloGet(ctx, "/api/v2/catalog/items/"+row.ContentID, nil, profile, &detail); err != nil {
				return nil, err
			}
			if detail.ContentID != row.ContentID || detail.Type != row.Type {
				return nil, errors.New("silo detail identity changed during scan")
			}
			if detail.IMDbID == "" && detail.TMDbID == "" && detail.TVDbID == "" {
				// Unmatched/custom items are legitimate. They cannot invalidate an
				// observed exact hit, but prevent proving a miss for this media type.
				snapshot.unidentified[detail.Type] = true
				continue
			}
			if (detail.IMDbID != "" && !imdbID.MatchString(detail.IMDbID)) || (detail.TMDbID != "" && !positiveLiteralID(detail.TMDbID)) || (detail.TVDbID != "" && !positiveLiteralID(detail.TVDbID)) {
				return nil, errors.New("silo returned invalid provider identity")
			}
			snapshot.items = append(snapshot.items, detail)
		}
		cursor, err = nextSiloCursor(page.Page, seenCursor)
		if err != nil {
			return nil, err
		}
		if cursor == "" {
			snapshot.at = time.Now().UTC()
			c.siloCache = snapshot
			return snapshot, nil
		}
	}
	return nil, errors.New("silo catalog exceeds pagination safety bound")
}
func positiveLiteralID(id string) bool {
	if id == "" || id[0] < '1' || id[0] > '9' {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// SiloAvailability reports exact, type-consistent catalog presence only, not
// disk readability or playability. Absence requires a complete catalog scan.
// Cached snapshots are per-client, credential-scoped, and published atomically.
func (c *Client) SiloAvailability(ctx context.Context, m Media) (out LocalAvailability, err error) {
	out = LocalAvailability{Status: "unknown", CheckedAt: time.Now().UTC()}
	defer func() {
		if err != nil {
			out.Status = "error"
			out.Error = err.Error()
		}
	}()
	if m.Type != "movie" && m.Type != "show" {
		return out, errors.New("invalid Silo media type")
	}
	if m.TMDbID < 0 || m.TVDbID < 0 || (m.IMDbID != "" && !imdbID.MatchString(m.IMDbID)) {
		return out, errors.New("invalid Silo media identity")
	}
	if m.TMDbID == 0 && m.TVDbID == 0 && m.IMDbID == "" {
		return out, nil
	}
	c.siloMu.Lock()
	defer c.siloMu.Unlock()
	snapshot, err := c.siloInventory(ctx)
	if err != nil {
		return out, err
	}
	var match *siloItem
	kind := m.Type
	if kind == "show" {
		kind = "series"
	}
	for _, item := range snapshot.items {
		if item.Type != kind {
			continue
		}
		shared := (m.TMDbID > 0 && item.TMDbID == strconv.Itoa(m.TMDbID)) || (m.IMDbID != "" && item.IMDbID == m.IMDbID) || (m.TVDbID > 0 && item.TVDbID == strconv.Itoa(m.TVDbID))
		if !shared {
			continue
		}
		if (m.TMDbID > 0 && item.TMDbID != "" && item.TMDbID != strconv.Itoa(m.TMDbID)) || (m.IMDbID != "" && item.IMDbID != "" && item.IMDbID != m.IMDbID) || (m.TVDbID > 0 && item.TVDbID != "" && item.TVDbID != strconv.Itoa(m.TVDbID)) {
			return out, errors.New("silo match has conflicting provider identities")
		}
		if match != nil {
			return out, errors.New("silo match is ambiguous")
		}
		copy := item
		match = &copy
	}
	if match == nil {
		if snapshot.unidentified[kind] {
			return out, errors.New("silo item has no provider identity; availability is unknown")
		}
		out.Status = "absent"
		return out, nil
	}
	out.Status = "available"
	out.URL, err = endpoint(c.cfg.SiloURL, "/item/"+match.ContentID, nil)
	return out, err
}
