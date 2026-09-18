-- 000002_trees.up.sql — SQLite translation of ../../000002_trees.up.sql
--
-- Translation rules applied (full table in docs/SQLITE-PIVOT.md):
--   uuid        -> TEXT
--   jsonb       -> TEXT + CHECK (json_valid(..))   (jsonb validates on write in PG)
--   timestamptz -> TEXT, RFC3339 UTC with milliseconds
--   char_length -> length                          (character semantics match)
--
-- Deviations from the PG text, each deliberate:
--   * id has NO DEFAULT: PG's `DEFAULT uuidv7()` (../../000002_trees.up.sql:14) cannot
--     exist in SQLite (see 000001_extensions.up.sql). Wave-2 Go write-path obligation.
--   * PG's `DEFAULT clock_timestamp()` (:20) becomes strftime('%Y-%m-%dT%H:%M:%fZ','now'),
--     the SQLite equivalent wall-clock, same UTC-RFC3339 shape as all other timestamps.

CREATE TABLE trees (
    id              TEXT    PRIMARY KEY NOT NULL,
    owner_id        TEXT    NOT NULL,
    title           TEXT    NOT NULL DEFAULT '',
    description     TEXT    NOT NULL DEFAULT '',
    root_node_id    TEXT,
    metadata        TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    edited_at       TEXT,
    deleted_at      TEXT,
    CONSTRAINT chk_tree_title CHECK (length(title) <= 500)
);

CREATE INDEX idx_trees_owner ON trees(owner_id);
