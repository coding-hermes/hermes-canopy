-- 000021_node_resolved_refs.up.sql
-- Stores the authoritative mapping from a node to the topics it references.
-- One row per (node, topic) pair. Supports cascade delete and fast lookups.

CREATE TABLE node_resolved_refs (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    node_id         uuid        NOT NULL,
    tree_id         uuid        NOT NULL,
    topic_id        uuid        NOT NULL,
    raw_ref         text        NOT NULL,    -- Original #slug text (e.g., "#database-schema")
    slug            text        NOT NULL,    -- Normalized slug
    resolved_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    resolved_by     uuid        NOT NULL REFERENCES profiles(id) ON DELETE SET NULL,
    -- The context hash of the topic at the time of resolution, used for cache coherency
    context_hash    text        NOT NULL DEFAULT '',
    CONSTRAINT fk_nrr_node
        FOREIGN KEY (node_id) REFERENCES nodes(id)
        ON DELETE CASCADE,
    CONSTRAINT fk_nrr_topic
        FOREIGN KEY (topic_id) REFERENCES topics(id)
        ON DELETE CASCADE,
    CONSTRAINT uq_nrr_node_topic
        UNIQUE (node_id, topic_id),
    CONSTRAINT chk_nrr_slug_format
        CHECK (slug ~ '^[a-z]([a-z0-9-]*[a-z0-9])?$')
);

CREATE INDEX idx_nrr_node_id         ON node_resolved_refs(node_id);
CREATE INDEX idx_nrr_tree_id         ON node_resolved_refs(tree_id);
CREATE INDEX idx_nrr_topic_id        ON node_resolved_refs(topic_id);
CREATE INDEX idx_nrr_resolved_at     ON node_resolved_refs(tree_id, resolved_at DESC);
CREATE INDEX idx_nrr_slug            ON node_resolved_refs(tree_id, slug);
