-- 000026_topic_search.down.sql
-- Reverse SPEC-TM-03 §3 additions.

DROP TABLE IF EXISTS topic_search_log;
DROP FUNCTION IF EXISTS refresh_topic_node_content_index(uuid, uuid[]);
DROP TABLE IF EXISTS topic_node_content_search;

-- Restore the simple search_vector trigger from migration 000020.
DROP TRIGGER IF EXISTS trg_topic_search_vector ON topics;

CREATE OR REPLACE FUNCTION update_topic_search_vector() RETURNS trigger AS $$
BEGIN
    NEW.search_vector := to_tsvector('english', COALESCE(NEW.title, '') || ' ' || COALESCE(NEW.description, ''));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_topic_search_vector
    BEFORE INSERT OR UPDATE OF title, description ON topics
    FOR EACH ROW EXECUTE FUNCTION update_topic_search_vector();

-- Re-index with the old simple formula.
UPDATE topics SET search_vector =
    to_tsvector('english', COALESCE(title, '') || ' ' || COALESCE(description, ''));

-- Drop last_active_at.
DROP INDEX IF EXISTS idx_topics_last_active;
ALTER TABLE topics DROP COLUMN IF EXISTS last_active_at;
