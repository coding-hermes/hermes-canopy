// User, Profile and TreeMember repositories over the SQLite core-graph store.
//
// The package comment lives in store.go; repo.go documents the core-graph slice. This file
// is the next slice of the same wave: concrete database/sql repositories for the three
// identity interfaces declared in internal/db — db.UserRepo, db.ProfileRepo and
// db.TreeMemberRepo — mirroring the grouping of internal/db/user_repo.go, which holds all
// three PostgreSQL implementations in one file.
//
// # Boundary (what this slice is, and is not)
//
//   - *UserRepo, *ProfileRepo and *TreeMemberRepo implement their interfaces in full. All
//     three are asserted at compile time below, so a future drift in any interface is a
//     build failure rather than a runtime surprise. Unlike db.EdgeRepo (see repo.go), none
//     of these three interfaces mentions a pgx handle, so no method is out of reach and
//     nothing in this file imports pgx — the package stays pure Go, as the pivot contract
//     requires.
//   - Out of scope, deliberately untouched: boot wiring (cmd/canopyd still gates on
//     PostgreSQL — wave 3), retiring PostgreSQL (wave 4), and every feature repo that is
//     not one of these three aggregates (approvals, topics, workspaces, MLS, transport,
//     federation, files — their own waves).
//
// # What is preserved from the PostgreSQL repos
//
// Column lists, result ordering, nil-input errors, every sentinel (db.ErrNotFound) and
// every Go-side default match their PG counterparts, so a caller can move between the two
// implementations without new error handling. Where SQLite forced a different mechanism the
// difference is documented at the method AND pinned by a test. Five are worth knowing
// before reading the code:
//
//  1. id generation — the DDL carries no `DEFAULT uuidv7()` for users.id / profiles.id /
//     tree_members.id, so every insert generates its id with NewID (obligation 1 of the
//     package comment). A caller-supplied id is not honored, exactly as in the PG repos
//     where the DDL default wins.
//  2. RETURNING does not see AFTER-trigger writes. The wave-1 SQLite DDL keeps PG's
//     `set_users_updated_at` / `set_profiles_updated_at` triggers, and SQLite evaluates a
//     RETURNING list before the AFTER triggers fire (see repo.go point 4). UserRepo.Update
//     therefore writes with one statement and reads the row back with a SELECT; the trigger
//     stays the single owner of users.updated_at. The tables with no AFTER trigger — insert
//     paths, and tree_members, which has none at all — use RETURNING directly.
//  3. Soft-delete filtering — `deleted_at IS NULL` stays in every read path, including the
//     UPDATE's own WHERE (so a soft-deleted user is not updated and reports ErrNotFound).
//     Neither db.UserRepo nor db.ProfileRepo declares a delete method, so this file exposes
//     none either; the filter is what makes a row that a future soft-delete moves out of the
//     active set invisible here, and it is pinned by tests that set deleted_at with plain
//     SQL.
//  4. Careful string matching — PostgreSQL's ILIKE has no direct SQLite equivalent. LIKE is
//     used instead, which is case-insensitive for ASCII only; that narrower fold is the same
//     documented translation db.TreeRepo.Search carries, and it is not widened here.
//  5. Boolean columns are INTEGER 0/1 — the DDL's CHECK (col IN (0,1)) and the write helper
//     boolArg make that the single stored domain, and boolColumn validates it on read rather
//     than coercing an unexpected value into a bool.
//
// Uniqueness is left to the schema, as in PostgreSQL: a duplicate users.hermes_user_id,
// profiles.(owner_id, name) or tree_members.(tree_id, user_id) fails the insert and is
// wrapped by the same `db: insert …: %w` shape the PG repos produce. Neither implementation
// maps a constraint violation onto a sentinel (db.ErrDuplicated belongs to
// internal/db/workspace_repo.go, which is a different aggregate), so no caller-visible
// change is introduced by translating the DDL rather than the error handling.

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// Compile-time proof that the three identity repositories satisfy the public interfaces of
// internal/db in full (the core-graph proofs live in repo.go).
var (
	_ db.UserRepo       = (*UserRepo)(nil)
	_ db.ProfileRepo    = (*ProfileRepo)(nil)
	_ db.TreeMemberRepo = (*TreeMemberRepo)(nil)
)

