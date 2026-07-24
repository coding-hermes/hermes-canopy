// Approval repository. Implements ApprovalRepo with pgx against
// PostgreSQL. State transitions (Approve / Deny / ExpirePending)
// follow the BEGIN/COMMIT-with-audit patterns defined in
// SPEC-API-05 §4.4 and §5.5: the UPDATE on the approvals row and the
// INSERT into approval_audit_log MUST run in the same transaction
// so the audit trail can never disagree with the row state. See
// migration 000009 for the DDL.

package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ApprovalRepo defines approval-scoped persistence operations.
type ApprovalRepo interface {
	// Create inserts a new pending approval.
	Create(ctx context.Context, a *Approval) (*Approval, error)

	// GetByID returns the approval with the given ID.
	// Returns ErrNotFound if no row matches.
	GetByID(ctx context.Context, id uuid.UUID) (*Approval, error)

	// GetByNodeID returns the (at most one) approval for a node.
	// Returns ErrNotFound if no row matches.
	GetByNodeID(ctx context.Context, nodeID uuid.UUID) (*Approval, error)

	// ListPending returns all pending approvals for an owner,
	// sorted by created_at ASC (FIFO queue). treeID may be nil to
	// span all trees owned by ownerID. Returns the slice and the
	// total count matching the filter (ignoring limit/offset).
	ListPending(ctx context.Context, ownerID uuid.UUID, treeID *uuid.UUID, limit, offset int) ([]Approval, int, error)

	// ListByTree returns approvals for a tree, optionally filtered
	// by status. status == "" returns all rows.
	ListByTree(ctx context.Context, treeID uuid.UUID, status string, limit, offset int) ([]Approval, error)

	// Approve atomically transitions a pending approval to
	// approved and inserts an audit entry. Returns ErrNotFound if
	// the row doesn't exist or is no longer pending / has expired,
	// or ErrAlreadyDecided if it is in a terminal state.
	Approve(ctx context.Context, id, actorID uuid.UUID, ruleID *uuid.UUID) (*Approval, error)

	// Deny atomically transitions a pending approval to denied and
	// inserts an audit entry. reason must be non-empty (the DB
	// CHECK constraint also enforces this).
	Deny(ctx context.Context, id, actorID uuid.UUID, reason string) (*Approval, error)

	// ExpirePending marks every pending approval whose expires_at
	// has passed as expired, inserts an audit row per expired
	// approval, and returns the expired IDs so the caller can fan
	// out SSE events.
	ExpirePending(ctx context.Context) ([]uuid.UUID, error)
}

// PGApprovalRepo is the pgx-backed ApprovalRepo implementation.
type PGApprovalRepo struct {
	pool *pgxpool.Pool
}

// NewPGApprovalRepo wires the repo to a pgxpool. The pool is owned
// by the caller — typically the parent db.DB — and is not closed here.
func NewPGApprovalRepo(pool *pgxpool.Pool) *PGApprovalRepo {
	return &PGApprovalRepo{pool: pool}
}

const approvalColumns = `id, tree_id, node_id, owner_id, requested_by,
    status, denied_reason, auto_rule_id, decided_by, created_at,
    decided_at, expires_at`

// scanApproval centralises the column order for approvals row scans.
func scanApproval(row pgx.Row, a *Approval) error {
	return row.Scan(
		&a.ID, &a.TreeID, &a.NodeID, &a.OwnerID, &a.RequestedBy,
		&a.Status, &a.DeniedReason, &a.AutoRuleID, &a.DecidedBy,
		&a.CreatedAt, &a.DecidedAt, &a.ExpiresAt,
	)
}

// Create inserts a new pending approval. If a.Status is non-empty
// it is used (caller must supply a valid approval_status value);
// otherwise the database default ('pending') is applied. ID and
// CreatedAt / ExpiresAt are populated by database defaults when
// zero-valued.
func (r *PGApprovalRepo) Create(ctx context.Context, a *Approval) (*Approval, error) {
	if a == nil {
		return nil, errors.New("db: approval is nil")
	}
	status := a.Status
	if status == "" {
		status = ApprovalStatusPending
	}
	row := r.pool.QueryRow(ctx, `
        INSERT INTO approvals
            (tree_id, node_id, owner_id, requested_by, status,
             denied_reason, auto_rule_id, decided_by, decided_at, expires_at)
        VALUES ($1, $2, $3, $4, $5::approval_status,
                $6, $7, $8, $9,
                COALESCE(NULLIF($10, '0001-01-01 00:00:00+00:00'::timestamptz), now() + INTERVAL '7 days'))
        RETURNING `+approvalColumns,
		a.TreeID, a.NodeID, a.OwnerID, a.RequestedBy, status,
		a.DeniedReason, a.AutoRuleID, a.DecidedBy, a.DecidedAt, a.ExpiresAt,
	)
	var out Approval
	if err := scanApproval(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert approval: %w", err)
	}
	return &out, nil
}

