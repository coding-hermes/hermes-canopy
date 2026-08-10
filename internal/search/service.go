// Package search implements SPEC-TM-03: topic full-text search and
// one-button context injection. Contains the service interface, the
// context compiler, and the concrete implementation.
//
// This package does NOT import internal/db to avoid import cycles.
// The db package implements the repo interfaces defined here.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// --- Errors (spec §8) -------------------------------------------------------

var (
	ErrSearchQueryTooShort  = errors.New("search query must be at least 2 characters")
	ErrSearchQueryTooLong   = errors.New("search query must be at most 200 characters")
	ErrSearchStopWordsOnly  = errors.New("search query contains only common words; try adding more specific terms")
	ErrSearchInvalidSort    = errors.New("sort must be one of: relevance, last_active, title")
	ErrSearchInvalidLimit   = errors.New("limit must be between 1 and 100")
	ErrTopicNotFound        = errors.New("topic not found")
	ErrTopicDeleted         = errors.New("topic has been deleted")
	ErrTopicArchived        = errors.New("archived topics cannot be injected. Unarchive first")
	ErrContextTooManyTopics = errors.New("cannot inject more than 5 topics at once")
	ErrContextTooLarge      = errors.New("requested topics exceed the per-injection global max nodes")
	ErrContextInjectionFail = errors.New("failed to inject context")
	ErrContextIndexStale    = errors.New("topic content index is out of date")
)

// --- Constants --------------------------------------------------------------

// GlobalMaxNodes is the per-injection hard cap across all topics.
const GlobalMaxNodes = 5000

// DefaultMaxNodes is the per-topic default node limit.
const DefaultMaxNodes = 500

// MaxTopics is the maximum number of topics per injection request.
const MaxTopics = 5

// --- Structs (spec §4.2) ----------------------------------------------------

// ContextNode is a minimal node representation for context compilation.
// It carries only the fields needed for agent injection — not the full
// db.Node. The repo layer maps from the database row to this type.
type ContextNode struct {
	ID         uuid.UUID `json:"id"`
	TreeID     uuid.UUID `json:"tree_id"`
	AuthorID   uuid.UUID `json:"author_id"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"created_at"`
	SequenceNum int64    `json:"sequence_num"`
}

// TopicSearchResult is a single search result returned to the client.
type TopicSearchResult struct {
	TopicID    uuid.UUID `json:"topic_id"`
	TreeID     uuid.UUID `json:"tree_id"`
	Title      string    `json:"title"`
	Slug       string    `json:"slug"`
	Snippet    string    `json:"snippet"`
	Status     string    `json:"status"`
	NodeCount  int       `json:"node_count"`
	LastActive time.Time `json:"last_active_at"`
	Relevance  float64   `json:"relevance"`
}

// SearchOptions contains pagination and filtering for search queries.
type SearchOptions struct {
	Query        string `json:"query"`
	MaxResults   int    `json:"max_results"`
	Offset       int    `json:"offset"`
	StatusFilter string `json:"status_filter"`
	SortBy       string `json:"sort_by"`
}

// InjectContextRequest is the payload for the context injection endpoint.
type InjectContextRequest struct {
	TopicIDs []uuid.UUID `json:"topic_ids" validate:"required,min=1,max=5"`
	MaxNodes int         `json:"max_nodes"`
}

// TopicContext is the complete context payload for one topic.
type TopicContext struct {
	TopicID     uuid.UUID     `json:"topic_id"`
	Title       string        `json:"title"`
	Slug        string        `json:"slug"`
	RootNodeID  uuid.UUID     `json:"root_node_id"`
	Nodes       []ContextNode `json:"nodes"`
	TotalNodes  int           `json:"total_nodes"`
	HasMore     bool          `json:"has_more"`
	ContextHash string        `json:"context_hash"`
}

