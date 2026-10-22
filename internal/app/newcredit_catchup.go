package app

import (
	"context"
	"log"
	"time"
)

// Reconcile all local watches on every worker wake/tick using processing eligibility.
func (a *App) seedExistingCreditCatchup(ctx context.Context) error {
	_, err := a.db.Exec(ctx, `INSERT INTO app_state(key,value)
 SELECT 'credit_catchup:'||eligible.id::text,'{}'::jsonb FROM (`+tmdbCreditActorEligibility+`) eligible
 ON CONFLICT DO NOTHING`, time.Now().UTC().Add(-tmdbCreditRefreshInterval).Format(time.RFC3339Nano), []int64(nil))
	return err
}

func (a *App) wakeCreditCatchup() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil || a.creditCatchupWake == nil {
		return
	}
	select {
	case a.creditCatchupWake <- struct{}{}:
	default:
	}
}

func (a *App) creditCatchupLoop() {
	report := func(message string, err error) {
		log.Printf("%s: %v", message, err)
		ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
		defer cancel()
		a.activity(ctx, "credits", message+": "+err.Error(), "error")
	}
	run := func() {
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
		defer cancel()
		if err := a.seedExistingCreditCatchup(ctx); err != nil {
			report("Credit catch-up queue initialization failed", err)
			return
		}

		for pass := 0; pass < 16; pass++ {
			s, err := a.settings(ctx)
			if err != nil {
				report("Credit catch-up settings failed", err)
				return
			}
			more, err := a.runCreditCatchupPass(ctx, s)
			if err != nil {
				report("Credit catch-up failed; pending work remains durable", err)
				return
			}
			if !more {
				return
			}
		}
	}
	run()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.creditCatchupWake:
			run()
		case <-ticker.C:
			run()
		}
	}
}

// The queue is local watch IDs, not a request to backfill the catalogue. Cache
// retry times remain the authority for provider deferrals and restart progress.
func (a *App) runCreditCatchupPass(ctx context.Context, s Settings) (bool, error) {
	if err := a.pruneCreditCatchup(ctx); err != nil {
		return false, err
	}
	if !s.TMDB.Enabled || s.TMDB.APIKey == "" {
		return false, nil
	}
	rows, err := a.db.Query(ctx, `SELECT p.id FROM actresses p JOIN app_state q ON q.key='credit_catchup:'||p.id::text ORDER BY p.id`)
	if err != nil {
		return false, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if len(ids) == 0 {
		return false, nil
	}
	ready := func() (bool, error) {
		var found bool
		err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM (`+tmdbCreditActorSelection+`) eligible JOIN app_state q ON q.key='credit_catchup:'||eligible.id::text)`, time.Now().UTC().Add(-tmdbCreditRefreshInterval).Format(time.RFC3339Nano), ids).Scan(&found)
		return found, err
	}
	if found, err := ready(); err != nil || !found {
		return false, err
	}
	conn, err := a.db.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	var locked bool
	// Same session advisory lease as syncOnceResult: neither worker can overwrite
	// the other's credit cache while a full catalogue sync is in flight.
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(486722121)`).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, e := conn.Exec(cleanup, `SELECT pg_advisory_unlock(486722121)`); e != nil {
			_ = conn.Conn().Close(cleanup)
		}
	}()
	if _, err = a.syncTMDBCreditsForWatches(ctx, s, ids); err != nil {
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if _, err = a.db.Exec(ctx, `INSERT INTO activity(kind,message,status) VALUES('credits','Bounded watch credit discovery pass finished; remaining/deferred work is retained in cached coverage','ok')`); err != nil {
		return false, err
	}
	if err = a.pruneCreditCatchup(ctx); err != nil {
		return false, err
	}
	return ready()
}

func (a *App) pruneCreditCatchup(ctx context.Context) error {
	_, err := a.db.Exec(ctx, `DELETE FROM app_state q WHERE q.key LIKE 'credit_catchup:%' AND NOT EXISTS (
 SELECT 1 FROM actresses p LEFT JOIN app_state c ON c.key='tmdb:actor:'||p.tmdb_id
 WHERE q.key='credit_catchup:'||p.id::text AND (COALESCE(c.value->>'complete','false')<>'true'
 OR COALESCE(NULLIF(c.value->>'fetched','')::timestamptz,'-infinity'::timestamptz)<$1::timestamptz)
 AND COALESCE(p.data->>'metadata_status','') NOT LIKE 'unavailable: identity mismatch%')`, time.Now().UTC().Add(-tmdbCreditRefreshInterval).Format(time.RFC3339Nano))
	return err
}