// GetByID returns the approval with the given ID, regardless of
// status. ErrNotFound if no row matches.
func (r *PGApprovalRepo) GetByID(ctx context.Context, id uuid.UUID) (*Approval, error) {
	var a Approval
	err := scanApproval(r.pool.QueryRow(ctx, `
        SELECT `+approvalColumns+`
        FROM approvals
        WHERE id = $1`, id), &a)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select approval: %w", err)
	}
	return &a, nil
}

// GetByNodeID returns the at-most-one approval for a node (the DB
// enforces a unique constraint on node_id).
func (r *PGApprovalRepo) GetByNodeID(ctx context.Context, nodeID uuid.UUID) (*Approval, error) {
	var a Approval
	err := scanApproval(r.pool.QueryRow(ctx, `
        SELECT `+approvalColumns+`
        FROM approvals
        WHERE node_id = $1`, nodeID), &a)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select approval by node: %w", err)
	}
	return &a, nil
}

// ListPending returns pending approvals for ownerID. If treeID is
// non-nil the result is restricted to that tree. Sorted ascending
// by created_at (FIFO queue — SPEC-API-05 §3.4 step 4). limit is
// clamped to [1, 100]; offset is floored at 0.
func (r *PGApprovalRepo) ListPending(ctx context.Context, ownerID uuid.UUID, treeID *uuid.UUID, limit, offset int) ([]Approval, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var total int
	if err := r.pool.QueryRow(ctx, `
        SELECT COUNT(*)::int
        FROM approvals
        WHERE owner_id = $1
          AND status = 'pending'
          AND ($2::uuid IS NULL OR tree_id = $2)`,
		ownerID, treeID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("db: count pending approvals: %w", err)
	}
	if total == 0 {
		return []Approval{}, 0, nil
	}

	rows, err := r.pool.Query(ctx, `
        SELECT `+approvalColumns+`
        FROM approvals
        WHERE owner_id = $1
          AND status = 'pending'
          AND ($2::uuid IS NULL OR tree_id = $2)
        ORDER BY created_at ASC
        LIMIT $3 OFFSET $4`,
		ownerID, treeID, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("db: select pending approvals: %w", err)
	}
	defer rows.Close()

	out := make([]Approval, 0, limit)
	for rows.Next() {
		var a Approval
		if err := scanApproval(rows, &a); err != nil {
			return nil, 0, fmt.Errorf("db: scan approval: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("db: iterate pending approvals: %w", err)
	}
	return out, total, nil
}

// ListByTree returns approvals for a tree, optionally filtered by
// status (empty string = no status filter). limit is clamped to
// [1, 200]; offset floored at 0.
func (r *PGApprovalRepo) ListByTree(ctx context.Context, treeID uuid.UUID, status string, limit, offset int) ([]Approval, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := r.pool.Query(ctx, `
        SELECT `+approvalColumns+`
        FROM approvals
        WHERE tree_id = $1
          AND ($2 = '' OR status = $2::approval_status)
        ORDER BY created_at ASC
        LIMIT $3 OFFSET $4`,
		treeID, status, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("db: select approvals by tree: %w", err)
	}
	defer rows.Close()

	out := make([]Approval, 0, limit)
	for rows.Next() {
		var a Approval
		if err := scanApproval(rows, &a); err != nil {
			return nil, fmt.Errorf("db: scan approval: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate approvals by tree: %w", err)
	}
	return out, nil
}

// Approve atomically marks a pending approval as approved AND
// inserts a corresponding audit row in the SAME transaction
// (SPEC-API-05 §4.4). The WHERE clause guards against double
// approvals and against deciding an expired approval. Returns
// ErrNotFound if no row matched (already decided or expired).
func (r *PGApprovalRepo) Approve(ctx context.Context, id, actorID uuid.UUID, ruleID *uuid.UUID) (*Approval, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("db: begin approve tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. UPDATE — only succeeds while status='pending' AND not expired.
	row := tx.QueryRow(ctx, `
        UPDATE approvals
        SET status = 'approved',
            decided_by = $2,
            decided_at = clock_timestamp()
        WHERE id = $1
          AND status = 'pending'
          AND expires_at > clock_timestamp()
        RETURNING `+approvalColumns,
		id, actorID,
	)
	var out Approval
	if err := scanApproval(row, &out); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("db: update approval approve: %w", err)
	}

	// 2. INSERT audit entry — same tx.
	auditDetails, err := json.Marshal(map[string]any{
		"tree_id":      out.TreeID,
		"node_id":      out.NodeID,
		"requested_by": out.RequestedBy,
		"owner_id":     actorID,
	})
	if err != nil {
		return nil, fmt.Errorf("db: marshal audit details: %w", err)
	}
	var action string
	if ruleID != nil {
		action = AuditActionRuleAutoApproved
	} else {
		action = AuditActionApprovalGranted
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO approval_audit_log
            (approval_id, action, actor, previous_status, new_status, details)
        VALUES ($1, $2::audit_action, $3, 'pending', 'approved', $4)`,
		out.ID, action, actorID, auditDetails,
	); err != nil {
		return nil, fmt.Errorf("db: insert approve audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("db: commit approve tx: %w", err)
	}
	return &out, nil
}

// Deny atomically marks a pending approval as denied (with the
// mandatory reason) AND inserts a corresponding audit row in the
// SAME transaction (SPEC-API-05 §5.5). The DB CHECK constraint
// enforces that denied_reason is non-empty when status='denied';
// pgx surfaces that as a regular error here.
func (r *PGApprovalRepo) Deny(ctx context.Context, id, actorID uuid.UUID, reason string) (*Approval, error) {
	if reason == "" {
		return nil, errors.New("db: deny reason required")
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("db: begin deny tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. UPDATE — only succeeds while pending AND not expired.
	row := tx.QueryRow(ctx, `
        UPDATE approvals
        SET status = 'denied',
            denied_reason = $2,
            decided_by = $3,
            decided_at = clock_timestamp()
        WHERE id = $1
          AND status = 'pending'
          AND expires_at > clock_timestamp()
        RETURNING `+approvalColumns,
		id, reason, actorID,
	)
	var out Approval
	if err := scanApproval(row, &out); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("db: update approval deny: %w", err)
	}

	// 2. INSERT audit entry — same tx.
	auditDetails, err := json.Marshal(map[string]any{
		"tree_id":      out.TreeID,
		"node_id":      out.NodeID,
		"requested_by": out.RequestedBy,
		"owner_id":     actorID,
		"deny_reason":  reason,
	})
	if err != nil {
		return nil, fmt.Errorf("db: marshal deny audit details: %w", err)
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO approval_audit_log
            (approval_id, action, actor, previous_status, new_status, details)
        VALUES ($1, 'approval_denied'::audit_action, $2, 'pending', 'denied', $3)`,
		out.ID, actorID, auditDetails,
	); err != nil {
		return nil, fmt.Errorf("db: insert deny audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("db: commit deny tx: %w", err)
	}
	return &out, nil
}

// ExpirePending is a single statement that:
//  1. UPDATE pending approvals whose expires_at <= now() to
//     status='expired', decided_at=now(), decided_by=NULL (the
//     system is the decider — there is no human actor).
//  2. INSERT one audit_audit_log row per expired approval.
//     RETURNING gives us the expired IDs and tree IDs in the
//     same pass for downstream SSE fan-out.
//
// Per SPEC-DM-03 §6 and the expire_pending_approvals() SQL
// function defined in migration 000009.
func (r *PGApprovalRepo) ExpirePending(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `
        WITH expired AS (
            UPDATE approvals
            SET status = 'expired',
                decided_at = now(),
                decided_by = NULL
            WHERE status = 'pending'
              AND expires_at <= now()
            RETURNING id, tree_id
        ),
        inserted AS (
            INSERT INTO approval_audit_log
                (approval_id, action, actor, previous_status, new_status, details)
            SELECT e.id,
                   'approval_expired'::audit_action,
                   NULL,
                   'pending'::approval_status,
                   'expired'::approval_status,
                   jsonb_build_object(
                       'tree_id', e.tree_id,
                       'expired_at', now()
                   )
            FROM expired e
            RETURNING approval_id
        )
        SELECT id FROM expired`,
	)
	if err != nil {
		return nil, fmt.Errorf("db: expire pending approvals: %w", err)
	}
	defer rows.Close()

	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("db: scan expired id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate expired ids: %w", err)
	}
	return out, nil
}
