package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MDBWatchlistMedia retains literal source identifiers; no title matching or ID repair.
type MDBWatchlistMedia struct{ Type, TMDbID, IMDbID string }

type MDBWatchlistResult struct {
	Status      string    `json:"status"`
	InWatchlist *bool     `json:"in_watchlist"`
	CheckedAt   time.Time `json:"checked_at"`
	Error       string    `json:"error,omitempty"`
	Verified    bool      `json:"verified,omitempty"`
}

func watchlistState(present bool) MDBWatchlistResult {
	status := "absent"
	if present {
		status = "present"
	}
	return MDBWatchlistResult{Status: status, InWatchlist: &present, CheckedAt: time.Now().UTC()}
}
func watchlistUnknown(err error) (MDBWatchlistResult, error) {
	return MDBWatchlistResult{Status: "unknown", CheckedAt: time.Now().UTC(), Error: err.Error()}, err
}
func validateWatchlistMedia(m MDBWatchlistMedia) error {
	if !mediaType(m.Type) || (m.TMDbID == "" && m.IMDbID == "") || (m.TMDbID != "" && !validID(m.TMDbID)) || (m.IMDbID != "" && !imdbID.MatchString(m.IMDbID)) {
		return errors.New("MDBList watchlist requires literal valid IDs and movie/show type")
	}
	return nil
}

// MDBWatchlistContains is a fresh account watchlist read, not static-list membership.
func (c *Client) MDBWatchlistContains(ctx context.Context, m MDBWatchlistMedia) (MDBWatchlistResult, error) {
	if err := validateWatchlistMedia(m); err != nil {
		return watchlistUnknown(err)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	q := url.Values{"mediatype": {m.Type}, "limit": {"1000"}}
	seen := map[string]bool{}
	found := false
	for page := 0; page < 100; page++ {
		raw, headers, err := c.watchlistRequest(ctx, http.MethodGet, "/watchlist/items", q, nil)
		if err != nil {
			return watchlistUnknown(err)
		}
		rows, next, err := watchlistPage(raw, headers, m.Type)
		if err != nil {
			return watchlistUnknown(err)
		}
		for _, row := range rows {
			match, err := watchlistMatch(row, m)
			if err != nil {
				return watchlistUnknown(err)
			}
			if match {
				if found {
					return watchlistUnknown(errors.New("MDBList watchlist duplicate target identity"))
				}
				found = true
			}
		}
		if next == "" {
			return watchlistState(found), nil
		}
		if seen[next] {
			return watchlistUnknown(errors.New("MDBList watchlist pagination repeats cursor"))
		}
		seen[next] = true
		q.Set("cursor", next)
	}
	return watchlistUnknown(errors.New("MDBList watchlist pagination budget exhausted"))
}

// MDBSetWatchlist is reserved for explicit user requests; discovery/jobs must not call it.
// A successful mutation response is not evidence: only a fresh exact-ID read verifies state.
func (c *Client) MDBSetWatchlist(ctx context.Context, m MDBWatchlistMedia, present bool) (MDBWatchlistResult, error) {
	if err := validateWatchlistMedia(m); err != nil {
		return watchlistUnknown(err)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	c.addMu.Lock()
	defer c.addMu.Unlock()
	before, err := c.MDBWatchlistContains(ctx, m)
	if err != nil {
		return before, err
	}
	if before.InWatchlist != nil && *before.InWatchlist == present {
		before.Verified = true
		return before, nil
	}
	ids := map[string]any{}
	if m.TMDbID != "" {
		ids["tmdb"] = json.Number(m.TMDbID)
	}
	if m.IMDbID != "" {
		ids["imdb"] = m.IMDbID
	}
	body, _ := json.Marshal(map[string]any{m.Type + "s": []any{map[string]any{"ids": ids}}})
	action := "remove"
	if present {
		action = "add"
	}
	_, _, err = c.watchlistRequest(ctx, http.MethodPost, "/watchlist/items/"+action, nil, body)
	if err != nil {
		return watchlistUnknown(err)
	}
	after, err := c.MDBWatchlistContains(ctx, m)
	if err != nil {
		return after, err
	}
	if after.InWatchlist == nil || *after.InWatchlist != present {
		err := errors.New("MDBList watchlist change could not be verified")
		after.Error = err.Error()
		return after, err
	}
	after.Verified = true
	return after, nil
}

var errWatchlistSchema = errors.New("MDBList watchlist returned an unexpected or ambiguous response")

// Own the narrow typed endpoints, rather than expanding MDBGet's read allowlist.
// Headers are necessary: a true X-Has-More without a usable cursor is not absence.
func (c *Client) watchlistRequest(ctx context.Context, method, path string, q url.Values, body []byte) (json.RawMessage, http.Header, error) {
	if !((method == http.MethodGet && path == "/watchlist/items") || (method == http.MethodPost && (path == "/watchlist/items/add" || path == "/watchlist/items/remove"))) {
		return nil, nil, errors.New("unsupported MDBList watchlist operation")
	}
	target, err := c.mdbTarget(path, q)
	if err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	until := c.cooldown["MDBList"]
	c.mu.Unlock()
	if delay := time.Until(until); delay > 0 {
		return nil, nil, &HTTPError{Service: "MDBList", StatusCode: 429, RetryAfter: delay}
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, nil, errors.New("MDBList watchlist request configuration invalid")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MediaReleaseWatcher")
	req.Header.Set("Cache-Control", "no-cache, no-store")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, errors.New("MDBList watchlist request canceled or timed out")
		}
		return nil, nil, errors.New("MDBList watchlist request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		e := &HTTPError{Service: "MDBList", StatusCode: res.StatusCode}
		if res.StatusCode == 429 {
			e.RetryAfter = retryAfter(res.Header.Get("Retry-After"))
			c.mu.Lock()
			c.cooldown["MDBList"] = time.Now().Add(e.RetryAfter)
			c.mu.Unlock()
		}
		return nil, nil, e
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return nil, nil, errors.New("MDBList watchlist response read failed")
	}
	if len(raw) > maxResponseBytes {
		return nil, nil, errors.New("MDBList watchlist response exceeds size limit")
	}
	if !json.Valid(raw) {
		return nil, nil, errors.New("MDBList watchlist returned invalid JSON")
	}
	return raw, res.Header, nil
}

// Reject duplicate keys rather than letting encoding/json silently select a last
// identity or pagination value. Bound nesting even within unused provider fields.
func watchlistJSONValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return errWatchlistSchema
	}
	token, err := d.Token()
	if err != nil {
		return errWatchlistSchema
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return errWatchlistSchema
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return errWatchlistSchema
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errWatchlistSchema
			}
			seen[name] = true
		}
		if err := watchlistJSONValue(d, depth+1); err != nil {
			return err
		}
	}
	_, err = d.Token()
	if err != nil {
		return errWatchlistSchema
	}
	return nil
}

