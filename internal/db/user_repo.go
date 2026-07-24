// User, Profile, and TreeMember repositories.
//
// Each of these three aggregates lives in its own pgx-backed repo
// following the same interface-first / struct / constructor /
// scan helper / methods layout used by node_repo.go and
// tree_repo.go. See migration 000008 for the DDL and SPEC-DM-04
// for the field/relationship semantics.

package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ============================================================
// UserRepo
// ============================================================

// UserRepo manages the users table (SPEC-DM-04 §4.5).
type UserRepo interface {
	// Create inserts a new user. hermes_user_id must be unique.
	// ID, CreatedAt, UpdatedAt are server-assigned when zero.
	Create(ctx context.Context, u *User) (*User, error)

	// GetByID returns the active user with the given ID.
	// Returns ErrNotFound if no row matches or the row is soft-deleted.
	GetByID(ctx context.Context, id uuid.UUID) (*User, error)

	// GetByHermesUserID returns the active user with the given
	// external Hermes auth subject. Returns ErrNotFound if no row matches.
	GetByHermesUserID(ctx context.Context, hermesUserID string) (*User, error)

	// Update changes mutable fields and bumps updated_at via trigger.
	// Only DisplayName and AvatarURL are mutable per SPEC-DM-04 §4.1.
	Update(ctx context.Context, id uuid.UUID, displayName string, avatarURL *string) (*User, error)
}

// PGUserRepo is the pgx-backed UserRepo implementation.
type PGUserRepo struct {
	pool *pgxpool.Pool
}

// NewPGUserRepo wires the repo to a pgxpool. The pool is owned by
// the caller — typically the parent db.DB — and is not closed here.
func NewPGUserRepo(pool *pgxpool.Pool) *PGUserRepo {
	return &PGUserRepo{pool: pool}
}

const userColumns = `id, hermes_user_id, email, display_name, avatar_url,
    created_at, updated_at, last_seen_at, is_active, deleted_at`

// scanUser centralises the column order for users row scans.
func scanUser(row pgx.Row, u *User) error {
	return row.Scan(
		&u.ID, &u.HermesUserID, &u.Email, &u.DisplayName, &u.AvatarURL,
		&u.CreatedAt, &u.UpdatedAt, &u.LastSeenAt, &u.IsActive, &u.DeletedAt,
	)
}

// Create inserts a new user. Server-assigned fields populated via
// RETURNING.
func (r *PGUserRepo) Create(ctx context.Context, u *User) (*User, error) {
	if u == nil {
		return nil, errors.New("db: user is nil")
	}
	row := r.pool.QueryRow(ctx, `
        INSERT INTO users
            (hermes_user_id, email, display_name, avatar_url, last_seen_at, is_active)
        VALUES ($1, $2, $3, $4, $5, COALESCE($6, true))
        RETURNING `+userColumns,
		u.HermesUserID, u.Email, u.DisplayName, u.AvatarURL, u.LastSeenAt, u.IsActive,
	)
	var out User
	if err := scanUser(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert user: %w", err)
	}
	return &out, nil
}

// GetByID returns the active (not soft-deleted) user with the given ID.
func (r *PGUserRepo) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	var u User
	err := scanUser(r.pool.QueryRow(ctx, `
        SELECT `+userColumns+`
        FROM users
        WHERE id = $1 AND deleted_at IS NULL`, id), &u)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select user: %w", err)
	}
	return &u, nil
}

// GetByHermesUserID returns the active user with the given Hermes
// auth subject. Uses the unique index on hermes_user_id.
func (r *PGUserRepo) GetByHermesUserID(ctx context.Context, hermesUserID string) (*User, error) {
	var u User
	err := scanUser(r.pool.QueryRow(ctx, `
        SELECT `+userColumns+`
        FROM users
        WHERE hermes_user_id = $1 AND deleted_at IS NULL`, hermesUserID), &u)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select user by hermes: %w", err)
	}
	return &u, nil
}

