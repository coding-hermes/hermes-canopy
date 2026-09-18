-- 000002_trees.down.sql — SQLite translation of ../../000002_trees.down.sql
--
-- SQLite has no `DROP TABLE .. CASCADE` (verified: "near CASCADE: syntax error").
-- Dropping the table removes its indexes and triggers automatically, which is exactly
-- what CASCADE is doing in the PG file here.

DROP INDEX IF EXISTS idx_trees_owner;
DROP TABLE IF EXISTS trees;