// ── shared argument/scan helpers ─────────────────────────────────────────────────────

// nullableStringArg renders a nullable TEXT column: a nil Go pointer is SQL NULL, which is
// what the DDL's nullable columns mean and what pgx sends for a nil *string.
func nullableStringArg(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// nullableTimeArg renders a nullable timestamp column in the canonical store shape. Raw
// time.Time values are never bound: every timestamp this package writes goes through
// EncodeTime (the store's D1 contract, and the rule the seeded-time test helpers follow),
// which is what keeps a stored value strictly decodable by DecodeTime.
func nullableTimeArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return EncodeTime(*t)
}

// boolArg renders a Go bool as the INTEGER 0/1 domain the DDL's CHECK constraints declare.
// SQLite has no boolean storage class; 0/1 is the stored domain every read path here
// decodes, so the conversion is explicit rather than left to the driver.
func boolArg(b bool) int {
	if b {
		return 1
	}
	return 0
}

// boolColumn decodes an INTEGER 0/1 column into a Go bool, validating rather than coercing:
// the DDL constrains the domain with CHECK (col IN (0,1)), so any other value means the row
// was written by something that ignored the schema and must fail loudly here instead of
// silently becoming true.
func boolColumn(column string, v sql.NullInt64) (bool, error) {
	switch {
	case !v.Valid:
		return false, fmt.Errorf("%s is NULL; the DDL declares it NOT NULL", column)
	case v.Int64 == 0:
		return false, nil
	case v.Int64 == 1:
		return true, nil
	default:
		return false, fmt.Errorf("%s = %d, want 0 or 1", column, v.Int64)
	}
}

// optionalString decodes a nullable TEXT column: only SQL NULL becomes nil, so a stored
// empty string round-trips as an empty string (the same value pgx reports for a nullable
// *string column).
func optionalString(column string, v sql.NullString) (*string, error) {
	if !v.Valid {
		return nil, nil
	}
	s := v.String
	return &s, nil
}

// existsInto runs an EXISTS(...) probe and decodes the INTEGER 0/1 result into a bool.
// database/sql converts that value through driver.Bool, which accepts exactly 0 and 1.
func existsInto(ctx context.Context, q *sql.DB, query string, args ...any) (bool, error) {
	var found bool
	if err := q.QueryRowContext(ctx, query, args...).Scan(&found); err != nil {
		return false, err
	}
	return found, nil
}

// qualifyColumns prefixes every column of a canonical single-table column list, for a read
// that joins a second table. PGProfileRepo qualifies only the first column; that is enough
// today only because `id` is the sole name the two joined tables share, and it turns into an
// ambiguous-column error the moment the lists overlap again. Qualifying all of them keeps the
// join explicit at no cost, since the list is a compile-time constant.
func qualifyColumns(prefix, columns string) string {
	parts := strings.Split(columns, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, prefix+"."+strings.Join(strings.Fields(p), " "))
	}
	return strings.Join(out, ", ")
}

// ============================================================
// UserRepo
// ============================================================

// userColumns is the canonical users column list, identical to PGUserRepo's, so one scan
// helper serves every read path in both implementations.
const userColumns = `id, hermes_user_id, email, display_name, avatar_url,
    created_at, updated_at, last_seen_at, is_active, deleted_at`

// UserRepo is the modernc.org/sqlite + database/sql implementation of db.UserRepo over the
// SQLite core graph store. Construct it with NewUserRepo.
type UserRepo struct{ q *sql.DB }

// NewUserRepo binds the repository to an open Store; see NewTreeRepo for the ownership and
// nil-store contract (a nil store yields a handle whose methods return ErrNoStore).
func NewUserRepo(s *Store) *UserRepo {
	if s == nil {
		return &UserRepo{}
	}
	return &UserRepo{q: s.DB()}
}

