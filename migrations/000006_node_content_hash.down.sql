DROP TRIGGER IF EXISTS trg_node_content_hash ON nodes;
DROP FUNCTION IF EXISTS set_content_hash();
ALTER TABLE nodes DROP COLUMN IF EXISTS content_hash;
