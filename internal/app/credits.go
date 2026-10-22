package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"media-release-watcher/internal/integrations"
	"strconv"
	"strings"
	"time"
)

// Credit identities refresh daily, independently of person biography caching.
const tmdbCreditRefreshInterval = 24 * time.Hour

type tmdbCreditProgress struct {
	PendingActors          int
	ReadyActors            int
	DeferredActors         int
	UnattemptedActors      int
	UnattemptedCredits     int
	PendingCredits         int
	DeferredCredits        int
	ProviderBlockedCredits int
	ProviderBlockedActors  int
	ErrorActors            int
}

// Cached-only coverage: pending actors includes deferred actors; pending credits
// are immediately eligible, not proof that all pending actors have failed.
func (a *App) tmdbCreditProgress(ctx context.Context, now time.Time) (tmdbCreditProgress, error) {
	var progress tmdbCreditProgress
	rows, err := a.db.Query(ctx, `SELECT COALESCE(c.value,'{}'::jsonb), ARRAY(SELECT r.media_type||':'||r.tmdb_id FROM releases r JOIN jsonb_array_elements(COALESCE(NULLIF(c.value->'credits','null'::jsonb),'[]'::jsonb)) credit ON r.tmdb_id=credit->>'id' AND r.media_type=credit->>'kind' WHERE COALESCE(credit->>'resolved','false')<>'true') FROM actresses p LEFT JOIN app_state c ON c.key='tmdb:actor:'||p.tmdb_id WHERE COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%'`)
	if err != nil {
		return progress, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var primaryIDs []string
		if err := rows.Scan(&raw, &primaryIDs); err != nil {
			return progress, err
		}
		var cache tmdbCreditCache
		if err := json.Unmarshal(raw, &cache); err != nil {
			return progress, err
		}
		pending := !cache.Complete || cache.Fetched.IsZero() || now.Sub(cache.Fetched) > tmdbCreditRefreshInterval
		if pending {
			progress.PendingActors++
		}
		if cache.Fetched.IsZero() && cache.Attempted.IsZero() && cache.Error == "" {
			progress.UnattemptedActors++
		}
		ready := !cache.NextRetry.After(now) && (cache.Fetched.IsZero() || now.Sub(cache.Fetched) > tmdbCreditRefreshInterval || (!cache.Complete && len(cache.Credits) == 0))
		primary := map[string]bool{}
		for _, id := range primaryIDs {
			primary[id] = true
		}
		blocked := !cache.Complete && cache.Error != ""
		for _, credit := range cache.Credits {
			if credit.Resolved {
				continue
			}
			if credit.Attempts == 0 {
				progress.UnattemptedCredits++
			}
			if (credit.NextRetry.After(now) && !primary[credit.Kind+":"+credit.ID]) || cache.NextRetry.After(now) {
				progress.DeferredCredits++
			} else {
				progress.PendingCredits++
				ready = true
			}
			if credit.Error != "" {
				progress.ProviderBlockedCredits++
				blocked = true
			}
		}
		if pending {
			if ready {
				progress.ReadyActors++
			} else {
				progress.DeferredActors++
			}
		}
		if blocked {
			progress.ErrorActors++
			progress.ProviderBlockedActors++
		}
	}
	return progress, rows.Err()
}

type tmdbCreditCache struct {
	Credits       []tmdbCredit `json:"credits"`
	Fetched       time.Time    `json:"fetched"`
	Attempted     time.Time    `json:"attempted"`
	Complete      bool         `json:"complete"`
	Next          int          `json:"next"`
	Error         string       `json:"error,omitempty"`
	RetryAttempts int          `json:"retry_attempts,omitempty"`
	NextRetry     time.Time    `json:"next_retry,omitempty"`
}

func (c *tmdbCredit) deferRetry(now time.Time) {
	c.Attempts++
	c.Attempted = now
	c.NextRetry = now.Add(creditRetryDelay(c.Attempts))
	c.Error = "MDBList title metadata unavailable"
}