// Update changes the mutable profile fields. updated_at is bumped
// by the set_users_updated_at trigger (migration 000008). Returns
// ErrNotFound if no active row matches.
func (r *PGUserRepo) Update(ctx context.Context, id uuid.UUID, displayName string, avatarURL *string) (*User, error) {
	row := r.pool.QueryRow(ctx, `
        UPDATE users
        SET display_name = $2, avatar_url = $3
        WHERE id = $1 AND deleted_at IS NULL
        RETURNING `+userColumns,
		id, displayName, avatarURL,
	)
	var u User
	if err := scanUser(row, &u); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("db: update user: %w", err)
	}
	return &u, nil
}

// ============================================================
// ProfileRepo
// ============================================================

// ProfileRepo manages the profiles table (SPEC-DM-04 §4.5).
type ProfileRepo interface {
	// Create inserts a new profile owned by OwnerID.
	Create(ctx context.Context, p *Profile) (*Profile, error)

	// GetByID returns the active profile.
	GetByID(ctx context.Context, id uuid.UUID) (*Profile, error)

	// GetByOwner returns all active profiles owned by the given user.
	GetByOwner(ctx context.Context, ownerID uuid.UUID) ([]Profile, error)

	// ListByTree returns the active profiles that are members of the
	// given tree (joined via tree_members).
	ListByTree(ctx context.Context, treeID uuid.UUID) ([]Profile, error)
}

// PGProfileRepo is the pgx-backed ProfileRepo implementation.
type PGProfileRepo struct {
	pool *pgxpool.Pool
}

// NewPGProfileRepo wires the repo to a pgxpool.
func NewPGProfileRepo(pool *pgxpool.Pool) *PGProfileRepo {
	return &PGProfileRepo{pool: pool}
}

const profileColumns = `id, owner_id, profile_type, name, display_name,
    description, config_json, can_auto_respond, context_window_size,
    is_public, created_at, updated_at, deleted_at`

// scanProfile centralises the column order for profiles row scans.
func scanProfile(row pgx.Row, p *Profile) error {
	return row.Scan(
		&p.ID, &p.OwnerID, &p.ProfileType, &p.Name, &p.DisplayName,
		&p.Description, &p.ConfigJSON, &p.CanAutoRespond, &p.ContextWindowSize,
		&p.IsPublic, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt,
	)
}

// Create inserts a new profile. Server-assigned ID, CreatedAt, and
// UpdatedAt are populated via RETURNING.
func (r *PGProfileRepo) Create(ctx context.Context, p *Profile) (*Profile, error) {
	if p == nil {
		return nil, errors.New("db: profile is nil")
	}
	ptype := p.ProfileType
	if ptype == "" {
		ptype = ProfileTypeHermesProfile
	}
	cws := p.ContextWindowSize
	if cws == 0 {
		cws = 32768
	}
	row := r.pool.QueryRow(ctx, `
        INSERT INTO profiles
            (owner_id, profile_type, name, display_name, description,
             config_json, can_auto_respond, context_window_size, is_public)
        VALUES ($1, $2::profile_type, $3, $4, $5,
                COALESCE($6, '{}'::jsonb), $7, $8, $9)
        RETURNING `+profileColumns,
		p.OwnerID, ptype, p.Name, p.DisplayName, p.Description,
		p.ConfigJSON, p.CanAutoRespond, cws, p.IsPublic,
	)
	var out Profile
	if err := scanProfile(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert profile: %w", err)
	}
	return &out, nil
}

// GetByID returns the active (not soft-deleted) profile.
func (r *PGProfileRepo) GetByID(ctx context.Context, id uuid.UUID) (*Profile, error) {
	var p Profile
	err := scanProfile(r.pool.QueryRow(ctx, `
        SELECT `+profileColumns+`
        FROM profiles
        WHERE id = $1 AND deleted_at IS NULL`, id), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select profile: %w", err)
	}
	return &p, nil
}

