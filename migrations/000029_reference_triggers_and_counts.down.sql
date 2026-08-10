-- 000029_reference_triggers_and_counts.down.sql

DROP TRIGGER IF EXISTS trg_invalidate_reference_cache_node_update ON nodes;
DROP TRIGGER IF EXISTS trg_invalidate_reference_cache_node_insert ON nodes;
DROP FUNCTION IF EXISTS invalidate_reference_cache_for_node();

DROP TRIGGER IF EXISTS trg_update_topic_ref_count ON node_resolved_refs;
DROP FUNCTION IF EXISTS update_topic_ref_count();

DROP TRIGGER IF EXISTS trg_update_node_resolved_ref_count ON node_resolved_refs;
DROP FUNCTION IF EXISTS update_node_resolved_ref_count();

ALTER TABLE topics DROP COLUMN IF EXISTS ref_count;
ALTER TABLE nodes DROP COLUMN IF EXISTS resolved_ref_count;
