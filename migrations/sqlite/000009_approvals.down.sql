-- 000009_approvals.down.sql — SQLite translation of ../../000009_approvals.down.sql
--
-- PG drops the trigger, the three functions, the ALTER-added FK constraint, the three
-- tables and finally the three enums. In SQLite:
--   * DROP TRIGGER takes no ON <table> clause (verified: "near ON: syntax error").
--   * the PL/pgSQL functions never existed here (wave-2 Go obligations — see the .up.sql).
--   * fk_approvals_rule is declared INLINE on approvals.auto_rule_id, so it disappears
--     with the approvals table; there is no ALTER TABLE .. DROP CONSTRAINT in SQLite.
--   * the enums are CHECK constraints on the tables and go away with them.
-- Children before parents, matching the PG order.

DROP TRIGGER IF EXISTS trg_approval_rules_updated;
DROP TABLE IF EXISTS approval_audit_log;
DROP TABLE IF EXISTS approval_rules;
DROP TABLE IF EXISTS approvals;