type watchlistNumericID string

func (id *watchlistNumericID) UnmarshalJSON(raw []byte) error {
	if !validID(string(raw)) {
		return errWatchlistSchema
	}
	*id = watchlistNumericID(raw)
	return nil
}

func watchlistPage(raw json.RawMessage, headers http.Header, kind string) ([]json.RawMessage, string, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if watchlistJSONValue(d, 0) != nil {
		return nil, "", errWatchlistSchema
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, "", errWatchlistSchema
	}
	obj, err := watchlistExactKeys(raw, "movies", "shows", "seasons", "episodes", "pagination", "next_cursor", "has_more", "error", "errors", "status", "success")
	if err != nil {
		return nil, "", errWatchlistSchema
	}
	for _, key := range []string{"error", "errors", "status", "success"} {
		if _, ok := obj[key]; ok {
			// List-items schema has no status/success envelope. Do not turn a
			// mixed or failure envelope into evidence of an empty watchlist.
			return nil, "", errWatchlistSchema
		}
	}
	var rows []json.RawMessage
	if json.Unmarshal(obj[kind+"s"], &rows) != nil || rows == nil {
		return nil, "", errWatchlistSchema
	}
	for _, other := range []string{"movies", "shows", "seasons", "episodes"} {
		if other == kind+"s" {
			continue
		}
		if raw, ok := obj[other]; ok {
			var items []json.RawMessage
			if json.Unmarshal(raw, &items) != nil || items == nil || len(items) != 0 {
				return nil, "", errWatchlistSchema
			}
		}
	}
	next := ""
	hasNext := false
	more := false
	hasMore := false
	sources := []map[string]json.RawMessage{obj}
	if p, ok := obj["pagination"]; ok {
		pagination, err := watchlistExactKeys(p, "next_cursor", "has_more")
		if err != nil {
			return nil, "", errWatchlistSchema
		}
		sources = append(sources, pagination)
	}
	for _, src := range sources {
		if n, ok := src["next_cursor"]; ok {
			v := ""
			if string(n) != "null" && json.Unmarshal(n, &v) != nil {
				return nil, "", errWatchlistSchema
			}
			if len(v) > 4096 || strings.ContainsAny(v, "\r\n") || (hasNext && next != v) {
				return nil, "", errWatchlistSchema
			}
			hasNext = true
			next = v
		}
		if h, ok := src["has_more"]; ok {
			var v bool
			if string(h) == "null" || json.Unmarshal(h, &v) != nil || (hasMore && v != more) {
				return nil, "", errWatchlistSchema
			}
			hasMore = true
			more = v
		}
	}
	vals := headers.Values("X-Has-More")
	if len(vals) > 1 {
		return nil, "", errWatchlistSchema
	}
	if len(vals) == 1 {
		v := strings.ToLower(strings.TrimSpace(vals[0]))
		if v != "true" && v != "false" {
			return nil, "", errWatchlistSchema
		}
		b := v == "true"
		if hasMore && b != more {
			return nil, "", errWatchlistSchema
		}
		hasMore = true
		more = b
	}
	if hasMore && more != (next != "") {
		return nil, "", errWatchlistSchema
	}
	return rows, next, nil
}

