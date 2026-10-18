package app

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/http"
)

type CastPerson struct {
	TMDbID    string `json:"tmdb_id"`
	Name      string `json:"name"`
	Character string `json:"character"`
	Profile   string `json:"profile"`
}

func castDetails(m map[string]any) []CastPerson {
	out := []CastPerson{}
	seen := map[string]bool{}
	for _, set := range []any{m["cast"], object(m["credits"])["cast"], m["matched_people"], m["matched_persons"], m["favorite_people"]} {
		for _, value := range array(set) {
			p := object(value)
			id := first(p, "tmdb_id", "tmdb", "person_id", "id")
			if id == "" {
				id = first(object(p["ids"]), "tmdb")
			}
			if !numericID.MatchString(id) || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, CastPerson{TMDbID: id, Name: first(p, "name"), Character: first(p, "character", "role"), Profile: first(p, "profile", "profile_path", "avatar")})
		}
	}
	return out
}
func (a *App) releaseCast(w http.ResponseWriter, req *http.Request) {
	id, ok := pathID(w, req)
	if !ok {
		return
	}
	r, err := a.getRelease(req.Context(), id)
	if err != nil {
		resultError(w, err, "release")
		return
	}
	rows, err := a.db.Query(req.Context(), `SELECT DISTINCT ON (rc.tmdb_id) rc.tmdb_id,COALESCE(NULLIF(rc.data->>'name',''),p.data->>'name',''),COALESCE(rc.data->>'character',''),COALESCE(NULLIF(rc.data->>'profile',''),p.data->>'profile','') FROM release_cast rc LEFT JOIN actresses p ON p.tmdb_id=rc.tmdb_id AND COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%' WHERE rc.release_id=$1 ORDER BY rc.tmdb_id,(NULLIF(rc.data->>'name','') IS NOT NULL) DESC,rc.source`, id)
	if err != nil {
		fail(w, 500, "cannot load cast")
		return
	}
	defer rows.Close()
	out := []CastPerson{}
	for rows.Next() {
		var p CastPerson
		if err = rows.Scan(&p.TMDbID, &p.Name, &p.Character, &p.Profile); err != nil {
			fail(w, 500, "cannot load cast")
			return
		}
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		fail(w, 500, "cannot load cast")
		return
	}
	respond(w, 200, map[string]any{"items": out, "coverage": r.Coverage})
}
func (a *App) personDetail(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	if !numericID.MatchString(id) {
		fail(w, 400, "invalid literal person ID")
		return
	}
	ctx := req.Context()
	p := Actress{TMDbID: id}
	var b []byte
	var localID int64
	err := a.db.QueryRow(ctx, `SELECT id,data FROM actresses WHERE tmdb_id=$1`, id).Scan(&localID, &b)
	if err == nil {
		if err = json.Unmarshal(b, &p); err != nil {
			fail(w, 500, "cannot load person")
			return
		}
		p.ID = localID
		p.TMDbID = id
	} else if err != pgx.ErrNoRows {
		fail(w, 500, "cannot load person")
		return
	} else {
		var name, profile string
		err = a.db.QueryRow(ctx, `SELECT COALESCE(data->>'name',''),COALESCE(data->>'profile','') FROM release_cast WHERE tmdb_id=$1 ORDER BY (NULLIF(data->>'name','') IS NOT NULL) DESC LIMIT 1`, id).Scan(&name, &profile)
		if err != nil && err != pgx.ErrNoRows {
			fail(w, 500, "cannot load person")
			return
		}
		p.Name = name
		p.Profile = profile
	}
	s, err := a.settings(ctx)
	if err != nil {
		fail(w, 500, "cannot read settings")
		return
	}
	p = a.enrichPerson(ctx, s, p)
	if localID != 0 {
		if p, err = a.savePersonMetadata(ctx, p); err != nil {
			fail(w, 500, "cannot save person metadata")
			return
		}
	}
	releases, err := a.releasesForPerson(ctx, id)
	if err != nil {
		fail(w, 500, "cannot load person releases")
		return
	}
	respond(w, 200, map[string]any{"person": p, "releases": releases})
}
func (a *App) releasesForPerson(ctx context.Context, id string) ([]Release, error) {
	rows, err := a.db.Query(ctx, releaseSelect+` WHERE EXISTS(SELECT 1 FROM release_cast rc WHERE rc.release_id=r.id AND rc.tmdb_id=$1) ORDER BY r.id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Release{}
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