// GetByOwner returns all active profiles owned by ownerID, newest first.
func (r *PGProfileRepo) GetByOwner(ctx context.Context, ownerID uuid.UUID) ([]Profile, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+profileColumns+`
        FROM profiles
        WHERE owner_id = $1 AND deleted_at IS NULL
        ORDER BY created_at DESC`,
		ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("db: select profiles by owner: %w", err)
	}
	defer rows.Close()

	out := make([]Profile, 0)
	for rows.Next() {
		var p Profile
		if err := scanProfile(rows, &p); err != nil {
			return nil, fmt.Errorf("db: scan profile: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate profiles: %w", err)
	}
	return out, nil
}

// ListByTree returns all active profiles that are members of the
// given tree. Joins tree_members → profiles. Visibility- and
// role-filtering is the caller's responsibility (this method
// returns the membership pages so the UI can render the full
// panel; role-aware filtering belongs in the service layer).
func (r *PGProfileRepo) ListByTree(ctx context.Context, treeID uuid.UUID) ([]Profile, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+"p."+profileColumns+`
        FROM profiles p
        JOIN tree_members tm ON tm.profile_id = p.id
        WHERE tm.tree_id = $1
          AND p.deleted_at IS NULL
        ORDER BY tm.joined_at DESC`,
		treeID,
	)
	if err != nil {
		return nil, fmt.Errorf("db: select profiles by tree: %w", err)
	}
	defer rows.Close()

	out := make([]Profile, 0)
	for rows.Next() {
		var p Profile
		if err := scanProfile(rows, &p); err != nil {
			return nil, fmt.Errorf("db: scan profile by tree: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate profiles by tree: %w", err)
	}
	return out, nil
}

// ============================================================
// TreeMemberRepo
// ============================================================

// TreeMemberRepo manages tree memberships. Memberships are
// polymorphic: exactly one of UserID / ProfileID is set per row
// (enforced by the chk_tree_members_participant CHECK constraint).
type TreeMemberRepo interface {
	// Add inserts a new tree membership. Either UserID or ProfileID
	// must be set; both or neither is rejected (DB will reject).
	Add(ctx context.Context, m *TreeMember) (*TreeMember, error)

	// GetByTree returns all memberships for a tree, oldest first.
	GetByTree(ctx context.Context, treeID uuid.UUID) ([]TreeMember, error)

	// GetByUser returns all memberships for a user across all trees.
	GetByUser(ctx context.Context, userID uuid.UUID) ([]TreeMember, error)

	// UpdateRole changes a membership's role.
	UpdateRole(ctx context.Context, id uuid.UUID, role string) (*TreeMember, error)

	// Remove hard-deletes the membership row.
	// The matching approval_audit_log rows are preserved (the audit
	// table is immutable).
	Remove(ctx context.Context, id uuid.UUID) error

	// IsMember returns true when the given user is a member of the tree.
	IsMember(ctx context.Context, treeID, userID uuid.UUID) (bool, error)
}

// PGTreeMemberRepo is the pgx-backed TreeMemberRepo implementation.
type PGTreeMemberRepo struct {
	pool *pgxpool.Pool
}

// NewPGTreeMemberRepo wires the repo to a pgxpool.
func NewPGTreeMemberRepo(pool *pgxpool.Pool) *PGTreeMemberRepo {
	return &PGTreeMemberRepo{pool: pool}
}

const treeMemberColumns = `id, tree_id, user_id, profile_id, role,
    is_visible, auto_approved, joined_at, invited_by`

// scanTreeMember centralises the column order for tree_members scans.
func scanTreeMember(row pgx.Row, m *TreeMember) error {
	return row.Scan(
		&m.ID, &m.TreeID, &m.UserID, &m.ProfileID, &m.Role,
		&m.IsVisible, &m.AutoApproved, &m.JoinedAt, &m.InvitedBy,
	)
}