// MultiTopicContext is the merged context for multi-topic injection.
type MultiTopicContext struct {
	Topics     []TopicContext `json:"topics"`
	MergedText string         `json:"merged_text"`
	TotalNodes int            `json:"total_nodes"`
	Truncated  bool           `json:"truncated"`
}

// SearchLogEntry represents a row in topic_search_log.
type SearchLogEntry struct {
	ID               uuid.UUID       `json:"id"`
	TreeID           uuid.UUID       `json:"tree_id"`
	ProfileID        uuid.UUID       `json:"profile_id"`
	QueryText        string          `json:"query_text"`
	ResultCount      int             `json:"result_count"`
	FiltersApplied   json.RawMessage `json:"filters_applied"`
	InjectedCount    int             `json:"injected_count"`
	SearchDurationMs int             `json:"search_duration_ms"`
	CreatedAt        time.Time       `json:"created_at"`
}

// TopicPreview is a lightweight summary for hover tooltips.
type TopicPreview struct {
	TopicID          uuid.UUID `json:"topic_id"`
	Title            string    `json:"title"`
	Snippets         []string  `json:"snippets"`
	ParticipantCount int       `json:"participant_count"`
	NodeCount        int       `json:"node_count"`
	LastActive       time.Time `json:"last_active_at"`
	LastActiveRel    string    `json:"last_active_rel"`
}

// --- Repo Interfaces (spec §4.4, adapted) ----------------------------------
// Implemented by internal/db. Defined here in search so the service layer
// depends on search, not db (breaks the import cycle).

// TopicSearchRepo handles data access for topic search and context injection.
type TopicSearchRepo interface {
	SearchTopics(ctx context.Context, treeID uuid.UUID, opts SearchOptions) ([]TopicSearchResult, int, error)
	GetRecentTopics(ctx context.Context, treeID uuid.UUID, limit int) ([]TopicSearchResult, error)
	GetTopicNodes(ctx context.Context, topicID uuid.UUID, maxNodes int) ([]ContextNode, int, bool, error)
	GetTopicForInject(ctx context.Context, topicID uuid.UUID) (*TopicInjectMeta, error)
	GetTopicPreviewNodes(ctx context.Context, topicID uuid.UUID, limit int) ([]ContextNode, error)
	GetTopicPreviewMeta(ctx context.Context, topicID uuid.UUID) (*TopicPreviewMeta, error)
	RefreshNodeContentIndex(ctx context.Context, topicID uuid.UUID, nodeIDs []uuid.UUID) (int, error)
}

// TopicInjectMeta is the metadata needed for context injection.
type TopicInjectMeta struct {
	ID         uuid.UUID
	Title      string
	Slug       string
	RootNodeID uuid.UUID
	Status     string
}

// TopicPreviewMeta holds the DB-level metadata for a topic preview.
type TopicPreviewMeta struct {
	ID               uuid.UUID
	Title            string
	Status           string
	NodeCount        int
	LastActive       time.Time
	ParticipantCount int
}

// TopicSearchLogRepo handles the search analytics log.
type TopicSearchLogRepo interface {
	InsertSearchLog(ctx context.Context, entry SearchLogEntry) error
}

// --- Service Interface (spec §4.3) ------------------------------------------

// TopicSearchService handles full-text search and context injection for topics.
type TopicSearchService interface {
	Search(ctx context.Context, treeID uuid.UUID, opts SearchOptions) ([]TopicSearchResult, int, time.Duration, error)
	GetRecent(ctx context.Context, treeID uuid.UUID, limit int) ([]TopicSearchResult, error)
	InjectContext(ctx context.Context, treeID uuid.UUID, req InjectContextRequest) (*MultiTopicContext, error)
	GetTopicPreview(ctx context.Context, topicID uuid.UUID, snippetCount int) (*TopicPreview, error)
	RefreshNodeContentIndex(ctx context.Context, topicID uuid.UUID, nodeIDs []uuid.UUID) (int, error)
	LogSearch(ctx context.Context, entry SearchLogEntry) error
}
