-- 000026_topic_search.up.sql
-- SPEC-TM-03 §3: Topic Full-Text Search & One-Button Context
-- Adds: topic_node_content_search table, topic_search_log table,
--       refresh_topic_node_content_index() function,
--       last_active_at column on topics + index,
--       setweight(A/B) replacement of the simple search_vector trigger.

-- ── Replace the simple search_vector trigger with weighted version ───────
-- Migration 000020 created update_topic_search_vector() with a simple
-- to_tsvector(title||desc). SPEC-TM-03 §3.1 replaces it with setweight
-- A (title) + B (description) for title-boost ranking. Must DROP the
-- existing trigger first, then CREATE OR REPLACE FUNCTION, then recreate.

DROP TRIGGER IF EXISTS trg_topic_search_vector ON topics;

CREATE OR REPLACE FUNCTION update_topic_search_vector() RETURNS trigger AS $$
BEGIN
    NEW.search_vector :=
        setweight(to_tsvector('english', COALESCE(NEW.title, '')), 'A') ||
        setweight(to_tsvector('english', COALESCE(NEW.description, '')), 'B');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_topic_search_vector
    BEFORE INSERT OR UPDATE OF title, description ON topics
    FOR EACH ROW
    EXECUTE FUNCTION update_topic_search_vector();

-- Re-index existing topics so search_vector picks up the new weights.
UPDATE topics SET search_vector =
    setweight(to_tsvector('english', COALESCE(title, '')), 'A') ||
    setweight(to_tsvector('english', COALESCE(description, '')), 'B');

-- ── Add last_active_at to topics ────────────────────────────────────────

ALTER TABLE topics
    ADD COLUMN IF NOT EXISTS last_active_at timestamptz
        NOT NULL DEFAULT clock_timestamp();

CREATE INDEX IF NOT EXISTS idx_topics_last_active
    ON topics(tree_id, last_active_at DESC NULLS LAST);

-- ── Topic Node Content Index ───────────────────────────────────────────
-- Indexes node content within topic scopes for full-text search.
-- Refreshed by the application layer via refresh_topic_node_content_index().
-- NOTE: per project FK convention (TEST-001 pitfall), the node FK references
-- nodes(id) only — nodes has no UNIQUE(id, tree_id) so a composite FK fails.

CREATE TABLE topic_node_content_search (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    topic_id        uuid        NOT NULL,
    node_id         uuid        NOT NULL,
    tree_id         uuid        NOT NULL,
    content_text    text        NOT NULL DEFAULT '',
    content_vector  tsvector    NOT NULL DEFAULT to_tsvector('english', ''),
    content_lang    text        NOT NULL DEFAULT 'english',
    updated_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT fk_tncs_topic
        FOREIGN KEY (topic_id) REFERENCES topics(id)
        ON DELETE CASCADE,
    CONSTRAINT fk_tncs_node
        FOREIGN KEY (node_id) REFERENCES nodes(id)
        ON DELETE CASCADE,
    CONSTRAINT uq_tncs_topic_node
        UNIQUE (topic_id, node_id)
);

CREATE INDEX idx_tncs_topic            ON topic_node_content_search(topic_id);
CREATE INDEX idx_tncs_topic_updated    ON topic_node_content_search(topic_id, updated_at DESC);
CREATE INDEX idx_tncs_search           ON topic_node_content_search USING gin(content_vector);
CREATE INDEX idx_tncs_tree_content     ON topic_node_content_search(tree_id, content_vector);

-- ── Node Content Index Refresh Function ─────────────────────────────────
-- Application calls this after node content changes within a topic scope.
-- Strips markdown formatting, upserts into topic_node_content_search.

CREATE OR REPLACE FUNCTION refresh_topic_node_content_index(p_topic_id uuid, p_node_ids uuid[]) RETURNS integer AS $$
DECLARE
    inserted_count integer;
BEGIN
    WITH content_src AS (
        SELECT
            t.id AS topic_id,
            n.id AS node_id,
            n.tree_id,
            regexp_replace(
                regexp_replace(
                    regexp_replace(
                        COALESCE(n.content, ''),
                        E'[\\[\\]()#*_~`>|\\-]', ' ', 'g'
                    ),
                    E'\\s+', ' ', 'g'
                ),
                E'^\\s+|\\s+$', '', 'g'
            ) AS content_text,
            to_tsvector(
                'english',
                regexp_replace(
                    regexp_replace(
                        COALESCE(n.content, ''),
                        E'[\\[\\]()#*_~`>|\\-]', ' ', 'g'
                    ),
                    E'\\s+', ' ', 'g'
                )
            ) AS content_vector
        FROM topics t
        JOIN topic_member_nodes tmn ON tmn.topic_id = t.id
        JOIN nodes n ON n.id = tmn.node_id
        WHERE t.id = p_topic_id
          AND n.id = ANY(p_node_ids)
    )
    INSERT INTO topic_node_content_search (topic_id, node_id, tree_id, content_text, content_vector)
    SELECT cs.topic_id, cs.node_id, cs.tree_id, cs.content_text, cs.content_vector
    FROM content_src cs
    ON CONFLICT (topic_id, node_id)
    DO UPDATE SET
        content_text   = EXCLUDED.content_text,
        content_vector = EXCLUDED.content_vector,
        updated_at     = clock_timestamp();

    GET DIAGNOSTICS inserted_count = ROW_COUNT;
    RETURN inserted_count;
END;
$$ LANGUAGE plpgsql;

-- ── Topic Search Log (Analytics) ────────────────────────────────────────

CREATE TABLE topic_search_log (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid        NOT NULL,
    profile_id      uuid        REFERENCES profiles(id) ON DELETE SET NULL,
    query_text      text        NOT NULL,
    result_count    integer     NOT NULL DEFAULT 0,
    filters_applied jsonb       NOT NULL DEFAULT '{}'::jsonb,
    injected_count  integer     NOT NULL DEFAULT 0,
    search_duration_ms integer  NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX idx_tsl_tree_created  ON topic_search_log(tree_id, created_at DESC);
CREATE INDEX idx_tsl_profile       ON topic_search_log(profile_id);
CREATE INDEX idx_tsl_query_hash    ON topic_search_log USING hash(query_text);
