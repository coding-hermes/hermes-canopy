// Package reference implements SPEC-TM-04: #Reference Resolution.
// Parses #topic-slug references from message content, resolves them to
// topics, and provides autocomplete + context injection for referenced topics.
//
// This package defines its own types and repository interface (same pattern
// as internal/search) to avoid import cycles. The internal/db package
// implements ReferenceRepo against PostgreSQL.
package reference

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// ── Constants (spec §2) ───────────────────────────────────────────────────

const (
	// SoftCap is the soft limit for references per message. Exceeding this
	// triggers a warning but does not block.
	SoftCap = 5

	// HardCap is the hard limit. More than this returns an error.
	HardCap = 10

	// DefaultMaxNodes is the per-topic default node limit.
	DefaultMaxNodes = 500

	// CacheTTL is the lifetime of a reference cache entry.
	CacheTTL = 24 * time.Hour
)

// ── Canonical Regex (SPEC-TM-01 §5.3, restated §2) ───────────────────────
//
// slug = [a-z]([a-z0-9-]*[a-z0-9])? — must start with a letter, may contain
// lowercase alphanumeric and hyphens, must not end with a hyphen.
// Single letters are allowed.
//
// The reference is a '#' immediately followed by the slug. The '#' must not
// be preceded by a word character (to avoid matching '#' inside words or
// URLs).

var (
	// referenceRe matches #topic-slug references in message content.
	// The '(?:^|[^a-zA-Z0-9#])' lookbehind ensures the '#' is at the start
	// of the text or preceded by a non-word, non-'#' character.
	referenceRe = regexp.MustCompile(`(?:^|[^a-zA-Z0-9#])#([a-z](?:[a-z0-9-]*[a-z0-9])?|[a-z])`)
)

// ── Parsed Types ──────────────────────────────────────────────────────────

// ParsedReference is a single #reference found in message content.
type ParsedReference struct {
	Raw    string `json:"raw"`    // Full match including '#': "#topic-slug"
	Slug   string `json:"slug"`   // Extracted slug: "topic-slug"
	Offset int    `json:"offset"` // Character offset of '#' in message content
	Length int    `json:"length"` // Length of matched text (including '#')
}

