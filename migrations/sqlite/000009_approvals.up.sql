-- 000009_approvals.up.sql — SQLite translation of ../../000009_approvals.up.sql
--
-- Translation rules applied (docs/SQLITE-PIVOT.md):
--   * PG enums -> TEXT + CHECK (col IN (..)), same value lists:
--       approval_status   (../../000009_approvals.up.sql:11-16)   -> approvals.status,
--                                                                    approval_audit_log.previous_status/new_status,
--                                                                    approval_rules.decision (narrowed, see below)
--       rule_scope_type   (:23-28)                                -> approval_rules.scope_type
--       audit_action      (:35-45)                                -> approval_audit_log.action
--   * boolean -> INTEGER 0/1 + CHECK; timestamptz -> TEXT RFC3339;
--     jsonb -> TEXT + CHECK (json_valid(..)); `int` -> INTEGER;
--     `now() + INTERVAL '7 days'` -> strftime(..,'+7 days').
--   * uuid ids carry no default (PG DEFAULT uuidv7() at :54,97,125) — wave-2 Go
--     write-path obligation.
--   * 000009:118 `ALTER TABLE approvals ADD CONSTRAINT fk_approvals_rule FOREIGN KEY ..`
--     has no SQLite form (SQLite cannot ADD CONSTRAINT), so the FK is declared INLINE on
--     approvals.auto_rule_id below. SQLite accepts a reference to a table created later
--     in the same batch (verified) and resolves it at DML time, so the PG statement order
--     is preserved here.
--
-- NOT TRANSLATABLE — recorded here, never silently dropped:
--   * `REVOKE UPDATE, DELETE ON approval_audit_log FROM PUBLIC;` (../../000009_approvals.up.sql:144)
--     SQLite has no role/privilege model, so there is no equivalent statement. Note that
--     this PG clause is effectively a no-op even in PostgreSQL: a freshly created table
--     grants nothing to PUBLIC (only the owner has privileges), and the file's own
--     comment says enforcement is at the application layer (:135). Wave-2 owner: the
--     audit-log writer (internal/db audit_repo.go) — if real immutability is wanted in
--     SQLite it must be a BEFORE UPDATE/BEFORE DELETE trigger using RAISE(ABORT, ..),
--     which IS expressible here; that is a new guarantee, not a translation, so it is
--     listed as an OPEN DECISION instead of being invented in this wave.
--   * `expire_pending_approvals()` (../../000009_approvals.up.sql:166-178)
--     A SQL-language function whose body is `UPDATE approvals SET status='expired', .. RETURNING id, tree_id`.
--     SQLite has no stored functions. The statement itself IS expressible and is the
--     wave-2 Go obligation: the expiry job issues exactly
--       UPDATE approvals SET status='expired',
--              decided_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), decided_by=NULL
--        WHERE status='pending' AND expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')
--       RETURNING id, tree_id;
--     (UPDATE .. RETURNING is supported by modernc.org/sqlite v1.58.0; verified.)
--   * `update_approval_rule_timestamp()` (:152-158) is translated — its single statement
--     is inlined into trg_approval_rules_updated at the bottom of this file.

CREATE TABLE IF NOT EXISTS approvals (
    id              TEXT    PRIMARY KEY NOT NULL,
    tree_id         TEXT    NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    node_id         TEXT    NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    owner_id        TEXT    NOT NULL,
    requested_by    TEXT    NOT NULL,
    status          TEXT    NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'approved', 'denied', 'expired')),
    denied_reason   TEXT,
    auto_rule_id    TEXT    REFERENCES approval_rules(id) ON DELETE SET NULL,
    decided_by      TEXT,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    decided_at      TEXT,
    expires_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now','+7 days')),

    CONSTRAINT uq_approvals_node UNIQUE (node_id),
    CONSTRAINT ck_denied_reason_required CHECK (
        (status = 'denied' AND denied_reason IS NOT NULL AND length(trim(denied_reason)) > 0)
        OR (status != 'denied')
    ),
    CONSTRAINT ck_decided_fields CHECK (
        (status IN ('approved', 'denied') AND decided_at IS NOT NULL AND decided_by IS NOT NULL)
        OR (status = 'expired' AND decided_at IS NOT NULL)
        OR (status = 'pending' AND decided_at IS NULL AND decided_by IS NULL)
    ),
    CONSTRAINT ck_expired_no_decider CHECK (
        status != 'expired' OR decided_by IS NULL
    )
);

CREATE INDEX IF NOT EXISTS idx_approvals_tree_status   ON approvals(tree_id, status);
CREATE INDEX IF NOT EXISTS idx_approvals_owner_pending  ON approvals(owner_id, status) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_approvals_node           ON approvals(node_id);
CREATE INDEX IF NOT EXISTS idx_approvals_expires        ON approvals(expires_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_approvals_created        ON approvals(created_at DESC);

CREATE TABLE IF NOT EXISTS approval_rules (
    id              TEXT    PRIMARY KEY NOT NULL,
    tree_id         TEXT    NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    owner_id        TEXT    NOT NULL,
    scope_type      TEXT    NOT NULL
                            CHECK (scope_type IN ('thread', 'user', 'profile', 'action_type')),
    scope_target    TEXT    NOT NULL,
    decision        TEXT    NOT NULL CHECK (decision IN ('approved', 'denied')),
    priority        INTEGER NOT NULL DEFAULT 0,
    is_active       INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0,1)),
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),

    CONSTRAINT ck_rule_decision CHECK (decision IN ('approved', 'denied')),
    CONSTRAINT uq_rule_scope UNIQUE (tree_id, scope_type, scope_target)
);

CREATE INDEX IF NOT EXISTS idx_rules_tree_active ON approval_rules(tree_id, is_active) WHERE is_active = 1;
CREATE INDEX IF NOT EXISTS idx_rules_target      ON approval_rules(scope_type, scope_target);

CREATE TABLE IF NOT EXISTS approval_audit_log (
    id              TEXT    PRIMARY KEY NOT NULL,
    approval_id     TEXT    NOT NULL REFERENCES approvals(id) ON DELETE CASCADE,
    action          TEXT    NOT NULL CHECK (action IN (
                        'approval_requested', 'approval_granted', 'approval_denied', 'approval_expired',
                        'rule_created', 'rule_updated', 'rule_deleted',
                        'rule_auto_approved', 'rule_auto_denied'
                    )),
    actor           TEXT,
    previous_status TEXT    CHECK (previous_status IS NULL OR
                                   previous_status IN ('pending', 'approved', 'denied', 'expired')),
    new_status      TEXT    CHECK (new_status IS NULL OR
                                   new_status IN ('pending', 'approved', 'denied', 'expired')),
    details         TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(details)),
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- PG's idx_audit_tree is an expression index on ((details->>'tree_id'), created_at).
-- SQLite's equivalent uses json_extract (a deterministic function, so it is index-legal;
-- verified on modernc.org/sqlite v1.58.0).
CREATE INDEX IF NOT EXISTS idx_audit_approval   ON approval_audit_log(approval_id, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_tree       ON approval_audit_log(json_extract(details, '$.tree_id'), created_at);
CREATE INDEX IF NOT EXISTS idx_audit_actor      ON approval_audit_log(actor, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_created    ON approval_audit_log(created_at DESC);

-- update_approval_rule_timestamp() (:152-158) + trg_approval_rules_updated (:161-163),
-- with the function body inlined (SQLite has no functions).
CREATE TRIGGER trg_approval_rules_updated
    AFTER UPDATE ON approval_rules
    FOR EACH ROW
BEGIN
    UPDATE approval_rules
       SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
     WHERE id = NEW.id;
END;
