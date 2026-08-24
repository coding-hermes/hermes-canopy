// Package plugin implements the backend half of Canopy's plugin sandbox:
// plugin registration (manifest-as-comment-block + source JS), PostgreSQL
// persistence, capability-scoped permission checks on API calls, and the
// sandbox bootstrap document served to the frontend's sandboxed iframes.
//
// Spec: SPEC-IMPL-GAP-002 (implementation), SPEC-PL-01 (design).
package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Permission is a capability granted to a plugin at install time.
type Permission string

const (
	PermissionDataRead       Permission = "data_read"
	PermissionDataWrite      Permission = "data_write"
	PermissionNotification   Permission = "notification"
	PermissionCalendarRead   Permission = "calendar_read"
	PermissionCalendarWrite  Permission = "calendar_write"
	PermissionNetworkRequest Permission = "network_request"
)

// AllPermissions is the canonical set a plugin may declare.
var AllPermissions = []Permission{
	PermissionDataRead,
	PermissionDataWrite,
	PermissionNotification,
	PermissionCalendarRead,
	PermissionCalendarWrite,
	PermissionNetworkRequest,
}

// ValidPermission reports whether p is a member of AllPermissions.
func ValidPermission(p Permission) bool {
	for _, candidate := range AllPermissions {
		if candidate == p {
			return true
		}
	}
	return false
}

// PluginStatus is the lifecycle status of a plugin_registry row.
type PluginStatus string

const (
	PluginStatusActive   PluginStatus = "active"
	PluginStatusDisabled PluginStatus = "disabled"
	PluginStatusArchived PluginStatus = "archived"
)

// PluginRenderType determines UI mounting.
type PluginRenderType string

const (
	RenderTypeCard       PluginRenderType = "card"
	RenderTypeEmbed      PluginRenderType = "embed"
	RenderTypeBackground PluginRenderType = "background"
)

// ValidRenderType reports whether t is a supported render type.
func ValidRenderType(t PluginRenderType) bool {
	switch t {
	case RenderTypeCard, RenderTypeEmbed, RenderTypeBackground:
		return true
	default:
		return false
	}
}

// InstanceStatus values for plugin_instances.status.
const (
	InstanceStatusActive      = "active"
	InstanceStatusPaused      = "paused"
	InstanceStatusUninstalled = "uninstalled"
)

// PluginManifest is the parsed manifest embedded in the plugin source header.
type PluginManifest struct {
	Name         string           `json:"name"`
	Version      string           `json:"version"` // semver
	Description  string           `json:"description"`
	Permissions  []Permission     `json:"permissions"`
	RenderType   PluginRenderType `json:"render_type"`
	EntryPoint   string           `json:"entry_point"` // e.g. "main"
	IconURL      string           `json:"icon_url,omitempty"`
	AuthorName   string           `json:"author_name,omitempty"`
	MinCanopyVer string           `json:"min_canopy_version,omitempty"`
}

// Plugin is a row in plugin_registry.
type Plugin struct {
	ID                uuid.UUID    `db:"id"                  json:"id"`
	Name              string       `db:"name"                json:"name"`
	Slug              string       `db:"slug"                json:"slug"`
	Version           string       `db:"version"             json:"version"`
	Description       string       `db:"description"         json:"description"`
	AuthorProfileID   uuid.UUID    `db:"author_profile_id"   json:"authorProfileId"`
	Permissions       []Permission `db:"permissions"         json:"permissions"`
	ManifestJSON      []byte       `db:"manifest_json"       json:"manifest"`
	SourceJS          string       `db:"source_js"           json:"-"`
	SourceSHA256      string       `db:"source_sha256"       json:"sourceSha256"`
	SourceByteSize    int          `db:"source_byte_size"    json:"sourceByteSize"`
	IconURL           string       `db:"icon_url"            json:"iconUrl"`
	Status            PluginStatus `db:"status"              json:"status"`
	InstallCount      int          `db:"install_count"       json:"installCount"`
	IsRootVersion     bool         `db:"is_root_version"     json:"isRootVersion"`
	SupersededByID    *uuid.UUID   `db:"superseded_by_id"    json:"supersededById,omitempty"`
	PreviousVersionID *uuid.UUID   `db:"previous_version_id" json:"previousVersionId,omitempty"`
	ArchivedAt        *time.Time   `db:"archived_at"         json:"archivedAt,omitempty"`
	CreatedAt         time.Time    `db:"created_at"          json:"createdAt"`
	UpdatedAt         time.Time    `db:"updated_at"          json:"updatedAt"`
}

