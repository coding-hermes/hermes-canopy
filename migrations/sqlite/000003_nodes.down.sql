-- 000003_nodes.down.sql — SQLite translation of ../../000003_nodes.down.sql
--
-- PG drops the two triggers with `DROP TRIGGER .. ON nodes` and then the two PL/pgSQL
-- functions. The SQLite equivalents:
--   * `DROP TRIGGER IF EXISTS set_edited_at()`-style function drops: SQLite has no
--     functions, so there is nothing to drop.
--   * DROP TRIGGER takes no ON <table> clause in SQLite (verified: "near ON: syntax error").
--   * set_node_sequence()/trg_node_sequence were never created in the SQLite DDL
--     (wave-2 Go write-path obligation, see the .up.sql header).

DROP TRIGGER IF EXISTS trg_node_edited_at;
DROP TABLE IF EXISTS nodes;
