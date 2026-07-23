-- 000003_nodes.up.sql
-- Nodes are the conversation-message primitive. Soft-delete via
-- deleted_at; single-parent via parent_id REFERENCES nodes(id);
-- multi-parent synthesis supported at the edges layer (SPEC-DM-01 §3.5).

CREATE TABLE nodes (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid        NOT NULL,
    parent_id       uuid        REFERENCES nodes(id) ON DELETE SET NULL,
    author_id       uuid        NOT NULL,                                       -- FK to profiles (SPEC-DM-04)
    content         text        NOT NULL DEFAULT '',
    content_format  text        NOT NULL DEFAULT 'markdown',                    -- 'markdown' | 'plain' | 'rich'
    node_type       text        NOT NULL DEFAULT 'message',                      -- 'message' | 'synthesis' | 'system'
    sequence_num    bigint      NOT NULL,                                       -- Monotonic within tree_id
    metadata        jsonb       NOT NULL DEFAULT '{}',                          -- Arbitrary key-value
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    edited_at       timestamptz,
    deleted_at      timestamptz,                                                -- Soft delete (NULL = active)
    CONSTRAINT fk_nodes_tree
        FOREIGN KEY (tree_id) REFERENCES trees(id)
        ON DELETE CASCADE,
    CONSTRAINT chk_content_format
        CHECK (content_format IN ('markdown', 'plain', 'rich')),
    CONSTRAINT chk_node_type
        CHECK (node_type IN ('message', 'synthesis', 'system'))
);

-- Indexes
CREATE INDEX idx_nodes_tree_id        ON nodes(tree_id);
CREATE INDEX idx_nodes_tree_parent    ON nodes(tree_id, parent_id);
CREATE INDEX idx_nodes_tree_created   ON nodes(tree_id, created_at);
CREATE INDEX idx_nodes_tree_sequence  ON nodes(tree_id, sequence_num);
CREATE INDEX idx_nodes_author         ON nodes(author_id);
CREATE INDEX idx_nodes_deleted        ON nodes(tree_id) WHERE deleted_at IS NOT NULL;
CREATE INDEX idx_nodes_content_fts    ON nodes USING gin(to_tsvector('english', content));

-- Auto-increment sequence_num within tree_id scope.
CREATE OR REPLACE FUNCTION set_node_sequence() RETURNS trigger AS $$
BEGIN
    SELECT COALESCE(MAX(sequence_num), 0) + 1
    INTO NEW.sequence_num
    FROM nodes
    WHERE tree_id = NEW.tree_id;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_node_sequence
    BEFORE INSERT ON nodes
    FOR EACH ROW
    WHEN (NEW.sequence_num IS NULL)
    EXECUTE FUNCTION set_node_sequence();

-- Auto-set edited_at on content/metadata update.
CREATE OR REPLACE FUNCTION set_edited_at() RETURNS trigger AS $$
BEGIN
    NEW.edited_at = clock_timestamp();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_node_edited_at
    BEFORE UPDATE ON nodes
    FOR EACH ROW
    WHEN (OLD.content IS DISTINCT FROM NEW.content
       OR OLD.metadata IS DISTINCT FROM NEW.metadata)
    EXECUTE FUNCTION set_edited_at();
