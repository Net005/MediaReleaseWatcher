package app

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

// A non-nil lists collection is authoritative, including an explicit empty list.
func configuredLists(s MDBSettings) []MDBListPolicy {
	if s.Lists != nil {
		return s.Lists
	}
	out := []MDBListPolicy{}
	seen := map[string]bool{}
	for _, id := range listIDs(s.ListIDs) {
		if !seen[id] {
			out = append(out, MDBListPolicy{ID: id, Enabled: true})
			seen[id] = true
		}
	}
	return out
}

const actorAcquireGate = `EXISTS(SELECT 1 FROM release_people rp JOIN actresses p ON p.id=rp.actress_id WHERE rp.release_id=r.id AND COALESCE((p.data->>'monitored')::boolean,false) AND COALESCE((p.data->>'auto_add')::boolean,false) AND COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%')`
const actorNotifyGate = `EXISTS(SELECT 1 FROM release_people rp JOIN actresses p ON p.id=rp.actress_id WHERE rp.release_id=r.id AND COALESCE((p.data->>'monitored')::boolean,false) AND COALESCE((p.data->>'notify')::boolean,false) AND COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%')`
const listAcquireGate = `EXISTS(SELECT 1 FROM release_lists rl JOIN mdblist_lists l ON l.id=rl.list_id WHERE rl.release_id=r.id AND l.enabled AND l.monitored AND l.auto_add)`
const actorMonitorGate = `EXISTS(SELECT 1 FROM release_people rp JOIN actresses p ON p.id=rp.actress_id WHERE rp.release_id=r.id AND COALESCE((p.data->>'monitored')::boolean,false) AND (COALESCE((p.data->>'auto_add')::boolean,false) OR COALESCE((p.data->>'notify')::boolean,false)) AND COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%')`
const listMonitorGate = `EXISTS(SELECT 1 FROM release_lists rl JOIN mdblist_lists l ON l.id=rl.list_id WHERE rl.release_id=r.id AND l.enabled AND l.monitored)`

// Manual overrides remain authoritative; automatic policies only monitor upcoming titles.
const recomputeMonitoring = `UPDATE releases r SET monitored=COALESCE(r.monitoring_override,(` + upcomingReleasePredicate + `) AND (` + listMonitorGate + ` OR ` + actorMonitorGate + `))`

func syncListPolicies(ctx context.Context, tx pgx.Tx, s Settings) error {
	if _, err := tx.Exec(ctx, `UPDATE mdblist_lists SET enabled=false,monitored=false,auto_add=false`); err != nil {
		return err
	}
	for _, p := range configuredLists(s.MDBList) {
		if _, err := tx.Exec(ctx, `INSERT INTO mdblist_lists(id,name,enabled,monitored,auto_add) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET name=excluded.name,enabled=excluded.enabled,monitored=excluded.monitored,auto_add=excluded.auto_add`, p.ID, p.Name, p.Enabled, p.Monitored, p.AutoAdd); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, recomputeMonitoring)
	return err
}

// Replace only after every source page succeeded: a failed feed cannot erase membership.
func (a *App) replaceListMembership(ctx context.Context, id string, ids []int64) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var baselined bool
	if err = tx.QueryRow(ctx, `SELECT baselined FROM mdblist_lists WHERE id=$1 FOR UPDATE`, id).Scan(&baselined); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM release_lists WHERE list_id=$1`, id); err != nil {
		return err
	}
	for _, rid := range ids {
		if _, err = tx.Exec(ctx, `INSERT INTO release_lists(release_id,list_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, rid, id); err != nil {
			return err
		}
		if !baselined {
			r, err := scanRelease(tx.QueryRow(ctx, releaseSelect+`WHERE r.id=$1`, rid))
			if err != nil {
				return err
			}
			for _, kind := range []string{"acquire", "notify"} {
				if _, err = tx.Exec(ctx, `INSERT INTO actions(release_id,kind,status,event_key) VALUES($1,$2,'baseline',$3) ON CONFLICT DO NOTHING`, rid, kind, releaseEvent(r, time.Now().UTC())); err != nil {
					return err
				}
			}
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE mdblist_lists SET baselined=true WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, recomputeMonitoring); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (a *App) listMDBLists(w http.ResponseWriter, r *http.Request) {
	s, err := a.settings(r.Context())
	if err != nil {
		fail(w, 500, "cannot read lists")
		return
	}
	respond(w, 200, map[string]any{"items": configuredLists(s.MDBList)})
}

// Explicit false is retained; only legacy configured IDs default enabled=true.
func (p *MDBListPolicy) UnmarshalJSON(b []byte) error {
	type policy MDBListPolicy
	v := policy{Enabled: true}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*p = MDBListPolicy(v)
	return nil
}
