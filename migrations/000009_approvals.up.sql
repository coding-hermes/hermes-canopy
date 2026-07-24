-- 000009_approvals.up.sql
-- Approval state machine: pending → approved / denied / expired.
-- SPEC-DM-03 §3. Runs after 000008_users_profiles.

-- ============================================================
-- Enums
-- ============================================================

-- Approval state enum
DO $$ BEGIN
    CREATE TYPE approval_status AS ENUM (
        'pending',
        'approved',
        'denied',
        'expired'
    );
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

-- Approval scope type enum (for rules)
DO $$ BEGIN
    CREATE TYPE rule_scope_type AS ENUM (
        'thread',
        'user',
        'profile',
        'action_type'
    );
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

-- Audit action enum
DO $$ BEGIN
    CREATE TYPE audit_action AS ENUM (
        'approval_requested',
        'approval_granted',
        'approval_denied',
        'approval_expired',
        'rule_created',
        'rule_updated',
        'rule_deleted',
        'rule_auto_approved',
        'rule_auto_denied'
    );
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

-- ============================================================
-- approvals: Pending and decided agent actions
-- ============================================================
CREATE TABLE IF NOT EXISTS approvals (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    node_id         uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    owner_id        uuid NOT NULL,                             -- who must approve (tree owner for MVP)
    requested_by    uuid NOT NULL,                             -- which profile/agent requested action
    status          approval_status NOT NULL DEFAULT 'pending',
    denied_reason   text,                                      -- mandatory on deny, CHECK constraint below
    auto_rule_id    uuid,                                      -- FK added after rules table creation
    decided_by      uuid,                                      -- user who approved/denied (null if auto or pending)
    created_at      timestamptz NOT NULL DEFAULT now(),
    decided_at      timestamptz,                               -- null until decision
    expires_at      timestamptz NOT NULL DEFAULT (now() + INTERVAL '7 days'),

    -- One approval per node (enforced: no duplicate pending for same node)
    CONSTRAINT uq_approvals_node UNIQUE (node_id),
    -- Denied reason mandatory when denied
    CONSTRAINT ck_denied_reason_required CHECK (
        (status = 'denied' AND denied_reason IS NOT NULL AND length(trim(denied_reason)) > 0)
        OR (status != 'denied')
    ),
    -- Decided fields required when not pending
    CONSTRAINT ck_decided_fields CHECK (
        (status IN ('approved', 'denied', 'expired') AND decided_at IS NOT NULL AND decided_by IS NOT NULL)
        OR (status = 'pending' AND decided_at IS NULL AND decided_by IS NULL)
    ),
    -- Expired must be decided
    CONSTRAINT ck_expired_no_decider CHECK (
        status != 'expired' OR decided_by IS NULL  -- system-decided, no human decider
    )
);

-- Indexes
CREATE INDEX IF NOT EXISTS idx_approvals_tree_status   ON approvals(tree_id, status);
CREATE INDEX IF NOT EXISTS idx_approvals_owner_pending  ON approvals(owner_id, status) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_approvals_node           ON approvals(node_id);
CREATE INDEX IF NOT EXISTS idx_approvals_expires        ON approvals(expires_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_approvals_created        ON approvals(created_at DESC);

-- ============================================================
-- approval_rules: Auto-approval rule definitions
-- ============================================================
CREATE TABLE IF NOT EXISTS approval_rules (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    tree_id         uuid NOT NULL REFERENCES trees(id) ON DELETE CASCADE,
    owner_id        uuid NOT NULL,                             -- who created this rule
    scope_type      rule_scope_type NOT NULL,                  -- what this rule applies to
    scope_target    uuid NOT NULL,                             -- thread_id, user_id, or profile_id
    decision        approval_status NOT NULL,                  -- 'approved' or 'denied' (not 'pending'/'expired')
    priority        int NOT NULL DEFAULT 0,                    -- higher = more specific, wins conflicts
    is_active       boolean NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- Decision must be 'approved' or 'denied' (rules don't pend or expire)
    CONSTRAINT ck_rule_decision CHECK (decision IN ('approved', 'denied')),
    -- No duplicate rules for same scope
    CONSTRAINT uq_rule_scope UNIQUE (tree_id, scope_type, scope_target)
);

CREATE INDEX IF NOT EXISTS idx_rules_tree_active ON approval_rules(tree_id, is_active) WHERE is_active = true;
CREATE INDEX IF NOT EXISTS idx_rules_target      ON approval_rules(scope_type, scope_target);

-- Add FK for auto_rule_id after rules table exists
ALTER TABLE approvals ADD CONSTRAINT fk_approvals_rule
    FOREIGN KEY (auto_rule_id) REFERENCES approval_rules(id) ON DELETE SET NULL;

-- ============================================================
-- approval_audit_log: Immutable audit trail
-- ============================================================
CREATE TABLE IF NOT EXISTS approval_audit_log (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    approval_id     uuid NOT NULL REFERENCES approvals(id) ON DELETE CASCADE,
    action          audit_action NOT NULL,
    actor           uuid,                                      -- who took the action (NULL for system actions)
    previous_status approval_status,                           -- state before this action
    new_status      approval_status,                           -- state after this action
    details         jsonb NOT NULL DEFAULT '{}',               -- full context snapshot at time of action
    created_at      timestamptz NOT NULL DEFAULT now()

    -- IMMUTABLE: UPDATE and DELETE are forbidden on this table
    -- Enforced at application layer (REVOKE UPDATE, DELETE in migration)
);

CREATE INDEX IF NOT EXISTS idx_audit_approval   ON approval_audit_log(approval_id, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_tree       ON approval_audit_log((details->>'tree_id'), created_at);
CREATE INDEX IF NOT EXISTS idx_audit_actor      ON approval_audit_log(actor, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_created    ON approval_audit_log(created_at DESC);

-- Revoke UPDATE/DELETE to enforce immutability at DB level
REVOKE UPDATE, DELETE ON approval_audit_log FROM PUBLIC;
REVOKE UPDATE, DELETE ON approval_audit_log FROM canopy_app;

-- ============================================================
-- Triggers
-- ============================================================

-- updated_at trigger for approval_rules
CREATE OR REPLACE FUNCTION update_approval_rule_timestamp()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_approval_rules_updated ON approval_rules;
CREATE TRIGGER trg_approval_rules_updated
    BEFORE UPDATE ON approval_rules
    FOR EACH ROW EXECUTE FUNCTION update_approval_rule_timestamp();

-- Auto-expire: function for periodic expiration job
CREATE OR REPLACE FUNCTION expire_pending_approvals()
RETURNS TABLE (
    expired_id      uuid,
    expired_tree_id uuid
) AS $$
    UPDATE approvals
    SET status = 'expired',
        decided_at = now(),
        decided_by = NULL  -- system action, no human decider
    WHERE status = 'pending'
      AND expires_at <= now()
    RETURNING id, tree_id;
$$ LANGUAGE sql;