// conn returns the pool or ErrNoStore.
func (r *UserRepo) conn() (*sql.DB, error) {
	if r == nil || r.q == nil {
		return nil, ErrNoStore
	}
	return r.q, nil
}

// scanUser centralises the users column order, decoding ids, timestamps and the boolean
// domain explicitly — the same rules scanTree follows, and the reason a value written in a
// different shape fails loudly instead of being silently reinterpreted.
func scanUser(row rowScanner, u *db.User) error {
	var (
		id, hermesUserID      string
		email, avatarURL      sql.NullString
		createdAt, updatedAt  sql.NullString
		lastSeenAt, deletedAt sql.NullString
		isActive              sql.NullInt64
	)
	if err := row.Scan(&id, &hermesUserID, &email, &u.DisplayName, &avatarURL,
		&createdAt, &updatedAt, &lastSeenAt, &isActive, &deletedAt); err != nil {
		return err
	}
	userID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("users.id %q: %w", id, err)
	}
	u.ID = userID
	u.HermesUserID = hermesUserID
	if u.Email, err = optionalString("users.email", email); err != nil {
		return err
	}
	if u.AvatarURL, err = optionalString("users.avatar_url", avatarURL); err != nil {
		return err
	}
	if u.CreatedAt, err = requiredTime("users.created_at", createdAt); err != nil {
		return err
	}
	if u.UpdatedAt, err = requiredTime("users.updated_at", updatedAt); err != nil {
		return err
	}
	if u.LastSeenAt, err = optionalTime("users.last_seen_at", lastSeenAt); err != nil {
		return err
	}
	if u.IsActive, err = boolColumn("users.is_active", isActive); err != nil {
		return err
	}
	u.DeletedAt, err = optionalTime("users.deleted_at", deletedAt)
	return err
}

// Create inserts a user and returns the stored row.
//
// Three PostgreSQL mechanisms are replaced or narrowed here:
//
//   - the id comes from NewID, because the SQLite DDL declares no `DEFAULT uuidv7()`
//     (obligation 1). A caller-supplied User.ID is not honored — the same behaviour as
//     PGUserRepo, where the DDL default wins.
//   - created_at and updated_at are left to the DDL defaults
//     (`strftime('%Y-%m-%dT%H:%M:%fZ','now')`, byte-identical to sqlNow), so a
//     Go-written and a DDL-written timestamp stay indistinguishable.
//   - is_active is written as INTEGER 0/1 through boolArg.
//
// is_active is passed through unchanged, which IS the PostgreSQL behaviour: PGUserRepo's
// `COALESCE($6, true)` never fires because the parameter is a non-nullable Go bool (a Go
// bool is never SQL NULL), so a zero-valued User.IsActive inserts is_active = false in both
// stores. The DDL default (true / DEFAULT 1) is reached only by a raw SQL insert that omits
// the column, again in both stores. Defaulting it here instead would make an explicit
// IsActive=false impossible to express.
//
// A duplicate hermes_user_id fails the UNIQUE constraint and is wrapped by
// `db: insert user: %w`, matching PGUserRepo — neither implementation maps it to a sentinel.
func (r *UserRepo) Create(ctx context.Context, u *db.User) (*db.User, error) {
	if u == nil {
		return nil, errors.New("db: user is nil")
	}
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	row := q.QueryRowContext(ctx, `
        INSERT INTO users
            (id, hermes_user_id, email, display_name, avatar_url, last_seen_at, is_active)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        RETURNING `+userColumns,
		idArg(id), u.HermesUserID, nullableStringArg(u.Email), u.DisplayName,
		nullableStringArg(u.AvatarURL), nullableTimeArg(u.LastSeenAt), boolArg(u.IsActive),
	)
	var out db.User
	if err := scanUser(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert user: %w", err)
	}
	return &out, nil
}

// GetByID returns the active (not soft-deleted) user with the given ID.
func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*db.User, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	var u db.User
	err = scanUser(q.QueryRowContext(ctx, `
        SELECT `+userColumns+`
        FROM users
        WHERE id = ? AND deleted_at IS NULL`, idArg(id)), &u)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, db.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select user: %w", err)
	}
	return &u, nil
}

