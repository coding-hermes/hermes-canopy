-- 000006_node_content_hash.down.sql — SQLite translation of ../../000006_node_content_hash.down.sql
--
-- PG drops the trigger, the function and then the column. In SQLite neither the trigger
-- nor the function exists (they are wave-2 Go obligations — see the .up.sql header), so
-- only the column drop remains. SQLite supports ALTER TABLE .. DROP COLUMN (verified),
-- and nothing in the schema references content_hash.

ALTER TABLE nodes DROP COLUMN content_hash;