// Manifest decodes the stored manifest_json into a PluginManifest.
func (p *Plugin) Manifest() (*PluginManifest, error) {
	var m PluginManifest
	if err := json.Unmarshal(p.ManifestJSON, &m); err != nil {
		return nil, fmt.Errorf("plugin: decode manifest: %w", err)
	}
	return &m, nil
}

// PluginInstance is a per-tree/per-user install of a plugin.
type PluginInstance struct {
	ID                 uuid.UUID    `db:"id"                  json:"id"`
	PluginID           uuid.UUID    `db:"plugin_id"           json:"pluginId"`
	TreeID             *uuid.UUID   `db:"tree_id"             json:"treeId,omitempty"`
	ProfileID          uuid.UUID    `db:"profile_id"          json:"profileId"`
	InstanceName       string       `db:"instance_name"       json:"instanceName"`
	Settings           []byte       `db:"settings"            json:"settings"`
	GrantedPermissions []Permission `db:"granted_permissions" json:"grantedPermissions"`
	Status             string       `db:"status"              json:"status"` // "active" | "paused" | "uninstalled"
	InvokeCount        int          `db:"invoke_count"        json:"invokeCount"`
	CreatedAt          time.Time    `db:"created_at"          json:"createdAt"`
}

// PluginAuditEntry is a row in plugin_audit_log.
type PluginAuditEntry struct {
	ID             uuid.UUID      `db:"id"                 json:"id"`
	PluginID       uuid.UUID      `db:"plugin_id"          json:"pluginId"`
	InstanceID     *uuid.UUID     `db:"instance_id"        json:"instanceId,omitempty"`
	EventType      string         `db:"event_type"         json:"eventType"`
	ActorProfileID uuid.UUID      `db:"actor_profile_id"   json:"actorProfileId"`
	Metadata       map[string]any `db:"metadata"           json:"metadata"`
	CreatedAt      time.Time      `db:"created_at"         json:"createdAt"`
}

// Audit event types (must match chk_plugin_audit_event_type).
const (
	AuditEventRegistered  = "registered"
	AuditEventUpdated     = "updated"
	AuditEventInstalled   = "installed"
	AuditEventPaused      = "paused"
	AuditEventResumed     = "resumed"
	AuditEventUninstalled = "uninstalled"
	AuditEventRolledBack  = "rolled_back"
)

// PluginVersion is a slim view of a Plugin (no source) used in version
// list/lookup endpoints (SPEC-PL-01 §4.2).
type PluginVersion struct {
	ID                uuid.UUID    `json:"id"`
	Name              string       `json:"name"`
	Slug              string       `json:"slug"`
	Version           string       `json:"version"`
	Description       string       `json:"description"`
	Permissions       []Permission `json:"permissions"`
	IconURL           string       `json:"iconUrl"`
	Status            PluginStatus `json:"status"`
	InstallCount      int          `json:"installCount"`
	IsRootVersion     bool         `json:"isRootVersion"`
	SupersededByID    *uuid.UUID   `json:"supersededById"`
	PreviousVersionID *uuid.UUID   `json:"previousVersionId"`
	CreatedAt         time.Time    `json:"createdAt"`
}

// ToVersion projects a Plugin row onto its slim, source-free PluginVersion
// view (SPEC-PL-01 §4.2).
func (p *Plugin) ToVersion() PluginVersion {
	return PluginVersion{
		ID:                p.ID,
		Name:              p.Name,
		Slug:              p.Slug,
		Version:           p.Version,
		Description:       p.Description,
		Permissions:       p.Permissions,
		IconURL:           p.IconURL,
		Status:            p.Status,
		InstallCount:      p.InstallCount,
		IsRootVersion:     p.IsRootVersion,
		SupersededByID:    p.SupersededByID,
		PreviousVersionID: p.PreviousVersionID,
		CreatedAt:         p.CreatedAt,
	}
}

