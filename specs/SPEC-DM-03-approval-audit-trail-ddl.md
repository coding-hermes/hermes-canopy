# SPEC-DM-03 — Approval & Audit Trail DDL

> **Status:** Spec | **Blocks:** SPEC-DM-04, Phase 3 (API), Phase 4 (Backend)
> **Prerequisite:** SPEC-DM-01 (nodes/edges/trees DDL)

---

## 1. Purpose

Define the exact PostgreSQL DDL, Go structs, TypeScript types, approval state machine, auto-approval rule engine, and immutable audit trail for Canopy's approval system. A worker reading this spec must produce the correct database layer, Go repository, TypeScript types, approval engine, and audit logger with zero clarifying questions.

The approval system is a **safety-critical control surface** — the agent listens to all input but only acts on owner-approved input. This spec defines the data model that enforces that invariant.

---

## 2. Design Decisions (from ARCHITECTURE.md)

| Decision | Choice | Source |
|----------|--------|--------|
| Approval model | Per-message, per-thread, per-user granularity | ARCHITECTURE.md §8.2 |
| Agent stance | Agent LISTENS to all input, only ACTS on approved input | ARCHITECTURE.md §8.2 |
| Auto-approval | Rule engine: most-specific-wins conflict resolution | ARCHITECTURE.md §8.4 |
| Audit trail | Immutable append-only log, never deleted | ARCHITECTURE.md §8.3 |
| Denial | Mandatory reason text (not nullable) | T1.5 §2.2.4 |
| Expiration | Auto-deny after configurable N days | T1.5 §8 Q1 |
| Authoritative DB | PostgreSQL 17+, pgx v5, golang-migrate | ARCHITECTURE.md §2.3, §2.1 |
| IDs | UUIDv7 (time-ordered, time-sortable) | SPEC-DM-01 §3.1 |
| Single-user MVP | Owner is sole approver. Multi-user approval chains post-MVP | ARCHITECTURE.md §8, §11 |
| Separation of duties | Schema supports future multi-approver chains | T1.5 §1.4 |
| Owner field | `tree.owner_id` present from SPEC-DM-01 day one | SPEC-DM-01 §2 |

---

## 3. PostgreSQL DDL

### 3.1 Approvals Table

```sql
-- 000003_approvals.up.sql

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

-- Auto-expire: mark pending approvals as expired when expires_at passes
-- This runs as a periodic job (cron or pg_cron), not a trigger, but the
-- function is defined here for the migration.
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
```

### 3.2 Down Migration

```sql
-- 000003_approvals.down.sql
DROP TRIGGER IF EXISTS trg_approval_rules_updated ON approval_rules;
DROP FUNCTION IF EXISTS update_approval_rule_timestamp();
DROP FUNCTION IF EXISTS expire_pending_approvals();
ALTER TABLE approvals DROP CONSTRAINT IF EXISTS fk_approvals_rule;
DROP TABLE IF EXISTS approval_audit_log;
DROP TABLE IF EXISTS approval_rules;
DROP TABLE IF EXISTS approvals;
DROP TYPE IF EXISTS audit_action;
DROP TYPE IF EXISTS rule_scope_type;
DROP TYPE IF EXISTS approval_status;
```

---

## 4. Go Structs & Repository Interface

### 4.1 Domain Types

```go
package approval

import (
    "time"
    "github.com/google/uuid"
)

// ApprovalStatus represents the lifecycle of an approval request.
type ApprovalStatus string

const (
    ApprovalPending  ApprovalStatus = "pending"
    ApprovalApproved ApprovalStatus = "approved"
    ApprovalDenied   ApprovalStatus = "denied"
    ApprovalExpired  ApprovalStatus = "expired"
)

func (s ApprovalStatus) Valid() bool {
    switch s {
    case ApprovalPending, ApprovalApproved, ApprovalDenied, ApprovalExpired:
        return true
    }
    return false
}

func (s ApprovalStatus) IsTerminal() bool {
    return s == ApprovalApproved || s == ApprovalDenied || s == ApprovalExpired
}

// RuleScopeType defines what a rule applies to.
type RuleScopeType string

const (
    ScopeThread     RuleScopeType = "thread"
    ScopeUser       RuleScopeType = "user"
    ScopeProfile    RuleScopeType = "profile"
    ScopeActionType RuleScopeType = "action_type"
)

// AuditAction records what happened.
type AuditAction string

const (
    AuditRequested    AuditAction = "approval_requested"
    AuditGranted      AuditAction = "approval_granted"
    AuditDenied       AuditAction = "approval_denied"
    AuditExpired      AuditAction = "approval_expired"
    AuditRuleCreated  AuditAction = "rule_created"
    AuditRuleUpdated  AuditAction = "rule_updated"
    AuditRuleDeleted  AuditAction = "rule_deleted"
    AuditAutoApproved AuditAction = "rule_auto_approved"
    AuditAutoDenied   AuditAction = "rule_auto_denied"
)
```

