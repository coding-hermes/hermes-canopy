package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo defines plugin persistence. Implemented with pgx in PGPluginRepo
// (pattern: internal/db/PGNodeRepo); service tests use an in-memory stub.
//
// Audit is part of the interface because the register algorithm (GAP-002 §5
// step 8) appends a 'registered' entry to plugin_audit_log and the service
// only talks to persistence through this interface.
type Repo interface {
	Register(ctx context.Context, p *Plugin) (*Plugin, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Plugin, error)
	GetActiveByName(ctx context.Context, name string) (*Plugin, error) // status='active'
	List(ctx context.Context, limit, offset int) ([]Plugin, int, error)
	Install(ctx context.Context, inst *PluginInstance) (*PluginInstance, error)
	GetInstance(ctx context.Context, id uuid.UUID) (*PluginInstance, error)
	ListInstances(ctx context.Context, profileID uuid.UUID, treeID *uuid.UUID) ([]PluginInstance, error)
	UpdateInstanceStatus(ctx context.Context, id uuid.UUID, status string) error
	IncrementInvokeCount(ctx context.Context, id uuid.UUID) error
	Archive(ctx context.Context, id uuid.UUID) error
	// GetVersionByName returns the stored row for (name, version), any
	// status. ErrPluginNotFound when the tuple does not exist.
	GetVersionByName(ctx context.Context, name, version string) (*Plugin, error)
	// ListVersionsByName returns every row for name, newest first
	// (ORDER BY created_at DESC, SPEC-PL-01 §12.1 scenario 24).
	ListVersionsByName(ctx context.Context, name string) ([]Plugin, error)
	// UpdateVersionChain archives oldID and links the version chain in one
	// transaction: old row → status 'archived' + archived_at + superseded_by_id
	// = newID; new row → previous_version_id = oldID.
	UpdateVersionChain(ctx context.Context, oldID, newID uuid.UUID) error
	// ActivateVersion flips targetID back to 'active' (clearing archived_at)
	// and records supersedingID as the row it replaced (rollback direction).
	ActivateVersion(ctx context.Context, targetID, supersedingID uuid.UUID) error
	Audit(ctx context.Context, entry *PluginAuditEntry) error
}

// PGPluginRepo is the pgx-backed Repo implementation.
type PGPluginRepo struct {
	pool *pgxpool.Pool
}

// NewPGPluginRepo wires the repo to a pgxpool.
func NewPGPluginRepo(pool *pgxpool.Pool) *PGPluginRepo {
	return &PGPluginRepo{pool: pool}
}

const pluginColumns = `id, name, slug, version, description, author_profile_id,
    permissions, manifest_json, source_js, source_sha256, source_byte_size,
    icon_url, status, install_count, is_root_version, superseded_by_id,
    previous_version_id, created_at, updated_at, archived_at`

// scanPlugin centralises the column order for plugin_registry row scans.
func scanPlugin(row pgx.Row, p *Plugin) error {
	var permissions []string
	var status string
	var supersededByID, previousVersionID *uuid.UUID
	var archivedAt *pgtype.Timestamptz
	err := row.Scan(
		&p.ID, &p.Name, &p.Slug, &p.Version, &p.Description, &p.AuthorProfileID,
		&permissions, &p.ManifestJSON, &p.SourceJS, &p.SourceSHA256, &p.SourceByteSize,
		&p.IconURL, &status, &p.InstallCount, &p.IsRootVersion,
		&supersededByID, &previousVersionID, &p.CreatedAt, &p.UpdatedAt, &archivedAt,
	)
	if err != nil {
		return err
	}
	p.Permissions = convertPermissions(permissions)
	p.Status = PluginStatus(status)
	p.SupersededByID = supersededByID
	p.PreviousVersionID = previousVersionID
	if archivedAt != nil {
		p.ArchivedAt = &archivedAt.Time
	}
	return nil
}

