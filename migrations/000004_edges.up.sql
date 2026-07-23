-- 000004_edges.up.sql
-- Edges connect nodes into a DAG. Each edge is typed so the
-- application can distinguish replies, forks, synthesis, and
-- #references. Self-edges are forbidden; the UNIQUE constraint
-- on (source_id, target_id, edge_type) prevents duplicates.

CREATE TABLE edges (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid        NOT NULL,
    source_id       uuid        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    target_id       uuid        NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    edge_type       text        NOT NULL DEFAULT 'reply',  -- 'reply' | 'fork' | 'synthesis' | 'reference'
    sequence_num    bigint      NOT NULL,                  -- Order among siblings from same source
    metadata        jsonb       NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    deleted_at      timestamptz,                           -- Soft delete

    CONSTRAINT fk_edges_tree
        FOREIGN KEY (tree_id) REFERENCES trees(id)
        ON DELETE CASCADE,
    CONSTRAINT chk_edge_type
        CHECK (edge_type IN ('reply', 'fork', 'synthesis', 'reference')),
    CONSTRAINT chk_no_self_edge
        CHECK (source_id != target_id),
    CONSTRAINT chk_unique_edge
        UNIQUE (source_id, target_id, edge_type)
);

CREATE INDEX idx_edges_tree_id        ON edges(tree_id);
CREATE INDEX idx_edges_source         ON edges(source_id);
CREATE INDEX idx_edges_target         ON edges(target_id);
CREATE INDEX idx_edges_tree_source    ON edges(tree_id, source_id);
CREATE INDEX idx_edges_tree_target    ON edges(tree_id, target_id);
CREATE INDEX idx_edges_type           ON edges(tree_id, edge_type);
