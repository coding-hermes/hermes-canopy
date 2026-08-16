package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// wrapRepoErr converts a persistence-layer error into the service error
// catalog. Known plugin sentinels pass through untouched; anything else is
// treated as a database failure (SPEC-IMPL-GAP-002 §6: ErrDatabaseUnavailable
// → 503, reusing service.ErrDatabaseUnavailable).
func wrapRepoErr(err error) error {
	switch {
	case errors.Is(err, ErrPluginNotFound),
		errors.Is(err, ErrInstanceNotFound),
		errors.Is(err, ErrAlreadyInstalled),
		errors.Is(err, ErrPermissionDenied),
		errors.Is(err, ErrAPINotFound),
		errors.Is(err, ErrInstanceNotActive):
		return err
	default:
		return fmt.Errorf("%w: %v", service.ErrDatabaseUnavailable, err)
	}
}

// Service is the plugin domain service. In addition to the GAP-002 §2
// interface, it exposes ListInstances/PauseInstance/ResumeInstance — the
// service half of the install lifecycle routes (GAP-002 §4.2).
type Service interface {
	Register(ctx context.Context, manifest PluginManifest, sourceJS string, authorID uuid.UUID) (*Plugin, error)
	Get(ctx context.Context, id uuid.UUID) (*Plugin, error)
	List(ctx context.Context, limit, offset int) ([]Plugin, int, error)
	Install(ctx context.Context, pluginID uuid.UUID, treeID *uuid.UUID, profileID uuid.UUID, granted []Permission) (*PluginInstance, error)
	// CheckPermission validates an api_call from a plugin instance. Returns
	// nil when allowed, or a typed error (ErrPermissionDenied / ErrAPINotFound /
	// ErrInstanceNotActive / ErrInstanceNotFound).
	CheckPermission(ctx context.Context, instanceID uuid.UUID, method string) error
	GetSource(ctx context.Context, id uuid.UUID) (string, string, error) // (sourceJS, sha256)
	ListInstances(ctx context.Context, profileID uuid.UUID, treeID *uuid.UUID) ([]PluginInstance, error)
	PauseInstance(ctx context.Context, id uuid.UUID) error
	ResumeInstance(ctx context.Context, id uuid.UUID) error
}

// PluginServiceImpl implements Service against a Repo.
type PluginServiceImpl struct {
	repo          Repo
	maxSourceSize int
}

// NewService returns a Service backed by repo with the configured maximum
// plugin source size in bytes.
func NewService(repo Repo, maxSourceSize int) *PluginServiceImpl {
	if maxSourceSize <= 0 {
		maxSourceSize = 1048576 // 1 MB default
	}
	return &PluginServiceImpl{repo: repo, maxSourceSize: maxSourceSize}
}

// Register implements the GAP-002 §5 register algorithm (exact order):
// size check → manifest parse → permission validation → slug derivation →
// SHA-256 → existing-row check (idempotent / version update) → insert → audit.
func (s *PluginServiceImpl) Register(ctx context.Context, manifest PluginManifest, sourceJS string, authorID uuid.UUID) (*Plugin, error) {
	// 1. Size check.
	if len(sourceJS) > s.maxSourceSize {
		return nil, ErrPluginTooLarge
	}

	// 2. Parse manifest (permission membership validated in step 3).
	if _, err := ParseManifest(sourceJS); err != nil {
		return nil, err
	}

	// 3. Validate permissions ⊆ AllPermissions.
	for _, p := range manifest.Permissions {
		if !ValidPermission(p) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidPermission, p)
		}
	}

	// 4. Slug derivation.
	slug := DeriveSlug(manifest.Name)
	if slug == "" {
		return nil, fmt.Errorf("%w: name %q cannot be slugified", ErrManifestValidationFailed, manifest.Name)
	}

	// 5. SHA-256 of the raw source bytes.
	sum := sha256.Sum256([]byte(sourceJS))
	shaHex := hex.EncodeToString(sum[:])

	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("plugin: encode manifest: %w", err)
	}

	// 6. Existing active row for this name?
	existing, err := s.repo.GetActiveByName(ctx, manifest.Name)
	if err != nil && !errors.Is(err, ErrPluginNotFound) {
		return nil, wrapRepoErr(err)
	}

	newPlugin := &Plugin{
		Name:            manifest.Name,
		Slug:            slug,
		Version:         manifest.Version,
		Description:     manifest.Description,
		AuthorProfileID: authorID,
		Permissions:     manifest.Permissions,
		ManifestJSON:    manifestJSON,
		SourceJS:        sourceJS,
		SourceSHA256:    shaHex,
		SourceByteSize:  len(sourceJS),
		IconURL:         manifest.IconURL,
		Status:          PluginStatusActive,
	}

	if existing != nil {
		// Same (name, version) → idempotent: return the stored row, no new
		// row, no new audit entry (SPEC-PL-01 §12.1 test 7).
		if existing.Version == manifest.Version {
			return existing, nil
		}
		// Same name, new version → archive the old active row and link the
		// new row to it (GAP-002 §5 step 6).
		if err := s.repo.Archive(ctx, existing.ID); err != nil {
			return nil, wrapRepoErr(err)
		}
		newPlugin.PreviousVersionID = &existing.ID
	} else {
		// First version of this name.
		newPlugin.IsRootVersion = true
	}

	// 7. Insert.
	created, err := s.repo.Register(ctx, newPlugin)
	if err != nil {
		return nil, wrapRepoErr(err)
	}

	// 8. Audit the registration.
	if err := s.repo.Audit(ctx, &PluginAuditEntry{
		PluginID:       created.ID,
		EventType:      AuditEventRegistered,
		ActorProfileID: authorID,
		Metadata: map[string]any{
			"name":    created.Name,
			"version": created.Version,
		},
	}); err != nil {
		return nil, wrapRepoErr(err)
	}

	return created, nil
}