// GetByHermesUserID returns the active user with the given Hermes auth subject. Uses the
// unique index on hermes_user_id.
func (r *UserRepo) GetByHermesUserID(ctx context.Context, hermesUserID string) (*db.User, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	var u db.User
	err = scanUser(q.QueryRowContext(ctx, `
        SELECT `+userColumns+`
        FROM users
        WHERE hermes_user_id = ? AND deleted_at IS NULL`, hermesUserID), &u)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, db.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select user by hermes: %w", err)
	}
	return &u, nil
}

// GetByEmail returns the active user with the given email address. Uses the idx_users_email
// partial index, and matches case-insensitively.
//
// PostgreSQL's ILIKE is translated to LIKE: SQLite's LIKE folds ASCII case only, which is
// narrower than a UTF-8 ILIKE. The stored value is constrained by chk_users_email to an
// ASCII-shaped address, so the narrower fold is exact for rows this store can hold; widening
// it for other data is the open FTS5/ICU decision (docs/SQLITE-PIVOT.md §6 D2), not something
// this slice pre-empts. As in PG, the argument is a LIKE pattern — a `%` or `_` inside the
// address acts as a wildcard in both implementations, so the port neither introduces nor
// repairs that behaviour.
func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*db.User, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	var u db.User
	err = scanUser(q.QueryRowContext(ctx, `
        SELECT `+userColumns+`
        FROM users
        WHERE email LIKE ? AND deleted_at IS NULL`, email), &u)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, db.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select user by email: %w", err)
	}
	return &u, nil
}

// Update changes the mutable account fields — display name and avatar URL, the only two
// db.UserRepo declares mutable — and returns the stored row.
//
// updated_at is bumped by the translated set_users_updated_at trigger, not by this statement
// (PG does the same). The row is read back with a SELECT instead of RETURNING: SQLite
// evaluates a RETURNING list before the AFTER trigger fires, so RETURNING here would report
// the pre-update updated_at (repo.go point 4). Returns ErrNotFound if no active row matches,
// which keeps a soft-deleted user un-updatable.
func (r *UserRepo) Update(ctx context.Context, id uuid.UUID, displayName string, avatarURL *string) (*db.User, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	res, err := q.ExecContext(ctx, `
        UPDATE users
        SET display_name = ?, avatar_url = ?
        WHERE id = ? AND deleted_at IS NULL`,
		displayName, nullableStringArg(avatarURL), idArg(id))
	if err != nil {
		return nil, fmt.Errorf("db: update user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("db: update user: %w", err)
	}
	if n == 0 {
		return nil, db.ErrNotFound
	}
	return r.GetByID(ctx, id)
}

// ============================================================
// ProfileRepo
// ============================================================

// profileColumns is the canonical profiles column list, identical to PGProfileRepo's.
const profileColumns = `id, owner_id, profile_type, name, display_name,
    description, config_json, can_auto_respond, context_window_size,
    is_public, created_at, updated_at, deleted_at`

// ProfileRepo is the modernc.org/sqlite + database/sql implementation of db.ProfileRepo over
// the SQLite core graph store. Construct it with NewProfileRepo.
//
// db.ProfileRepo declares no Update and no delete, so neither exists here: a profile is
// created, read individually, listed per owner and listed per tree, and every read filters
// the soft-deleted rows out.
type ProfileRepo struct{ q *sql.DB }

// NewProfileRepo binds the repository to an open Store; see NewTreeRepo for the ownership
// and nil-store contract.
func NewProfileRepo(s *Store) *ProfileRepo {
	if s == nil {
		return &ProfileRepo{}
	}
	return &ProfileRepo{q: s.DB()}
}

// conn returns the pool or ErrNoStore.
func (r *ProfileRepo) conn() (*sql.DB, error) {
	if r == nil || r.q == nil {
		return nil, ErrNoStore
	}
	return r.q, nil
}

// scanProfile centralises the profiles column order. config_json is scanned into
// json.RawMessage's backing slice, so the stored TEXT is handed to the caller verbatim.
func scanProfile(row rowScanner, p *db.Profile) error {
	var (
		id, ownerID          string
		description          sql.NullString
		configJSON           []byte
		canAutoRespond       sql.NullInt64
		ctxWindow            int
		isPublic             sql.NullInt64
		createdAt, updatedAt sql.NullString
		deletedAt            sql.NullString
	)
	if err := row.Scan(&id, &ownerID, &p.ProfileType, &p.Name, &p.DisplayName, &description,
		&configJSON, &canAutoRespond, &ctxWindow, &isPublic,
		&createdAt, &updatedAt, &deletedAt); err != nil {
		return err
	}
	profileID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("profiles.id %q: %w", id, err)
	}
	p.ID = profileID
	if p.OwnerID, err = uuid.Parse(ownerID); err != nil {
		return fmt.Errorf("profiles.owner_id %q: %w", ownerID, err)
	}
	if p.Description, err = optionalString("profiles.description", description); err != nil {
		return err
	}
	p.ConfigJSON = configJSON
	if p.CanAutoRespond, err = boolColumn("profiles.can_auto_respond", canAutoRespond); err != nil {
		return err
	}
	p.ContextWindowSize = ctxWindow
	if p.IsPublic, err = boolColumn("profiles.is_public", isPublic); err != nil {
		return err
	}
	if p.CreatedAt, err = requiredTime("profiles.created_at", createdAt); err != nil {
		return err
	}
	if p.UpdatedAt, err = requiredTime("profiles.updated_at", updatedAt); err != nil {
		return err
	}
	p.DeletedAt, err = optionalTime("profiles.deleted_at", deletedAt)
	return err
}

