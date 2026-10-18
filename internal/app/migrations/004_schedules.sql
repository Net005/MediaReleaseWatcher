-- Additive, idempotent upgrade. Never rewrite or delete user schedules.
CREATE TABLE IF NOT EXISTS scheduled_tasks (
 id bigserial PRIMARY KEY,
 data jsonb NOT NULL CHECK (jsonb_typeof(data) = 'object'),
 enabled boolean NOT NULL DEFAULT false,
 next_run timestamptz,
 last_run timestamptz,
 last_status text NOT NULL DEFAULT 'never',
 last_error text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS scheduled_tasks_due ON scheduled_tasks(next_run) WHERE enabled;
INSERT INTO schema_migrations(version) VALUES(4) ON CONFLICT DO NOTHING;
