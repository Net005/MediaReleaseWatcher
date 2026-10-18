package integrations

import (
	"context"
	"errors"
	"net/http"
)

// ArrPresence describes manager membership, not an acquisition or download.
// Ready means at least one actual media file, not a complete series.
type ArrPresence struct {
	ID        int    `json:"id"`
	Ready     bool   `json:"ready"`
	FileCount int    `json:"file_count"`
	URL       string `json:"url"`
	// CompleteCoverage is transient Sonarr evidence, separate from Ready/UI state.
	CompleteCoverage bool `json:"-"`
}

// ArrInventory is a complete immutable manager snapshot. Match never performs
// network requests, so one inventory can reconcile an entire app catalog.
type ArrInventory struct {
	kind  string
	base  string
	items []arrItem
}

func (c *Client) ArrInventory(ctx context.Context, kind string) (*ArrInventory, error) {
	s, err := c.arr(kind)
	if err != nil {
		return nil, err
	}
	var items []arrItem
	if err = c.arrRequest(ctx, s, http.MethodGet, s.resource, nil, nil, &items); err != nil {
		return nil, err
	}
	if items == nil {
		return nil, errors.New("manager returned incomplete inventory")
	}
	seen := map[int]bool{}
	for _, item := range items {
		missingIdentity := kind == "radarr" && item.TMDbID == 0 && item.IMDbID == "" || kind == "sonarr" && item.TVDbID == 0 && item.IMDbID == ""
		if missingIdentity || item.ID <= 0 || seen[item.ID] || item.TMDbID < 0 || item.TVDbID < 0 || (item.IMDbID != "" && !imdbID.MatchString(item.IMDbID)) {
			return nil, errors.New("manager returned invalid or duplicate inventory identity")
		}
		seen[item.ID] = true
	}
	return &ArrInventory{kind: kind, base: s.base, items: items}, nil
}
func (inventory *ArrInventory) Match(m Media) (ArrPresence, error) {
	if inventory == nil {
		return ArrPresence{}, errors.New("manager inventory unavailable")
	}
	if err := validMedia(inventory.kind, m); err != nil {
		return ArrPresence{}, err
	}
	var match *arrItem
	for _, item := range inventory.items {
		sharedNative := inventory.kind == "radarr" && m.TMDbID > 0 && item.TMDbID == m.TMDbID || inventory.kind == "sonarr" && m.TVDbID > 0 && item.TVDbID == m.TVDbID
		sharedIMDb := m.IMDbID != "" && item.IMDbID == m.IMDbID
		if !sharedNative && !sharedIMDb {
			continue
		}
		conflict := inventory.kind == "radarr" && m.TMDbID > 0 && item.TMDbID > 0 && item.TMDbID != m.TMDbID || inventory.kind == "sonarr" && m.TVDbID > 0 && item.TVDbID > 0 && item.TVDbID != m.TVDbID || m.IMDbID != "" && item.IMDbID != "" && item.IMDbID != m.IMDbID
		if conflict {
			return ArrPresence{}, errors.New("manager match has conflicting provider identities")
		}
		if match != nil {
			return ArrPresence{}, errors.New("manager match is ambiguous")
		}
		copy := item
		match = &copy
	}
	if match == nil {
		return ArrPresence{}, nil
	}
	count := 0
	if inventory.kind == "radarr" && match.HasFile && match.MovieFile.ID > 0 && match.MovieFile.Size > 0 {
		count = 1
	}
	if inventory.kind == "sonarr" && match.Statistics.EpisodeFileCount > 0 {
		count = match.Statistics.EpisodeFileCount
	}
	c := &Client{}
	resource := "movie"
	if inventory.kind == "sonarr" {
		resource = "series"
	}
	result := c.arrResult(arrService{kind: inventory.kind, base: inventory.base, resource: resource}, *match, true)
	episodeCount := match.Statistics.EpisodeCount
	complete := inventory.kind == "sonarr" && episodeCount != nil && *episodeCount > 0 && match.Statistics.EpisodeFileCount >= *episodeCount
	return ArrPresence{ID: match.ID, Ready: count > 0, FileCount: count, URL: result.URL, CompleteCoverage: complete}, nil
}
