// Package db provides the PostgreSQL data layer for Canopy.
// WorkspaceRepo implements the data layer for SPEC-FTR-01 Phase P1:
// workspace CRUD, membership, and invitation create/consume.
// Identity = users (see internal/collaboration package doc comment).
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkspaceRow is the persisted form of a workspace (migration 000032).
type WorkspaceRow struct {
	ID          uuid.UUID
	OwnerID     uuid.UUID
	Name        string
	Slug        string
	Description string
	TreeID      *uuid.UUID
	ApprovalTTL int64 // seconds
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// WorkspaceMemberRow is a persisted workspace membership.
type WorkspaceMemberRow struct {
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
	Role        int
	JoinedAt    time.Time
}

// InvitationRow is a persisted invitation (token stored as SHA-256 hash).
type InvitationRow struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	CreatedBy   uuid.UUID
	TokenHash   string
	ExpiresAt   time.Time
	Used        bool
	CreatedAt   time.Time
}

// ErrDuplicated is returned when an insert violates a uniqueness
// constraint (e.g. an existing workspace membership pair).
var ErrDuplicated = errors.New("db: resource already exists")

// WorkspaceRepo handles CRUD for workspaces, members, and invitations.
type WorkspaceRepo interface {
	// CreateWorkspace inserts a workspace row and returns it.
	CreateWorkspace(ctx context.Context, w *WorkspaceRow) (*WorkspaceRow, error)
	// GetWorkspaceByID returns a workspace row or ErrNotFound.
	GetWorkspaceByID(ctx context.Context, id uuid.UUID) (*WorkspaceRow, error)
	// UpdateWorkspace updates mutable workspace fields (name, description,
	// tree_id, approval_ttl) and bumps updated_at.
	UpdateWorkspace(ctx context.Context, id uuid.UUID, name, description string, treeID *uuid.UUID, approvalTTL int64) (*WorkspaceRow, error)
	// DeleteWorkspace removes a workspace row (members/invitations cascade).
	DeleteWorkspace(ctx context.Context, id uuid.UUID) error
	// ListWorkspacesForUser returns workspace rows for all workspaces the
	// user is a member of.
	ListWorkspacesForUser(ctx context.Context, userID uuid.UUID) ([]WorkspaceRow, error)

	// AddMember inserts a membership row. Returns ErrDuplicated when the
	// pair already exists.
	AddMember(ctx context.Context, workspaceID, userID uuid.UUID, role int) error
	// GetMember returns a membership row or ErrNotFound.
	GetMember(ctx context.Context, workspaceID, userID uuid.UUID) (*WorkspaceMemberRow, error)
	// ListMembers returns all members of a workspace ordered by joined_at.
	ListMembers(ctx context.Context, workspaceID uuid.UUID) ([]WorkspaceMemberRow, error)
	// UpdateMemberRole changes a member's role.
	UpdateMemberRole(ctx context.Context, workspaceID, userID uuid.UUID, role int) error
	// RemoveMember deletes a membership row.
	RemoveMember(ctx context.Context, workspaceID, userID uuid.UUID) error

	// CreateInvitation inserts an invitation row.
	CreateInvitation(ctx context.Context, inv *InvitationRow) (*InvitationRow, error)
	// GetInvitationByHash returns an invitation by token hash or ErrNotFound.
	GetInvitationByHash(ctx context.Context, tokenHash string) (*InvitationRow, error)
	// ConsumeInvitation atomically marks an invitation used. Returns true
	// when the row was flipped from unused to used (single-use guarantee).
	ConsumeInvitation(ctx context.Context, id uuid.UUID) (bool, error)
}

// PGWorkspaceRepo is the pgx-backed WorkspaceRepo implementation.
type PGWorkspaceRepo struct {
	pool *pgxpool.Pool
}

