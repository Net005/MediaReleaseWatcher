CREATE TABLE IF NOT EXISTS schema_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS account(id integer PRIMARY KEY CHECK(id=1), password_hash bytea NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS sessions(token_hash bytea PRIMARY KEY, expires_at timestamptz NOT NULL);
CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS settings(id integer PRIMARY KEY CHECK(id=1), encrypted bytea NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS app_state(key text PRIMARY KEY, value jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS actresses(id bigserial PRIMARY KEY, tmdb_id text NOT NULL UNIQUE, data jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS releases(id bigserial PRIMARY KEY, canonical_key text NOT NULL UNIQUE, media_type text NOT NULL CHECK(media_type IN('movie','show')), tmdb_id text NOT NULL DEFAULT '', imdb_id text NOT NULL DEFAULT '', data jsonb NOT NULL, monitored boolean NOT NULL DEFAULT true, acquisition jsonb NOT NULL DEFAULT '{"sonarr":"","radarr":""}', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX IF NOT EXISTS releases_tmdb ON releases(media_type,tmdb_id) WHERE tmdb_id<>'';
CREATE UNIQUE INDEX IF NOT EXISTS releases_imdb ON releases(media_type,imdb_id) WHERE imdb_id<>'';
CREATE INDEX IF NOT EXISTS releases_updated ON releases(updated_at);
CREATE TABLE IF NOT EXISTS release_cast(release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE, tmdb_id text NOT NULL, source text NOT NULL, PRIMARY KEY(release_id,tmdb_id));
CREATE TABLE IF NOT EXISTS release_people(release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE, actress_id bigint NOT NULL REFERENCES actresses(id) ON DELETE CASCADE, source text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(release_id,actress_id));
CREATE TABLE IF NOT EXISTS activity(id bigserial PRIMARY KEY, kind text NOT NULL, message text NOT NULL, status text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS activity_created ON activity(created_at DESC);
CREATE TABLE IF NOT EXISTS actions(id bigserial PRIMARY KEY, release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE, kind text NOT NULL, status text NOT NULL, error text NOT NULL DEFAULT '', payload jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(release_id,kind));
INSERT INTO schema_migrations(version) VALUES(1) ON CONFLICT DO NOTHING;
-- Upgrade pre-event outboxes without replaying already recorded decisions.
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM schema_migrations WHERE version=2) THEN
  ALTER TABLE actions ADD COLUMN IF NOT EXISTS event_key text NOT NULL DEFAULT 'discovered';
  ALTER TABLE actions DROP CONSTRAINT IF EXISTS actions_release_id_kind_key;
  UPDATE actions a SET event_key=CASE WHEN nullif(r.data->>'release_date','')::date>CURRENT_DATE THEN 'upcoming' WHEN nullif(r.data->>'release_date','') IS NOT NULL THEN 'released' ELSE 'discovered' END FROM releases r WHERE r.id=a.release_id;
  CREATE UNIQUE INDEX IF NOT EXISTS actions_release_kind_event ON actions(release_id,kind,event_key);
  INSERT INTO schema_migrations(version) VALUES(2);
 END IF;
END $$;