### 4.2 Data Structs

```go
// Approval represents a pending or decided agent action.
type Approval struct {
    ID           uuid.UUID       `json:"id"`
    TreeID       uuid.UUID       `json:"tree_id"`
    NodeID       uuid.UUID       `json:"node_id"`
    OwnerID      uuid.UUID       `json:"owner_id"`
    RequestedBy  uuid.UUID       `json:"requested_by"`
    Status       ApprovalStatus  `json:"status"`
    DeniedReason *string         `json:"denied_reason,omitempty"`
    AutoRuleID   *uuid.UUID      `json:"auto_rule_id,omitempty"`
    DecidedBy    *uuid.UUID      `json:"decided_by,omitempty"`
    CreatedAt    time.Time       `json:"created_at"`
    DecidedAt    *time.Time      `json:"decided_at,omitempty"`
    ExpiresAt    time.Time       `json:"expires_at"`
}

// ApprovalRule defines an auto-approval or auto-denial rule.
type ApprovalRule struct {
    ID          uuid.UUID       `json:"id"`
    TreeID      uuid.UUID       `json:"tree_id"`
    OwnerID     uuid.UUID       `json:"owner_id"`
    ScopeType   RuleScopeType   `json:"scope_type"`
    ScopeTarget uuid.UUID       `json:"scope_target"`
    Decision    ApprovalStatus  `json:"decision"`  // always 'approved' or 'denied'
    Priority    int             `json:"priority"`
    IsActive    bool            `json:"is_active"`
    CreatedAt   time.Time       `json:"created_at"`
    UpdatedAt   time.Time       `json:"updated_at"`
}

// AuditEntry is an immutable record of an approval action.
type AuditEntry struct {
    ID             uuid.UUID       `json:"id"`
    ApprovalID     uuid.UUID       `json:"approval_id"`
    Action         AuditAction     `json:"action"`
    Actor          *uuid.UUID      `json:"actor,omitempty"`
    PreviousStatus *ApprovalStatus `json:"previous_status,omitempty"`
    NewStatus      *ApprovalStatus `json:"new_status,omitempty"`
    Details        json.RawMessage `json:"details"`
    CreatedAt      time.Time       `json:"created_at"`
}

// AuditDetail contains the full context snapshot stored in audit.details.
type AuditDetail struct {
    TreeID      uuid.UUID       `json:"tree_id"`
    NodeID      uuid.UUID       `json:"node_id"`
    RequestedBy uuid.UUID       `json:"requested_by"`
    OwnerID     uuid.UUID       `json:"owner_id"`
    RuleID      *uuid.UUID      `json:"rule_id,omitempty"`
    DenyReason  *string         `json:"deny_reason,omitempty"`
    Context     string          `json:"context"`  // first 500 chars of node content
}
```

### 4.3 Repository Interface

