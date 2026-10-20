package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

func (a *App) saveActress(ctx context.Context, p Actress) (Actress, error) {
	tx, e := a.db.Begin(ctx)
	if e != nil {
		return p, e
	}
	defer tx.Rollback(ctx)
	var before Actress
	var prior []byte
	lookup := tx.QueryRow(ctx, `SELECT data FROM actresses WHERE tmdb_id=$1 FOR UPDATE`, p.TMDbID).Scan(&prior)
	if lookup != nil && lookup != pgx.ErrNoRows {
		return p, lookup
	}
	if lookup == nil {
		if e = json.Unmarshal(prior, &before); e != nil {
			return p, e
		}
	}
	p.UpdatedAt = time.Now().UTC()
	b, e := json.Marshal(p)
	if e != nil {
		return p, e
	}
	e = tx.QueryRow(ctx, `INSERT INTO actresses(tmdb_id,data) VALUES($1,$2) ON CONFLICT(tmdb_id) DO UPDATE SET data=excluded.data,updated_at=now() RETURNING id`, p.TMDbID, b).Scan(&p.ID)
	if e != nil {
		return p, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO release_people(release_id,actress_id,source) SELECT release_id,$1,source FROM release_cast WHERE tmdb_id=$2 AND $3 NOT LIKE 'unavailable: identity mismatch%' ON CONFLICT DO NOTHING`, p.ID, p.TMDbID, p.MetadataStatus)
	if e != nil {
		return p, e
	}
	if e = baselineActorActivation(ctx, tx, before, p); e != nil {
		return p, e
	}
	if _, e = tx.Exec(ctx, recomputeMonitoring); e != nil {
		return p, e
	}
	return p, tx.Commit(ctx)
}

// Background enrichment writes metadata only; never stale monitoring preferences.
func (a *App) savePersonMetadata(ctx context.Context, p Actress) (Actress, error) {
	metadata := map[string]any{"profile": p.Profile, "biography": p.Biography, "birthday": p.Birthday, "place_of_birth": p.PlaceOfBirth, "metadata_status": p.MetadataStatus}
	for k, v := range metadata {
		if v == "" {
			delete(metadata, k)
		}
	}
	b, e := json.Marshal(metadata)
	if e != nil {
		return p, e
	}
	tx, e := a.db.Begin(ctx)
	if e != nil {
		return p, e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `UPDATE actresses SET data=data||$2::jsonb,updated_at=now() WHERE id=$1`, p.ID, b)
	if e != nil {
		return p, e
	}
	if tag.RowsAffected() != 1 {
		return p, pgx.ErrNoRows
	}
	if strings.HasPrefix(p.MetadataStatus, "unavailable: identity mismatch") {
		if _, e = tx.Exec(ctx, `DELETE FROM release_people WHERE actress_id=$1`, p.ID); e != nil {
			return p, e
		}
	}
	if _, e = tx.Exec(ctx, recomputeMonitoring); e != nil {
		return p, e
	}
	if e = tx.Commit(ctx); e != nil {
		return p, e
	}
	return a.getActress(ctx, p.ID)
}
func (a *App) getActress(ctx context.Context, id int64) (Actress, error) {
	var p Actress
	var b []byte
	e := a.db.QueryRow(ctx, `SELECT data,id,updated_at FROM actresses WHERE id=$1`, id).Scan(&b, &p.ID, &p.UpdatedAt)
	if e != nil {
		return p, e
	}
	id = p.ID
	updated := p.UpdatedAt
	e = json.Unmarshal(b, &p)
	p.ID = id
	p.UpdatedAt = updated
	if e != nil {
		return p, e
	}
	if strings.HasPrefix(p.MetadataStatus, "unavailable: identity mismatch") {
		p.ReleaseCount = 0
		p.UpcomingCount = 0
		return p, nil
	}
	e = a.db.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE `+upcomingReleasePredicate+`) FROM release_people rp JOIN releases r ON r.id=rp.release_id WHERE rp.actress_id=$1`, id).Scan(&p.ReleaseCount, &p.UpcomingCount)
	return p, e
}

const upcomingReleasePredicate = `nullif(r.data->>'release_date','')::date > (now() AT TIME ZONE 'UTC')::date`

