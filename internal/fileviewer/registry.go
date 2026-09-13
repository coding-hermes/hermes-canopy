// In-memory viewer dispatch (spec §6.4 override-resolution order, tier 4+).
//
// Phase 1 resolves built-in dispatch only: MIME → viewer_hint → extension →
// download-only. Override tiers 1-3 depend on viewer_config_overrides rows;
// the overrides repo methods arrive with the overrides HTTP surface in a
// later phase and resolve through the same Resolve entry point.

package fileviewer

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ViewerRegistry is the in-memory dispatch table over the persisted
// viewer_registry rows.
type ViewerRegistry struct {
	repo ViewerRegistryRepo
}

// NewViewerRegistry builds a dispatch table backed by the repo.
func NewViewerRegistry(repo ViewerRegistryRepo) *ViewerRegistry {
	return &ViewerRegistry{repo: repo}
}

// List returns all active viewer registrations.
func (vr *ViewerRegistry) List(ctx context.Context) ([]ViewerRegistration, error) {
	return vr.repo.List(ctx)
}

// Get returns one active viewer by slug.
func (vr *ViewerRegistry) Get(ctx context.Context, slug string) (*ViewerRegistration, error) {
	return vr.repo.GetBySlug(ctx, slug)
}

// Dispatch builds the ViewerDispatchResult for a resolved viewer slug.
// isBuiltIn is always true in phase 1 — everything in the registry is a
// compile-time built-in.
func (vr *ViewerRegistry) Dispatch(ctx context.Context, slug string) (*ViewerDispatchResult, error) {
	v, err := vr.repo.GetBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	return &ViewerDispatchResult{
		ViewerSlug:   v.ViewerSlug,
		RenderType:   v.RenderType,
		BundlePath:   v.BundlePath,
		BundleSHA256: v.BundleSHA256,
		DisplayName:  v.DisplayName,
		IconURL:      v.IconURL,
		Config:       map[string]any{},
		IsBuiltIn:    true,
	}, nil
}

// resolveSlug implements dispatch tiers 4-6 for a file with no matching
// override: exact-MIME map, then server viewer_hint, then extension map.
// Returns "" when nothing matches (tier 7 → download-only).
func resolveSlug(file *FileMetadata) string {
	if slug, ok := DefaultViewerDispatch[stripMimeParams(file.MimeType)]; ok {
		return slug
	}
	if file.ViewerHint != "" {
		return file.ViewerHint
	}
	if slug, ok := ExtensionDispatch[file.Extension]; ok {
		return slug
	}
	return ""
}

// ResolveViewer picks the viewer for a file (spec §4.3
// FileViewerService.ResolveViewer). treeID is accepted now and consulted
// once per-tree overrides land; ErrViewerNotFound maps to the download-only
// fallback per tier 7.
func (vr *ViewerRegistry) ResolveViewer(ctx context.Context, file *FileMetadata, _ uuid.UUID, _ *uuid.UUID) (*ViewerDispatchResult, error) {
	slug := resolveSlug(file)
	if slug == "" {
		return nil, ErrViewerNotFound
	}
	return vr.Dispatch(ctx, slug)
}

// streamURLTTL is how long a resolve-issued stream URL stays valid
// (spec §7.5: default 15 min).
const streamURLTTL = 15 * time.Minute
