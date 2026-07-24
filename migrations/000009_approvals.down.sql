-- 000009_approvals.down.sql
-- Drop approvals, approval_rules, and approval_audit_log tables.

DROP TRIGGER IF EXISTS trg_approval_rules_updated ON approval_rules;
DROP FUNCTION IF EXISTS update_approval_rule_timestamp();
DROP FUNCTION IF EXISTS expire_pending_approvals();
ALTER TABLE approvals DROP CONSTRAINT IF EXISTS fk_approvals_rule;
DROP TABLE IF EXISTS approval_audit_log CASCADE;
DROP TABLE IF EXISTS approval_rules CASCADE;
DROP TABLE IF EXISTS approvals CASCADE;
DROP TYPE IF EXISTS audit_action;
DROP TYPE IF EXISTS rule_scope_type;
DROP TYPE IF EXISTS approval_status;