// NewPGWorkspaceRepo wires the repo to a pgxpool. The pool is owned by
// the caller — typically the parent db.DB — and is not closed here.
func NewPGWorkspaceRepo(pool *pgxpool.Pool) *PGWorkspaceRepo {
	return &PGWorkspaceRepo{pool: pool}
}

const workspaceColumns = `id, owner_id, name, slug, description, tree_id,
    approval_ttl, created_at, updated_at`

// scanWorkspace scans a workspace row.
func scanWorkspace(row pgx.Row, w *WorkspaceRow) error {
	return row.Scan(
		&w.ID, &w.OwnerID, &w.Name, &w.Slug, &w.Description,
		&w.TreeID, &w.ApprovalTTL, &w.CreatedAt, &w.UpdatedAt,
	)
}

// CreateWorkspace inserts a workspace row and returns it.
func (r *PGWorkspaceRepo) CreateWorkspace(ctx context.Context, w *WorkspaceRow) (*WorkspaceRow, error) {
	row := r.pool.QueryRow(ctx, `
        INSERT INTO workspaces (id, owner_id, name, slug, description, tree_id, approval_ttl)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
        RETURNING `+workspaceColumns,
		w.ID, w.OwnerID, w.Name, w.Slug, w.Description, w.TreeID, w.ApprovalTTL,
	)
	var out WorkspaceRow
	if err := scanWorkspace(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert workspace: %w", err)
	}
	return &out, nil
}

// GetWorkspaceByID returns a workspace row or ErrNotFound.
func (r *PGWorkspaceRepo) GetWorkspaceByID(ctx context.Context, id uuid.UUID) (*WorkspaceRow, error) {
	var w WorkspaceRow
	err := scanWorkspace(r.pool.QueryRow(ctx, `
        SELECT `+workspaceColumns+` FROM workspaces WHERE id = $1`, id), &w)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select workspace: %w", err)
	}
	return &w, nil
}

// UpdateWorkspace updates mutable workspace fields and bumps updated_at.
func (r *PGWorkspaceRepo) UpdateWorkspace(ctx context.Context, id uuid.UUID, name, description string, treeID *uuid.UUID, approvalTTL int64) (*WorkspaceRow, error) {
	var w WorkspaceRow
	err := scanWorkspace(r.pool.QueryRow(ctx, `
        UPDATE workspaces
        SET name = $2, description = $3, tree_id = $4, approval_ttl = $5,
            updated_at = now()
        WHERE id = $1
        RETURNING `+workspaceColumns,
		id, name, description, treeID, approvalTTL), &w)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: update workspace: %w", err)
	}
	return &w, nil
}