// TopicSummary is a lightweight view of a topic for reference responses.
// Matches the json shape of db.TopicSummary so the handler can marshal
// either interchangeably.
type TopicSummary struct {
	ID          uuid.UUID  `json:"id"`
	TreeID      uuid.UUID  `json:"treeId"`
	Title       string     `json:"title"`
	Slug        string     `json:"slug"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	NodeCount   int32      `json:"nodeCount"`
	TopicTags   []string   `json:"topicTags"`
	CreatedAt   time.Time  `json:"createdAt"`
	ArchivedAt  *time.Time `json:"archivedAt,omitempty"`
}

// Topic is a full topic row, used by GetTopicBySlug for resolution.
type Topic struct {
	ID            uuid.UUID  `json:"id"`
	TreeID        uuid.UUID  `json:"treeId"`
	RootNodeID    uuid.UUID  `json:"rootNodeId"`
	Title         string     `json:"title"`
	Description   string     `json:"description"`
	Slug          string     `json:"slug"`
	ParentTopicID *uuid.UUID `json:"parentTopicId,omitempty"`
	Status        string     `json:"status"`
	TopicTags     []string   `json:"topicTags"`
	NodeCount     int32      `json:"nodeCount"`
	CreatedAt     time.Time  `json:"createdAt"`
	ArchivedAt    *time.Time `json:"archivedAt,omitempty"`
}

// ResolvedReference pairs a parsed reference with its resolved topic.
type ResolvedReference struct {
	Reference ParsedReference `json:"reference"`
	Topic     TopicSummary    `json:"topic"`
}

// ReferenceResolutionResult is the outcome of resolving all references.
type ReferenceResolutionResult struct {
	NodeID            uuid.UUID           `json:"node_id"`
	TreeID            uuid.UUID           `json:"tree_id"`
	References        []ResolvedReference `json:"references"`
	NotFound          []ParsedReference   `json:"not_found,omitempty"`
	TooMany           bool                `json:"too_many"`
	Warning           string              `json:"warning,omitempty"`
	TotalNodesInScope int                 `json:"total_nodes_in_scope"`
}

// ReferenceAutocompleteResult is a single autocomplete suggestion.
type ReferenceAutocompleteResult struct {
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	MatchType string `json:"match_type"` // "prefix" | "contains"
	Status    string `json:"status"`
	NodeCount int32  `json:"node_count"`
}

// ── Request/Response Types (spec §4.2, §6) ───────────────────────────────

// ReferenceAutocompleteRequest is the payload for the autocomplete endpoint.
type ReferenceAutocompleteRequest struct {
	TreeID  uuid.UUID
	Prefix  string
	Limit   int
	Include string // "active" | "archived" | "all"; default "active"
}

// ResolveReferencesRequest is the payload for the resolve endpoint.
type ResolveReferencesRequest struct {
	TreeID      uuid.UUID
	Content     string
	MaxNodes    int
	WithContext bool
}

// InjectWithReferencesRequest combines explicit topic IDs with references.
type InjectWithReferencesRequest struct {
	TopicIDs   []uuid.UUID
	References []string // Raw #slug strings from message content
	MaxNodes   int
}

// ── Persistence Types ─────────────────────────────────────────────────────

// ResolvedReferenceLink is the stored link between a node and a topic.
type ResolvedReferenceLink struct {
	ID          uuid.UUID `db:"id"           json:"id"`
	NodeID      uuid.UUID `db:"node_id"      json:"nodeId"`
	TreeID      uuid.UUID `db:"tree_id"      json:"treeId"`
	TopicID     uuid.UUID `db:"topic_id"     json:"topicId"`
	RawRef      string    `db:"raw_ref"      json:"rawRef"`
	Slug        string    `db:"slug"         json:"slug"`
	ResolvedAt  time.Time `db:"resolved_at"  json:"resolvedAt"`
	ResolvedBy  uuid.UUID `db:"resolved_by"  json:"resolvedBy"`
	ContextHash string    `db:"context_hash" json:"contextHash"`
}

// ReferenceCacheEntry is a cached TopicContext payload for a topic.
type ReferenceCacheEntry struct {
	ID          uuid.UUID       `db:"id"           json:"id"`
	TopicID     uuid.UUID       `db:"topic_id"     json:"topicId"`
	TreeID      uuid.UUID       `db:"tree_id"      json:"treeId"`
	ContextHash string          `db:"context_hash" json:"contextHash"`
	NodeCount   int             `db:"node_count"   json:"nodeCount"`
	Payload     json.RawMessage `db:"payload"      json:"payload"`
	CreatedAt   time.Time       `db:"created_at"   json:"createdAt"`
	ExpiresAt   time.Time       `db:"expires_at"   json:"expiresAt"`
	HitCount    int             `db:"hit_count"    json:"hitCount"`
}

// ReferenceLogEntry is a row in reference_resolution_log.
type ReferenceLogEntry struct {
	ID         uuid.UUID  `db:"id"          json:"id"`
	TreeID     uuid.UUID  `db:"tree_id"     json:"treeId"`
	NodeID     *uuid.UUID `db:"node_id"     json:"nodeId,omitempty"`
	ProfileID  uuid.UUID  `db:"profile_id"  json:"profileId"`
	RawRef     string     `db:"raw_ref"     json:"rawRef"`
	Slug       string     `db:"slug"        json:"slug"`
	TopicID    *uuid.UUID `db:"topic_id"    json:"topicId,omitempty"`
	Status     string     `db:"status"      json:"status"`
	ErrorCode  *string    `db:"error_code"  json:"errorCode,omitempty"`
	DurationMs int        `db:"duration_ms" json:"durationMs"`
	CreatedAt  time.Time  `db:"created_at"  json:"createdAt"`
}

// ── Errors (spec §10) ─────────────────────────────────────────────────────

// ── Errors (spec §10) ─────────────────────────────────────────────────────

var (
	ErrPrefixTooShort    = fmt.Errorf("autocomplete prefix must be at least 1 character")
	ErrPrefixTooLong     = fmt.Errorf("autocomplete prefix must be at most 100 characters")
	ErrInvalidInclude    = fmt.Errorf("include must be one of: active, archived, all")
	ErrReferencesTooMany = fmt.Errorf("cannot resolve more than 10 references at once")
	ErrInvalidInput      = fmt.Errorf("at least one topicId or reference must be provided")
	ErrTooManyTopics     = fmt.Errorf("cannot inject more than 5 topics at once via references")
	ErrContentTooLong    = fmt.Errorf("message content is too long for reference parsing")
	ErrTopicNotFound     = fmt.Errorf("topic not found for reference")
	ErrResolutionFailed  = fmt.Errorf("failed to resolve references")
	ErrInjectionFailed   = fmt.Errorf("failed to inject referenced topics")
)