```go
// ApprovalRepo manages the approval lifecycle.
type ApprovalRepo interface {
    // Create creates a new pending approval for an agent's proposed action.
    Create(ctx context.Context, approval *Approval) error

    // GetByID retrieves an approval by ID.
    GetByID(ctx context.Context, id uuid.UUID) (*Approval, error)

    // GetByNodeID retrieves the approval for a specific node (at most one).
    GetByNodeID(ctx context.Context, nodeID uuid.UUID) (*Approval, error)

    // ListPending returns all pending approvals for an owner, sorted by created_at desc.
    ListPending(ctx context.Context, ownerID uuid.UUID, limit, offset int) ([]*Approval, int, error)

    // ListByTree returns all approvals for a tree, optionally filtered by status.
    ListByTree(ctx context.Context, treeID uuid.UUID, status *ApprovalStatus, limit, offset int) ([]*Approval, int, error)

    // Approve marks an approval as approved. Returns the updated approval.
    // actorID is the human who approved (owner or delegated admin).
    // If triggered by an auto-rule, pass ruleID.
    Approve(ctx context.Context, id, actorID uuid.UUID, ruleID *uuid.UUID) (*Approval, error)

    // Deny marks an approval as denied. reason is mandatory.
    Deny(ctx context.Context, id, actorID uuid.UUID, reason string) (*Approval, error)

    // ExpirePending marks all pending approvals past their expires_at as expired.
    // Returns the list of expired approval IDs for SSE event emission.
    ExpirePending(ctx context.Context) ([]uuid.UUID, error)

    // CountPending returns the count of pending approvals for an owner.
    CountPending(ctx context.Context, ownerID uuid.UUID) (int, error)
}

// ApprovalRuleRepo manages auto-approval rules.
type ApprovalRuleRepo interface {
    Create(ctx context.Context, rule *ApprovalRule) error
    GetByID(ctx context.Context, id uuid.UUID) (*ApprovalRule, error)
    Update(ctx context.Context, rule *ApprovalRule) error
    Delete(ctx context.Context, id uuid.UUID) error
    SoftDelete(ctx context.Context, id uuid.UUID) error // sets is_active=false

    // ListActive returns all active rules for a tree, sorted by priority desc.
    ListActive(ctx context.Context, treeID uuid.UUID) ([]*ApprovalRule, error)

    // FindMatch finds the highest-priority rule matching scope_type + scope_target.
    // Returns nil if no active rule matches.
    FindMatch(ctx context.Context, treeID uuid.UUID, scopeType RuleScopeType, scopeTarget uuid.UUID) (*ApprovalRule, error)

    // EvaluateAll matches a node against all active rules for its tree.
    // Returns the winning rule (if any) and its decision.
    // Conflict resolution: most-specific wins (thread > user > profile > action_type).
    EvaluateAll(ctx context.Context, treeID uuid.UUID, node *NodeContext) (*ApprovalRule, error)
}
```

### 4.4 NodeContext (for rule evaluation)

```go
// NodeContext carries the information an approval rule needs to evaluate a node.
type NodeContext struct {
    NodeID      uuid.UUID
    TreeID      uuid.UUID
    ThreadID    uuid.UUID  // the topic/thread this node belongs to
    AuthorID    uuid.UUID  // who wrote this (profile or user)
    ProfileID   *uuid.UUID // if authored by a Hermes profile
}
```

### 4.5 Approval Service (Engine)

```go
// ApprovalService orchestrates the approval lifecycle with audit logging.
type ApprovalService struct {
    approvals ApprovalRepo
    rules     ApprovalRuleRepo
    audit     AuditRepo
}

func NewApprovalService(ar ApprovalRepo, rr ApprovalRuleRepo, au AuditRepo) *ApprovalService {
    return &ApprovalService{approvals: ar, rules: rr, audit: au}
}

// RequestApproval creates a pending approval for an agent's proposed action.
// First checks auto-approval rules. If a rule matches, auto-decides immediately.
// Otherwise, creates a pending approval and logs to audit trail.
func (s *ApprovalService) RequestApproval(ctx context.Context, node *NodeContext) (*Approval, error)

// Approve approves a pending approval. Logs to audit trail.
func (s *ApprovalService) Approve(ctx context.Context, approvalID, actorID uuid.UUID) (*Approval, error)

// Deny denies a pending approval. reason is mandatory.
func (s *ApprovalService) Deny(ctx context.Context, approvalID, actorID uuid.UUID, reason string) (*Approval, error)

// EvaluateRules checks all active rules against a node and returns the winning decision.
// Returns (nil, nil) if no rules match — manual approval required.
func (s *ApprovalService) EvaluateRules(ctx context.Context, node *NodeContext) (*ApprovalRule, error)
```

---

## 5. TypeScript Types

### 5.1 Types

