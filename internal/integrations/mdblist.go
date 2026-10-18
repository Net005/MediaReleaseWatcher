package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var positiveID = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)
var imdbID = regexp.MustCompile(`^tt[0-9]{7,12}$`)

func validID(s string) bool {
	if !positiveID.MatchString(s) {
		return false
	}
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}
func mediaType(s string) bool { return s == "movie" || s == "show" }

// mdbPath is an allowlist of the read-only discovery routes used by the app.
// Do not accept arbitrary relative URLs, encoded segments, or account mutations.
func mdbPath(path string) bool {
	if strings.ContainsAny(path, "%?#\\\r\n") || !strings.HasPrefix(path, "/") {
		return false
	}
	switch path {
	case "/user", "/people/favorites/upcoming", "/catalog/movie", "/catalog/show", "/search/movie", "/search/show", "/search/any":
		return true
	}
	p := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(p) == 2 && p[0] == "people" {
		return validID(p[1])
	}
	if len(p) == 3 && p[0] == "lists" && p[2] == "items" {
		return validID(p[1])
	}
	if len(p) == 3 && mediaType(p[1]) {
		return (p[0] == "tmdb" && validID(p[2])) || (p[0] == "imdb" && imdbID.MatchString(p[2]))
	}
	return false
}

func (c *Client) mdbTarget(path string, q url.Values) (string, error) {
	if strings.TrimSpace(c.cfg.MDBListAPIKey) == "" {
		return "", errors.New("MDBList API key is not configured")
	}
	cloned := make(url.Values, len(q)+1)
	for k, v := range q {
		cloned[k] = append([]string(nil), v...)
	}
	// The official schema defines API keys in the query, not an invented header.
	cloned.Set("apikey", c.cfg.MDBListAPIKey)
	if len(cloned.Encode()) > 16<<10 {
		return "", errors.New("MDBList query exceeds size limit")
	}
	return endpoint(c.cfg.MDBListBaseURL, path, cloned)
}

func (c *Client) MDBGet(ctx context.Context, path string, q url.Values) (json.RawMessage, error) {
	if !mdbPath(path) {
		return nil, errors.New("unsupported MDBList read path or invalid ID")
	}
	target, err := c.mdbTarget(path, q)
	if err != nil {
		return nil, err
	}
	return c.request(ctx, "MDBList", http.MethodGet, target, "", "", nil)
}

// MDBBatch uses the documented read-only batch lookup. Only numeric TMDb IDs
// are accepted: the live schema declares integer ids, so IMDb string batches
// are deliberately not guessed. Use MDBGet for IMDb lookup instead.
func (c *Client) MDBBatch(ctx context.Context, provider, kind string, ids []string) ([]json.RawMessage, error) {
	if provider != "tmdb" || !mediaType(kind) {
		return nil, errors.New("MDBList batch supports tmdb movie or show only")
	}
	if len(ids) == 0 || len(ids) > 100 {
		return nil, errors.New("MDBList batch requires between 1 and 100 IDs")
	}
	numbers := make([]int64, len(ids))
	for i, id := range ids {
		if !validID(id) {
			return nil, errors.New("invalid MDBList batch ID")
		}
		numbers[i], _ = strconv.ParseInt(id, 10, 64)
	}
	target, err := c.mdbTarget("/"+provider+"/"+kind, nil)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(struct {
		IDs    []int64  `json:"ids"`
		Append []string `json:"append_to_response"`
	}{numbers, []string{"keyword"}})
	raw, err := c.request(ctx, "MDBList", http.MethodPost, target, "", "application/json", body)
	if err != nil {
		return nil, err
	}
	var rows []json.RawMessage
	if err := decode(raw, &rows, "MDBList"); err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []json.RawMessage{}
	}
	// Response ordering is not guaranteed. Check identities against the submitted
	// set, never zip rows with input IDs. Missing IDs are permitted (not found).
	wanted := make(map[int64]bool, len(numbers))
	for _, id := range numbers {
		wanted[id] = true
	}
	seen := make(map[int64]bool, len(rows))
	for _, row := range rows {
		var identity struct {
			IDs struct {
				TMDb int64 `json:"tmdb"`
			} `json:"ids"`
			Type string `json:"type"`
		}
		if json.Unmarshal(row, &identity) != nil || identity.Type != kind || !wanted[identity.IDs.TMDb] || seen[identity.IDs.TMDb] {
			return nil, errors.New("MDBList batch returned an unexpected or duplicate media identity")
		}
		seen[identity.IDs.TMDb] = true
	}
	return rows, nil
}