func creditRetryDelay(attempts int) time.Duration {
	d := time.Hour
	for i := 1; i < attempts && d < 7*24*time.Hour; i++ {
		d *= 2
	}
	if d > 7*24*time.Hour {
		d = 7 * 24 * time.Hour
	}
	return d
}

// Unattempted work takes precedence; due retries retain circular fairness.
func (c *tmdbCreditCache) selectCredits(now time.Time, budget int) map[string][]int {
	selected := map[string][]int{}
	n := len(c.Credits)
	if n == 0 {
		return selected
	}
	start := c.Next % n
	if start < 0 {
		start = 0
	}
	for pass := 0; pass < 2 && budget > 0; pass++ {
		for examined := 0; examined < n && budget > 0; examined++ {
			i := (start + examined) % n
			credit := c.Credits[i]
			if credit.Resolved || credit.NextRetry.After(now) || (pass == 0) != (credit.Attempts == 0) {
				continue
			}
			selected[credit.Kind] = append(selected[credit.Kind], i)
			c.Next = (i + 1) % n
			budget--
		}
	}
	return selected
}

// Four actors and ten title resolutions per actor per sync, independently resumable.
// Full credit identities are saved before any title enrichment, never inferred from crew.
const tmdbCreditActorEligibility = `SELECT p.id,p.data,COALESCE(c.value,'{}'::jsonb) FROM actresses p LEFT JOIN app_state c ON c.key='tmdb:actor:'||p.tmdb_id
 WHERE ($2::bigint[] IS NULL OR p.id=ANY($2::bigint[])) AND COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%'
 AND COALESCE(NULLIF(c.value->>'next_retry','')::timestamptz,'-infinity'::timestamptz)<=now()
 AND (COALESCE(NULLIF(c.value->>'fetched','')::timestamptz,'-infinity'::timestamptz)<$1::timestamptz
 OR (COALESCE(c.value->>'complete','false')<>'true' AND (
 jsonb_array_length(COALESCE(NULLIF(c.value->'credits','null'::jsonb),'[]'::jsonb))=0 OR EXISTS (
 SELECT 1 FROM jsonb_array_elements(COALESCE(NULLIF(c.value->'credits','null'::jsonb),'[]'::jsonb)) credit
 WHERE COALESCE(credit->>'resolved','false')<>'true'
 AND (COALESCE(NULLIF(credit->>'next_retry','')::timestamptz,'-infinity'::timestamptz)<=now()
 OR EXISTS (SELECT 1 FROM releases r WHERE r.media_type=credit->>'kind' AND r.tmdb_id=credit->>'id'))))))`

const tmdbCreditActorSelection = tmdbCreditActorEligibility + ` ORDER BY COALESCE(NULLIF(c.value->>'attempted','')::timestamptz,'-infinity'::timestamptz),p.id LIMIT 4`

func (a *App) syncTMDBCredits(ctx context.Context, s Settings) (int, error) {
	return a.syncTMDBCreditsForWatches(ctx, s, nil)
}

