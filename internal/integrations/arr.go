package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
)

type arrService struct{ kind, base, key, resource string }

func (c *Client) arr(kind string) (arrService, error) {
	var s arrService
	switch kind {
	case "sonarr":
		s = arrService{kind, c.cfg.SonarrURL, c.cfg.SonarrAPIKey, "series"}
	case "radarr":
		s = arrService{kind, c.cfg.RadarrURL, c.cfg.RadarrAPIKey, "movie"}
	default:
		return s, errors.New("unsupported acquisition target")
	}
	if strings.TrimSpace(s.base) == "" || strings.TrimSpace(s.key) == "" {
		return s, fmt.Errorf("%s URL and API key must be configured", kind)
	}
	if _, err := endpoint(s.base, "", nil); err != nil {
		return s, err
	}
	return s, nil
}

// Full manager catalogues can exceed 8 MiB (observed Radarr: 41 MiB).
// Keep this allowance local to unfiltered inventory GETs, never mutations,
// lookups, item readbacks or other providers.
const maxArrInventoryBytes = 64 << 20

func (c *Client) arrRequest(ctx context.Context, s arrService, method, path string, q url.Values, body any, out any) error {
	target, err := endpoint(s.base, "/api/v3/"+path, q)
	if err != nil {
		return err
	}
	var payload []byte
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s request encoding failed", s.kind)
		}
	}
	limit := maxResponseBytes
	if method == http.MethodGet && body == nil && len(q) == 0 && ((s.kind == "radarr" && path == "movie") || (s.kind == "sonarr" && path == "series")) {
		limit = maxArrInventoryBytes
	}
	raw, err := c.requestHeadersLimit(ctx, s.kind, method, target, s.key, "application/json", payload, nil, limit)
	if err != nil {
		return err
	}
	return decode(raw, out, s.kind)
}
func (c *Client) ArrOptions(ctx context.Context, kind string) (Options, error) {
	out := Options{RootFolders: []RootFolder{}, QualityProfiles: []QualityProfile{}}
	s, err := c.arr(kind)
	if err != nil {
		return out, err
	}
	if err := c.arrRequest(ctx, s, http.MethodGet, "rootfolder", nil, nil, &out.RootFolders); err != nil {
		return out, err
	}
	if err := c.arrRequest(ctx, s, http.MethodGet, "qualityprofile", nil, nil, &out.QualityProfiles); err != nil {
		return out, err
	}
	return out, nil
}

type arrSeason struct {
	SeasonNumber    int  `json:"seasonNumber"`
	Monitored       bool `json:"monitored"`
	monitoringKnown bool
}
type arrItem struct {
	ID                  int         `json:"id"`
	Title               string      `json:"title"`
	TitleSlug           string      `json:"titleSlug"`
	TMDbID              int         `json:"tmdbId"`
	TVDbID              int         `json:"tvdbId"`
	IMDbID              string      `json:"imdbId"`
	QualityProfileID    int         `json:"qualityProfileId"`
	RootFolderPath      string      `json:"rootFolderPath"`
	Path                string      `json:"path"`
	Monitored           *bool       `json:"monitored"`
	Tags                []int       `json:"tags"`
	MinimumAvailability string      `json:"minimumAvailability"`
	Seasons             []arrSeason `json:"seasons"`
	HasFile             bool        `json:"hasFile"`
	MovieFile           struct {
		ID   int   `json:"id"`
		Size int64 `json:"size"`
	} `json:"movieFile"`
	Statistics struct {
		EpisodeFileCount int  `json:"episodeFileCount"`
		EpisodeCount     *int `json:"episodeCount"`
	} `json:"statistics"`
}

func itemFromMap(m map[string]any) arrItem {
	raw, _ := json.Marshal(m)
	var item arrItem
	_ = json.Unmarshal(raw, &item)
	return item
}