// scanPluginRows collects a pgx.Rows result into a Plugin slice.
func scanPluginRows(rows pgx.Rows) ([]Plugin, error) {
	defer rows.Close()
	var out []Plugin
	for rows.Next() {
		var p Plugin
		if err := scanPlugin(rows, &p); err != nil {
			return nil, fmt.Errorf("db: scan plugin row: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate plugin rows: %w", err)
	}
	return out, nil
}

// Register inserts a plugin row and returns it. A concurrent duplicate
// (same name, version) violates uq_plugin_name_version; the existing row is
// fetched and returned instead (idempotent — matches SPEC-PL-01 §12.1 test 7).
func (r *PGPluginRepo) Register(ctx context.Context, p *Plugin) (*Plugin, error) {
	row := r.pool.QueryRow(ctx, `
        INSERT INTO plugin_registry
            (name, slug, version, description, author_profile_id, permissions,
             manifest_json, source_js, source_sha256, source_byte_size,
             icon_url, status, is_root_version, previous_version_id)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
        RETURNING `+pluginColumns,
		p.Name, p.Slug, p.Version, p.Description, p.AuthorProfileID,
		permissionStrings(p.Permissions), p.ManifestJSON, p.SourceJS,
		p.SourceSHA256, p.SourceByteSize, p.IconURL,
		string(p.Status), p.IsRootVersion, p.PreviousVersionID,
	)
	var out Plugin
	if err := scanPlugin(row, &out); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_plugin_name_version" {
			return r.getByNameVersion(ctx, p.Name, p.Version)
		}
		return nil, fmt.Errorf("db: insert plugin: %w", err)
	}
	return &out, nil
}

// getByNameVersion returns the stored row for (name, version), any status.
func (r *PGPluginRepo) getByNameVersion(ctx context.Context, name, version string) (*Plugin, error) {
	var p Plugin
	err := scanPlugin(r.pool.QueryRow(ctx, `
        SELECT `+pluginColumns+`
        FROM plugin_registry
        WHERE name = $1 AND version = $2`, name, version), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPluginNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select plugin by name/version: %w", err)
	}
	return &p, nil
}

// GetByID returns the plugin with the given id (any status).
func (r *PGPluginRepo) GetByID(ctx context.Context, id uuid.UUID) (*Plugin, error) {
	var p Plugin
	err := scanPlugin(r.pool.QueryRow(ctx, `
        SELECT `+pluginColumns+`
        FROM plugin_registry
        WHERE id = $1`, id), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPluginNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select plugin: %w", err)
	}
	return &p, nil
}

// GetActiveByName returns the active version of a plugin by name.
func (r *PGPluginRepo) GetActiveByName(ctx context.Context, name string) (*Plugin, error) {
	var p Plugin
	err := scanPlugin(r.pool.QueryRow(ctx, `
        SELECT `+pluginColumns+`
        FROM plugin_registry
        WHERE name = $1 AND status = 'active'`, name), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPluginNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select active plugin: %w", err)
	}
	return &p, nil
}

// List returns plugins ordered by created_at DESC with a total count.
func (r *PGPluginRepo) List(ctx context.Context, limit, offset int) ([]Plugin, int, error) {
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM plugin_registry`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("db: count plugins: %w", err)
	}
	rows, err := r.pool.Query(ctx, `
        SELECT `+pluginColumns+`
        FROM plugin_registry
        ORDER BY created_at DESC
        LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("db: list plugins: %w", err)
	}
	plugins, err := scanPluginRows(rows)
	if err != nil {
		return nil, 0, err
	}
	return plugins, total, nil
}

const instanceColumns = `id, plugin_id, tree_id, profile_id, instance_name,
    settings, granted_permissions, status, invoke_count, created_at`

// scanInstance centralises the column order for plugin_instances row scans.
func scanInstance(row pgx.Row, inst *PluginInstance) error {
	var granted []string
	err := row.Scan(
		&inst.ID, &inst.PluginID, &inst.TreeID, &inst.ProfileID, &inst.InstanceName,
		&inst.Settings, &granted, &inst.Status, &inst.InvokeCount, &inst.CreatedAt,
	)
	if err != nil {
		return err
	}
	inst.GrantedPermissions = convertPermissions(granted)
	return nil
}