// Add inserts a new tree membership. Joins-back default role to
// 'member' and is_visible to true if zero-valued.
func (r *PGTreeMemberRepo) Add(ctx context.Context, m *TreeMember) (*TreeMember, error) {
	if m == nil {
		return nil, errors.New("db: tree member is nil")
	}
	if (m.UserID == nil) == (m.ProfileID == nil) {
		return nil, errors.New("db: tree member must reference exactly one of user_id, profile_id")
	}
	role := m.Role
	if role == "" {
		role = TreeRoleMember
	}
	row := r.pool.QueryRow(ctx, `
        INSERT INTO tree_members
            (tree_id, user_id, profile_id, role, is_visible, auto_approved, invited_by)
        VALUES ($1, $2, $3, $4::tree_role, $5, $6, $7)
        RETURNING `+treeMemberColumns,
		m.TreeID, m.UserID, m.ProfileID, role, m.IsVisible, m.AutoApproved, m.InvitedBy,
	)
	var out TreeMember
	if err := scanTreeMember(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert tree member: %w", err)
	}
	return &out, nil
}

// GetByTree returns all memberships for a tree, oldest first (joined_at ASC).
func (r *PGTreeMemberRepo) GetByTree(ctx context.Context, treeID uuid.UUID) ([]TreeMember, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+treeMemberColumns+`
        FROM tree_members
        WHERE tree_id = $1
        ORDER BY joined_at ASC`,
		treeID,
	)
	if err != nil {
		return nil, fmt.Errorf("db: select tree members by tree: %w", err)
	}
	defer rows.Close()

	out := make([]TreeMember, 0)
	for rows.Next() {
		var m TreeMember
		if err := scanTreeMember(rows, &m); err != nil {
			return nil, fmt.Errorf("db: scan tree member: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate tree members: %w", err)
	}
	return out, nil
}

// GetByUser returns every membership the given user has, across
// all trees, newest first. The DB uses the partial index on
// user_id (WHERE user_id IS NOT NULL).
func (r *PGTreeMemberRepo) GetByUser(ctx context.Context, userID uuid.UUID) ([]TreeMember, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+treeMemberColumns+`
        FROM tree_members
        WHERE user_id = $1
        ORDER BY joined_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("db: select tree members by user: %w", err)
	}
	defer rows.Close()

	out := make([]TreeMember, 0)
	for rows.Next() {
		var m TreeMember
		if err := scanTreeMember(rows, &m); err != nil {
			return nil, fmt.Errorf("db: scan tree member: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate tree members: %w", err)
	}
	return out, nil
}

// UpdateRole changes a membership's tree_role. Returns
// ErrNotFound if no row matches.
func (r *PGTreeMemberRepo) UpdateRole(ctx context.Context, id uuid.UUID, role string) (*TreeMember, error) {
	row := r.pool.QueryRow(ctx, `
        UPDATE tree_members
        SET role = $2::tree_role
        WHERE id = $1
        RETURNING `+treeMemberColumns,
		id, role,
	)
	var m TreeMember
	if err := scanTreeMember(row, &m); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("db: update tree member role: %w", err)
	}
	return &m, nil
}

// Remove hard-deletes the membership row. Unlike a soft-delete,
// approval_audit_log entries that reference the user or profile
// survive unchanged.
func (r *PGTreeMemberRepo) Remove(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM tree_members WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("db: delete tree member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// IsMember returns true when the specified user has a membership row for the
// given tree. Both user_id-tree_id combinations are valid (the PK constraint
// enforces uniqueness, so EXISTS is safe regardless of duplicates).
func (r *PGTreeMemberRepo) IsMember(ctx context.Context, treeID, userID uuid.UUID) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM tree_members WHERE tree_id = $1 AND user_id = $2)`,
		treeID, userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("db: check tree member: %w", err)
	}
	return exists, nil
}
