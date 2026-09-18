-- 000007_tree_events.up.sql — SQLite translation of ../../000007_tree_events.up.sql
--
-- Same rules: uuid->TEXT, jsonb->TEXT+json_valid, timestamptz->TEXT RFC3339,
-- bigint->INTEGER. `id` has no default (PG DEFAULT uuidv7(),
-- ../../000007_tree_events.up.sql:7) — wave-2 Go write-path obligation.
-- The event_type CHECK is carried over verbatim (it is widened to include 'yjs_update'
-- by migration 000031, which is out of this wave).

CREATE TABLE tree_events (
    id              TEXT    PRIMARY KEY NOT NULL,
    tree_id         TEXT    NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    snapshot_id     TEXT    REFERENCES tree_snapshots(id) ON DELETE SET NULL,
    event_type      TEXT    NOT NULL,
    node_id         TEXT,
    edge_id         TEXT,
    payload         TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
    sequence_num    INTEGER NOT NULL,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),

    CONSTRAINT chk_event_type CHECK (event_type IN (
        'node_added', 'node_updated', 'node_removed',
        'edge_added', 'edge_removed'
    ))
);

CREATE INDEX idx_tree_events_tree     ON tree_events(tree_id, sequence_num);
CREATE INDEX idx_tree_events_snapshot ON tree_events(snapshot_id);
CREATE INDEX idx_tree_events_node     ON tree_events(node_id) WHERE node_id IS NOT NULL;
CREATE INDEX idx_tree_events_created  ON tree_events(tree_id, created_at);

-- Sequence counter per tree for monotonic sequence_num.
-- PG's DEFAULT 1 on next_seq is a literal default and carries over unchanged; the
-- increment itself is application logic in both engines (no PG trigger exists for it).
CREATE TABLE tree_event_seq (
    tree_id       TEXT    PRIMARY KEY NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    next_seq      INTEGER NOT NULL DEFAULT 1
);