func (a *App) actresses(ctx context.Context, q, group string, upcomingOnly ...bool) ([]Actress, error) {
	upcoming := len(upcomingOnly) > 0 && upcomingOnly[0]
	rows, e := a.db.Query(ctx, `SELECT p.id FROM actresses p WHERE ($1='' OR p.data->>'name' ILIKE '%'||$1||'%') AND ($2='' OR p.data->>'group'=$2) AND (NOT $3::boolean OR (COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%' AND EXISTS(SELECT 1 FROM release_people rp JOIN releases r ON r.id=rp.release_id WHERE rp.actress_id=p.id AND `+upcomingReleasePredicate+`))) ORDER BY lower(p.data->>'name'),p.id`, q, group, upcoming)
	if e != nil {
		return nil, e
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []Actress{}
	for _, id := range ids {
		p, e := a.getActress(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, nil
}
func (a *App) saveRelease(ctx context.Context, r Release, people []string) (Release, error) {
	return a.saveReleaseSource(ctx, r, people, "mdblist:cast")
}
func (a *App) saveReleaseSource(ctx context.Context, r Release, people []string, source string) (Release, error) {
	return a.saveReleaseCredit(ctx, r, people, source, "", "")
}

// saveReleaseCredit reconciles metadata and cast under the existing identity lock.
// Supplemental discovery must commit its silent baseline with the title itself.
func (a *App) saveReleaseCredit(ctx context.Context, r Release, people []string, source, creditPerson, creditSource string) (Release, error) {
	tx, e := a.db.Begin(ctx)
	if e != nil {
		return r, e
	}
	defer tx.Rollback(ctx)
	// Serialize canonical identity reconciliation across discovery workers.
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(486722120)`); e != nil {
		return r, e
	}
	if creditPerson != "" {
		r.SourceLinks = append(r.SourceLinks, creditSource)
		r.Coverage = coverageWithSourceWarnings(r.Coverage, "MDBList title metadata; verified TMDb person combined cast credits (not crew); bounded discovery, no complete filmography claimed.")
	}
	canonical := r.Type + ":imdb:" + r.IMDbID
	if r.TMDbID != "" {
		canonical = r.Type + ":tmdb:" + r.TMDbID
	}
	b, e := json.Marshal(r)
	if e != nil {
		return r, e
	}
	var id int64
	var previous []byte
	e = tx.QueryRow(ctx, `SELECT id,data FROM releases WHERE media_type=$1 AND (($2<>'' AND tmdb_id=$2) OR ($3<>'' AND imdb_id=$3)) LIMIT 1 FOR UPDATE`, r.Type, r.TMDbID, r.IMDbID).Scan(&id, &previous)
	created := e == pgx.ErrNoRows
	if created {
		e = tx.QueryRow(ctx, `INSERT INTO releases(canonical_key,media_type,tmdb_id,imdb_id,data) VALUES($1,$2,$3,$4,$5) RETURNING id`, canonical, r.Type, r.TMDbID, r.IMDbID, b).Scan(&id)
	} else if e == nil {
		var old Release
		if e = json.Unmarshal(previous, &old); e != nil {
			return r, e
		}
		// A shared secondary ID cannot authorize replacing a different native ID.
		if (old.TMDbID != "" && r.TMDbID != "" && old.TMDbID != r.TMDbID) || (old.IMDbID != "" && r.IMDbID != "" && old.IMDbID != r.IMDbID) {
			return r, fmt.Errorf("metadata identity conflicts with saved release")
		}
		r = mergeRelease(old, r)
		b, e = json.Marshal(r)
		if e != nil {
			return r, e
		}
		_, e = tx.Exec(ctx, `UPDATE releases SET data=$2,tmdb_id=CASE WHEN $3='' THEN tmdb_id ELSE $3 END,imdb_id=CASE WHEN $4='' THEN imdb_id ELSE $4 END,updated_at=now() WHERE id=$1`, id, b, r.TMDbID, r.IMDbID)
	}
	if e != nil {
		return r, e
	}
	for _, person := range people {
		if !numericID.MatchString(person) {
			continue
		}
		detail := map[string]string{}
		for _, p := range r.Cast {
			if p.TMDbID == person {
				if p.Name != "" {
					detail["name"] = p.Name
				}
				if p.Character != "" {
					detail["character"] = p.Character
				}
				if p.Profile != "" {
					detail["profile"] = p.Profile
				}
				break
			}
		}
		data, _ := json.Marshal(detail)
		_, e = tx.Exec(ctx, `INSERT INTO release_cast(release_id,tmdb_id,source,data) VALUES($1,$2,$3,$4) ON CONFLICT(release_id,tmdb_id) DO UPDATE SET data=release_cast.data||excluded.data`, id, person, source, data)
		if e != nil {
			return r, e
		}
	}
	if creditPerson != "" {
		if _, e = tx.Exec(ctx, `INSERT INTO release_cast(release_id,tmdb_id,source,data) VALUES($1,$2,$3,'{}') ON CONFLICT DO NOTHING`, id, creditPerson, "tmdb:person_cast:"+creditSource); e != nil {
			return r, e
		}
		// Existing primary titles retain their eligibility; future material events
		// must not be swallowed by supplemental discovery of an upcoming title.
		if created {
			if _, e = tx.Exec(ctx, `INSERT INTO actions(release_id,kind,status,event_key) SELECT $1,k,'baseline',$2 FROM unnest(ARRAY['acquire','notify']) k ON CONFLICT DO NOTHING`, id, releaseEvent(r, time.Now().UTC())); e != nil {
				return r, e
			}
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO release_people(release_id,actress_id,source) SELECT rc.release_id,p.id,rc.source FROM release_cast rc JOIN actresses p ON p.tmdb_id=rc.tmdb_id WHERE rc.release_id=$1 AND COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%' ON CONFLICT DO NOTHING`, id)
	if e != nil {
		return r, e
	}
	if _, e = tx.Exec(ctx, recomputeMonitoring+` WHERE r.id=$1`, id); e != nil {
		return r, e
	}
	if e = tx.Commit(ctx); e != nil {
		return r, e
	}
	return a.getRelease(ctx, id)
}

const releaseSelect = `SELECT r.id,r.data,r.monitored,r.acquisition,r.created_at,r.updated_at,COALESCE((SELECT jsonb_agg(jsonb_build_object('id',p.id,'name',p.data->>'name','group',p.data->>'group','profile',p.data->>'profile') ORDER BY p.data->>'name') FROM release_people rp JOIN actresses p ON p.id=rp.actress_id WHERE rp.release_id=r.id AND COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%'),'[]'::jsonb),COALESCE((SELECT jsonb_agg(jsonb_build_object('id',l.id,'name',l.name,'enabled',l.enabled,'monitored',l.monitored,'auto_add',l.auto_add) ORDER BY l.id) FROM release_lists rl JOIN mdblist_lists l ON l.id=rl.list_id WHERE rl.release_id=r.id),'[]'::jsonb),CASE WHEN r.monitoring_override IS NOT NULL THEN 'manual' WHEN ` + listMonitorGate + ` THEN 'list' WHEN ` + actorMonitorGate + ` THEN 'actress' ELSE 'discovery' END FROM releases r `

func scanRelease(row pgx.Row) (Release, error) {
	var r Release
	var b, ac, ps, lists []byte
	var origin string
	var id int64
	var mon bool
	var created, updated time.Time
	e := row.Scan(&id, &b, &mon, &ac, &created, &updated, &ps, &lists, &origin)
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return r, e
	}
	if e = json.Unmarshal(ac, &r.Acquisition); e != nil {
		return r, e
	}
	if e = json.Unmarshal(ps, &r.Actresses); e != nil {
		return r, e
	}
	if r.Genres == nil {
		r.Genres = []string{}
	}
	if r.Ratings == nil {
		r.Ratings = []map[string]any{}
	}
	if r.WatchProviders == nil {
		r.WatchProviders = []map[string]any{}
	}
	if r.Actresses == nil {
		r.Actresses = []PersonLink{}
	}
	if r.SourceLinks == nil {
		r.SourceLinks = []string{}
	}
	if r.ContentInfo == nil {
		r.ContentInfo = map[string]any{}
	}
	if r.Score != nil && *r.Score < 0 {
		r.Score = nil
	}
	if r.AgeRating != nil && *r.AgeRating < 0 {
		r.AgeRating = nil
	}
	r.ID = id
	if r.LocalAvailability == nil {
		r.LocalAvailability = map[string]any{"status": "unknown", "url": "", "checked_at": ""}
	}
	if e = json.Unmarshal(lists, &r.Lists); e != nil {
		return r, e
	}
	r.MonitoringOrigin = origin
	r.Monitored = mon
	r.CreatedAt = created
	r.UpdatedAt = updated
	return r, nil
}
func (a *App) getRelease(ctx context.Context, id int64) (Release, error) {
	return scanRelease(a.db.QueryRow(ctx, releaseSelect+`WHERE r.id=$1`, id))
}

type releaseFilter struct {
	Query, Window, Type, Group, Sort, ListID string
	ActressID                                int64
	Page, Limit                              int
}
type releasePage struct {
	Items []Release `json:"items"`
	Total int       `json:"total"`
	Page  int       `json:"page"`
	Limit int       `json:"limit"`
}

func (a *App) releases(ctx context.Context, f releaseFilter) (releasePage, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Limit < 1 {
		f.Limit = 36
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	out := releasePage{Items: []Release{}, Page: f.Page, Limit: f.Limit}
	where := `WHERE ($1='' OR r.data->>'title' ILIKE '%'||$1||'%') AND ($2='' OR $2='all' OR r.media_type=$2) AND ($3='' OR EXISTS(SELECT 1 FROM release_people rp JOIN actresses p ON p.id=rp.actress_id WHERE rp.release_id=r.id AND p.data->>'group'=$3 AND COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%')) AND ($4::bigint=0 OR EXISTS(SELECT 1 FROM release_people rp JOIN actresses p ON p.id=rp.actress_id WHERE rp.release_id=r.id AND rp.actress_id=$4 AND COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%'))`
	switch f.Window {
	case "current":
		where += ` AND nullif(r.data->>'release_date','')::date BETWEEN CURRENT_DATE-30 AND CURRENT_DATE`
	case "past":
		where += ` AND nullif(r.data->>'release_date','')::date<CURRENT_DATE-30`
	case "upcoming":
		where += ` AND ` + upcomingReleasePredicate
	}
	where += ` AND ($5='' OR EXISTS(SELECT 1 FROM release_lists rl WHERE rl.release_id=r.id AND rl.list_id=$5))`
	args := []any{f.Query, f.Type, f.Group, f.ActressID, f.ListID}
	e := a.db.QueryRow(ctx, `SELECT count(*) FROM releases r `+where, args...).Scan(&out.Total)
	if e != nil {
		return out, e
	}
	order := `nullif(r.data->>'release_date','') DESC NULLS LAST,r.id DESC`
	if f.Window == "upcoming" {
		order = `nullif(r.data->>'release_date','') ASC NULLS LAST,r.id DESC`
	}
	switch f.Sort {
	case "title":
		order = `lower(r.data->>'title'),r.id`
	case "score":
		order = `nullif(r.data->>'score','')::numeric DESC NULLS LAST,r.id DESC`
	case "added":
		order = `r.created_at DESC,r.id DESC`
	}
	args = append(args, f.Limit, (f.Page-1)*f.Limit)
	rows, e := a.db.Query(ctx, releaseSelect+where+` ORDER BY `+order+` LIMIT $6 OFFSET $7`, args...)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		r, e := scanRelease(rows)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, r)
	}
	return out, rows.Err()
}
func (a *App) allMatched(ctx context.Context, id int64) ([]Release, error) {
	out := []Release{}
	for p := 1; ; p++ {
		page, e := a.releases(ctx, releaseFilter{ActressID: id, Page: p, Limit: 500})
		if e != nil {
			return nil, e
		}
		out = append(out, page.Items...)
		if len(out) >= page.Total {
			return out, nil
		}
	}
}
func (a *App) activities(ctx context.Context, limit, offset int) ([]Activity, int, error) {
	out := []Activity{}
	var total int
	e := a.db.QueryRow(ctx, `SELECT count(*) FROM activity`).Scan(&total)
	if e != nil {
		return out, 0, e
	}
	rows, e := a.db.Query(ctx, `SELECT id,kind,message,status,created_at FROM activity ORDER BY id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if e != nil {
		return out, 0, e
	}
	defer rows.Close()
	for rows.Next() {
		var x Activity
		if e = rows.Scan(&x.ID, &x.Kind, &x.Message, &x.Status, &x.CreatedAt); e != nil {
			return out, 0, e
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}
func listIDs(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })
}
func safeError(e error) string {
	if e == nil {
		return ""
	}
	if e == context.Canceled || e == context.DeadlineExceeded {
		return e.Error()
	}
	return fmt.Sprintf("operation failed (%T); check integration configuration and activity", e)
}
