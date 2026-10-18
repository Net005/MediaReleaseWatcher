package integrations

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// ArrSearch submits exactly one native MoviesSearch/SeriesSearch command after
// re-reading the selected manager record and verifying its exact provider IDs
// and monitoring opt-in. Callers MUST enforce global, task and integration
// acquisition/search consent; this low-level adapter has no app settings.
// Native contract: NzbDrone.Core/IndexerSearch/{Movies,Series}SearchCommand.cs
// and {Radarr,Sonarr}.Api.V3/Commands/CommandResource.cs. Never use whole-library
// MissingMoviesSearch/MissingEpisodeSearch commands or retry uncertain writes.
// A successful return proves bounded command acceptance, not download success.
func (c *Client) ArrSearch(ctx context.Context, kind string, id int, media Media) error {
	return c.ArrSearchGuarded(ctx, kind, id, media, nil)
}

// ArrSearchGuarded validates write ownership after the final identity/monitoring
// read and immediately before the command POST. The guard must fail closed;
// ctx must also be cancelled when its ownership session is lost.
func (c *Client) ArrSearchGuarded(ctx context.Context, kind string, id int, media Media, beforePost func(context.Context) error) error {
	if id <= 0 {
		return errors.New("search requires an exact manager item ID")
	}
	if err := validMedia(kind, media); err != nil {
		return err
	}
	service, err := c.arr(kind)
	if err != nil {
		return err
	}
	c.addMu.Lock()
	defer c.addMu.Unlock()
	item, err := c.readArr(ctx, service, id)
	if err != nil {
		return err
	}
	if item.ID != id || !sameMedia(item, media, kind) {
		return errors.New("search item failed exact identity verification")
	}
	if item.Monitored == nil || !*item.Monitored {
		return errors.New("search requires a monitored manager item")
	}
	name := "MoviesSearch"
	payload := map[string]any{"name": name, "movieIds": []int{id}}
	if kind == "sonarr" {
		name = "SeriesSearch"
		payload = map[string]any{"name": name, "seriesId": id}
	}
	var accepted struct {
		ID int `json:"id"`
	}
	if beforePost != nil {
		if err = beforePost(ctx); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = c.arrRequest(ctx, service, http.MethodPost, "command", nil, payload, &accepted); err != nil {
		return err
	}
	uncertain := errors.New("search command may have been accepted but exact readback failed; inspect the manager before retrying")
	if accepted.ID <= 0 {
		return uncertain
	}
	var verified struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Body   struct {
			Name     string `json:"name"`
			MovieIDs []int  `json:"movieIds"`
			SeriesID int    `json:"seriesId"`
		} `json:"body"`
	}
	if err = c.arrRequest(ctx, service, http.MethodGet, "command/"+strconv.Itoa(accepted.ID), nil, nil, &verified); err != nil {
		return fmt.Errorf("%w: %w", uncertain, err)
	}
	scopeOK := len(verified.Body.MovieIDs) == 1 && verified.Body.MovieIDs[0] == id
	if kind == "sonarr" {
		scopeOK = verified.Body.SeriesID == id
	}
	if verified.ID != accepted.ID || verified.Name != name || verified.Body.Name != name || !scopeOK {
		return uncertain
	}
	if verified.Status == "failed" || verified.Status == "aborted" || verified.Status == "cancelled" {
		return errors.New("manager search command failed or was cancelled")
	}
	return nil
}