// Create inserts a profile and returns the stored row.
//
// The two Go-side defaults of PGProfileRepo are applied here, in the same order: an empty
// ProfileType becomes 'hermes-profile' and a zero ContextWindowSize becomes 32768. The
// PostgreSQL-only `$2::profile_type` cast is dropped — SQLite has no enum type, and the
// translated `CHECK (profile_type IN ('human','hermes-profile'))` enforces the same closed
// set. An empty ConfigJSON is written as '{}' through jsonText, the equivalent of PG's
// `COALESCE($6, '{}'::jsonb)`, and is bound as TEXT so the json_valid CHECK and the
// updated_at trigger's comparisons behave (repo.go point 5). can_auto_respond and is_public
// are passed through as 0/1, exactly as PG passes the Go bools it is given.
//
// A duplicate (owner_id, name) fails uq_profiles_owner_name and is wrapped by
// `db: insert profile: %w`, matching PGProfileRepo; an owner that does not exist fails the
// owner_id foreign key.
func (r *ProfileRepo) Create(ctx context.Context, p *db.Profile) (*db.Profile, error) {
	if p == nil {
		return nil, errors.New("db: profile is nil")
	}
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	ptype := p.ProfileType
	if ptype == "" {
		ptype = db.ProfileTypeHermesProfile
	}
	cws := p.ContextWindowSize
	if cws == 0 {
		cws = 32768
	}
	row := q.QueryRowContext(ctx, `
        INSERT INTO profiles
            (id, owner_id, profile_type, name, display_name, description,
             config_json, can_auto_respond, context_window_size, is_public)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        RETURNING `+profileColumns,
		idArg(id), idArg(p.OwnerID), ptype, p.Name, p.DisplayName,
		nullableStringArg(p.Description), jsonText(p.ConfigJSON),
		boolArg(p.CanAutoRespond), cws, boolArg(p.IsPublic),
	)
	var out db.Profile
	if err := scanProfile(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert profile: %w", err)
	}
	return &out, nil
}

// GetByID returns the active (not soft-deleted) profile.
func (r *ProfileRepo) GetByID(ctx context.Context, id uuid.UUID) (*db.Profile, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	var p db.Profile
	err = scanProfile(q.QueryRowContext(ctx, `
        SELECT `+profileColumns+`
        FROM profiles
        WHERE id = ? AND deleted_at IS NULL`, idArg(id)), &p)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, db.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select profile: %w", err)
	}
	return &p, nil
}

