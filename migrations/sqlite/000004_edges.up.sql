-- 000004_edges.up.sql — SQLite translation of ../../000004_edges.up.sql
--
-- Same rules: uuid->TEXT, jsonb->TEXT+json_valid, timestamptz->TEXT RFC3339,
-- bigint->INTEGER, char_length->length. `id` has no default (PG DEFAULT uuidv7(),
-- ../../000004_edges.up.sql:8) — wave-2 Go write-path obligation.
-- Named UNIQUE / CHECK / FK table constraints carry over verbatim; SQLite enforces FKs
-- when PRAGMA foreign_keys=ON (see the pragma set in docs/SQLITE-PIVOT.md).

CREATE TABLE edges (
    id              TEXT    PRIMARY KEY NOT NULL,
    tree_id         TEXT    NOT NULL,
    source_id       TEXT    NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    target_id       TEXT    NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    edge_type       TEXT    NOT NULL DEFAULT 'reply',
    sequence_num    INTEGER NOT NULL,
    metadata        TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    deleted_at      TEXT,

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