// scanInstanceRows collects a pgx.Rows result into a PluginInstance slice.
func scanInstanceRows(rows pgx.Rows) ([]PluginInstance, error) {
	defer rows.Close()
	var out []PluginInstance
	for rows.Next() {
		var inst PluginInstance
		if err := scanInstance(rows, &inst); err != nil {
			return nil, fmt.Errorf("db: scan instance row: %w", err)
		}
		out = append(out, inst)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate instance rows: %w", err)
	}
	return out, nil
}

// Install inserts an instance and increments the plugin's install_count in
// the same transaction (SPEC-PL-01 §6.1). A duplicate install violates
// idx_plugin_instances_unique_install → ErrAlreadyInstalled.
func (r *PGPluginRepo) Install(ctx context.Context, inst *PluginInstance) (*PluginInstance, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("db: begin install tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
        INSERT INTO plugin_instances
            (plugin_id, tree_id, profile_id, instance_name, settings,
             granted_permissions, status)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
        RETURNING `+instanceColumns,
		inst.PluginID, inst.TreeID, inst.ProfileID, inst.InstanceName,
		inst.Settings, permissionStrings(inst.GrantedPermissions), inst.Status,
	)
	var out PluginInstance
	if err := scanInstance(row, &out); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_plugin_instances_unique_install" {
			return nil, ErrAlreadyInstalled
		}
		return nil, fmt.Errorf("db: insert instance: %w", err)
	}

	if _, err := tx.Exec(ctx, `
        UPDATE plugin_registry
        SET install_count = install_count + 1
        WHERE id = $1`, inst.PluginID); err != nil {
		return nil, fmt.Errorf("db: increment install_count: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("db: commit install tx: %w", err)
	}
	return &out, nil
}

// GetInstance returns an instance by id.
func (r *PGPluginRepo) GetInstance(ctx context.Context, id uuid.UUID) (*PluginInstance, error) {
	var inst PluginInstance
	err := scanInstance(r.pool.QueryRow(ctx, `
        SELECT `+instanceColumns+`
        FROM plugin_instances
        WHERE id = $1`, id), &inst)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInstanceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select instance: %w", err)
	}
	return &inst, nil
}

// ListInstances returns instances for a profile, optionally filtered to a
// single tree (treeID nil = global instances only, per route contract).
func (r *PGPluginRepo) ListInstances(ctx context.Context, profileID uuid.UUID, treeID *uuid.UUID) ([]PluginInstance, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+instanceColumns+`
        FROM plugin_instances
        WHERE profile_id = $1 AND ($2::uuid IS NULL OR tree_id = $2)
        ORDER BY created_at DESC`, profileID, treeID)
	if err != nil {
		return nil, fmt.Errorf("db: list instances: %w", err)
	}
	return scanInstanceRows(rows)
}

// UpdateInstanceStatus transitions an instance's status.
func (r *PGPluginRepo) UpdateInstanceStatus(ctx context.Context, id uuid.UUID, status string) error {
	tag, err := r.pool.Exec(ctx, `
        UPDATE plugin_instances
        SET status = $2, updated_at = clock_timestamp()
        WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("db: update instance status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInstanceNotFound
	}
	return nil
}

// IncrementInvokeCount bumps the invoke_count of an instance.
func (r *PGPluginRepo) IncrementInvokeCount(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
        UPDATE plugin_instances
        SET invoke_count = invoke_count + 1, last_invoked_at = clock_timestamp()
        WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("db: increment invoke count: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInstanceNotFound
	}
	return nil
}

// Archive marks a plugin row archived (used when a new version replaces it).
func (r *PGPluginRepo) Archive(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
        UPDATE plugin_registry
        SET status = 'archived', archived_at = clock_timestamp(),
            updated_at = clock_timestamp()
        WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("db: archive plugin: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPluginNotFound
	}
	return nil
}

// GetVersionByName returns the stored row for (name, version), any status
// (SPEC-PL-01 §4.4 GetVersion backing).
func (r *PGPluginRepo) GetVersionByName(ctx context.Context, name, version string) (*Plugin, error) {
	p, err := r.getByNameVersion(ctx, name, version)
	if errors.Is(err, ErrPluginNotFound) {
		return nil, fmt.Errorf("%w: %s@%s", ErrPluginVersionNotFound, name, version)
	}
	return p, err
}

// ListVersionsByName returns every row for a name, newest first
// (SPEC-PL-01 §12.1 scenario 24). An unknown name yields an empty slice
// (callers resolve existence via GetActiveByName).
func (r *PGPluginRepo) ListVersionsByName(ctx context.Context, name string) ([]Plugin, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+pluginColumns+`
        FROM plugin_registry
        WHERE name = $1
        ORDER BY created_at DESC`, name)
	if err != nil {
		return nil, fmt.Errorf("db: list plugin versions: %w", err)
	}
	return scanPluginRows(rows)
}