```typescript
// approval.types.ts
import { z } from 'zod';

// ── Enums ──────────────────────────────────────────────────────

export const ApprovalStatus = z.enum([
  'pending',
  'approved',
  'denied',
  'expired',
]);
export type ApprovalStatus = z.infer<typeof ApprovalStatus>;

export const RuleScopeType = z.enum([
  'thread',
  'user',
  'profile',
  'action_type',
]);
export type RuleScopeType = z.infer<typeof RuleScopeType>;

export const AuditAction = z.enum([
  'approval_requested',
  'approval_granted',
  'approval_denied',
  'approval_expired',
  'rule_created',
  'rule_updated',
  'rule_deleted',
  'rule_auto_approved',
  'rule_auto_denied',
]);
export type AuditAction = z.infer<typeof AuditAction>;

// ── Data Types ─────────────────────────────────────────────────

export const ApprovalSchema = z.object({
  id: z.string().uuid(),
  treeId: z.string().uuid(),
  nodeId: z.string().uuid(),
  ownerId: z.string().uuid(),
  requestedBy: z.string().uuid(),
  status: ApprovalStatus,
  deniedReason: z.string().nullable().optional(),
  autoRuleId: z.string().uuid().nullable().optional(),
  decidedBy: z.string().uuid().nullable().optional(),
  createdAt: z.string().datetime(),
  decidedAt: z.string().datetime().nullable().optional(),
  expiresAt: z.string().datetime(),
});
export type Approval = z.infer<typeof ApprovalSchema>;

export const ApprovalRuleSchema = z.object({
  id: z.string().uuid(),
  treeId: z.string().uuid(),
  ownerId: z.string().uuid(),
  scopeType: RuleScopeType,
  scopeTarget: z.string().uuid(),
  decision: z.enum(['approved', 'denied']),
  priority: z.number().int().min(0),
  isActive: z.boolean(),
  createdAt: z.string().datetime(),
  updatedAt: z.string().datetime(),
});
export type ApprovalRule = z.infer<typeof ApprovalRuleSchema>;

export const AuditEntrySchema = z.object({
  id: z.string().uuid(),
  approvalId: z.string().uuid(),
  action: AuditAction,
  actor: z.string().uuid().nullable().optional(),
  previousStatus: ApprovalStatus.nullable().optional(),
  newStatus: ApprovalStatus.nullable().optional(),
  details: z.record(z.unknown()),
  createdAt: z.string().datetime(),
});
export type AuditEntry = z.infer<typeof AuditEntrySchema>;

export const AuditDetailSchema = z.object({
  treeId: z.string().uuid(),
  nodeId: z.string().uuid(),
  requestedBy: z.string().uuid(),
  ownerId: z.string().uuid(),
  ruleId: z.string().uuid().nullable().optional(),
  denyReason: z.string().nullable().optional(),
  context: z.string().max(500),
});
export type AuditDetail = z.infer<typeof AuditDetailSchema>;

// ── API Types ──────────────────────────────────────────────────

export const CreateApprovalRequestSchema = z.object({
  nodeId: z.string().uuid(),
  treeId: z.string().uuid(),
});

export const ApproveRequestSchema = z.object({
  approvalId: z.string().uuid(),
});

export const DenyRequestSchema = z.object({
  approvalId: z.string().uuid(),
  reason: z.string().min(1, 'Denial reason is required'),
});

export const CreateRuleRequestSchema = z.object({
  treeId: z.string().uuid(),
  scopeType: RuleScopeType,
  scopeTarget: z.string().uuid(),
  decision: z.enum(['approved', 'denied']),
  priority: z.number().int().min(0).default(0),
});

export const PendingApprovalsResponseSchema = z.object({
  approvals: z.array(ApprovalSchema),
  total: z.number().int(),
});

export const ApprovalCountSchema = z.object({
  count: z.number().int().min(0),
});
```

### 5.2 Approval State Machine

```typescript
// approval-fsm.ts

/**
 * Approval State Machine
 *
 *           ┌──────────┐
 *           │  pending  │──── expires_at passed ────▶ expired
 *           └────┬──┬───┘
 *                │  │
 *       approve  │  │  deny (reason required)
 *                ▼  ▼
 *        ┌──────┐  ┌──────┐
 *        │approved│  │denied│
 *        └──────┘  └──────┘
 *
 * Transitions:
 *   pending → approved  : owner clicks Approve OR auto-approval rule matches
 *   pending → denied    : owner clicks Deny (with mandatory reason) OR auto-deny rule matches
 *   pending → expired   : expires_at <= now(), system action, no human decider
 *
 * Terminal states: approved, denied, expired — no further transitions allowed.
 */

export type ApprovalTransition =
  | { from: 'pending'; to: 'approved'; by: 'manual' | 'auto_rule' }
  | { from: 'pending'; to: 'denied';   by: 'manual' | 'auto_rule' }
  | { from: 'pending'; to: 'expired';  by: 'system' };

export function canTransition(
  current: ApprovalStatus,
  target: ApprovalStatus
): boolean {
  if (current !== 'pending') return false;
  return target === 'approved' || target === 'denied' || target === 'expired';
}

export function isTerminal(status: ApprovalStatus): boolean {
  return status === 'approved' || status === 'denied' || status === 'expired';
}
```

---

## 6. Auto-Approval Rule Evaluation Algorithm

### 6.1 Conflict Resolution

Rules are evaluated in order of **specificity** (priority). Multiple rules can match a given node. The winning rule is determined by:

```
1. Collect all active, matching rules for (tree_id, node)
2. Sort by priority DESC (higher = more specific)
3. First (highest priority) wins
4. If no rules match → manual approval required
```

