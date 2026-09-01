-- Per-node causal state and durable concurrent-write records (FTR-02 P6).
CREATE TABLE federation_node_clocks (
    tree_id         uuid        NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    node_id         uuid        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    clock           jsonb       NOT NULL DEFAULT '{}',
    winner_payload  jsonb       NOT NULL DEFAULT '{}',
    winner_lamport bigint       NOT NULL DEFAULT 0,
    winner_peer_id uuid         NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tree_id, node_id),
    CONSTRAINT chk_federation_node_clock_object CHECK (jsonb_typeof(clock) = 'object')
);

CREATE TABLE federation_conflicts (
    id            uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id       uuid        NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    node_id       uuid        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    left_payload  jsonb       NOT NULL,
    right_payload jsonb       NOT NULL,
    detected_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    resolution_state text     NOT NULL DEFAULT 'unresolved',
    resolution    text,
    resolved_at   timestamptz,
    CONSTRAINT chk_federation_conflict_state
        CHECK (resolution_state IN ('unresolved', 'resolved')),
    CONSTRAINT chk_federation_conflict_resolution
        CHECK (resolution IS NULL OR resolution IN ('left', 'right'))
);

CREATE INDEX idx_federation_conflicts_tree_unresolved
    ON federation_conflicts(tree_id, detected_at DESC) WHERE resolution_state = 'unresolved';
