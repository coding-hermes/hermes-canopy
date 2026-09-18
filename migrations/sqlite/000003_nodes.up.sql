-- 000003_nodes.up.sql — SQLite translation of ../../000003_nodes.up.sql
--
-- Same translation rules as 000002 (uuid->TEXT, jsonb->TEXT+json_valid,
-- timestamptz->TEXT RFC3339, bigint->INTEGER, char_length->length).
-- `id` carries no default (PG DEFAULT uuidv7() at ../../000003_nodes.up.sql:7);
-- wave-2 Go write-path obligation.
--
-- ── NOT TRANSLATABLE: recorded here, never silently dropped ─────────────────────────
-- (each entry is mirrored in the docs/SQLITE-PIVOT.md inventory with its PG file:line)

-- 1. idx_nodes_content_fts — ../../000003_nodes.up.sql:35
--    PG: `CREATE INDEX .. USING gin(to_tsvector('english', content))`.
--    SQLite has neither GIN nor tsvector. The replacement (FTS5 external-content table
--    vs plain LIKE / json_each scan) is an OPEN DECISION for the owner; wave-2 owner:
--    the search/query layer (internal/db). No index is created here on purpose.

-- 2. set_node_sequence() + trg_node_sequence — ../../000003_nodes.up.sql:38-52
--    Fills NEW.sequence_num with COALESCE(MAX(sequence_num),0)+1 per tree inside a
--    BEFORE INSERT trigger. SQLite cannot assign `NEW.<col>` in a trigger body
--    (verified: `SET NEW.x = ..` -> syntax error), and sequence_num is NOT NULL, so an
--    AFTER-INSERT trigger cannot repair the row either. Wave-2 owner: the repo layer's
--    node-insert path must compute the next sequence in the same transaction as the
--    insert (SELECT MAX(sequence_num)+1 .. WHERE tree_id = ? FOR the tree).

-- Translated trigger (see below): set_edited_at() + trg_node_edited_at
-- (../../000003_nodes.up.sql:55-67) — the only PG trigger in this migration whose logic
-- IS expressible in SQLite DDL. SQLite's AFTER UPDATE trigger performs the same
-- side-effect update; recursive_triggers is OFF by default, so it does not re-fire.

CREATE TABLE nodes (
    id              TEXT    PRIMARY KEY NOT NULL,
    tree_id         TEXT    NOT NULL,
    parent_id       TEXT    REFERENCES nodes(id) ON DELETE SET NULL,
    author_id       TEXT    NOT NULL,
    content         TEXT    NOT NULL DEFAULT '',
    content_format  TEXT    NOT NULL DEFAULT 'markdown',
    node_type       TEXT    NOT NULL DEFAULT 'message',
    sequence_num    INTEGER NOT NULL,
    metadata        TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    edited_at       TEXT,
    deleted_at      TEXT,
    CONSTRAINT fk_nodes_tree
        FOREIGN KEY (tree_id) REFERENCES trees(id)
        ON DELETE CASCADE,
    CONSTRAINT chk_content_format
        CHECK (content_format IN ('markdown', 'plain', 'rich')),
    CONSTRAINT chk_node_type
        CHECK (node_type IN ('message', 'synthesis', 'system'))
);

-- Indexes (all of the PG set except the GIN full-text index noted above)
CREATE INDEX idx_nodes_tree_id        ON nodes(tree_id);
CREATE INDEX idx_nodes_tree_parent    ON nodes(tree_id, parent_id);
CREATE INDEX idx_nodes_tree_created   ON nodes(tree_id, created_at);
CREATE INDEX idx_nodes_tree_sequence  ON nodes(tree_id, sequence_num);
CREATE INDEX idx_nodes_author         ON nodes(author_id);
CREATE INDEX idx_nodes_deleted        ON nodes(tree_id) WHERE deleted_at IS NOT NULL;

-- set_edited_at() / trg_node_edited_at translation.
-- PG: BEFORE UPDATE .. WHEN (OLD.content IS DISTINCT FROM NEW.content OR
--     OLD.metadata IS DISTINCT FROM NEW.metadata) { NEW.edited_at = clock_timestamp(); }
-- SQLite: same WHEN semantics via `IS NOT` (null-safe, the SQLite spelling of
-- IS DISTINCT FROM); the row rewrite becomes an UPDATE on the same row, which does not
-- re-trigger (recursive_triggers defaults to OFF; verified).
CREATE TRIGGER trg_node_edited_at
    AFTER UPDATE ON nodes
    FOR EACH ROW
    WHEN OLD.content IS NOT NEW.content OR OLD.metadata IS NOT NEW.metadata
BEGIN
    UPDATE nodes
       SET edited_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
     WHERE id = NEW.id;
END;
