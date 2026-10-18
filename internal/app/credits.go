package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"media-release-watcher/internal/integrations"
	"strconv"
	"strings"
	"time"
)

// Credit discovery is on-demand and cached. There is deliberately no claimed
// complete filmography: this supplemental source covers television cast only.
func (a *App) queueCredits(id int64) {
	if a.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Second)
	defer cancel()
	key := "person_credits:" + strconv.FormatInt(id, 10)
	tag, e := a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES($1,jsonb_build_object('status','queued','checked_at',now(),'source','TVMaze cast credits (CC BY-SA 4.0)')) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE (app_state.value->>'checked_at')::timestamptz<now()-CASE WHEN app_state.value->>'status'='ok' THEN interval '30 days' ELSE interval '1 day' END`, key)
	if e != nil || tag.RowsAffected() != 1 {
		return
	}
	a.mu.Lock()
	if a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	a.wg.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		ctx, cancel := context.WithTimeout(a.ctx, 20*time.Minute)
		defer cancel()
		matched, e := a.discoverTVCredits(ctx, id)
		status := "ok"
		message := fmt.Sprintf("Supplemental TVMaze television cast credits (CC BY-SA 4.0): %d MDBList-enriched titles. Not a complete movie filmography.", matched)
		if e != nil {
			status = "error"
			message = "Supplemental TVMaze credits unavailable: " + providerError(e) + "; no inferred matches."
		}
		save, done := context.WithTimeout(context.WithoutCancel(a.ctx), 5*time.Second)
		defer done()
		b, _ := json.Marshal(map[string]any{"status": status, "checked_at": time.Now().UTC(), "matched": matched, "source": "TVMaze cast credits (CC BY-SA 4.0)", "limitation": "TV only, not complete filmography"})
		_, _ = a.db.Exec(save, `UPDATE app_state SET value=$2 WHERE key=$1`, key, b)
		if p, err := a.getActress(save, id); err == nil {
			if i := strings.Index(p.MetadataStatus, " | Supplemental"); i >= 0 {
				p.MetadataStatus = p.MetadataStatus[:i]
			}
			p.MetadataStatus += " | " + message
			_, _ = a.savePersonMetadata(save, p)
		}
		a.activity(save, "credits", message, status)
	}()
}
func (a *App) discoverTVCredits(ctx context.Context, id int64) (int, error) {
	p, e := a.getActress(ctx, id)
	if e != nil {
		return 0, e
	}
	s, e := a.settings(ctx)
	if e != nil {
		return 0, e
	}
	if strings.HasPrefix(p.MetadataStatus, "pending") || p.Biography == "" {
		p = a.enrichPerson(ctx, s, p)
		p, e = a.savePersonMetadata(ctx, p)
		if e != nil {
			return 0, e
		}
	}
	if strings.HasPrefix(p.MetadataStatus, "unavailable: identity mismatch") {
		return 0, errors.New("metadata identity mismatch; supplemental credits suspended until source identity is reviewed")
	}
	a.activity(ctx, "credits", "Resolving exact supplemental television cast identity; MDBList remains title metadata source", "running")
	credits, e := a.client(s).TVMazeCredits(ctx, p.Name, p.Birthday)
	if e != nil {
		return 0, e
	}
	return a.enrichTVCredits(ctx, p, s, credits)
}
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