// Get returns a plugin by id (any status).
func (s *PluginServiceImpl) Get(ctx context.Context, id uuid.UUID) (*Plugin, error) {
	p, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, wrapRepoErr(err)
	}
	return p, nil
}

// List returns plugins with pagination and the total count.
func (s *PluginServiceImpl) List(ctx context.Context, limit, offset int) ([]Plugin, int, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	plugins, total, err := s.repo.List(ctx, limit, offset)
	if err != nil {
		return nil, 0, wrapRepoErr(err)
	}
	return plugins, total, nil
}

// Install creates a per-tree/per-profile instance of a plugin. The granted
// permission set must be a subset of the plugin's declared permissions.
func (s *PluginServiceImpl) Install(ctx context.Context, pluginID uuid.UUID, treeID *uuid.UUID, profileID uuid.UUID, granted []Permission) (*PluginInstance, error) {
	p, err := s.repo.GetByID(ctx, pluginID)
	if err != nil {
		return nil, wrapRepoErr(err)
	}
	switch p.Status {
	case PluginStatusDisabled:
		return nil, ErrPluginDisabled
	case PluginStatusArchived:
		return nil, ErrPluginArchived
	}

	// Every granted permission must be declared by the plugin.
	declared := make(map[Permission]bool, len(p.Permissions))
	for _, perm := range p.Permissions {
		declared[perm] = true
	}
	for _, perm := range granted {
		if !declared[perm] {
			return nil, fmt.Errorf("%w: %q", ErrPermissionNotDeclared, perm)
		}
	}

	inst, err := s.repo.Install(ctx, &PluginInstance{
		PluginID:           pluginID,
		TreeID:             treeID,
		ProfileID:          profileID,
		Settings:           []byte("{}"),
		GrantedPermissions: granted,
		Status:             InstanceStatusActive,
	})
	if err != nil {
		return nil, wrapRepoErr(err)
	}

	if err := s.repo.Audit(ctx, &PluginAuditEntry{
		PluginID:       pluginID,
		InstanceID:     &inst.ID,
		EventType:      AuditEventInstalled,
		ActorProfileID: profileID,
		Metadata: map[string]any{
			"tree_id": treeID,
		},
	}); err != nil {
		return nil, wrapRepoErr(err)
	}
	return inst, nil
}

// CheckPermission resolves an api_call from a plugin instance against its
// granted permissions and status (GAP-002 §2 / SPEC-PL-01 §7.4).
func (s *PluginServiceImpl) CheckPermission(ctx context.Context, instanceID uuid.UUID, method string) error {
	inst, err := s.repo.GetInstance(ctx, instanceID)
	if err != nil {
		return wrapRepoErr(err)
	}
	if inst.Status != InstanceStatusActive {
		return ErrInstanceNotActive
	}
	if err := CheckPermissionGate(inst.GrantedPermissions, method); err != nil {
		return err
	}
	if err := s.repo.IncrementInvokeCount(ctx, instanceID); err != nil {
		return wrapRepoErr(err)
	}
	return nil
}

// GetSource returns the raw plugin source and its SHA-256 hex digest.
func (s *PluginServiceImpl) GetSource(ctx context.Context, id uuid.UUID) (string, string, error) {
	p, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return "", "", wrapRepoErr(err)
	}
	return p.SourceJS, p.SourceSHA256, nil
}

// ListInstances returns instances for the caller's profile, optionally
// scoped to a tree.
func (s *PluginServiceImpl) ListInstances(ctx context.Context, profileID uuid.UUID, treeID *uuid.UUID) ([]PluginInstance, error) {
	instances, err := s.repo.ListInstances(ctx, profileID, treeID)
	if err != nil {
		return nil, wrapRepoErr(err)
	}
	return instances, nil
}

// PauseInstance pauses an instance (status 'active' → 'paused').
func (s *PluginServiceImpl) PauseInstance(ctx context.Context, id uuid.UUID) error {
	inst, err := s.repo.GetInstance(ctx, id)
	if err != nil {
		return wrapRepoErr(err)
	}
	if err := s.repo.UpdateInstanceStatus(ctx, id, InstanceStatusPaused); err != nil {
		return wrapRepoErr(err)
	}
	if err := s.repo.Audit(ctx, &PluginAuditEntry{
		PluginID:       inst.PluginID,
		InstanceID:     &id,
		EventType:      AuditEventPaused,
		ActorProfileID: inst.ProfileID,
	}); err != nil {
		return wrapRepoErr(err)
	}
	return nil
}

// ResumeInstance resumes an instance (status 'paused' → 'active').
func (s *PluginServiceImpl) ResumeInstance(ctx context.Context, id uuid.UUID) error {
	inst, err := s.repo.GetInstance(ctx, id)
	if err != nil {
		return wrapRepoErr(err)
	}
	if err := s.repo.UpdateInstanceStatus(ctx, id, InstanceStatusActive); err != nil {
		return wrapRepoErr(err)
	}
	if err := s.repo.Audit(ctx, &PluginAuditEntry{
		PluginID:       inst.PluginID,
		InstanceID:     &id,
		EventType:      AuditEventResumed,
		ActorProfileID: inst.ProfileID,
	}); err != nil {
		return wrapRepoErr(err)
	}
	return nil
}
