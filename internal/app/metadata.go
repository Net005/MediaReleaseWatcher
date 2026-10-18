package app

import "encoding/json"

// Sparse discovery and supplemental credits must never erase richer metadata.
// Collection absence/empty means unavailable, not an authoritative deletion.
func mergeRelease(old, in Release) Release {
	// Availability belongs to verified local checks, never provider metadata.
	in.LocalAvailability = old.LocalAvailability
	if in.Title == "" {
		in.Title = old.Title
	}
	if in.Type == "" {
		in.Type = old.Type
	}
	if in.Year == 0 {
		in.Year = old.Year
	}
	if in.IMDbID == "" {
		in.IMDbID = old.IMDbID
	}
	if in.TMDbID == "" {
		in.TMDbID = old.TMDbID
	}
	if in.TVDbID == "" {
		in.TVDbID = old.TVDbID
	}
	if in.MDBListID == "" {
		in.MDBListID = old.MDBListID
	}
	if in.Poster == "" {
		in.Poster = old.Poster
	}
	if in.Backdrop == "" {
		in.Backdrop = old.Backdrop
	}
	if in.Description == "" {
		in.Description = old.Description
	}
	if in.Country == "" {
		in.Country = old.Country
	}
	if in.Runtime == 0 {
		in.Runtime = old.Runtime
	}
	if in.ReleaseDate == "" {
		in.ReleaseDate = old.ReleaseDate
	}
	if in.DigitalDate == "" {
		in.DigitalDate = old.DigitalDate
	}
	if in.Status == "" {
		in.Status = old.Status
	}
	if in.Certification == "" {
		in.Certification = old.Certification
	}
	if in.AgeRating == nil {
		in.AgeRating = old.AgeRating
	}
	if in.Score == nil {
		in.Score = old.Score
	}
	if len(in.Genres) == 0 {
		in.Genres = old.Genres
	}
	if len(in.Cast) == 0 {
		in.Cast = old.Cast
	}
	if len(in.Ratings) == 0 {
		in.Ratings = old.Ratings
	}
	if len(in.WatchProviders) == 0 {
		in.WatchProviders = old.WatchProviders
	}
	if in.NextEpisode == nil {
		in.NextEpisode = old.NextEpisode
	}
	in.ContentInfo = mergeMetadata(old.ContentInfo, in.ContentInfo)
	prior, _ := decodeObject(old.Raw)
	current, e := decodeObject(in.Raw)
	if e == nil {
		in.Raw, _ = json.Marshal(mergeMetadata(prior, current))
	} else {
		in.Raw = old.Raw
	}
	seen := map[string]bool{}
	links := []string{}
	for _, set := range [][]string{old.SourceLinks, in.SourceLinks} {
		for _, u := range set {
			if u != "" && !seen[u] {
				seen[u] = true
				links = append(links, u)
			}
		}
	}
	in.SourceLinks = links
	if in.MetadataFetchedAt.IsZero() {
		in.MetadataFetchedAt = old.MetadataFetchedAt
		if !old.MetadataFetchedAt.IsZero() {
			in.Coverage = coverageWithSourceWarnings(in.Coverage, old.Coverage)
		}
	}
	if in.Coverage == "" {
		in.Coverage = old.Coverage
	}
	if in.Source == "" {
		in.Source = old.Source
	}
	return in
}
func mergeMetadata(old, in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range old {
		out[k] = v
	}
	for k, v := range in {
		if v == nil {
			continue
		}
		switch x := v.(type) {
		case string:
			if x == "" {
				continue
			}
		case []any:
			if len(x) == 0 {
				continue
			}
		case map[string]any:
			out[k] = mergeMetadata(object(out[k]), x)
			continue
		}
		out[k] = v
	}
	return out
}