### 6.2 Rule Matching Pseudocode

```
function EvaluateRules(treeID, node):
    rules = LoadActiveRules(treeID)          // all active rules for this tree, ordered priority desc
    
    candidates = []
    
    for rule in rules:
        match = false
        switch rule.scope_type:
            case 'thread':
                match = (node.thread_id == rule.scope_target)
            case 'user':
                match = (node.author_id == rule.scope_target)
            case 'profile':
                match = (node.profile_id == rule.scope_target)
            case 'action_type':
                match = (node.action_type == rule.scope_target)
        
        if match:
            candidates.append(rule)
    
    if candidates is empty:
        return null  // no rule matches, manual approval required
    
    // candidates already sorted by priority DESC (from query)
    winning = candidates[0]
    
    return winning.decision  // 'approved' or 'denied'
```

### 6.3 Priority Assignment

| Scope Type | Default Priority | Rationale |
|-----------|-----------------|-----------|
| `thread` | 100 | Most specific — single conversation context |
| `user` | 50 | Per-person trust decision |
| `profile` | 30 | Per-agent-profile trust decision |
| `action_type` | 10 | Least specific — general action category |

When two rules have the same scope type, the one with the higher explicit priority value wins. Users can adjust priority when creating rules.

### 6.4 Auto-Approval Flow

```
Agent proposes action (new node)
    │
    ▼
ApprovalService.RequestApproval(node)
    │
    ├── EvaluateRules(treeID, node)
    │       │
    │       ├── Match found? → decision = 'approved' or 'denied'
    │       │       │
    │       │       ├── approved → auto-approve, log audit (action: rule_auto_approved)
    │       │       └── denied   → auto-deny, log audit (action: rule_auto_denied)
    │       │
    │       └── No match → create pending approval, log audit (action: approval_requested)
    │
    └── Emit SSE event: approval_pending or approval_granted/denied
```

---

## 7. Audit Trail Architecture

### 7.1 Immutability Guarantees

1. **Database-level:** `REVOKE UPDATE, DELETE ON approval_audit_log` — no user or application role can modify past entries
2. **Application-level:** `AuditRepo` only exposes `Create` and `List*` methods — no update or delete operations
3. **Schema-level:** No `updated_at` column — entries are write-once, read-many
4. **Details snapshot:** `details` JSONB captures the full context at the time of the action (tree_id, node_id, requested_by, owner_id, rule_id, context excerpt, deny_reason)

### 7.2 Audit Entry Creation

```go
// AuditRepo logs immutable audit trail entries.
type AuditRepo interface {
    // Create records an audit entry. Called by ApprovalService on every state transition.
    Create(ctx context.Context, entry *AuditEntry) error

    // ListByApproval retrieves the full audit trail for an approval, ordered by created_at.
    ListByApproval(ctx context.Context, approvalID uuid.UUID, limit, offset int) ([]*AuditEntry, int, error)

    // ListByTree retrieves the audit trail for an entire tree, ordered by created_at desc.
    ListByTree(ctx context.Context, treeID uuid.UUID, limit, offset int) ([]*AuditEntry, int, error)

    // Search performs full-text search over audit trail details.
    // Searches: details->>'context', details->>'deny_reason'
    Search(ctx context.Context, treeID uuid.UUID, query string, limit, offset int) ([]*AuditEntry, int, error)
}
```

### 7.3 Audit Event Timeline

For a single approval lifecycle, the audit trail would contain:

```
t0: approval_requested    (pending)     — agent proposed action
t1: approval_granted      (approved)    — owner approved
```

Or with denial:

```
t0: approval_requested    (pending)     — agent proposed action
t1: approval_denied       (denied)      — owner denied with reason "Don't drop production tables"
```

Auto-rule path:

```
t0: rule_auto_approved    (approved)    — auto-approval rule "Always approve in Deploy Logs" matched
```

### 7.4 Filtering & Search

```sql
-- Filter by decision
SELECT * FROM approval_audit_log
WHERE action IN ('approval_granted', 'approval_denied', 'rule_auto_approved', 'rule_auto_denied');

-- Full-text search over denial reasons and context excerpts
SELECT * FROM approval_audit_log
WHERE details->>'deny_reason' ILIKE '%production%'
   OR details->>'context' ILIKE '%DROP TABLE%';

-- All actions for a specific approval
SELECT * FROM approval_audit_log
WHERE approval_id = '...'
ORDER BY created_at;
```

---

## 8. Wiring

### 8.1 Integration Points