// GetByOwner returns all active profiles owned by ownerID, newest first.
//
// `ORDER BY created_at DESC` is the PG ordering verbatim. created_at has millisecond
// precision, so two profiles created in the same millisecond tie; the ordering is therefore
// only deterministic across distinct instants, which is exactly what PG promises too (no
// secondary key is added here, since adding one would change the order PG produces).
func (r *ProfileRepo) GetByOwner(ctx context.Context, ownerID uuid.UUID) ([]db.Profile, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
        SELECT `+profileColumns+`
        FROM profiles
        WHERE owner_id = ? AND deleted_at IS NULL
        ORDER BY created_at DESC`, idArg(ownerID))
	if err != nil {
		return nil, fmt.Errorf("db: select profiles by owner: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectProfiles(rows)
}

// ListByTree returns all active profiles that are members of the given tree, joined through
// tree_members and ordered by membership age (oldest membership last, as in PGProfileRepo).
// Visibility- and role-filtering stay the caller's responsibility, exactly as the interface
// documents.
//
// The profiles columns are all qualified with `p.`: PGProfileRepo qualifies only the first
// one, which resolves today only because `id` is the single name the two joined tables share.
func (r *ProfileRepo) ListByTree(ctx context.Context, treeID uuid.UUID) ([]db.Profile, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
        SELECT `+qualifyColumns("p", profileColumns)+`
        FROM profiles p
        JOIN tree_members tm ON tm.profile_id = p.id
        WHERE tm.tree_id = ?
          AND p.deleted_at IS NULL
        ORDER BY tm.joined_at DESC`, idArg(treeID))
	if err != nil {
		return nil, fmt.Errorf("db: select profiles by tree: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectProfiles(rows)
}

// collectProfiles drains rows into a []db.Profile.
//
// The slice is pre-allocated non-nil to match PGProfileRepo, which builds its results with
// `make([]Profile, 0)`: an empty result is then an empty slice rather than nil, and the wire
// form of "no profiles" stays `[]`. (The core-graph collectors in this package return nil for
// an empty result because PGTreeRepo/PGNodeRepo do — each port mirrors its own counterpart.)
func collectProfiles(rows *sql.Rows) ([]db.Profile, error) {
	out := make([]db.Profile, 0)
	for rows.Next() {
		var p db.Profile
		if err := scanProfile(rows, &p); err != nil {
			return nil, fmt.Errorf("db: scan profile: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: scan profile: %w", err)
	}
	return out, nil
}

// ============================================================
// TreeMemberRepo
// ============================================================

// treeMemberColumns is the canonical tree_members column list, identical to
// PGTreeMemberRepo's.
const treeMemberColumns = `id, tree_id, user_id, profile_id, role,
    is_visible, auto_approved, joined_at, invited_by`

// TreeMemberRepo is the modernc.org/sqlite + database/sql implementation of
// db.TreeMemberRepo over the SQLite core graph store. Construct it with NewTreeMemberRepo.
type TreeMemberRepo struct{ q *sql.DB }

// NewTreeMemberRepo binds the repository to an open Store; see NewTreeRepo for the ownership
// and nil-store contract.
func NewTreeMemberRepo(s *Store) *TreeMemberRepo {
	if s == nil {
		return &TreeMemberRepo{}
	}
	return &TreeMemberRepo{q: s.DB()}
}

// conn returns the pool or ErrNoStore.
func (r *TreeMemberRepo) conn() (*sql.DB, error) {
	if r == nil || r.q == nil {
		return nil, ErrNoStore
	}
	return r.q, nil
}

// scanTreeMember centralises the tree_members column order.
func scanTreeMember(row rowScanner, m *db.TreeMember) error {
	var (
		id, treeID            string
		userID, profileID     sql.NullString
		isVisible, autoApprov sql.NullInt64
		joinedAt              sql.NullString
		invitedBy             sql.NullString
	)
	if err := row.Scan(&id, &treeID, &userID, &profileID, &m.Role,
		&isVisible, &autoApprov, &joinedAt, &invitedBy); err != nil {
		return err
	}
	memberID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("tree_members.id %q: %w", id, err)
	}
	m.ID = memberID
	if m.TreeID, err = uuid.Parse(treeID); err != nil {
		return fmt.Errorf("tree_members.tree_id %q: %w", treeID, err)
	}
	if m.UserID, err = optionalID("tree_members.user_id", userID); err != nil {
		return err
	}
	if m.ProfileID, err = optionalID("tree_members.profile_id", profileID); err != nil {
		return err
	}
	if m.IsVisible, err = boolColumn("tree_members.is_visible", isVisible); err != nil {
		return err
	}
	if m.AutoApproved, err = boolColumn("tree_members.auto_approved", autoApprov); err != nil {
		return err
	}
	if m.JoinedAt, err = requiredTime("tree_members.joined_at", joinedAt); err != nil {
		return err
	}
	m.InvitedBy, err = optionalID("tree_members.invited_by", invitedBy)
	return err
}

// Add inserts a tree membership and returns the stored row.
//
// The polymorphic participant rule is checked in Go before any write, with the same message
// PGTreeMemberRepo returns; the DDL's chk_tree_members_participant CHECK enforces it a second
// time, so a row that bypasses this method still cannot hold both or neither participant. An
// empty Role becomes 'member' (the PG Go-side default, which the DDL default mirrors); the
// PostgreSQL-only `$4::tree_role` cast is dropped because the translated `CHECK (role IN
// ('owner','admin','member','viewer'))` enforces the same closed set. is_visible and
// auto_approved are passed through as 0/1 rather than defaulted in Go, exactly as
// PGTreeMemberRepo does — writing the column explicitly is what makes an explicit false
// expressible.
//
// A duplicate (tree_id, user_id) or (tree_id, profile_id) fails the matching UNIQUE
// constraint and is wrapped by `db: insert tree member: %w`; unknown tree/user/profile ids
// fail the foreign keys (foreign_keys=ON, D6).
func (r *TreeMemberRepo) Add(ctx context.Context, m *db.TreeMember) (*db.TreeMember, error) {
	if m == nil {
		return nil, errors.New("db: tree member is nil")
	}
	if (m.UserID == nil) == (m.ProfileID == nil) {
		return nil, errors.New("db: tree member must reference exactly one of user_id, profile_id")
	}
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	role := m.Role
	if role == "" {
		role = db.TreeRoleMember
	}
	row := q.QueryRowContext(ctx, `
        INSERT INTO tree_members
            (id, tree_id, user_id, profile_id, role, is_visible, auto_approved, invited_by)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        RETURNING `+treeMemberColumns,
		idArg(id), idArg(m.TreeID), optionalIDArg(m.UserID), optionalIDArg(m.ProfileID),
		role, boolArg(m.IsVisible), boolArg(m.AutoApproved), optionalIDArg(m.InvitedBy),
	)
	var out db.TreeMember
	if err := scanTreeMember(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert tree member: %w", err)
	}
	return &out, nil
}

// GetByTree returns all memberships for a tree, oldest first (joined_at ASC).
//
// Membership has no soft-delete column, so "active" needs no predicate here: Remove is a hard
// delete, as in PGTreeMemberRepo.
func (r *TreeMemberRepo) GetByTree(ctx context.Context, treeID uuid.UUID) ([]db.TreeMember, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
        SELECT `+treeMemberColumns+`
        FROM tree_members
        WHERE tree_id = ?
        ORDER BY joined_at ASC`, idArg(treeID))
	if err != nil {
		return nil, fmt.Errorf("db: select tree members by tree: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectTreeMembers(rows)
}

// GetByUser returns every membership the given user has, across all trees, newest first.
// The DB uses the partial index on user_id (WHERE user_id IS NOT NULL).
func (r *TreeMemberRepo) GetByUser(ctx context.Context, userID uuid.UUID) ([]db.TreeMember, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
        SELECT `+treeMemberColumns+`
        FROM tree_members
        WHERE user_id = ?
        ORDER BY joined_at DESC`, idArg(userID))
	if err != nil {
		return nil, fmt.Errorf("db: select tree members by user: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectTreeMembers(rows)
}

// UpdateRole changes a membership's role and returns the stored row.
//
// Unlike users.Update this can use RETURNING: tree_members carries no AFTER trigger in the
// translated DDL — the wave-1 batch defines exactly four triggers (trg_node_edited_at on
// nodes, set_users_updated_at on users, set_profiles_updated_at on profiles and
// trg_approval_rules_updated on approval_rules), none of them on this table — so there is no
// trigger-written column for RETURNING to report stale. The role is not validated in Go,
// matching PGTreeMemberRepo, where the `$2::tree_role` cast rejects an unknown role; here the
// translated CHECK does.
func (r *TreeMemberRepo) UpdateRole(ctx context.Context, id uuid.UUID, role string) (*db.TreeMember, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	row := q.QueryRowContext(ctx, `
        UPDATE tree_members
        SET role = ?
        WHERE id = ?
        RETURNING `+treeMemberColumns,
		role, idArg(id))
	var m db.TreeMember
	if err := scanTreeMember(row, &m); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, db.ErrNotFound
		}
		return nil, fmt.Errorf("db: update tree member role: %w", err)
	}
	return &m, nil
}

