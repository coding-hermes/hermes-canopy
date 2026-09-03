package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrPluginNotFound  = errors.New("db: plugin not found")
	ErrPluginDuplicate = errors.New("db: plugin name and version already exist")
)

type Plugin struct {
	ID                uuid.UUID       `json:"id"`
	Name              string          `json:"name"`
	Slug              string          `json:"slug"`
	Version           string          `json:"version"`
	Description       string          `json:"description"`
	AuthorProfileID   uuid.UUID       `json:"authorProfileId"`
	Permissions       []string        `json:"permissions"`
	ManifestJSON      json.RawMessage `json:"manifest"`
	SourceJS          string          `json:"-"`
	SourceSHA256      string          `json:"sourceSha256"`
	SourceByteSize    int             `json:"sourceByteSize"`
	IconURL           string          `json:"iconUrl"`
	Status            string          `json:"status"`
	InstallCount      int             `json:"installCount"`
	IsRootVersion     bool            `json:"isRootVersion"`
	SupersededByID    *uuid.UUID      `json:"supersededById,omitempty"`
	PreviousVersionID *uuid.UUID      `json:"previousVersionId,omitempty"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
	ArchivedAt        *time.Time      `json:"archivedAt,omitempty"`
}

type PluginRepo interface {
	Register(context.Context, *Plugin) (*Plugin, error)
	GetByID(context.Context, uuid.UUID) (*Plugin, error)
	ListActive(context.Context) ([]Plugin, error)
	Versions(context.Context, string) ([]Plugin, error)
	Activate(context.Context, string, uuid.UUID) (*Plugin, error)
	Disable(context.Context, string) (*Plugin, error)
	Archive(context.Context, string) (*Plugin, error)
	Rollback(context.Context, string) (*Plugin, error)
	Audit(context.Context, uuid.UUID, string, uuid.UUID, map[string]any) error
	Update(context.Context, *Plugin, uuid.UUID) (*Plugin, error)
	RollbackTo(context.Context, string, string, uuid.UUID) (*Plugin, error)
}

func (r *PGPluginRepo) Update(ctx context.Context, p *Plugin, actor uuid.UUID) (*Plugin, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var activeID uuid.UUID
	var activeVersion string
	if err = tx.QueryRow(ctx, `SELECT id,version FROM plugin_registry WHERE slug=$1 AND status='active' FOR UPDATE`, p.Slug).Scan(&activeID, &activeVersion); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPluginNotFound
	} else if err != nil {
		return nil, err
	}
	if activeVersion == p.Version {
		return nil, ErrPluginDuplicate
	}
	var exists uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM plugin_registry WHERE name=$1 AND version=$2`, p.Name, p.Version).Scan(&exists); err == nil {
		return nil, ErrPluginDuplicate
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE plugin_registry SET status='archived',archived_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, activeID); err != nil {
		return nil, err
	}
	row := tx.QueryRow(ctx, `INSERT INTO plugin_registry (name,slug,version,description,author_profile_id,permissions,manifest_json,source_js,source_sha256,source_byte_size,icon_url,status,is_root_version,previous_version_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'active',false,$12) RETURNING `+registryColumns, p.Name, p.Slug, p.Version, p.Description, p.AuthorProfileID, p.Permissions, p.ManifestJSON, p.SourceJS, p.SourceSHA256, p.SourceByteSize, p.IconURL, activeID)
	out, err := scanRegistry(row)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE plugin_registry SET superseded_by_id=$2 WHERE id=$1`, activeID, out.ID); err != nil {
		return nil, err
	}
	meta, _ := json.Marshal(map[string]any{"name": out.Name, "version": out.Version, "previous_version": activeVersion})
	if _, err = tx.Exec(ctx, `INSERT INTO plugin_audit_log(plugin_id,event_type,actor_profile_id,metadata) VALUES($1,'updated',$2,$3)`, out.ID, actor, meta); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *PGPluginRepo) RollbackTo(ctx context.Context, slug, version string, actor uuid.UUID) (*Plugin, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var activeID uuid.UUID
	var name, activeVersion string
	if err = tx.QueryRow(ctx, `SELECT id,name,version FROM plugin_registry WHERE slug=$1 AND status='active' FOR UPDATE`, slug).Scan(&activeID, &name, &activeVersion); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPluginNotFound
	} else if err != nil {
		return nil, err
	}
	var targetID uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM plugin_registry WHERE name=$1 AND version=$2 FOR UPDATE`, name, version).Scan(&targetID); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPluginNotFound
	} else if err != nil {
		return nil, err
	}
	if targetID == activeID {
		return nil, ErrPluginDuplicate
	}
	if _, err = tx.Exec(ctx, `UPDATE plugin_registry SET status='archived',archived_at=clock_timestamp(),superseded_by_id=$2,updated_at=clock_timestamp() WHERE id=$1`, activeID, targetID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE plugin_registry SET status='active',archived_at=NULL,previous_version_id=$2,superseded_by_id=NULL,updated_at=clock_timestamp() WHERE id=$1`, targetID, activeID); err != nil {
		return nil, err
	}
	meta, _ := json.Marshal(map[string]any{"name": name, "version": version, "previous_version": activeVersion})
	if _, err = tx.Exec(ctx, `INSERT INTO plugin_audit_log(plugin_id,event_type,actor_profile_id,metadata) VALUES($1,'rolled_back',$2,$3)`, targetID, actor, meta); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.GetByID(ctx, targetID)
}

type PGPluginRepo struct{ pool *pgxpool.Pool }

func NewPGPluginRegistryRepo(pool *pgxpool.Pool) *PGPluginRepo { return &PGPluginRepo{pool: pool} }

const registryColumns = `id,name,slug,version,description,author_profile_id,permissions,
manifest_json,source_js,source_sha256,source_byte_size,icon_url,status,install_count,
is_root_version,superseded_by_id,previous_version_id,created_at,updated_at,archived_at`

func scanRegistry(row pgx.Row) (*Plugin, error) {
	var p Plugin
	err := row.Scan(&p.ID, &p.Name, &p.Slug, &p.Version, &p.Description,
		&p.AuthorProfileID, &p.Permissions, &p.ManifestJSON, &p.SourceJS,
		&p.SourceSHA256, &p.SourceByteSize, &p.IconURL, &p.Status,
		&p.InstallCount, &p.IsRootVersion, &p.SupersededByID,
		&p.PreviousVersionID, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPluginNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: scan plugin: %w", err)
	}
	return &p, nil
}

func (r *PGPluginRepo) Register(ctx context.Context, p *Plugin) (*Plugin, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var existing uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM plugin_registry WHERE name=$1 AND version=$2`, p.Name, p.Version).Scan(&existing); err == nil {
		return nil, ErrPluginDuplicate
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var previous *uuid.UUID
	var previousID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM plugin_registry WHERE name=$1 AND status='active' FOR UPDATE`, p.Name).Scan(&previousID)
	if err == nil {
		previous = &previousID
		if _, err = tx.Exec(ctx, `UPDATE plugin_registry SET status='archived',archived_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, previousID); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	p.IsRootVersion = previous == nil
	row := tx.QueryRow(ctx, `INSERT INTO plugin_registry
(name,slug,version,description,author_profile_id,permissions,manifest_json,source_js,source_sha256,source_byte_size,icon_url,status,is_root_version)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'active',$12) RETURNING `+registryColumns,
		p.Name, p.Slug, p.Version, p.Description, p.AuthorProfileID, p.Permissions, p.ManifestJSON, p.SourceJS, p.SourceSHA256, p.SourceByteSize, p.IconURL, p.IsRootVersion)
	out, err := scanRegistry(row)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return nil, ErrPluginDuplicate
		}
		return nil, err
	}
	if previous != nil {
		if _, err = tx.Exec(ctx, `UPDATE plugin_registry SET superseded_by_id=$2 WHERE id=$1`, previousID, out.ID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE plugin_registry SET previous_version_id=$2 WHERE id=$1`, out.ID, previousID); err != nil {
			return nil, err
		}
		out.PreviousVersionID = previous
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *PGPluginRepo) GetByID(ctx context.Context, id uuid.UUID) (*Plugin, error) {
	return scanRegistry(r.pool.QueryRow(ctx, `SELECT `+registryColumns+` FROM plugin_registry WHERE id=$1`, id))
}

func scanRegistryRows(rows pgx.Rows) ([]Plugin, error) {
	defer rows.Close()
	out := []Plugin{}
	for rows.Next() {
		p, err := scanRegistry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *PGPluginRepo) ListActive(ctx context.Context) ([]Plugin, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+registryColumns+` FROM plugin_registry WHERE status='active' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return scanRegistryRows(rows)
}
func (r *PGPluginRepo) Versions(ctx context.Context, name string) ([]Plugin, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+registryColumns+` FROM plugin_registry WHERE name=$1 ORDER BY created_at DESC`, name)
	if err != nil {
		return nil, err
	}
	return scanRegistryRows(rows)
}