| From | To | Via | Purpose |
|------|----|-----|---------|
| Agent (Hermes) | `ApprovalService.RequestApproval` | canopyd API | Agent proposes action → creates pending approval |
| Frontend | `GET /api/approvals/pending` | canopyd REST | Fetch owner's pending approvals |
| Frontend | `POST /api/approvals/{id}/approve` | canopyd REST | Owner approves |
| Frontend | `POST /api/approvals/{id}/deny` | canopyd REST | Owner denies |
| `ApprovalService` | SSE Hub | `APPROVAL_PENDING`, `APPROVAL_GRANTED`, `APPROVAL_DENIED`, `APPROVAL_EXPIRED` opcodes | Real-time approval state changes to all connected clients |
| `ApprovalService` | `AuditRepo.Create` | internal | Every state transition logged immutably |
| Cron / pg_cron | `ExpirePending` | canopyd scheduled job | Auto-expire stale approvals |

### 8.2 SSE Opcodes (from ARCHITECTURE.md §4.3)

| Opcode | Trigger | Payload |
|--------|---------|---------|
| `APPROVAL_PENDING` | Agent requests approval | `{approval_id, tree_id, node_id, requested_by}` |
| `APPROVAL_GRANTED` | Owner approves (manual or auto-rule) | `{approval_id, tree_id, node_id, decided_by, auto_rule_id?}` |
| `APPROVAL_DENIED` | Owner denies (manual or auto-rule) | `{approval_id, tree_id, node_id, decided_by, deny_reason, auto_rule_id?}` |
| `APPROVAL_EXPIRED` | System expires stale approval | `{approval_id, tree_id, node_id}` |

### 8.3 Expiration Job

```go
// ExpirationJob runs every 5 minutes to expire stale pending approvals.
type ExpirationJob struct {
    approvals ApprovalRepo
    sseHub    *sse.Hub
}

func (j *ExpirationJob) Run(ctx context.Context) error {
    expired, err := j.approvals.ExpirePending(ctx)
    if err != nil {
        return fmt.Errorf("expire pending: %w", err)
    }
    for _, id := range expired {
        approval, _ := j.approvals.GetByID(ctx, id)
        j.sseHub.Broadcast(approval.TreeID, sse.Event{
            Type:    "APPROVAL_EXPIRED",
            Payload: approval,
        })
    }
    return nil
}
```

---

## 9. Error Catalog

| Code | HTTP | Message | Condition |
|------|------|---------|-----------|
| `APPROVAL_NOT_FOUND` | 404 | Approval not found | `GetByID` returns nil |
| `APPROVAL_ALREADY_DECIDED` | 409 | Approval has already been decided | `status != 'pending'` on approve/deny |
| `APPROVAL_NOT_PENDING` | 409 | Only pending approvals can be modified | Attempting approve/deny on a terminal status |
| `DENY_REASON_REQUIRED` | 400 | Denial reason is required | `deny()` called with empty reason |
| `DENY_REASON_TOO_LONG` | 400 | Denial reason exceeds 2000 characters | `len(reason) > 2000` |
| `NOT_TREE_OWNER` | 403 | Only the tree owner can approve or deny | `actor_id != approval.owner_id` (MVP) |
| `RULE_NOT_FOUND` | 404 | Approval rule not found | `GetByID` returns nil |
| `RULE_SCOPE_CONFLICT` | 409 | A rule already exists for this scope | Duplicate `(tree_id, scope_type, scope_target)` |
| `RULE_INVALID_DECISION` | 400 | Rule decision must be 'approved' or 'denied' | Attempted to create rule with `decision='pending'` |
| `NODE_ALREADY_HAS_APPROVAL` | 409 | This node already has an approval | `Create` called with node that has existing approval |
| `APPROVAL_EXPIRED` | 410 | This approval has expired | Attempting approve/deny on an expired approval |
| `AUDIT_IMMUTABLE` | 405 | Audit trail entries cannot be modified | Attempted UPDATE or DELETE (also blocked at DB level) |
| `OWNER_ID_MISMATCH` | 403 | Tree ownership mismatch | `approval.owner_id != tree.owner_id` |

---

## 10. Edge Cases

1. **Duplicate approval for same node:** `uq_approvals_node` UNIQUE constraint prevents creating two pending approvals for the same node. `Create()` must check this and return `NODE_ALREADY_HAS_APPROVAL` error.

2. **Race condition: approve + expire simultaneously:** The `Approve()` method uses `SELECT ... FOR UPDATE` to lock the approval row, preventing concurrent expire from interfering. If the row was already expired by the time the lock is acquired, return `APPROVAL_EXPIRED` error.