// Remove hard-deletes the membership row, as PGTreeMemberRepo does. Approval audit rows are
// untouched: approval_audit_log references approvals, not tree_members, so no cascade reaches
// it — and unlike PostgreSQL (REVOKE UPDATE, DELETE) SQLite has no privilege layer, so the
// audit table's immutability here rests on the repository contract rather than on GRANTs.
// Returns ErrNotFound when no row matched.
func (r *TreeMemberRepo) Remove(ctx context.Context, id uuid.UUID) error {
	q, err := r.conn()
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, `DELETE FROM tree_members WHERE id = ?`, idArg(id))
	if err != nil {
		return fmt.Errorf("db: delete tree member: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: delete tree member: %w", err)
	}
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// IsMember returns true when the specified user has a membership row for the given tree.
// The UNIQUE constraint on (tree_id, user_id) makes EXISTS safe regardless of duplicates.
func (r *TreeMemberRepo) IsMember(ctx context.Context, treeID, userID uuid.UUID) (bool, error) {
	q, err := r.conn()
	if err != nil {
		return false, err
	}
	found, err := existsInto(ctx, q,
		`SELECT EXISTS(SELECT 1 FROM tree_members WHERE tree_id = ? AND user_id = ?)`,
		idArg(treeID), idArg(userID))
	if err != nil {
		return false, fmt.Errorf("db: check tree member: %w", err)
	}
	return found, nil
}

