-- 000025_node_content_hash_utf8.up.sql
-- BUG-034: set_content_hash trigger used NEW.content::bytea, which parses
-- the TEXT value as bytea literal syntax. Content containing backslashes
-- or \x escape sequences (e.g. tool output from session imports) throws
-- 22P02 invalid input syntax for type bytea. Fix: convert_to(content,'UTF8')
-- converts TEXT directly to bytea without literal-syntax parsing.
CREATE OR REPLACE FUNCTION set_content_hash() RETURNS trigger AS $$
BEGIN
    NEW.content_hash := encode(sha256(convert_to(NEW.content, 'UTF8')), 'hex');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