func watchlistExactKeys(raw json.RawMessage, names ...string) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, errWatchlistSchema
	}
	for key := range obj {
		for _, name := range names {
			if key != name && strings.EqualFold(key, name) {
				return nil, errWatchlistSchema
			}
		}
	}
	return obj, nil
}

func watchlistMatch(raw json.RawMessage, m MDBWatchlistMedia) (bool, error) {
	fields, err := watchlistExactKeys(raw, "id", "imdb_id", "mediatype", "type", "ids")
	if err != nil {
		return false, err
	}
	if ids, ok := fields["ids"]; ok && string(bytes.TrimSpace(ids)) != "null" {
		if _, err := watchlistExactKeys(ids, "tmdb", "imdb"); err != nil {
			return false, err
		}
	}
	var row struct {
		ID        *watchlistNumericID `json:"id"`
		IMDbID    *string             `json:"imdb_id"`
		MediaType *string             `json:"mediatype"`
		Type      *string             `json:"type"`
		IDs       *struct {
			TMDb *watchlistNumericID `json:"tmdb"`
			IMDb *string             `json:"imdb"`
		} `json:"ids"`
	}
	if json.Unmarshal(raw, &row) != nil || string(raw) == "null" {
		return false, errWatchlistSchema
	}
	if (row.MediaType != nil && *row.MediaType != m.Type) || (row.Type != nil && *row.Type != m.Type) {
		return false, errWatchlistSchema
	}
	tmdb, imdb := "", ""
	addNumeric := func(n *watchlistNumericID) bool {
		if n == nil {
			return true
		}
		s := string(*n)
		if !validID(s) || (tmdb != "" && tmdb != s) {
			return false
		}
		tmdb = s
		return true
	}
	addIMDb := func(s *string) bool {
		if s == nil || *s == "" {
			return true
		}
		if !imdbID.MatchString(*s) || (imdb != "" && imdb != *s) {
			return false
		}
		imdb = *s
		return true
	}
	if !addNumeric(row.ID) || !addIMDb(row.IMDbID) {
		return false, errWatchlistSchema
	}
	if row.IDs != nil {
		if !addNumeric(row.IDs.TMDb) || !addIMDb(row.IDs.IMDb) {
			return false, errWatchlistSchema
		}
	}
	comparable, matched, conflict := false, false, false
	if m.TMDbID != "" && tmdb != "" {
		comparable = true
		if m.TMDbID == tmdb {
			matched = true
		} else {
			conflict = true
		}
	}
	if m.IMDbID != "" && imdb != "" {
		comparable = true
		if m.IMDbID == imdb {
			matched = true
		} else {
			conflict = true
		}
	}
	if !comparable || (matched && conflict) {
		return false, errWatchlistSchema
	}
	return matched, nil
}