// --- Error sentinels (SPEC-IMPL-GAP-002 §6) --------------------------------

var (
	// ErrInvalidManifest: manifest block missing or JSON malformed (400 INVALID_MANIFEST).
	ErrInvalidManifest = errors.New("plugin: invalid manifest")
	// ErrManifestValidationFailed: required field missing/bad (400 MANIFEST_VALIDATION_FAILED).
	ErrManifestValidationFailed = errors.New("plugin: manifest validation failed")
	// ErrInvalidPermission: unknown permission string (422 INVALID_PERMISSION).
	ErrInvalidPermission = errors.New("plugin: invalid permission")
	// ErrPluginTooLarge: source exceeds PluginMaxSize (413 PLUGIN_TOO_LARGE).
	ErrPluginTooLarge = errors.New("plugin: source exceeds maximum size")
	// ErrPluginNotFound: Get/GetSource/install by unknown id (404).
	ErrPluginNotFound = errors.New("plugin: not found")
	// ErrPluginDisabled: status='disabled' (410 PLUGIN_DISABLED).
	ErrPluginDisabled = errors.New("plugin: disabled")
	// ErrPluginArchived: status='archived' (410 PLUGIN_ARCHIVED).
	ErrPluginArchived = errors.New("plugin: archived")
	// ErrAlreadyInstalled: unique install violated (409 PLUGIN_ALREADY_INSTALLED).
	ErrAlreadyInstalled = errors.New("plugin: already installed")
	// ErrPermissionNotDeclared: granted permission not in plugin.Permissions (403).
	ErrPermissionNotDeclared = errors.New("plugin: permission not declared by plugin")
	// ErrInstanceNotFound: unknown plugin instance id.
	ErrInstanceNotFound = errors.New("plugin: instance not found")
	// ErrInstanceNotActive: instance status is not 'active' (paused/uninstalled).
	ErrInstanceNotActive = errors.New("plugin: instance is not active")
	// ErrAPINotFound: unknown API method in CheckPermission.
	ErrAPINotFound = errors.New("plugin: unknown API method")
	// ErrPermissionDenied: method requires a permission the instance lacks.
	ErrPermissionDenied = errors.New("plugin: permission denied")
	// ErrPluginVersionNotFound: (name, version) tuple does not exist
	// (404 PLUGIN_VERSION_NOT_FOUND, SPEC-PL-01 §12.2).
	ErrPluginVersionNotFound = errors.New("plugin: version not found")
	// ErrVersionConflict: re-uploading the same (name, version) via the
	// Update path (409 VERSION_CONFLICT, SPEC-PL-01 §12.1 scenario 9).
	ErrVersionConflict = errors.New("plugin: version conflict")
	// ErrRollbackFailed: rollback target missing from the plugin's history
	// or already the active version (400 ROLLBACK_FAILED, SPEC-PL-01 §12.1
	// scenario 18). Unwraps to ErrPluginVersionNotFound for callers that
	// only care about "not in history".
	ErrRollbackFailed = fmt.Errorf("%w: rollback target not in plugin history", ErrPluginVersionNotFound)
)

// PermissionDeniedError is the typed PERMISSION_DENIED error returned by
// CheckPermission when an instance calls a method its granted permissions
// do not cover. It unwraps to ErrPermissionDenied for errors.Is.
type PermissionDeniedError struct {
	Method   string
	Required Permission
}

// Error implements the error interface.
func (e *PermissionDeniedError) Error() string {
	return fmt.Sprintf("%s: method %q requires permission %q", ErrPermissionDenied, e.Method, e.Required)
}

// Unwrap exposes the ErrPermissionDenied sentinel.
func (e *PermissionDeniedError) Unwrap() error { return ErrPermissionDenied }