// IsTreeDeleted returns true when the tree exists but is soft-deleted (deleted_at IS NOT
// NULL). Backs TreeMembershipMiddleware's deleted-tree gate (BUG-043): a member of a
// soft-deleted tree must get 410 TREE_DELETED, matching the tree handler's GetTree
// behaviour. A tree that never existed returns false — callers that need the not-found
// distinction use the tree repo directly.
func (r *TreeMemberRepo) IsTreeDeleted(ctx context.Context, treeID uuid.UUID) (bool, error) {
	q, err := r.conn()
	if err != nil {
		return false, err
	}
	found, err := existsInto(ctx, q,
		`SELECT EXISTS(SELECT 1 FROM trees WHERE id = ? AND deleted_at IS NOT NULL)`,
		idArg(treeID))
	if err != nil {
		return false, fmt.Errorf("db: check tree deleted: %w", err)
	}
	return found, nil
}

// collectTreeMembers drains rows into a []db.TreeMember, non-nil when empty for the same
// reason collectProfiles is (PGTreeMemberRepo builds `make([]TreeMember, 0)`).
func collectTreeMembers(rows *sql.Rows) ([]db.TreeMember, error) {
	out := make([]db.TreeMember, 0)
	for rows.Next() {
		var m db.TreeMember
		if err := scanTreeMember(rows, &m); err != nil {
			return nil, fmt.Errorf("db: scan tree member: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: scan tree member: %w", err)
	}
	return out, nil
}
