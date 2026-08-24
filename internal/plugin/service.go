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
		errors.Is(err, ErrPluginVersionNotFound),
		errors.Is(err, ErrVersionConflict),
		errors.Is(err, ErrRollbackFailed),
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
	// Update publishes a new version of an existing plugin: the old active
	// row is archived + chain-linked, the new row becomes active
	// (SPEC-PL-01 §4.4). Same (name, version) → ErrVersionConflict.
	Update(ctx context.Context, name, sourceJS string, actorID uuid.UUID) (*Plugin, error)
	// Rollback re-activates a historical version of a plugin, archiving the
	// current active row and linking the chain in both directions
	// (SPEC-PL-01 §4.4 / §12.1 scenario 17).
	Rollback(ctx context.Context, name, targetVersion string, actorID uuid.UUID) (*Plugin, error)
	// ListVersions returns the full version history of a plugin, newest
	// first (SPEC-PL-01 §4.4).
	ListVersions(ctx context.Context, name string) ([]PluginVersion, error)
	// GetVersion returns the slim view of one (name, version) row
	// (SPEC-PL-01 §4.4).
	GetVersion(ctx context.Context, name, version string) (*PluginVersion, error)
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

// --- Version lifecycle (SPEC-PL-01 §4.4) ----------------------------------

// validateUpdateManifest runs the shared Register validation pipeline
// (size → parse → permissions ⊆ AllPermissions → slug derivation) for the
// Update path, then enforces that the manifest name matches the plugin
// being updated.
func (s *PluginServiceImpl) validateUpdateManifest(name, sourceJS string) (*PluginManifest, error) {
	if len(sourceJS) > s.maxSourceSize {
		return nil, ErrPluginTooLarge
	}
	manifest, err := ParseManifest(sourceJS)
	if err != nil {
		return nil, err
	}
	if manifest.Name != name {
		return nil, fmt.Errorf("%w: manifest name %q does not match plugin %q", ErrManifestValidationFailed, manifest.Name, name)
	}
	for _, p := range manifest.Permissions {
		if !ValidPermission(p) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidPermission, p)
		}
	}
	if slug := DeriveSlug(manifest.Name); slug == "" {
		return nil, fmt.Errorf("%w: name %q cannot be slugified", ErrManifestValidationFailed, manifest.Name)
	}
	return manifest, nil
}

// Update publishes a new version of an existing plugin (SPEC-PL-01 §4.4 /
// §12.1 scenarios 8-9). The previous active row is archived and chain-linked
// (superseded_by_id / previous_version_id), the new row becomes active, and
// an 'updated' audit entry is appended.
//
// NOTE: no SSE broadcast here — the tree-scoped hub only relays per-tree
// events and plugin registry events are global. Hot-reload SSE is a later
// wave (SPEC-PL-01 §8.3).
func (s *PluginServiceImpl) Update(ctx context.Context, name, sourceJS string, actorID uuid.UUID) (*Plugin, error) {
	manifest, err := s.validateUpdateManifest(name, sourceJS)
	if err != nil {
		return nil, err
	}

	// The plugin must exist with an active version.
	active, err := s.repo.GetActiveByName(ctx, name)
	if err != nil {
		return nil, wrapRepoErr(err)
	}

	// Scenario 9: same (name, version) re-upload → VERSION_CONFLICT.
	if active.Version == manifest.Version {
		return nil, fmt.Errorf("%w: %s@%s is already the active version", ErrVersionConflict, name, manifest.Version)
	}

	// Build the new row (mirrors Register; Update never sets is_root_version).
	sum := sha256.Sum256([]byte(sourceJS))
	shaHex := hex.EncodeToString(sum[:])
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("plugin: encode manifest: %w", err)
	}
	newPlugin := &Plugin{
		Name:            manifest.Name,
		Slug:            DeriveSlug(manifest.Name),
		Version:         manifest.Version,
		Description:     manifest.Description,
		AuthorProfileID: actorID,
		Permissions:     manifest.Permissions,
		ManifestJSON:    manifestJSON,
		SourceJS:        sourceJS,
		SourceSHA256:    shaHex,
		SourceByteSize:  len(sourceJS),
		IconURL:         manifest.IconURL,
		Status:          PluginStatusActive,
	}

	created, err := s.repo.Register(ctx, newPlugin)
	if err != nil {
		return nil, wrapRepoErr(err)
	}

	// Archive the old active row and link the chain in both directions.
	if err := s.repo.UpdateVersionChain(ctx, active.ID, created.ID); err != nil {
		return nil, wrapRepoErr(err)
	}

	if err := s.repo.Audit(ctx, &PluginAuditEntry{
		PluginID:       created.ID,
		EventType:      AuditEventUpdated,
		ActorProfileID: actorID,
		Metadata: map[string]any{
			"name":             created.Name,
			"version":          created.Version,
			"previous_version": active.Version,
		},
	}); err != nil {
		return nil, wrapRepoErr(err)
	}
	return created, nil
}