// sameMedia rejects conflicting supplied identities, rather than matching the
// title or silently accepting one matching ID alongside another mismatching ID.
func sameMedia(item arrItem, m Media, kind string) bool {
	matched := false
	if kind == "radarr" && m.TMDbID > 0 {
		if item.TMDbID != m.TMDbID {
			return false
		}
		matched = true
	}
	if kind == "sonarr" && m.TVDbID > 0 {
		if item.TVDbID != m.TVDbID {
			return false
		}
		matched = true
	}
	if m.IMDbID != "" {
		if item.IMDbID != "" && item.IMDbID != m.IMDbID {
			return false
		}
		if !matched {
			return item.IMDbID == m.IMDbID
		}
	}
	return matched
}
func validMedia(kind string, m Media) error {
	if (kind == "radarr" && m.Type != "movie") || (kind == "sonarr" && m.Type != "show") {
		return errors.New("media type is incompatible with acquisition target")
	}
	if m.TMDbID < 0 || m.TVDbID < 0 || (m.IMDbID != "" && !imdbID.MatchString(m.IMDbID)) {
		return errors.New("invalid media ID")
	}
	if m.IMDbID == "" && ((kind == "radarr" && m.TMDbID == 0) || (kind == "sonarr" && m.TVDbID == 0)) {
		return errors.New("acquisition requires a native provider ID or IMDb ID")
	}
	return nil
}
func validAddOptions(o AddOptions, kind string) error {
	if o.QualityProfileID <= 0 || o.RootFolderPath == "" {
		return errors.New("select an explicit root folder and quality profile")
	}
	for _, tag := range o.Tags {
		if tag <= 0 {
			return errors.New("invalid acquisition tag")
		}
	}
	if kind == "radarr" && o.MinimumAvailability != "" && !slices.Contains([]string{"tba", "announced", "inCinemas", "released"}, o.MinimumAvailability) {
		return errors.New("invalid Radarr minimum availability")
	}
	return nil
}
func validateSelections(options Options, o AddOptions) error {
	rootOK, profileOK := false, false
	for _, r := range options.RootFolders {
		if r.Path == o.RootFolderPath {
			rootOK = true
		}
	}
	for _, p := range options.QualityProfiles {
		if p.ID == o.QualityProfileID {
			profileOK = true
		}
	}
	if !rootOK || !profileOK {
		return errors.New("selected root folder or quality profile is not available")
	}
	return nil
}

