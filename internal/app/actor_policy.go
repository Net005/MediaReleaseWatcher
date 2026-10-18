package app

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Activating an actor rule applies only to future events. Known matching event
// states are recorded silently even if no catalogue sync has run yet.
func baselineActorActivation(ctx context.Context, tx pgx.Tx, before, after Actress) error {
	for _, kind := range []string{"acquire", "notify"} {
		old, on := before.Monitored && before.AutoAdd, after.Monitored && after.AutoAdd
		if kind == "notify" {
			old, on = before.Monitored && before.Notify, after.Monitored && after.Notify
		}
		if old || !on {
			continue
		}
		_, err := tx.Exec(ctx, `INSERT INTO actions(release_id,kind,status,event_key) SELECT rp.release_id,$2,'baseline',CASE WHEN nullif(r.data->>'release_date','')::date>(now() AT TIME ZONE 'UTC')::date THEN 'upcoming' WHEN nullif(r.data->>'release_date','') IS NOT NULL THEN 'released' ELSE 'discovered' END FROM release_people rp JOIN releases r ON r.id=rp.release_id WHERE rp.actress_id=$1 ON CONFLICT DO NOTHING`, after.ID, kind)
		if err != nil {
			return err
		}
	}
	return nil
}
