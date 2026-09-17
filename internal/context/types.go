// Package context implements the Context Compiler — transparent, budgeted,
// auditable context assembly for model calls. Given a node ID, it walks the
// node's ancestry chain, resolves #references, applies a token budget, and
// produces a JSON manifest documenting exactly what was included.
//
// The compiler is stateless and safe for concurrent use.
//
// Spec: SPEC-IMPL-GAP-001-context-compiler.md §2
package context

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// Compiler assembles budgeted context for a model call.
type Compiler interface {
	// Compile builds the context payload for the conversation ending at nodeID.
	// Returns the assembled context + its manifest. Never returns a nil
	// CompiledContext even on partial failure — degraded results are valid results
	// (see Error Catalog).
	Compile(ctx context.Context, req CompileRequest) (*CompiledContext, error)
}

// CompileRequest carries the parameters for a single context compilation.
type CompileRequest struct {
	TreeID       uuid.UUID `json:"treeId"`
	NodeID       uuid.UUID `json:"nodeId"`       // current node (end of thread)
	TokenBudget  int       `json:"tokenBudget"`  // max tokens for the payload
	MaxAncestors int       `json:"maxAncestors"` // default 50 when 0
	IncludeCards bool      `json:"includeCards"` // attach card data
	ResolveRefs  bool      `json:"resolveRefs"`  // default true

	// MultiReference, when non-nil, is a preflight-validated multi-reference
	// selection (SPEC-PL-06 §6). Its block is compiled first and prepended to
	// the payload because it is the highest-priority block of the turn: every
	// selected message participates. An invalid selection fails the whole
	// compilation (§6.4) — never a partial source set. nil leaves compilation
	// exactly as it was before multi-reference support.
	MultiReference *MultiReferenceSelection `json:"multiReference,omitempty"`
}

// CompiledContext is the final payload + manifest.
type CompiledContext struct {
	// Content is the assembled context text (ancestry + references + cards),
	// ready to be placed in the model prompt.
	Content string `json:"content"`

	// Manifest is the auditable record of what was included.
	Manifest *Manifest `json:"manifest"`
}

// Manifest documents exactly what the compiler did. This is the
// user-visible artifact.
type Manifest struct {
	RequestID         string         `json:"requestId"`
	NodeID            uuid.UUID      `json:"nodeId"`
	CompiledAt        time.Time      `json:"compiledAt"`
	TokenBudget       int            `json:"tokenBudget"`
	TokensUsed        int            `json:"tokensUsed"`
	Ancestry          []ManifestItem `json:"ancestry"`
	References        []ManifestItem `json:"references"`
	Cards             []ManifestItem `json:"cards"`
	OmittedCount      int            `json:"omittedCount"`      // nodes dropped by budget
	OmittedReason     string         `json:"omittedReason"`     // "budget" | "depth" | ""
	TruncationMarkers []string       `json:"truncationMarkers"` // e.g. "3 messages omitted"
	Warnings          []string       `json:"warnings"`          // e.g. "5+ references: context becoming unfocused"

	// ManifestHash is the stable digest of this manifest's content-bearing
	// fields (GAP-080 phase 5a): lowercase 64-hex sha256 over everything
	// except RequestID, CompiledAt and this field itself — see
	// ManifestDigest for the exact recipe and why each exclusion is required.
	//
	// It binds the manifest a user READS to the payload a run was GIVEN: the
	// panel's preview compile and the run record's manifest hash equally when
	// they describe the same compiled content, and the digest is recomputable
	// from the record's JSON by anyone (ManifestDigestFromJSON).
	//
	// NOT omitempty: a manifest that exists always carries its digest.
	ManifestHash string `json:"manifestHash"`

	// PinnedCount is the number of PINNED ancestry items the budget walk kept
	// (GAP-080 phase 1). Pinned items are exempt from the budget: they are
	// never dropped and never counted as omitted, so TokensUsed may exceed
	// TokenBudget when they are kept. Omitted for payloads with no pins.
	PinnedCount int `json:"pinnedCount,omitempty"`

	// MultiReference is the §6.3 audit record of the selected-source block,
	// when the turn was compiled from a multi-reference selection. Omitted
	// entirely for ordinary turns.
	MultiReference *MultiReferenceManifestEntry `json:"multiReference,omitempty"`
}

// ManifestItem describes one component of the compiled context.
type ManifestItem struct {
	ID         uuid.UUID `json:"id"`
	Kind       string    `json:"kind"`  // "node" | "topic" | "card"
	Title      string    `json:"title"` // node: content preview (120 chars); topic: slug; card: card type
	TokenCount int       `json:"tokenCount"`
	Truncated  bool      `json:"truncated"` // true if item content was elided

	// Pinned marks an ancestry item whose node carried `metadata.pinned: true`
	// and was therefore exempted from the budget walk (GAP-080 phase 1).
	// Omitted from the JSON for unpinned items.
	Pinned bool `json:"pinned,omitempty"`
}

// TokenEstimator estimates tokens for a string. Injectable for tests.
type TokenEstimator interface {
	Estimate(s string) int
}

// --- Reader interfaces (implemented by existing repos; not reimplemented) ---

// NodeReader is satisfied by *db.PGNodeRepo.
type NodeReader interface {
	GetByID(ctx context.Context, id uuid.UUID) (*db.Node, error)
	GetAncestors(ctx context.Context, nodeID uuid.UUID) ([]db.Node, error)
}

// TopicReader is satisfied by *db.PGTopicRepo.
type TopicReader interface {
	GetBySlug(ctx context.Context, treeID uuid.UUID, slug string) (*db.Topic, error)
	GetTopicsForNode(ctx context.Context, nodeID uuid.UUID) ([]db.Topic, error)
	// GetResolvedTopicsForNode returns topics explicitly referenced by the node
	// via node_resolved_refs (spec §8.1 — context-compiler reference handling).
	// Unlike GetTopicsForNode (scope membership via topic_member_nodes), this
	// captures references the author wrote as #topic-slug in the message.
	GetResolvedTopicsForNode(ctx context.Context, nodeID uuid.UUID) ([]db.Topic, error)
}

// CardReader is satisfied by *card.SQLiteCardRepo.
type CardReader interface {
	GetByContextHash(ctx context.Context, contextHash string) ([]card.Card, error)
}

// --- Helpers ---

// ContextHash returns the SHA-256 hex digest of content, matching the
// convention in the nodes table (encode(sha256(content::bytea), 'hex')).
func ContextHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

// contentPreview returns the first n runes of s, trimmed to one line.
func contentPreview(s string, n int) string {
	runes := []rune(s)
	if len(runes) > n {
		runes = runes[:n]
	}
	out := string(runes)
	// Trim to first newline
	for i, r := range out {
		if r == '\n' || r == '\r' {
			return out[:i]
		}
	}
	return out
}

// --- Sentinels ---

// Sentinel errors for context compilation failures.
// Degraded results never error out — only total failures produce these.
var (
	ErrNodeNotFound        = errors.New("context: node not found")
	ErrInvalidBudget       = errors.New("context: budget must be >= 1")
	ErrDatabaseUnavailable = errors.New("context: database unavailable")
)
