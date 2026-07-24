// Audit repository. Implements AuditRepo with pgx against
// PostgreSQL. The approval_audit_log table is IMMUTABLE — the
// migration (000009) revokes UPDATE and DELETE on the table at
// the database level, and this repo deliberately exposes no
// Update or Delete methods. Audit rows are inserted alongside
// the matching state transition inside the SAME transaction by
// the approval repo (SPEC-API-05 §4.4 and §5.5); a standalone
// Create is provided for cases where audit must be written
// outside a state transition (e.g. approval_requested at
// request time, rule_created by the rule manager).

package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditRepo defines audit-log persistence. No Update / Delete —
// the table is append-only.
type AuditRepo interface {
	// Create inserts a new audit row. Returns the inserted entry
	// with server-assigned ID and created_at.
	Create(ctx context.Context, e *AuditEntry) (*AuditEntry, error)

	// ListByApproval returns audit rows for one approval, oldest
	// first. limit is clamped to [1, 200]; offset floored at 0.
	ListByApproval(ctx context.Context, approvalID uuid.UUID, limit, offset int) ([]AuditEntry, error)

	// ListByTree returns audit rows whose details carry the given
	// tree_id, newest first. limit is clamped to [1, 200]; offset
	// floored at 0. Filters out rows where tree_id is not recorded
	// in the jsonb details snapshot.
	ListByTree(ctx context.Context, treeID uuid.UUID, limit, offset int) ([]AuditEntry, error)
}

// PGAuditRepo is the pgx-backed AuditRepo implementation.
type PGAuditRepo struct {
	pool *pgxpool.Pool
}

// NewPGAuditRepo wires the repo to a pgxpool. The pool is owned by
// the caller — typically the parent db.DB — and is not closed here.
func NewPGAuditRepo(pool *pgxpool.Pool) *PGAuditRepo {
	return &PGAuditRepo{pool: pool}
}

const auditColumns = `id, approval_id, action, actor,
    previous_status, new_status, details, created_at`

// scanAudit centralises the column order for audit row scans.
func scanAudit(row pgx.Row, e *AuditEntry) error {
	return row.Scan(
		&e.ID, &e.ApprovalID, &e.Action, &e.Actor,
		&e.PreviousStatus, &e.NewStatus, &e.Details, &e.CreatedAt,
	)
}

// Create inserts a new audit row. Caller must populate Action and
// ApprovalID; Details may be nil (defaults to '{}'::jsonb); Actor /
// PreviousStatus / NewStatus are all nullable in the schema.
func (r *PGAuditRepo) Create(ctx context.Context, e *AuditEntry) (*AuditEntry, error) {
	if e == nil {
		return nil, errors.New("db: audit entry is nil")
	}
	row := r.pool.QueryRow(ctx, `
        INSERT INTO approval_audit_log
            (approval_id, action, actor, previous_status, new_status, details)
        VALUES ($1, $2::audit_action, $3,
                $4::approval_status, $5::approval_status,
                COALESCE($6, '{}'::jsonb))
        RETURNING `+auditColumns,
		e.ApprovalID, e.Action, e.Actor,
		e.PreviousStatus, e.NewStatus, e.Details,
	)
	var out AuditEntry
	if err := scanAudit(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert audit: %w", err)
	}
	return &out, nil
}

// ListByApproval returns the audit trail for a single approval in
// chronological order. limit is clamped to [1, 200]; offset floored
// at 0.
func (r *PGAuditRepo) ListByApproval(ctx context.Context, approvalID uuid.UUID, limit, offset int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.pool.Query(ctx, `
        SELECT `+auditColumns+`
        FROM approval_audit_log
        WHERE approval_id = $1
        ORDER BY created_at ASC
        LIMIT $2 OFFSET $3`,
		approvalID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("db: select audit by approval: %w", err)
	}
	defer rows.Close()

	out := make([]AuditEntry, 0, limit)
	for rows.Next() {
		var a AuditEntry
		if err := scanAudit(rows, &a); err != nil {
			return nil, fmt.Errorf("db: scan audit: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate audit: %w", err)
	}
	return out, nil
}

// ListByTree returns audit rows for a tree, ordered by created_at
// DESC so the newest entries surface first. Tree ID is read from
// the immutable details JSONB snapshot recorded at action time —
// rows lacking a tree_id in details are skipped. limit is clamped
// to [1, 200]; offset floored at 0.
func (r *PGAuditRepo) ListByTree(ctx context.Context, treeID uuid.UUID, limit, offset int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.pool.Query(ctx, `
        SELECT `+auditColumns+`
        FROM approval_audit_log
        WHERE details->>'tree_id' = $1
        ORDER BY created_at DESC
        LIMIT $2 OFFSET $3`,
		treeID.String(), limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("db: select audit by tree: %w", err)
	}
	defer rows.Close()

	out := make([]AuditEntry, 0, limit)
	for rows.Next() {
		var a AuditEntry
		if err := scanAudit(rows, &a); err != nil {
			return nil, fmt.Errorf("db: scan audit: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate audit: %w", err)
	}
	return out, nil
}
