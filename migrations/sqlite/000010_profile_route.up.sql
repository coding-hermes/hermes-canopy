-- 000010_profile_route.up.sql — SQLite translation of ../../000010_profile_route.up.sql
--
-- Translation rules applied (docs/SQLITE-PIVOT.md):
--   UUID         -> TEXT (no default: PG DEFAULT gen_random_uuid() at
--                   ../../000010_profile_route.up.sql:7 is a wave-2 Go write-path obligation)
--   VARCHAR(n)   -> TEXT   (SQLite has no length-typed strings; PG's declared length is
--                   not re-derived as a CHECK because PG itself does not enforce it at
--                   the type level beyond varchar(n) — see the open decision in the doc)
--   BOOLEAN      -> INTEGER NOT NULL DEFAULT 0/1 + CHECK (col IN (0,1))
--   BYTEA        -> BLOB
--   TIMESTAMPTZ  -> TEXT RFC3339 UTC; DEFAULT NOW() -> strftime(..,'now')
--
-- The FK to workspaces(id) added by ../../000018_workspaces.up.sql:16 is not declarable
-- in SQLite (no ALTER TABLE .. ADD CONSTRAINT); that clause is recorded in the
-- 000018 entry of the docs/SQLITE-PIVOT.md inventory — wave-2 decision.

CREATE TABLE IF NOT EXISTS profile_route (
    id                       TEXT    PRIMARY KEY NOT NULL,
    workspace_id             TEXT    NOT NULL,
    profile_name             TEXT    NOT NULL,
    display_name             TEXT    NOT NULL DEFAULT '',
    is_active                INTEGER NOT NULL DEFAULT 0 CHECK (is_active IN (0,1)),
    model_preference         TEXT,
    profile_token_encrypted  BLOB,
    mapped_at                TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_used_at             TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE(workspace_id, profile_name)
);

CREATE INDEX IF NOT EXISTS idx_profile_route_active
    ON profile_route(workspace_id) WHERE is_active = 1;
