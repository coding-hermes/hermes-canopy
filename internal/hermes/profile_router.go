package hermes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNoProfileMapping    = errors.New("hermes: no profile mapped for this workspace")
	ErrProfileNameRequired = errors.New("hermes: profile name is required")
	ErrTokenEmpty          = errors.New("hermes: profile token is empty")
)

// ProfileMapping associates a Canopy workspace with a Hermes profile.
type ProfileMapping struct {
	WorkspaceID           uuid.UUID `json:"workspace_id"`
	ProfileName           string    `json:"profile_name"`
	DisplayName           string    `json:"display_name"`
	IsActive              bool      `json:"is_active"`
	ModelPreference       string    `json:"model_preference,omitempty"`
	ProfileTokenEncrypted []byte    `json:"-"`
	MappedAt              time.Time `json:"mapped_at"`
	LastUsedAt            time.Time `json:"last_used_at"`
}

// AvailableProfile represents a profile accessible through the Hermes gateway.
type AvailableProfile struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	DefaultModel string `json:"default_model"`
}

// ProfileRouter maps Canopy workspaces to Hermes profiles.
type ProfileRouter interface {
	GetActiveProfile(ctx context.Context, workspaceID uuid.UUID) (*ProfileMapping, error)
	SetActiveProfile(ctx context.Context, workspaceID uuid.UUID, profileName string, profileToken string) error
	ListProfiles(ctx context.Context, workspaceID uuid.UUID) ([]ProfileMapping, error)
	RemoveProfile(ctx context.Context, workspaceID uuid.UUID, profileName string) error
	GetProfileToken(ctx context.Context, workspaceID uuid.UUID, profileName string) (string, error)
	ListAvailableProfiles(ctx context.Context) ([]AvailableProfile, error)
}

// PGProfileRouter persists profile mappings in PostgreSQL.
type PGProfileRouter struct {
	pool          *pgxpool.Pool
	encryptionKey []byte
}

// NewPGProfileRouter returns a PostgreSQL-backed ProfileRouter.
func NewPGProfileRouter(pool *pgxpool.Pool, encryptionKey []byte) *PGProfileRouter {
	keyCopy := append([]byte(nil), encryptionKey...)
	return &PGProfileRouter{pool: pool, encryptionKey: keyCopy}
}

func (r *PGProfileRouter) GetActiveProfile(ctx context.Context, workspaceID uuid.UUID) (*ProfileMapping, error) {
	const query = `
		SELECT workspace_id, profile_name, display_name, is_active,
		       COALESCE(model_preference, ''), profile_token_encrypted,
		       mapped_at, last_used_at
		FROM profile_route
		WHERE workspace_id = $1 AND is_active = true
		ORDER BY last_used_at DESC
		LIMIT 1`

	mapping, err := scanProfileMapping(r.pool.QueryRow(ctx, query, workspaceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoProfileMapping
	}
	if err != nil {
		return nil, fmt.Errorf("hermes: get active profile: %w", err)
	}
	return mapping, nil
}

func (r *PGProfileRouter) SetActiveProfile(ctx context.Context, workspaceID uuid.UUID, profileName string, profileToken string) error {
	profileName = strings.TrimSpace(profileName)
	if profileName == "" {
		return ErrProfileNameRequired
	}
	if profileToken == "" {
		return ErrTokenEmpty
	}

	encryptedToken, err := EncryptToken(r.encryptionKey, profileToken)
	if err != nil {
		return fmt.Errorf("hermes: set active profile: encrypt token: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("hermes: set active profile: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE profile_route
		SET is_active = false
		WHERE workspace_id = $1 AND is_active = true`, workspaceID); err != nil {
		return fmt.Errorf("hermes: set active profile: reset active profile: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO profile_route (
			workspace_id, profile_name, display_name, is_active,
			profile_token_encrypted, last_used_at
		)
		VALUES ($1, $2, $2, true, $3, NOW())
		ON CONFLICT (workspace_id, profile_name) DO UPDATE
		SET is_active = true,
		    profile_token_encrypted = EXCLUDED.profile_token_encrypted,
		    last_used_at = NOW()`, workspaceID, profileName, encryptedToken); err != nil {
		return fmt.Errorf("hermes: set active profile: upsert profile: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("hermes: set active profile: commit transaction: %w", err)
	}
	return nil
}

func (r *PGProfileRouter) ListProfiles(ctx context.Context, workspaceID uuid.UUID) ([]ProfileMapping, error) {
	const query = `
		SELECT workspace_id, profile_name, display_name, is_active,
		       COALESCE(model_preference, ''), profile_token_encrypted,
		       mapped_at, last_used_at
		FROM profile_route
		WHERE workspace_id = $1
		ORDER BY last_used_at DESC`

	rows, err := r.pool.Query(ctx, query, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("hermes: list profiles: %w", err)
	}
	defer rows.Close()

	mappings := make([]ProfileMapping, 0)
	for rows.Next() {
		mapping, err := scanProfileMapping(rows)
		if err != nil {
			return nil, fmt.Errorf("hermes: list profiles: scan: %w", err)
		}
		mappings = append(mappings, *mapping)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hermes: list profiles: rows: %w", err)
	}
	return mappings, nil
}

func (r *PGProfileRouter) RemoveProfile(ctx context.Context, workspaceID uuid.UUID, profileName string) error {
	profileName = strings.TrimSpace(profileName)
	if profileName == "" {
		return ErrProfileNameRequired
	}
	if _, err := r.pool.Exec(ctx, `
		DELETE FROM profile_route
		WHERE workspace_id = $1 AND profile_name = $2`, workspaceID, profileName); err != nil {
		return fmt.Errorf("hermes: remove profile: %w", err)
	}
	return nil
}

func (r *PGProfileRouter) GetProfileToken(ctx context.Context, workspaceID uuid.UUID, profileName string) (string, error) {
	profileName = strings.TrimSpace(profileName)
	if profileName == "" {
		return "", ErrProfileNameRequired
	}

	var encryptedToken []byte
	err := r.pool.QueryRow(ctx, `
		SELECT profile_token_encrypted
		FROM profile_route
		WHERE workspace_id = $1 AND profile_name = $2`, workspaceID, profileName).Scan(&encryptedToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoProfileMapping
	}
	if err != nil {
		return "", fmt.Errorf("hermes: get profile token: %w", err)
	}
	if len(encryptedToken) == 0 {
		return "", ErrTokenEmpty
	}

	token, err := DecryptToken(r.encryptionKey, encryptedToken)
	if err != nil {
		return "", fmt.Errorf("hermes: get profile token: decrypt token: %w", err)
	}
	return token, nil
}

func (r *PGProfileRouter) ListAvailableProfiles(context.Context) ([]AvailableProfile, error) {
	return []AvailableProfile{
		{Name: "coding", DisplayName: "Coding", DefaultModel: "deepseek-v4-pro"},
		{Name: "creative", DisplayName: "Creative", DefaultModel: "gpt-5.6-sol"},
		{Name: "research", DisplayName: "Research", DefaultModel: "deepseek-v4-flash"},
	}, nil
}

type profileScanner interface {
	Scan(dest ...any) error
}

func scanProfileMapping(row profileScanner) (*ProfileMapping, error) {
	var mapping ProfileMapping
	if err := row.Scan(
		&mapping.WorkspaceID,
		&mapping.ProfileName,
		&mapping.DisplayName,
		&mapping.IsActive,
		&mapping.ModelPreference,
		&mapping.ProfileTokenEncrypted,
		&mapping.MappedAt,
		&mapping.LastUsedAt,
	); err != nil {
		return nil, err
	}
	return &mapping, nil
}
