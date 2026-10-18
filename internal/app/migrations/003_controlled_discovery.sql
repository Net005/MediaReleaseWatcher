-- User-confirmed release catalogue reset. The fixed marker and all destructive
-- work are one transaction (the migration loader holds the schema advisory lock).
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version=3) THEN
  ALTER TABLE releases ALTER COLUMN monitored SET DEFAULT false;
  ALTER TABLE releases ADD COLUMN IF NOT EXISTS monitoring_override boolean;
  CREATE TABLE IF NOT EXISTS mdblist_lists (
   id text PRIMARY KEY CHECK (id ~ '^[1-9][0-9]*$'),
   name text NOT NULL DEFAULT '',
   enabled boolean NOT NULL DEFAULT true,
   monitored boolean NOT NULL DEFAULT false,
   auto_add boolean NOT NULL DEFAULT false,
   baselined boolean NOT NULL DEFAULT false
  );
  CREATE TABLE IF NOT EXISTS release_lists (
   release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
   list_id text NOT NULL REFERENCES mdblist_lists(id) ON DELETE CASCADE,
   PRIMARY KEY(release_id,list_id)
  );
  ALTER TABLE release_cast ADD COLUMN IF NOT EXISTS data jsonb NOT NULL DEFAULT '{}';
  DELETE FROM releases;
  DELETE FROM activity;
  DELETE FROM app_state WHERE key IN ('baseline','sync') OR key LIKE 'person_credits:%' OR key LIKE 'silo:%';
  INSERT INTO schema_migrations(version) VALUES(3);
 END IF;
END $$;