// Rollback re-activates a historical version of a plugin (SPEC-PL-01 §4.4 /
// §12.1 scenarios 17-18). The current active row is archived with
// superseded_by_id pointing at the restored version, and the target gets
// previous_version_id set to the archived row — the chain is linked in both
// directions, so a later Update of the restored version archives it cleanly.
func (s *PluginServiceImpl) Rollback(ctx context.Context, name, targetVersion string, actorID uuid.UUID) (*Plugin, error) {
	active, err := s.repo.GetActiveByName(ctx, name)
	if err != nil {
		return nil, wrapRepoErr(err)
	}

	target, err := s.repo.GetVersionByName(ctx, name, targetVersion)
	if err != nil {
		// Scenario 18: target not in history → ROLLBACK_FAILED (400), not a
		// generic version-not-found. ErrRollbackFailed unwraps to
		// ErrPluginVersionNotFound so callers that only check the 404 sentinel
		// still match.
		if errors.Is(err, ErrPluginVersionNotFound) {
			return nil, fmt.Errorf("%w: version %s@%s not in plugin history", ErrRollbackFailed, name, targetVersion)
		}
		return nil, wrapRepoErr(err)
	}

	// Scenario 18: target must be a real historical version.
	if target.ID == active.ID {
		return nil, fmt.Errorf("%w: version %s@%s is already active", ErrRollbackFailed, name, targetVersion)
	}

	// Archive the current active (linked to the target) + re-activate the
	// target (linked to the archived row) — one direction each, both set.
	if err := s.repo.UpdateVersionChain(ctx, active.ID, target.ID); err != nil {
		return nil, wrapRepoErr(err)
	}
	if err := s.repo.ActivateVersion(ctx, target.ID, active.ID); err != nil {
		return nil, wrapRepoErr(err)
	}

	// Re-read the restored row so the response reflects the post-rollback
	// state (active, links set) rather than the pre-rollback snapshot.
	target, err = s.repo.GetVersionByName(ctx, name, targetVersion)
	if err != nil {
		return nil, wrapRepoErr(err)
	}

	if err := s.repo.Audit(ctx, &PluginAuditEntry{
		PluginID:       target.ID,
		EventType:      AuditEventRolledBack,
		ActorProfileID: actorID,
		Metadata: map[string]any{
			"name":             name,
			"version":          targetVersion,
			"previous_version": active.Version,
		},
	}); err != nil {
		return nil, wrapRepoErr(err)
	}
	return target, nil
}

// ListVersions returns the full version history of a plugin (slim view),
// newest first (SPEC-PL-01 §4.4 / §12.1 scenario 24).
func (s *PluginServiceImpl) ListVersions(ctx context.Context, name string) ([]PluginVersion, error) {
	rows, err := s.repo.ListVersionsByName(ctx, name)
	if err != nil {
		return nil, wrapRepoErr(err)
	}
	versions := make([]PluginVersion, 0, len(rows))
	for i := range rows {
		versions = append(versions, rows[i].ToVersion())
	}
	return versions, nil
}

// GetVersion returns the slim view of a single (name, version) row
// (SPEC-PL-01 §4.4).
func (s *PluginServiceImpl) GetVersion(ctx context.Context, name, version string) (*PluginVersion, error) {
	p, err := s.repo.GetVersionByName(ctx, name, version)
	if err != nil {
		return nil, wrapRepoErr(err)
	}
	v := p.ToVersion()
	return &v, nil
}
