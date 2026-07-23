-- 000006_node_content_hash.up.sql
ALTER TABLE nodes ADD COLUMN content_hash text;
UPDATE nodes SET content_hash = encode(sha256(content::bytea), 'hex') WHERE content_hash IS NULL;
ALTER TABLE nodes ALTER COLUMN content_hash SET NOT NULL;
CREATE OR REPLACE FUNCTION set_content_hash() RETURNS trigger AS $$
BEGIN
    NEW.content_hash := encode(sha256(NEW.content::bytea), 'hex');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trg_node_content_hash
    BEFORE INSERT OR UPDATE OF content ON nodes
    FOR EACH ROW EXECUTE FUNCTION set_content_hash();
