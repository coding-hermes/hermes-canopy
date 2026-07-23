-- 000003_nodes.down.sql
-- Drop nodes table and its functions/triggers.

DROP TRIGGER IF EXISTS trg_node_edited_at    ON nodes;
DROP TRIGGER IF EXISTS trg_node_sequence     ON nodes;

DROP FUNCTION IF EXISTS set_edited_at();
DROP FUNCTION IF EXISTS set_node_sequence();

DROP TABLE IF EXISTS nodes CASCADE;