func (r *PGPluginRepo) Activate(ctx context.Context, name string, target uuid.UUID) (*Plugin, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var old *uuid.UUID
	var oldID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM plugin_registry WHERE name=$1 AND status='active' FOR UPDATE`, name).Scan(&oldID)
	if err == nil {
		old = &oldID
		if oldID == target {
			return scanRegistry(tx.QueryRow(ctx, `SELECT `+registryColumns+` FROM plugin_registry WHERE id=$1`, target))
		}
		_, err = tx.Exec(ctx, `UPDATE plugin_registry SET status='archived',archived_at=clock_timestamp(),superseded_by_id=$2,updated_at=clock_timestamp() WHERE id=$1`, oldID, target)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `UPDATE plugin_registry SET status='active',archived_at=NULL,previous_version_id=$2,superseded_by_id=NULL,updated_at=clock_timestamp() WHERE id=$1 AND name=$3`, target, old, name)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrPluginNotFound
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.GetByID(ctx, target)
}

func (r *PGPluginRepo) setStatus(ctx context.Context, name, status string) (*Plugin, error) {
	return scanRegistry(r.pool.QueryRow(ctx, `UPDATE plugin_registry SET status=$2,archived_at=CASE WHEN $2='archived' THEN clock_timestamp() ELSE archived_at END,updated_at=clock_timestamp() WHERE name=$1 AND status='active' RETURNING `+registryColumns, name, status))
}
func (r *PGPluginRepo) Disable(ctx context.Context, name string) (*Plugin, error) {
	return r.setStatus(ctx, name, "disabled")
}
func (r *PGPluginRepo) Archive(ctx context.Context, name string) (*Plugin, error) {
	return r.setStatus(ctx, name, "archived")
}
func (r *PGPluginRepo) Rollback(ctx context.Context, name string) (*Plugin, error) {
	var id uuid.UUID
	err := r.pool.QueryRow(ctx, `SELECT previous_version_id FROM plugin_registry WHERE name=$1 AND status='active'`, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPluginNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.Activate(ctx, name, id)
}
func (r *PGPluginRepo) Audit(ctx context.Context, pluginID uuid.UUID, event string, actor uuid.UUID, metadata map[string]any) error {
	b, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO plugin_audit_log(plugin_id,event_type,actor_profile_id,metadata) VALUES($1,$2,$3,$4)`, pluginID, event, actor, b)
	return err
}
