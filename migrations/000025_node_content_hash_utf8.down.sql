-- 000025_node_content_hash_utf8.down.sql
-- Restore the original set_content_hash body (::bytea cast). This
-- reintroduces BUG-034 but is provided for migrate-down parity.
CREATE OR REPLACE FUNCTION set_content_hash() RETURNS trigger AS $$
BEGIN
    NEW.content_hash := encode(sha256(NEW.content::bytea), 'hex');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