func (a *App) syncTMDBCreditsForWatches(ctx context.Context, s Settings, watchIDs []int64) (int, error) {
	if !s.TMDB.Enabled || s.TMDB.APIKey == "" {
		return 0, nil
	}
	rows, e := a.db.Query(ctx, tmdbCreditActorSelection, time.Now().UTC().Add(-tmdbCreditRefreshInterval).Format(time.RFC3339Nano), watchIDs)
	if e != nil {
		return 0, e
	}
	type job struct {
		p Actress
		c tmdbCreditCache
	}
	jobs := []job{}
	for rows.Next() {
		var j job
		var raw, cache []byte
		var id int64
		if e = rows.Scan(&id, &raw, &cache); e != nil {
			rows.Close()
			return 0, e
		}
		if e = json.Unmarshal(raw, &j.p); e != nil {
			rows.Close()
			return 0, e
		}
		j.p.ID = id
		if e = json.Unmarshal(cache, &j.c); e != nil {
			rows.Close()
			return 0, e
		}
		jobs = append(jobs, j)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	for _, j := range jobs {
		j.c.Attempted = time.Now().UTC()
		j.c.Error = ""
		key := "tmdb:actor:" + j.p.TMDbID
		persist := func() error {
			b, _ := json.Marshal(j.c)
			_, err := a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, b)
			return err
		}
		if _, err := a.tmdbPerson(ctx, s, j.p); err != nil {
			j.c.Error = "TMDb person unavailable or identity mismatch; no credits attached"
			j.c.Complete = false
			j.c.RetryAttempts++
			j.c.NextRetry = tmdbRetryAt(err, j.c.Attempted.Add(creditRetryDelay(j.c.RetryAttempts)))
			if e = persist(); e != nil {
				return 0, e
			}
			continue
		}
		if j.c.Fetched.IsZero() || time.Since(j.c.Fetched) > tmdbCreditRefreshInterval {
			credits, err := a.tmdbPersonCredits(ctx, s, j.p.TMDbID)
			if err != nil {
				j.c.Complete = false
				j.c.Error = "TMDb combined cast credits unavailable; prior identities retained"
				j.c.RetryAttempts++
				j.c.NextRetry = tmdbRetryAt(err, j.c.Attempted.Add(creditRetryDelay(j.c.RetryAttempts)))
				if e = persist(); e != nil {
					return 0, e
				}
				continue
			}
			known := map[string]bool{}
			for _, c := range j.c.Credits {
				known[c.Kind+":"+c.ID] = true
			}
			for _, c := range credits {
				if !known[c.Kind+":"+c.ID] {
					j.c.Credits = append(j.c.Credits, c)
				}
			}
			j.c.Fetched = time.Now().UTC()
			j.c.Complete = false
			if e = persist(); e != nil {
				return 0, e
			}
		}
		j.c.RetryAttempts = 0
		j.c.NextRetry = time.Time{}
		// A title independently learned from the primary provider is usable now,
		// even when its previous missing-metadata lookup is still deferred.
		creditJSON, _ := json.Marshal(j.c.Credits)
		localRows, err := a.db.Query(ctx, `SELECT r.tmdb_id,r.media_type FROM releases r JOIN jsonb_array_elements(COALESCE(NULLIF($1::jsonb,'null'::jsonb),'[]'::jsonb)) credit ON r.tmdb_id=credit->>'id' AND r.media_type=credit->>'kind' WHERE COALESCE(credit->>'resolved','false')<>'true'`, creditJSON)
		if err != nil {
			return 0, err
		}
		local := map[string]bool{}
		for localRows.Next() {
			var id, kind string
			if err := localRows.Scan(&id, &kind); err != nil {
				localRows.Close()
				return 0, err
			}
			local[kind+":"+id] = true
		}
		err = localRows.Err()
		localRows.Close()
		if err != nil {
			return 0, err
		}
		for i := range j.c.Credits {
			if local[j.c.Credits[i].Kind+":"+j.c.Credits[i].ID] {
				j.c.Credits[i].NextRetry = time.Time{}
			}
		}
		selected := j.c.selectCredits(time.Now().UTC(), 10)
		for _, kind := range []string{"movie", "show"} {
			missing := []string{}
			resolved := map[string]Release{}
			cast := map[string][]string{}
			for _, i := range selected[kind] {
				c := j.c.Credits[i]
				var id int64
				err := a.db.QueryRow(ctx, `SELECT id FROM releases WHERE media_type=$1 AND tmdb_id=$2`, kind, c.ID).Scan(&id)
				if err == nil {
					// Pass identity only: merge fresh metadata inside the locked store helper.
					resolved[c.ID] = Release{Type: kind, TMDbID: c.ID}
				} else if errors.Is(err, pgx.ErrNoRows) {
					missing = append(missing, c.ID)
				} else {
					return 0, err
				}
			}
			if len(missing) > 0 {
				if e = a.pace(ctx); e != nil {
					return 0, e
				}
				raws, err := a.client(s).MDBBatch(ctx, "tmdb", kind, missing)
				if err == nil {
					batch, batchCast, batchErr := normalizeTMDBCreditBatch(raws, kind, missing)
					if batchErr != nil {
						j.c.Error = "MDBList batch returned an unexpected or duplicate media identity; no titles saved"
					} else {
						for id, r := range batch {
							resolved[id], cast[id] = r, batchCast[id]
						}
					}
				}
			}
			for _, i := range selected[kind] {
				c := j.c.Credits[i]
				r, ok := resolved[c.ID]
				if !ok {
					j.c.Credits[i].deferRetry(time.Now().UTC())
					j.c.Error = "MDBList title metadata unavailable for some verified TMDb cast identities; retry continuation retained"
					continue
				}
				source := "https://www.themoviedb.org/person/" + j.p.TMDbID + "?credit_department=Acting"
				// The exact supplemental cast and silent baseline share the title transaction.
				if _, e = a.saveReleaseCredit(ctx, r, cast[c.ID], "mdblist:cast", j.p.TMDbID, source); e != nil {
					return 0, e
				}
				j.c.Credits[i].Resolved = true
				j.c.Credits[i].Attempts++
				j.c.Credits[i].Attempted = time.Now().UTC()
				j.c.Credits[i].NextRetry = time.Time{}
				j.c.Credits[i].Error = ""
			}
		}
		j.c.Complete = true
		for _, c := range j.c.Credits {
			if !c.Resolved {
				j.c.Complete = false
			}
		}
		if e = persist(); e != nil {
			return 0, e
		}
	}
	var pending int
	e = a.db.QueryRow(ctx, `SELECT count(*) FROM actresses p LEFT JOIN app_state c ON c.key='tmdb:actor:'||p.tmdb_id WHERE ($1::bigint[] IS NULL OR p.id=ANY($1::bigint[])) AND COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%' AND COALESCE(c.value->>'complete','false')<>'true'`, watchIDs).Scan(&pending)
	return pending, e
}

