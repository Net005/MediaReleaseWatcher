package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// restoreArrLocked requires addMu and explicit manual RestoreMonitoring consent.
// Edit the fresh, exact resource rather than rebuilding it from add options;
// RawMessage preserves unknown fields and numbers without float64 conversion.
func (c *Client) restoreArrLocked(ctx context.Context, s arrService, id int, media Media, search bool) (AddResult, error) {
	zero := AddResult{}
	target := s.resource + "/" + strconv.Itoa(id)
	var resource map[string]json.RawMessage
	if err := c.arrRequest(ctx, s, http.MethodGet, target, nil, nil, &resource); err != nil {
		return zero, err
	}
	raw, err := json.Marshal(resource)
	if err != nil {
		return zero, errors.New("existing acquisition resource is invalid")
	}
	var item arrItem
	if json.Unmarshal(raw, &item) != nil || item.ID != id || !sameMedia(item, media, s.kind) {
		return zero, errors.New("existing acquisition record failed identity verification")
	}
	if item.Monitored == nil {
		return zero, errors.New("existing acquisition monitoring state is unknown")
	}
	// Bind IMDb-only requests to the native identity observed before any write.
	expected := media
	if s.kind == "radarr" {
		expected.TMDbID = item.TMDbID
	} else {
		expected.TVDbID = item.TVDbID
	}
	changed := !*item.Monitored
	requestedSeasons := map[int]bool{}
	if s.kind == "sonarr" {
		var seasons []map[string]json.RawMessage
		if json.Unmarshal(resource["seasons"], &seasons) != nil || seasons == nil {
			return zero, errors.New("existing series seasons are unknown")
		}
		for i, season := range item.Seasons {
			if !season.monitoringKnown || requestedSeasons[season.SeasonNumber] {
				return zero, errors.New("existing series season monitoring state is unknown or ambiguous")
			}
			requestedSeasons[season.SeasonNumber] = true
			if !season.Monitored {
				changed = true
			}
			seasons[i]["monitored"] = json.RawMessage("true")
		}
		resource["seasons"], err = json.Marshal(seasons)
		if err != nil {
			return zero, errors.New("existing series seasons are invalid")
		}
	}
	validate := func(got arrItem) error {
		if got.ID != id || !sameMedia(got, expected, s.kind) || got.Monitored == nil || !*got.Monitored || (s.kind == "sonarr" && !verifiedMonitoredSeasons(got.Seasons, requestedSeasons)) {
			return errors.New("monitoring update failed exact readback verification; inspect the manager before retrying")
		}
		return nil
	}
	if changed {
		resource["monitored"] = json.RawMessage("true")
		var ignored json.RawMessage
		if err := c.arrRequest(ctx, s, http.MethodPut, target, nil, resource, &ignored); err != nil {
			return zero, fmt.Errorf("monitoring update may have completed; inspect the manager before retrying: %w", err)
		}
		item, err = c.readArr(ctx, s, id)
		if err != nil {
			return zero, fmt.Errorf("monitoring update may have completed but readback failed; inspect the manager before retrying: %w", err)
		}
	}
	if err := validate(item); err != nil {
		return zero, err
	}
	if search {
		if err := c.arrSearchLocked(ctx, s, id, expected, nil, validate); err != nil {
			return zero, err
		}
	}
	result := c.arrResult(s, item, true)
	result.MonitoredRestored = changed
	result.SearchStarted = search
	return result, nil
}

// UnmarshalJSON distinguishes explicit false/season zero from omitted or null
// state. Legacy add/inventory behavior remains unchanged; restore fails closed.
func (s *arrSeason) UnmarshalJSON(raw []byte) error {
	var state struct {
		SeasonNumber *int  `json:"seasonNumber"`
		Monitored    *bool `json:"monitored"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	*s = arrSeason{}
	if state.SeasonNumber != nil {
		s.SeasonNumber = *state.SeasonNumber
	}
	if state.Monitored != nil {
		s.Monitored = *state.Monitored
	}
	s.monitoringKnown = state.SeasonNumber != nil && *state.SeasonNumber >= 0 && state.Monitored != nil
	return nil
}

func verifiedMonitoredSeasons(seasons []arrSeason, requested map[int]bool) bool {
	if seasons == nil {
		return false
	}
	seen := map[int]bool{}
	for _, season := range seasons {
		if !season.monitoringKnown || !season.Monitored || seen[season.SeasonNumber] {
			return false
		}
		seen[season.SeasonNumber] = true
	}
	for number := range requested {
		if !seen[number] {
			return false
		}
	}
	return true
}
