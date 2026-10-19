package app

import (
	"context"
	"encoding/json"
	"errors"
	"media-release-watcher/internal/integrations"
	"strconv"
	"strings"
	"time"
)

// currentPersonMetadataStatus hides retired supplemental-provider diagnostics
// from current biography coverage without deleting their stored history or links.
func currentPersonMetadataStatus(status string) string {
	parts := strings.Split(status, " | ")
	current := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" && !strings.Contains(strings.ToLower(part), "tvmaze") {
			current = append(current, part)
		}
	}
	if len(current) == 0 {
		return "Unknown / not enriched"
	}
	return strings.Join(current, " | ")
}

// Retained for historical explicit cast-credit ingestion and regression checks.
// Normal detail and sync paths do not request supplemental provider credits.
func (a *App) enrichTVCredits(ctx context.Context, p Actress, s Settings, credits []integrations.Credit) (int, error) {
	matched, incomplete := 0, 0
	var e error
	for _, credit := range credits {
		if e = ctx.Err(); e != nil {
			return matched, e
		}
		provider, id := "imdb", credit.IMDbID
		// MDBList has no documented TVDB lookup route. Reuse local TVDB
		// metadata when present; otherwise report an incomplete supplemental credit.

		var raw json.RawMessage
		// Reuse cached MDBList title metadata, avoiding one request per actor/title pair.
		var localID int64
		lookup := a.db.QueryRow(ctx, `SELECT id FROM releases WHERE media_type='show' AND (($1<>'' AND imdb_id=$1) OR ($2<>'' AND data->>'tvdb_id'=$2)) LIMIT 1`, credit.IMDbID, strconv.Itoa(credit.TVDbID)).Scan(&localID)
		var r Release
		var people []string
		if lookup == nil {
			r, e = a.getRelease(ctx, localID)
		} else if id == "" {
			incomplete++
			a.activity(ctx, "credits", "Supplemental TVDB-only credit has no cached MDBList metadata; unsupported lookup skipped, coverage incomplete", "warning")
			continue
		} else {
			raw, e = a.mdb(ctx, s, "/"+provider+"/show/"+id, nil)
			if e == nil {
				r, people, e = normalizeRelease(raw, "show")
			}
		}
		if e != nil {
			incomplete++
			a.activity(ctx, "credits", "One supplemental TV credit could not be enriched by MDBList; no title invented", "warning")
			continue
		}
		if (credit.IMDbID != "" && r.IMDbID != credit.IMDbID) || (credit.TVDbID > 0 && r.TVDbID != "" && r.TVDbID != strconv.Itoa(credit.TVDbID)) {
			incomplete++
			a.activity(ctx, "credits", "Supplemental metadata identity mismatch; no person match attached", "warning")
			continue
		}
		if lookup != nil {
			r.MetadataFetchedAt = time.Now().UTC()
		}
		people = append(people, p.TMDbID)
		r.Coverage = coverageWithSourceWarnings(r.Coverage, "MDBList title metadata; explicit television cast credit from TVMaze (CC BY-SA 4.0). TV only; not complete filmography.")
		found := false
		for _, u := range r.SourceLinks {
			if u == credit.URL {
				found = true
			}
		}
		if !found {
			r.SourceLinks = append(r.SourceLinks, credit.URL)
		}
		if _, e = a.saveReleaseSource(ctx, r, people, "tvmaze:castcredits:"+credit.URL); e != nil {
			return matched, e
		}
		matched++
	}
	if incomplete > 0 {
		return matched, errors.New("metadata supplemental credits incomplete; unsupported or failed MDBList lookups will not be cached as complete")
	}
	return matched, nil
}
