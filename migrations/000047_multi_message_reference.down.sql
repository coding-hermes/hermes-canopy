-- 000047_multi_message_reference.down.sql
-- Reverses 000047_multi_message_reference.up.sql (SPEC-PL-06 §3.1).
DROP INDEX IF EXISTS idx_nodes_multi_reference;
DROP INDEX IF EXISTS idx_edges_active_reference_target;
ALTER TABLE nodes DROP CONSTRAINT IF EXISTS chk_multi_reference_has_display_parent;
ALTER TABLE nodes DROP CONSTRAINT IF EXISTS chk_nodes_parent_mode;
ALTER TABLE nodes DROP COLUMN IF EXISTS parent_mode;
