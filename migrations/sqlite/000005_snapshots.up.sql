-- 000005_snapshots.up.sql — SQLite translation of ../../000005_snapshots.up.sql
--
-- Same rules: uuid->TEXT, jsonb->TEXT+json_valid, timestamptz->TEXT RFC3339,
-- integer->INTEGER. `id` has no default (PG DEFAULT uuidv7(),
-- ../../000005_snapshots.up.sql:3) — wave-2 Go write-path obligation.
-- `character_length(hash) = 64` -> `length(hash) = 64`; the DESC ordering in
-- idx_snapshots_tree_created is supported natively by SQLite.

CREATE TABLE tree_snapshots (
    id              TEXT    PRIMARY KEY NOT NULL,
    tree_id         TEXT    NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    parent_hash     TEXT,
    hash            TEXT    NOT NULL,
    node_count      INTEGER NOT NULL DEFAULT 0,
    edge_count      INTEGER NOT NULL DEFAULT 0,
    snapshot_data   TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(snapshot_data)),
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    CONSTRAINT chk_snapshot_hash_length CHECK (length(hash) = 64),
    CONSTRAINT chk_snapshot_parent_hash_length CHECK (parent_hash IS NULL OR length(parent_hash) = 64)
);

CREATE INDEX idx_snapshots_tree_id ON tree_snapshots(tree_id);
CREATE INDEX idx_snapshots_hash ON tree_snapshots(hash);
CREATE INDEX idx_snapshots_tree_created ON tree_snapshots(tree_id, created_at DESC);
CREATE UNIQUE INDEX idx_snapshots_tree_hash ON tree_snapshots(tree_id, hash);