// normalizeTMDBCreditBatch prepares the complete batch before persistence.
func normalizeTMDBCreditBatch(raws []json.RawMessage, kind string, requested []string) (map[string]Release, map[string][]string, error) {
	releases := map[string]Release{}
	cast := map[string][]string{}
	wanted := make(map[string]bool, len(requested))
	for _, id := range requested {
		wanted[id] = true
	}
	seen := make(map[string]bool, len(raws))
	identities := make([]string, 0, len(raws))
	// Validate the raw identity envelope for every row before normalization or
	// persistence. This is independent defense even if the client is replaced.
	for _, raw := range raws {
		var identity struct {
			IDs struct {
				TMDb int64 `json:"tmdb"`
			} `json:"ids"`
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &identity) != nil {
			return nil, nil, errors.New("invalid MDBList batch identity")
		}
		id := strconv.FormatInt(identity.IDs.TMDb, 10)
		if identity.Type != kind || !wanted[id] || seen[id] {
			return nil, nil, errors.New("unexpected or duplicate MDBList batch identity")
		}
		seen[id] = true
		identities = append(identities, id)
	}
	for i, raw := range raws {
		r, people, err := normalizeRelease(raw, kind)
		if err != nil {
			continue
		}
		if r.TMDbID != identities[i] || r.Type != kind {
			return nil, nil, errors.New("normalized MDBList identity conflicts with batch envelope")
		}
		r.MetadataFetchedAt = time.Now().UTC()
		releases[r.TMDbID], cast[r.TMDbID] = r, people
	}
	return releases, cast, nil
}

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