// DeleteWorkspace removes a workspace row (members/invitations cascade).
func (r *PGWorkspaceRepo) DeleteWorkspace(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM workspaces WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("db: delete workspace: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListWorkspacesForUser returns workspace rows for all workspaces the
// user is a member of.
func (r *PGWorkspaceRepo) ListWorkspacesForUser(ctx context.Context, userID uuid.UUID) ([]WorkspaceRow, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+workspaceColumns+`
        FROM workspaces w
        JOIN workspace_members wm ON wm.workspace_id = w.id
        WHERE wm.user_id = $1
        ORDER BY w.created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("db: list workspaces for user: %w", err)
	}
	defer rows.Close()

	var out []WorkspaceRow
	for rows.Next() {
		var w WorkspaceRow
		if err := scanWorkspace(rows, &w); err != nil {
			return nil, fmt.Errorf("db: scan workspace: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// AddMember inserts a membership row. Returns ErrDuplicated when the
// pair already exists.
func (r *PGWorkspaceRepo) AddMember(ctx context.Context, workspaceID, userID uuid.UUID, role int) error {
	_, err := r.pool.Exec(ctx, `
        INSERT INTO workspace_members (workspace_id, user_id, role)
        VALUES ($1, $2, $3)`,
		workspaceID, userID, role)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrDuplicated
		}
		return fmt.Errorf("db: insert workspace member: %w", err)
	}
	return nil
}

// GetMember returns a membership row or ErrNotFound.
func (r *PGWorkspaceRepo) GetMember(ctx context.Context, workspaceID, userID uuid.UUID) (*WorkspaceMemberRow, error) {
	var m WorkspaceMemberRow
	err := r.pool.QueryRow(ctx, `
        SELECT workspace_id, user_id, role, joined_at
        FROM workspace_members
        WHERE workspace_id = $1 AND user_id = $2`,
		workspaceID, userID,
	).Scan(&m.WorkspaceID, &m.UserID, &m.Role, &m.JoinedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select workspace member: %w", err)
	}
	return &m, nil
}

// ListMembers returns all members of a workspace ordered by joined_at.
func (r *PGWorkspaceRepo) ListMembers(ctx context.Context, workspaceID uuid.UUID) ([]WorkspaceMemberRow, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT workspace_id, user_id, role, joined_at
        FROM workspace_members
        WHERE workspace_id = $1
        ORDER BY joined_at ASC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("db: list workspace members: %w", err)
	}
	defer rows.Close()

	var out []WorkspaceMemberRow
	for rows.Next() {
		var m WorkspaceMemberRow
		if err := rows.Scan(&m.WorkspaceID, &m.UserID, &m.Role, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("db: scan workspace member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateMemberRole changes a member's role.
func (r *PGWorkspaceRepo) UpdateMemberRole(ctx context.Context, workspaceID, userID uuid.UUID, role int) error {
	tag, err := r.pool.Exec(ctx, `
        UPDATE workspace_members SET role = $3
        WHERE workspace_id = $1 AND user_id = $2`,
		workspaceID, userID, role)
	if err != nil {
		return fmt.Errorf("db: update workspace member role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveMember deletes a membership row.
func (r *PGWorkspaceRepo) RemoveMember(ctx context.Context, workspaceID, userID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
        DELETE FROM workspace_members
        WHERE workspace_id = $1 AND user_id = $2`,
		workspaceID, userID)
	if err != nil {
		return fmt.Errorf("db: delete workspace member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateInvitation inserts an invitation row.
func (r *PGWorkspaceRepo) CreateInvitation(ctx context.Context, inv *InvitationRow) (*InvitationRow, error) {
	row := r.pool.QueryRow(ctx, `
        INSERT INTO invitations (id, workspace_id, created_by, token_hash, expires_at)
        VALUES ($1, $2, $3, $4, $5)
        RETURNING id, workspace_id, created_by, token_hash, expires_at, used, created_at`,
		inv.ID, inv.WorkspaceID, inv.CreatedBy, inv.TokenHash, inv.ExpiresAt,
	)
	var out InvitationRow
	if err := row.Scan(
		&out.ID, &out.WorkspaceID, &out.CreatedBy, &out.TokenHash,
		&out.ExpiresAt, &out.Used, &out.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("db: insert invitation: %w", err)
	}
	return &out, nil
}

// GetInvitationByHash returns an invitation by token hash or ErrNotFound.
func (r *PGWorkspaceRepo) GetInvitationByHash(ctx context.Context, tokenHash string) (*InvitationRow, error) {
	var inv InvitationRow
	err := r.pool.QueryRow(ctx, `
        SELECT id, workspace_id, created_by, token_hash, expires_at, used, created_at
        FROM invitations
        WHERE token_hash = $1`, tokenHash,
	).Scan(&inv.ID, &inv.WorkspaceID, &inv.CreatedBy, &inv.TokenHash,
		&inv.ExpiresAt, &inv.Used, &inv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select invitation: %w", err)
	}
	return &inv, nil
}

// ConsumeInvitation atomically marks an invitation used. Returns true
// when the row was flipped from unused to used (single-use guarantee).
func (r *PGWorkspaceRepo) ConsumeInvitation(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
        UPDATE invitations SET used = true
        WHERE id = $1 AND used = false`, id)
	if err != nil {
		return false, fmt.Errorf("db: consume invitation: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