3. **Denial without reason:** Enforced at both DB level (`ck_denied_reason_required` CHECK constraint) and API level. Empty strings, whitespace-only strings, and NULL are all rejected.

4. **Auto-approval rule deleted while evaluation in flight:** `EvaluateAll` runs in a transaction with the rule read. If the rule is deleted between evaluation and approval insertion, the `auto_rule_id` FK with `ON DELETE SET NULL` ensures the approval still exists — the rule reference simply becomes NULL.

5. **Owner transfer:** When tree ownership changes, existing pending approvals must be re-evaluated. The new owner inherits all pending approvals. This is a post-MVP concern (SPEC-FTR-01) but the schema supports it: update `approvals.owner_id` in the same transaction as `trees.owner_id`.

6. **Approval for deleted node:** If the agent's proposed action node is soft-deleted (`nodes.deleted_at IS NOT NULL`), the associated approval should be auto-denied. The `FK ... ON DELETE CASCADE` handles hard-delete. For soft-delete, a trigger or application logic sets `status='expired'` with `details->>'cause' = 'node_deleted'`.

7. **Expiration at exactly `expires_at`:** The `ExpirePending` function uses `expires_at <= now()` — inclusive of exactly-matching timestamps. An approval that expires at 14:00:00 becomes expired at 14:00:00.000.

8. **Audit log for system-expired approvals:** When the system expires an approval, the audit entry has `actor=NULL` and `action='approval_expired'`. The `previous_status='pending'`, `new_status='expired'`.

9. **Batch approval:** Owner can approve multiple pending approvals in one request. Each approval is processed atomically in its own transaction (not all-or-nothing). Partial success is acceptable: some may succeed, some may fail (already decided, expired).

10. **Rule priority ties:** Two active rules with the same `priority` value. The tie-breaker is the scope type hierarchy: `thread` > `user` > `profile` > `action_type`. If same scope type AND same priority, the rule created first wins (lower `created_at`).

11. **Concurrent rule creation for same scope:** `uq_rule_scope` UNIQUE constraint prevents two active rules for the same `(tree_id, scope_type, scope_target)`. Second creation returns `RULE_SCOPE_CONFLICT`.

12. **Expiration window configuration:** Default 7 days. Configurable per-tree via a `tree_settings` table (post-MVP). For MVP, the 7-day default is hardcoded in the `DEFAULT` clause of `approvals.expires_at`.

---

## 11. Testing

### 11.1 Go Tests

```go
// approval_test.go — Test scenarios

func TestApprovalLifecycle(t *testing.T) {
    // 1. Create pending approval
    // 2. Approve → status becomes 'approved', decided_at and decided_by set
    // 3. Verify audit trail: approval_requested → approval_granted
}

func TestDenyRequiresReason(t *testing.T) {
    // 1. Create pending approval
    // 2. Deny with empty reason → DENY_REASON_REQUIRED error
    // 3. Deny with valid reason → success, audit trail includes reason
}

func TestCannotApproveTwice(t *testing.T) {
    // 1. Create + approve
    // 2. Approve again → APPROVAL_ALREADY_DECIDED error
}

func TestAutoApprovalRuleEvaluation(t *testing.T) {
    // 1. Create rule: "approve all in thread X" (priority 100)
    // 2. Create rule: "deny all from user Y" (priority 50)
    // 3. Evaluate: node in thread X from user Y → approved (thread rule wins)
    // 4. Evaluate: node in thread Z from user Y → denied (user rule matches, no thread rule)
    // 5. Evaluate: node in thread W from user Z → null (no rules match)
}

func TestAutoApprovalRulePriorityTiebreaker(t *testing.T) {
    // 1. Two thread-scope rules with same priority 100
    // 2. One for thread A, one for thread B
    // 3. Node in thread A → thread A rule wins
    // 4. Node in thread B → thread B rule wins
    // 5. Two rules for SAME thread with same priority → first-created wins
}

func TestExpiry(t *testing.T) {
    // 1. Create pending approval with expires_at = now() - 1s
    // 2. Run ExpirePending()
    // 3. Verify approval is now 'expired'
    // 4. Verify audit trail: approval_requested → approval_expired (actor=NULL)
}

func TestAuditTrailImmutability(t *testing.T) {
    // 1. Create approval, approve it
    // 2. Attempt direct UPDATE on approval_audit_log → permission denied
    // 3. Attempt direct DELETE on approval_audit_log → permission denied
    // 4. Verify AuditRepo has no Update/Delete methods
}

func TestNodeAlreadyHasApproval(t *testing.T) {
    // 1. Create pending approval for node X
    // 2. Create another approval for node X → NODE_ALREADY_HAS_APPROVAL error
}

func TestExpiryRaceCondition(t *testing.T) {
    // 1. Create pending approval
    // 2. In goroutine A: call ExpirePending (acquires lock)
    // 3. In goroutine B: call Approve (waits for lock)
    // 4. Goroutine A finishes → approval is expired
    // 5. Goroutine B's Approve returns APPROVAL_EXPIRED error
}
```

