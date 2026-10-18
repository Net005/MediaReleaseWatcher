package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type PersonLink struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Group   string `json:"group"`
	Profile string `json:"profile"`
}
type Release struct {
	ID                int64             `json:"id"`
	Title             string            `json:"title"`
	Type              string            `json:"type"`
	Year              int               `json:"year"`
	IMDbID            string            `json:"imdb_id"`
	TMDbID            string            `json:"tmdb_id"`
	TVDbID            string            `json:"tvdb_id"`
	MDBListID         string            `json:"mdblist_id"`
	Poster            string            `json:"poster"`
	Backdrop          string            `json:"backdrop"`
	Description       string            `json:"description"`
	Country           string            `json:"country"`
	Genres            []string          `json:"genres"`
	Runtime           int               `json:"runtime"`
	ReleaseDate       string            `json:"release_date"`
	DigitalDate       string            `json:"digital_date"`
	Status            string            `json:"status"`
	Certification     string            `json:"certification"`
	AgeRating         *int              `json:"age_rating"`
	Score             *float64          `json:"score"`
	Ratings           []map[string]any  `json:"ratings"`
	ContentInfo       map[string]any    `json:"content_info"`
	WatchProviders    []map[string]any  `json:"watch_providers"`
	NextEpisode       map[string]any    `json:"next_episode"`
	Actresses         []PersonLink      `json:"actresses"`
	Monitored         bool              `json:"monitored"`
	Acquisition       map[string]string `json:"acquisition"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
	MetadataFetchedAt time.Time         `json:"metadata_fetched_at"`
	Raw               json.RawMessage   `json:"raw"`
	Source            string            `json:"source"`
	Coverage          string            `json:"coverage"`
	SourceLinks       []string          `json:"source_links"`
}
type Actress struct {
	ID             int64     `json:"id"`
	TMDbID         string    `json:"tmdb_id"`
	Name           string    `json:"name"`
	Group          string    `json:"group"`
	Profile        string    `json:"profile"`
	Biography      string    `json:"biography"`
	Birthday       string    `json:"birthday"`
	PlaceOfBirth   string    `json:"place_of_birth"`
	Monitored      bool      `json:"monitored"`
	AutoAdd        bool      `json:"auto_add"`
	Notify         bool      `json:"notify"`
	ReleaseCount   int       `json:"release_count"`
	UpcomingCount  int       `json:"upcoming_count"`
	MetadataStatus string    `json:"metadata_status"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type Activity struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func decodeObject(raw []byte) (map[string]any, error) {
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if e := d.Decode(&m); e != nil {
		return nil, e
	}
	if m == nil {
		return nil, errors.New("expected metadata object")
	}
	return m, nil
}
func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	}
	return ""
}
func first(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(m[k]); s != "" {
			return s
		}
	}
	return ""
}
func integer(v any) int           { n, _ := strconv.Atoi(str(v)); return n }
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func array(v any) []any           { a, _ := v.([]any); return a }
func objects(v any) []map[string]any {
	out := []map[string]any{}
	for _, x := range array(v) {
		if m := object(x); m != nil {
			out = append(out, m)
		}
	}
	return out
}
func dateOnly(s string) string {
	if len(s) >= 10 {
		if _, e := time.Parse("2006-01-02", s[:10]); e == nil {
			return s[:10]
		}
	}
	return ""
}
func releaseWindow(s string, now time.Time) string {
	d, e := time.Parse("2006-01-02", s)
	if e != nil {
		return "unknown"
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if d.After(today) {
		return "upcoming"
	}
	if d.Before(today.AddDate(0, 0, -30)) {
		return "past"
	}
	return "current"
}

const nonIMDbPlaceholderWarning = "Source non-IMDb placeholder preserved in raw metadata; IMDb identity unavailable; independent TMDb identity used."

func coverageWithSourceWarnings(current, replacement string) string {
	if strings.Contains(current, nonIMDbPlaceholderWarning) && !strings.Contains(replacement, nonIMDbPlaceholderWarning) {
		return replacement + " " + nonIMDbPlaceholderWarning
	}
	return replacement
}

func normalizeRelease(raw json.RawMessage, kind string) (Release, []string, error) {
	expected := kind
	if expected == "tv" {
		expected = "show"
	}
	m, e := decodeObject(raw)
	if e != nil {
		return Release{}, nil, e
	}
	if v := object(m["movie"]); v != nil {
		m = v
		kind = "movie"
	}
	if v := object(m["show"]); v != nil {
		m = v
		kind = "show"
	}
	if kind == "" || kind == "any" {
		kind = first(m, "type", "mediatype", "media_type")
	}
	declared := first(m, "type", "mediatype", "media_type")
	if declared == "tv" {
		declared = "show"
	}
	if kind == "tv" {
		kind = "show"
	}
	if declared != "" && declared != kind {
		return Release{}, nil, errors.New("metadata media type does not match request")
	}
	if kind == "tv" {
		kind = "show"
	}
	if oneOf(expected, "movie", "show") && expected != kind {
		return Release{}, nil, errors.New("metadata media type does not match request")
	}
	if kind != "movie" && kind != "show" {
		return Release{}, nil, errors.New("metadata has no supported media type")
	}
	ids := object(m["ids"])
	r := Release{Title: first(m, "title", "name"), Type: kind, Year: integer(m["year"]), IMDbID: first(m, "imdb_id", "imdbid"), TMDbID: first(m, "tmdb_id", "tmdbid", "tmdb"), TVDbID: first(m, "tvdb_id", "tvdbid"), MDBListID: first(m, "mdblist_id"), Poster: first(m, "poster", "poster_url"), Backdrop: first(m, "backdrop", "backdrop_url"), Description: first(m, "description", "overview"), Country: first(m, "country"), Runtime: integer(m["runtime"]), ReleaseDate: dateOnly(first(m, "released", "release_date", "first_air_date")), DigitalDate: dateOnly(first(m, "released_digital", "digital_date")), Status: first(m, "status"), Certification: first(m, "certification"), Genres: []string{}, Ratings: objects(m["ratings"]), ContentInfo: object(m["content_info"]), WatchProviders: objects(m["watch_providers"]), NextEpisode: object(m["next_episode"]), Actresses: []PersonLink{}, Acquisition: map[string]string{"sonarr": "", "radarr": ""}, Raw: raw, Source: "mdblist", Coverage: "No cast supplied by MDBList; person biographies are not filmographies.", SourceLinks: []string{}}
	if r.Year == 0 {
		r.Year = integer(m["release_year"])
	}
	if r.IMDbID == "" {
		r.IMDbID = first(ids, "imdb")
	}
	if r.TMDbID == "" {
		r.TMDbID = first(ids, "tmdb")
	}
	if r.TVDbID == "" {
		r.TVDbID = first(ids, "tvdb")
	}
	if r.MDBListID == "" {
		r.MDBListID = first(ids, "mdblist")
	}
	if r.TMDbID == "" {
		r.TMDbID = first(m, "id")
	}
	identifierWarning := ""
	// Some MDBList catalogue records use a non-IMDb tr token in the IMDb
	// field. Quarantine that token only when a separate valid TMDb identity
	// exists. Preserve the literal source value in Raw; never rewrite it.
	if strings.HasPrefix(r.IMDbID, "tr") && numericID.MatchString(strings.TrimPrefix(r.IMDbID, "tr")) && numericID.MatchString(r.TMDbID) {
		identifierWarning = nonIMDbPlaceholderWarning
		r.IMDbID = ""
	}
	if (r.TMDbID != "" && !numericID.MatchString(r.TMDbID)) || (r.TVDbID != "" && !numericID.MatchString(r.TVDbID)) || (r.IMDbID != "" && !imdbID.MatchString(r.IMDbID)) {
		return Release{}, nil, errors.New("metadata has invalid literal identifiers")
	}
	if r.Title == "" || (r.TMDbID == "" && r.IMDbID == "") {
		return Release{}, nil, errors.New("metadata lacks title or canonical identifier")
	}
	if r.ContentInfo == nil {
		r.ContentInfo = map[string]any{}
	}
	for _, k := range []string{"parental_guide", "content_rating", "commonsense", "commonsense_media"} {
		if v, ok := m[k]; ok {
			r.ContentInfo[k] = v
		}
	}
	if len(r.WatchProviders) == 0 {
		r.WatchProviders = objects(m["watchproviders"])
	}
	if len(r.WatchProviders) == 0 {
		r.WatchProviders = objects(m["streams"])
	}
	if v, ok := m["score"]; ok && v != nil {
		if f, e := strconv.ParseFloat(str(v), 64); e == nil && f >= 0 && !math.IsNaN(f) && !math.IsInf(f, 0) {
			r.Score = &f
		}
	}
	if v, ok := m["age_rating"]; ok && v != nil {
		if n, e := strconv.Atoi(str(v)); e == nil && n >= 0 && n <= 21 {
			r.AgeRating = &n
		}
	}
	for _, v := range array(m["genres"]) {
		if x := str(v); x != "" {
			r.Genres = append(r.Genres, x)
		} else if x = first(object(v), "name", "title"); x != "" {
			r.Genres = append(r.Genres, x)
		}
	}
	people := castIDs(m)
	if len(people) > 0 {
		r.Coverage = "Matched only by explicit cast/person IDs supplied by MDBList."
	}
	if r.TMDbID != "" {
		t := "movie"
		if r.Type == "show" {
			t = "tv"
		}
		r.SourceLinks = append(r.SourceLinks, fmt.Sprintf("https://www.themoviedb.org/%s/%s", t, r.TMDbID))
	}
	if identifierWarning != "" {
		r.Coverage += " " + identifierWarning
	}
	return r, people, nil
}

func identityName(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}

// Only explicit cast and matched-person links are evidence. Crew, prose and names are never evidence.
func castIDs(m map[string]any) []string {
	seen := map[string]bool{}
	out := []string{}
	sets := []any{m["cast"], object(m["credits"])["cast"], m["matched_people"], m["matched_persons"], m["favorite_people"]}
	for _, v := range sets {
		for _, p := range array(v) {
			o := object(p)
			id := first(o, "tmdb_id", "tmdb", "person_id", "id")
			if id == "" {
				id = first(object(o["ids"]), "tmdb")
			}
			if numericID.MatchString(id) && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}
