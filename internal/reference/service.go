// Package reference — service and repository interfaces.
package reference

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/search"
)

// ── Repo Interfaces ──────────────────────────────────────────────────────
//
// Defined here in reference (not internal/db) so the service layer depends
// on reference, not db — same pattern as internal/search.

// ReferenceRepo handles persistence and queries for resolved references.
type ReferenceRepo interface {
	// InsertResolvedRef persists a single resolved reference link.
	InsertResolvedRef(ctx context.Context, link ResolvedReferenceLink) (*ResolvedReferenceLink, error)

	// InsertResolvedRefs persists multiple resolved reference links.
	InsertResolvedRefs(ctx context.Context, links []ResolvedReferenceLink) error

	// GetResolvedRefsForNode returns all resolved references for a node.
	GetResolvedRefsForNode(ctx context.Context, nodeID uuid.UUID) ([]ResolvedReferenceLink, error)

	// DeleteResolvedRefsForNode removes all resolved references for a node.
	DeleteResolvedRefsForNode(ctx context.Context, nodeID uuid.UUID) error

	// GetTopicReferenceCount returns the number of nodes referencing a topic.
	GetTopicReferenceCount(ctx context.Context, topicID uuid.UUID) (int, error)

	// AutocompleteTopics returns topic suggestions by prefix.
	AutocompleteTopics(ctx context.Context, treeID uuid.UUID, prefix, include string, limit int) ([]ReferenceAutocompleteResult, error)

	// GetTopicBySlug returns a topic by tree_id + slug. Used for resolution.
	GetTopicBySlug(ctx context.Context, treeID uuid.UUID, slug string) (*Topic, error)

	// ── Cache ──

	UpsertReferenceCache(ctx context.Context, topicID, treeID uuid.UUID, contextHash string, nodeCount int, payload json.RawMessage) error
	GetReferenceCache(ctx context.Context, topicID uuid.UUID) (*ReferenceCacheEntry, error)
	DeleteReferenceCache(ctx context.Context, topicID uuid.UUID) error

	// ── Log ──

	InsertReferenceLog(ctx context.Context, entry ReferenceLogEntry) error
}

// ── Service Interface (spec §4.3, adapted) ───────────────────────────────

// ReferenceService resolves #topic-slug references, provides autocomplete,
// and injects referenced topics into the agent context window.
type ReferenceService interface {
	// ParseReferences extracts all #topic-slug references from content.
	ParseReferences(ctx context.Context, content string) ([]ParsedReference, error)

	// Autocomplete returns topic suggestions for a partial slug prefix.
	Autocomplete(ctx context.Context, req ReferenceAutocompleteRequest) ([]ReferenceAutocompleteResult, error)

	// ResolveReferences parses and resolves all references in message content.
	// Does NOT persist anything.
	ResolveReferences(ctx context.Context, req ResolveReferencesRequest) (*ReferenceResolutionResult, error)

	// ResolveAtSend resolves references for a message being sent and persists
	// the resolved topic links on the node.
	ResolveAtSend(ctx context.Context, treeID, nodeID uuid.UUID, content string, requesterID uuid.UUID) (*ReferenceResolutionResult, error)

	// InjectWithReferences merges explicitly requested topic IDs with references
	// and returns a MultiTopicContext.
	InjectWithReferences(ctx context.Context, treeID uuid.UUID, req InjectWithReferencesRequest, requesterID uuid.UUID) (*search.MultiTopicContext, error)

	// GetReferencedContext builds the context for a set of referenced topics,
	// using the cache-backed path described in spec §8.1-8.4. For each topic:
	// check reference_resolution_cache → if hit AND context_hash matches the
	// current topic context hash, return the cached payload; otherwise rebuild
	// the TopicContext via the search machinery, upsert into cache, and return.
	GetReferencedContext(ctx context.Context, treeID uuid.UUID, topicIDs []uuid.UUID, maxNodes int) ([]search.TopicContext, error)
}
