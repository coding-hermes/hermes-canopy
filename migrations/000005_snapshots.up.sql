-- 000005_snapshots.up.sql
CREATE TABLE tree_snapshots (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid        NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    parent_hash     text,
    hash            text        NOT NULL,
    node_count      integer     NOT NULL DEFAULT 0,
    edge_count      integer     NOT NULL DEFAULT 0,
    snapshot_data   jsonb       NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT chk_snapshot_hash_length CHECK (char_length(hash) = 64),
    CONSTRAINT chk_snapshot_parent_hash_length CHECK (parent_hash IS NULL OR char_length(parent_hash) = 64)
);
CREATE INDEX idx_snapshots_tree_id ON tree_snapshots(tree_id);
CREATE INDEX idx_snapshots_hash ON tree_snapshots(hash);
CREATE INDEX idx_snapshots_tree_created ON tree_snapshots(tree_id, created_at DESC);
CREATE UNIQUE INDEX idx_snapshots_tree_hash ON tree_snapshots(tree_id, hash);
