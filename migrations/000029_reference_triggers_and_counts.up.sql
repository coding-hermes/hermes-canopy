-- 000029_reference_triggers_and_counts.up.sql
-- SPEC-TM-04 §3.4 (cache invalidation trigger), §3.5 (topics.ref_count),
-- §3.6 (nodes.resolved_ref_count).
-- node_resolved_refs table already exists from migration 000021.

-- ── §3.4: Reference Cache Invalidation Trigger ──────────────────────────
-- When a node is inserted/updated/soft-deleted inside a topic scope,
-- invalidate the cached TopicContext for all topics containing that node.

CREATE OR REPLACE FUNCTION invalidate_reference_cache_for_node() RETURNS trigger AS $$
DECLARE
    affected_topic_ids uuid[];
BEGIN
    SELECT array_agg(DISTINCT tmn.topic_id) INTO affected_topic_ids
    FROM topic_member_nodes tmn
    WHERE tmn.node_id = COALESCE(NEW.id, OLD.id);

    IF affected_topic_ids IS NOT NULL THEN
        DELETE FROM reference_resolution_cache
        WHERE topic_id = ANY(affected_topic_ids);
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_invalidate_reference_cache_node_insert
    AFTER INSERT ON nodes
    FOR EACH ROW
    EXECUTE FUNCTION invalidate_reference_cache_for_node();

CREATE TRIGGER trg_invalidate_reference_cache_node_update
    AFTER UPDATE OF content, deleted_at ON nodes
    FOR EACH ROW
    WHEN (OLD.content IS DISTINCT FROM NEW.content OR OLD.deleted_at IS DISTINCT FROM NEW.deleted_at)
    EXECUTE FUNCTION invalidate_reference_cache_for_node();

-- ── §3.5: Topic Reference Count Column + Trigger ────────────────────────

ALTER TABLE topics
    ADD COLUMN IF NOT EXISTS ref_count integer NOT NULL DEFAULT 0;

CREATE OR REPLACE FUNCTION update_topic_ref_count() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        UPDATE topics SET ref_count = ref_count + 1 WHERE id = NEW.topic_id;
    ELSIF TG_OP = 'DELETE' THEN
        UPDATE topics SET ref_count = GREATEST(ref_count - 1, 0) WHERE id = OLD.topic_id;
    ELSIF TG_OP = 'UPDATE' AND OLD.topic_id IS DISTINCT FROM NEW.topic_id THEN
        UPDATE topics SET ref_count = GREATEST(ref_count - 1, 0) WHERE id = OLD.topic_id;
        UPDATE topics SET ref_count = ref_count + 1 WHERE id = NEW.topic_id;
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_update_topic_ref_count
    AFTER INSERT OR DELETE OR UPDATE OF topic_id ON node_resolved_refs
    FOR EACH ROW
    EXECUTE FUNCTION update_topic_ref_count();

-- ── §3.6: Node Resolved Reference Count Column + Trigger ────────────────

ALTER TABLE nodes
    ADD COLUMN IF NOT EXISTS resolved_ref_count integer NOT NULL DEFAULT 0;

CREATE OR REPLACE FUNCTION update_node_resolved_ref_count() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        UPDATE nodes SET resolved_ref_count = resolved_ref_count + 1 WHERE id = NEW.node_id;
    ELSIF TG_OP = 'DELETE' THEN
        UPDATE nodes SET resolved_ref_count = GREATEST(resolved_ref_count - 1, 0) WHERE id = OLD.node_id;
    END IF;

    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_update_node_resolved_ref_count
    AFTER INSERT OR DELETE ON node_resolved_refs
    FOR EACH ROW
    EXECUTE FUNCTION update_node_resolved_ref_count();

-- ── Backfill: sync counts from existing node_resolved_refs rows ─────────
-- (Migration 000021 created the table, but ref_count columns are new.)
-- Use correlated subqueries so the backfill is idempotent.

UPDATE topics t
SET ref_count = COALESCE((
    SELECT COUNT(*) FROM node_resolved_refs nrr WHERE nrr.topic_id = t.id
), 0)
WHERE t.ref_count != COALESCE((
    SELECT COUNT(*) FROM node_resolved_refs nrr WHERE nrr.topic_id = t.id
), 0);

UPDATE nodes n
SET resolved_ref_count = COALESCE((
    SELECT COUNT(*) FROM node_resolved_refs nrr WHERE nrr.node_id = n.id
), 0)
WHERE n.resolved_ref_count != COALESCE((
    SELECT COUNT(*) FROM node_resolved_refs nrr WHERE nrr.node_id = n.id
), 0);
