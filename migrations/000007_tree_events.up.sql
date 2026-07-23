-- 000007_tree_events.up.sql
-- Append-only event log for tree mutations. Every node/edge mutation
-- creates an entry here, referenced by the SSE hub for replay and by
-- the sync engine for snapshot/delta computation.

CREATE TABLE tree_events (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid        NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    snapshot_id     uuid        REFERENCES tree_snapshots(id) ON DELETE SET NULL,
    event_type      text        NOT NULL,
    node_id         uuid,
    edge_id         uuid,
    payload         jsonb       NOT NULL DEFAULT '{}',
    sequence_num    bigint      NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),

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
CREATE TABLE tree_event_seq (
    tree_id       uuid        PRIMARY KEY REFERENCES trees(id) ON DELETE CASCADE,
    next_seq      bigint      NOT NULL DEFAULT 1
);
