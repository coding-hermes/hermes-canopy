-- 000002_trees.up.sql
-- Trees are the top-level container. Created before nodes/edges so
-- those tables can FK-reference trees(id).
--
-- NOTE: trees.root_node_id is intentionally a plain uuid column here,
-- NOT a FK to nodes(id). Trees must exist before nodes per the FK
-- direction in 000003_nodes (nodes.tree_id → trees.id), so the
-- reciprocal root_node_id FK cannot be declared in this migration.
-- Application code preserves the invariant. The constraint could be
-- added in a follow-up migration once both sides exist; for the
-- scaffold this is deferred.

CREATE TABLE trees (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    owner_id        uuid        NOT NULL,                                       -- FK to profiles (SPEC-DM-04, post-MVP)
    title           text        NOT NULL DEFAULT '',
    description     text        NOT NULL DEFAULT '',
    root_node_id    uuid,                                                       -- see NOTE above
    metadata        jsonb       NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    edited_at       timestamptz,
    deleted_at      timestamptz,                                                -- Soft delete (NULL = active)
    CONSTRAINT chk_tree_title CHECK (char_length(title) <= 500)
);

CREATE INDEX idx_trees_owner ON trees(owner_id);
