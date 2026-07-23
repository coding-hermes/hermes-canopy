-- 000001_extensions.down.sql
-- Drop the fallback uuidv7() function. Extensions (pgcrypto, pg_uuidv7)
-- are intentionally NOT dropped — they may be used by other tooling and
-- are cheap to leave installed.

DROP FUNCTION IF EXISTS uuidv7();