// UpdateVersionChain archives oldID and links the version chain in a single
// transaction (SPEC-PL-01 §4.4): oldID → status 'archived', archived_at set,
// superseded_by_id = newID; newID → previous_version_id = oldID.
func (r *PGPluginRepo) UpdateVersionChain(ctx context.Context, oldID, newID uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin version chain tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	oldTag, err := tx.Exec(ctx, `
        UPDATE plugin_registry
        SET status = 'archived', archived_at = clock_timestamp(),
            superseded_by_id = $2, updated_at = clock_timestamp()
        WHERE id = $1`, oldID, newID)
	if err != nil {
		return fmt.Errorf("db: archive old version: %w", err)
	}
	if oldTag.RowsAffected() == 0 {
		return ErrPluginNotFound
	}

	newTag, err := tx.Exec(ctx, `
        UPDATE plugin_registry
        SET previous_version_id = $2, updated_at = clock_timestamp()
        WHERE id = $1`, newID, oldID)
	if err != nil {
		return fmt.Errorf("db: link previous version: %w", err)
	}
	if newTag.RowsAffected() == 0 {
		return ErrPluginNotFound
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit version chain tx: %w", err)
	}
	return nil
}

// ActivateVersion re-activates a historical version after a rollback
// (SPEC-PL-01 §4.4): status 'active', archived_at cleared, superseded_by_id
// set to the row it replaced (the previously active version).
func (r *PGPluginRepo) ActivateVersion(ctx context.Context, targetID, supersedingID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
        UPDATE plugin_registry
        SET status = 'active', archived_at = NULL,
            superseded_by_id = $2, updated_at = clock_timestamp()
        WHERE id = $1`, targetID, supersedingID)
	if err != nil {
		return fmt.Errorf("db: activate version: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPluginNotFound
	}
	return nil
}

// Audit appends a plugin lifecycle event to plugin_audit_log.
func (r *PGPluginRepo) Audit(ctx context.Context, entry *PluginAuditEntry) error {
	metadata, err := json.Marshal(entry.Metadata)
	if err != nil {
		return fmt.Errorf("db: marshal audit metadata: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
        INSERT INTO plugin_audit_log
            (plugin_id, instance_id, event_type, actor_profile_id, metadata)
        VALUES ($1, $2, $3, $4, $5)`,
		entry.PluginID, entry.InstanceID, entry.EventType, entry.ActorProfileID, metadata)
	if err != nil {
		return fmt.Errorf("db: insert audit entry: %w", err)
	}
	return nil
}

// --- helpers --------------------------------------------------------------

// permissionStrings converts a Permission slice to plain strings for pgx.
func permissionStrings(perms []Permission) []string {
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = string(p)
	}
	return out
}

// convertPermissions converts a []string scan result to []Permission.
func convertPermissions(raw []string) []Permission {
	out := make([]Permission, len(raw))
	for i, s := range raw {
		out[i] = Permission(s)
	}
	return out
}