// Add serializes additions on this client, deduplicates by provider identity,
// uses lookup metadata, and verifies the exact returned record before success.
// No automatic retry is made after an ambiguous POST or verification failure.
func (c *Client) Add(ctx context.Context, kind string, media Media, options AddOptions) (AddResult, error) {
	zero := AddResult{}
	s, err := c.arr(kind)
	if err != nil {
		return zero, err
	}
	if err := validMedia(kind, media); err != nil {
		return zero, err
	}
	if err := validAddOptions(options, kind); err != nil {
		return zero, err
	}
	c.addMu.Lock()
	defer c.addMu.Unlock()
	if err := ctx.Err(); err != nil {
		return zero, errors.New("acquisition request canceled")
	}
	selections, err := c.ArrOptions(ctx, kind)
	if err != nil {
		return zero, err
	}
	if err := validateSelections(selections, options); err != nil {
		return zero, err
	}
	inventory, err := c.ArrInventory(ctx, kind)
	if err != nil {
		return zero, err
	}
	presence, err := inventory.Match(media)
	if err != nil {
		return zero, err
	}
	if presence.ID > 0 {
		if options.RestoreMonitoring {
			return c.restoreArrLocked(ctx, s, presence.ID, media, options.Search)
		}
		verified, err := c.readArr(ctx, s, presence.ID)
		if err != nil {
			return zero, fmt.Errorf("%s existing-item readback failed: %w", kind, err)
		}
		if verified.ID != presence.ID || !sameMedia(verified, media, kind) {
			return zero, errors.New("existing acquisition record failed identity verification")
		}
		return c.arrResult(s, verified, true), nil
	}
	term := "imdb:" + media.IMDbID
	if kind == "radarr" && media.TMDbID > 0 {
		term = "tmdb:" + strconv.Itoa(media.TMDbID)
	}
	if kind == "sonarr" && media.TVDbID > 0 {
		term = "tvdb:" + strconv.Itoa(media.TVDbID)
	}
	var candidates []map[string]any
	if err := c.arrRequest(ctx, s, http.MethodGet, s.resource+"/lookup", url.Values{"term": {term}}, nil, &candidates); err != nil {
		return zero, err
	}
	var metadata map[string]any
	var found arrItem
	for _, candidate := range candidates {
		item := itemFromMap(candidate)
		shared := kind == "radarr" && media.TMDbID > 0 && item.TMDbID == media.TMDbID || kind == "sonarr" && media.TVDbID > 0 && item.TVDbID == media.TVDbID || media.IMDbID != "" && item.IMDbID == media.IMDbID
		if shared && !sameMedia(item, media, kind) {
			return zero, errors.New("acquisition lookup has conflicting provider identities")
		}
		if sameMedia(item, media, kind) {
			if metadata != nil {
				return zero, errors.New("acquisition lookup match is ambiguous")
			}
			metadata = candidate
			found = item
		}
	}
	if metadata == nil {
		return zero, errors.New("no exact acquisition lookup match found")
	}
	if found.Title == "" || found.TitleSlug == "" || (kind == "radarr" && found.TMDbID <= 0) || (kind == "sonarr" && (found.TVDbID <= 0 || found.Seasons == nil)) {
		return zero, errors.New("acquisition lookup metadata is incomplete")
	}
	// Copy only documented fields needed for add; do not forward lookup IDs,
	// existing paths, add options, monitoring defaults or arbitrary provider data.
	payload := map[string]any{}
	for _, field := range []string{"title", "titleSlug", "year", "imdbId", "tmdbId", "tvdbId", "images", "overview", "originalLanguage", "seriesType"} {
		if v, ok := metadata[field]; ok {
			payload[field] = v
		}
	}
	payload["rootFolderPath"] = options.RootFolderPath
	payload["qualityProfileId"] = options.QualityProfileID
	payload["monitored"] = options.Monitored
	tags := append([]int{}, options.Tags...)
	slices.Sort(tags)
	tags = slices.Compact(tags)
	payload["tags"] = tags
	if kind == "radarr" {
		if options.MinimumAvailability == "" {
			options.MinimumAvailability = "released"
		}
		payload["minimumAvailability"] = options.MinimumAvailability
		monitor := "none"
		if options.Monitored {
			monitor = "movieOnly"
		}
		payload["addOptions"] = map[string]any{"searchForMovie": options.Search, "monitor": monitor}
	} else {
		seasons := append([]arrSeason{}, found.Seasons...)
		for i := range seasons {
			seasons[i].Monitored = options.Monitored
		}
		payload["seasons"] = seasons
		payload["seasonFolder"] = true
		monitor := "none"
		if options.Monitored {
			monitor = "all"
		}
		payload["monitorNewItems"] = monitor
		payload["addOptions"] = map[string]any{"searchForMissingEpisodes": options.Search, "searchForCutoffUnmetEpisodes": false, "monitor": monitor}
	}
	var added arrItem
	if err := c.arrRequest(ctx, s, http.MethodPost, s.resource, nil, payload, &added); err != nil {
		return zero, err
	}
	if added.ID <= 0 {
		return zero, errors.New("acquisition response did not provide an item ID; inspect the target before retrying")
	}
	verified, err := c.readArr(ctx, s, added.ID)
	if err != nil {
		return zero, fmt.Errorf("%s add may have completed but readback failed; inspect the target before retrying: %w", kind, err)
	}
	// Include native lookup identity even if the caller supplied only IMDb.
	expected := media
	if kind == "radarr" {
		expected.TMDbID = found.TMDbID
	} else {
		expected.TVDbID = found.TVDbID
	}
	if verified.ID != added.ID || !sameMedia(verified, expected, kind) || !verifyAdd(verified, found, options, kind, tags) {
		return zero, errors.New("acquisition add may have completed but exact readback verification failed; inspect the target before retrying")
	}
	return c.arrResult(s, verified, false), nil
}
func (c *Client) readArr(ctx context.Context, s arrService, id int) (arrItem, error) {
	var item arrItem
	err := c.arrRequest(ctx, s, http.MethodGet, s.resource+"/"+strconv.Itoa(id), nil, nil, &item)
	return item, err
}
func verifyAdd(got, lookup arrItem, o AddOptions, kind string, tags []int) bool {
	if got.QualityProfileID != o.QualityProfileID || got.Monitored == nil || *got.Monitored != o.Monitored {
		return false
	}
	if got.RootFolderPath != "" && got.RootFolderPath != o.RootFolderPath {
		return false
	}
	root := path.Clean(strings.ReplaceAll(o.RootFolderPath, "\\", "/"))
	actualPath := path.Clean(strings.ReplaceAll(got.Path, "\\", "/"))
	if got.Path == "" || actualPath == root || !strings.HasPrefix(actualPath, strings.TrimRight(root, "/")+"/") {
		return false
	}
	actualTags := append([]int{}, got.Tags...)
	slices.Sort(actualTags)
	actualTags = slices.Compact(actualTags)
	if !slices.Equal(actualTags, tags) {
		return false
	}
	if kind == "radarr" {
		return got.MinimumAvailability == o.MinimumAvailability
	}
	for _, season := range lookup.Seasons {
		found := false
		for _, actual := range got.Seasons {
			if actual.SeasonNumber == season.SeasonNumber {
				if actual.Monitored != o.Monitored {
					return false
				}
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func (c *Client) arrResult(s arrService, item arrItem, exists bool) AddResult {
	// Internal item IDs are always safe for API readback. The UI uses the lookup
	// slug; never forward an arbitrary URL returned by the remote API.
	slug := item.TitleSlug
	if !safeSiloID(slug) {
		slug = strconv.Itoa(item.ID)
	}
	// endpoint encodes Path exactly once; pre-escaping here corrupts Unicode slugs.
	path := "/" + s.resource + "/" + slug
	target, _ := endpoint(s.base, path, nil)
	return AddResult{ID: item.ID, AlreadyExists: exists, URL: target}
}