### 11.2 TypeScript Tests

```typescript
// approval-fsm.test.ts

describe('ApprovalFSM', () => {
  it('pending → approved (manual)', () => {
    expect(canTransition('pending', 'approved')).toBe(true);
  });

  it('pending → denied (manual)', () => {
    expect(canTransition('pending', 'denied')).toBe(true);
  });

  it('pending → expired (system)', () => {
    expect(canTransition('pending', 'expired')).toBe(true);
  });

  it('cannot transition from terminal states', () => {
    expect(canTransition('approved', 'denied')).toBe(false);
    expect(canTransition('denied', 'pending')).toBe(false);
    expect(canTransition('expired', 'approved')).toBe(false);
  });

  it('terminal states are correct', () => {
    expect(isTerminal('approved')).toBe(true);
    expect(isTerminal('denied')).toBe(true);
    expect(isTerminal('expired')).toBe(true);
    expect(isTerminal('pending')).toBe(false);
  });
});

describe('Zod validation', () => {
  it('DenyRequest requires non-empty reason', () => {
    expect(() => DenyRequestSchema.parse({ approvalId: uuid, reason: '' }))
      .toThrow();
  });

  it('ApprovalRule decision must be approved or denied', () => {
    expect(() => ApprovalRuleSchema.parse({ ...validRule, decision: 'pending' }))
      .toThrow();
  });
});
```

### 11.3 Integration Tests

```go
// approval_integration_test.go — requires real PostgreSQL

func TestIntegration_ApprovalLifecycle(t *testing.T) {
    // Setup: test PostgreSQL, migration, repos
    // 1. Create tree + node (via TreeRepo + NodeRepo)
    // 2. Agent requests approval via ApprovalService.RequestApproval
    // 3. Verify pending approval created, audit entry logged
    // 4. Owner approves via ApprovalService.Approve
    // 5. Verify approval status = 'approved', audit entry logged
    // 6. Verify full audit trail via AuditRepo.ListByApproval
}

func TestIntegration_AutoRuleEngine(t *testing.T) {
    // 1. Create approval rule for thread X (auto-approve)
    // 2. Agent requests approval in thread X
    // 3. Verify approval auto-approved (no pending state)
    // 4. Verify audit entry: rule_auto_approved
}

func TestIntegration_MostSpecificRuleWins(t *testing.T) {
    // 1. Create thread rule: approve (priority 100)
    // 2. Create user rule: deny (priority 50)
    // 3. Agent requests approval in thread X from user Y
    // 4. Verify: approved (thread rule wins)
}
```

---

## 12. Performance

| Operation | Target | Notes |
|-----------|--------|-------|
| `Create` approval | <5ms | Single INSERT + one audit INSERT |
| `Approve` / `Deny` | <5ms | SELECT FOR UPDATE + UPDATE + audit INSERT |
| `ListPending` (owner) | <10ms | Indexed query, returns up to 100 rows |
| `EvaluateAll` (rules) | <2ms | In-memory evaluation of active rules (typically <20 rules) |
| `ExpirePending` | <100ms | Batch UPDATE with RETURNING, typically <100 rows |
| `Search` audit trail | <50ms | ILIKE on JSONB, pages of 50 |
| SSE event emission | <1ms | In-process hub broadcast after state change |
| Rule match per node | <0.1ms | In-memory comparison against cached active rules |

Rule cache strategy: Load active rules for a tree once per request, cache for 5 seconds. Rules change infrequently.

---

## References

- **ARCHITECTURE.md** §8 — Multi-User & Approval architecture
- **ARCHITECTURE.md** §4.3 — SSE opcode definitions (APPROVAL_PENDING, etc.)
- **SPEC-DM-01** — Tree Node & Edge DDL (nodes table, trees table, UUIDv7 generator)
- **T1.5-approval-ux-research.md** — Full approval UX design (panel layout, interactions, data model impact)
- **DuckBrain:** `/project/hermes-canopy/multi-user` — Multi-user + approval gates architecture
- **DuckBrain:** `/project/hermes-canopy/architecture/decisions` — Stack decisions
