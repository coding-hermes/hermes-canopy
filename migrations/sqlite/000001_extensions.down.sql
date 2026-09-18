-- 000001_extensions.down.sql — SQLite translation of ../../000001_extensions.down.sql
--
-- NO-OP BY DESIGN: nothing was created by this migration's .up.sql.
--
-- The PG counterpart runs `DROP FUNCTION IF EXISTS uuidv7();` (../../000001_extensions.down.sql:6).
-- SQLite never had that function, and pgcrypto / pg_uuidv7 were never installed, so there
-- is nothing to drop. The uuidv7 creation path is owned by the Go write layer in wave 2
-- (docs/SQLITE-PIVOT.md).

-- (intentionally empty)
