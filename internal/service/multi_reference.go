// Package service — multi-message reference model, selection preflight.
//
// Implements SPEC-PL-06 §11 (TreeService changes: ValidateReferenceSelection,
// GetReferenceParents, AnalyzeReferenceBranchSpan), the signed five-minute
// selection token from §4.3/§9.1, the §9.4 error catalog mapping, and the
// §8.1 branch-span analysis. Creation lives in multi_reference_reply.go.
//
// The preflight is stateless: it writes no graph rows. Selection state is
// local UI state until it returns a token (§4.3).
package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// --- Limits (SPEC-PL-06 §5.1, §6.2) -----------------------------------------

const (
	// referenceSourceMinCount / referenceSourceMaxCount bound a
	// multi-reference reply's parent set (§2 decision 8, §5.1).
	referenceSourceMinCount = 2
	referenceSourceMaxCount = 20

	// referenceMinSourceTokens is the per-source floor the budget must be
	// able to fund; a selection that cannot reserve it is rejected rather
	// than silently truncating sources (§6.2, §15 scenario 16).
	referenceMinSourceTokens = 256
	// referenceMaxSourceTokens caps any single source before proportional
	// distribution (§6.2, §15 scenario 17).
	referenceMaxSourceTokens = 2048
	// referenceMaxBudget caps the whole selected-source budget (§6.2).
	referenceMaxBudget = 16384
	// referenceBudgetShare is the fraction of the profile's turn budget a
	// selected-source set may consume (§2 decision 24).
	referenceBudgetShare = 0.50
	// referenceDefaultProfileBudget matches config.ContextDefaultBudget and
	// is used when the preflight request omits profile_context_budget.
	referenceDefaultProfileBudget = 8000

	// referencePreviewRunes bounds the per-source content_preview returned
	// by preflight (§9.1 sources[]).
	referencePreviewRunes = 200

	// ReferenceSelectionTTL is the signed selection token's lifetime (§4.3,
	// §9.1: five minutes).
	ReferenceSelectionTTL = 5 * time.Minute

	referenceSelectionTokenPrefix = "mrs.v1"
)

// --- §9.4 error catalog ------------------------------------------------------

// Service-level sentinels for the SPEC-PL-06 §9.4 codes. Every one of them
// is wrapped in a *ReferenceAPIError, which carries the wire code and HTTP
// status, so handlers map without a second switch table.
var (
	ErrReferenceSourceCountTooLow     = errors.New("service: fewer than 2 reference sources")
	ErrReferenceSourceCountTooHigh    = errors.New("service: more than 20 reference sources")
	ErrReferenceSourceDuplicate       = errors.New("service: duplicate reference source")
	ErrReferenceSourceInvalid         = errors.New("service: invalid reference source id")
	ErrReferenceSourceNotFound        = errors.New("service: reference source not found in tree")
	ErrReferenceSourceDeleted         = errors.New("service: reference source is soft-deleted")
	ErrReferenceSourceSystemForbidden = errors.New("service: system nodes cannot be reference sources")
	ErrReferenceTreeMismatch          = errors.New("service: reference source or token belongs to another tree")
	ErrReferencePrimaryNotSelected    = errors.New("service: primary source is not in the selection")
	ErrReferenceContextBudgetExceeded = errors.New("service: profile budget cannot fund the selection")
	ErrReferenceSelectionTokenInvalid = errors.New("service: selection token is invalid")
	ErrReferenceSelectionTokenExpired = errors.New("service: selection token expired")
	ErrReferenceSelectionStale        = errors.New("service: selection snapshot no longer matches the token")
	ErrReferenceParentInvariant       = errors.New("service: reference parent invariant violated")
	ErrReferenceRequestIDConflict     = errors.New("service: request id reused with a different payload")
	ErrReferenceSelectionUnconfigured = errors.New("service: reference selection signer is not configured")
	ErrReferenceContextNotFound       = errors.New("service: target node is not a multi-reference reply")
)

// referenceErrorSpec is the catalog row for one sentinel: the §9.4 code,
// the HTTP status, and the default message.
type referenceErrorSpec struct {
	code    string
	status  int
	message string
}

// referenceErrorCatalog is SPEC-PL-06 §9.4 verbatim.
var referenceErrorCatalog = map[error]referenceErrorSpec{
	ErrReferenceSourceCountTooLow:     {"REFERENCE_SOURCE_COUNT_TOO_LOW", 400, "select at least 2 messages"},
	ErrReferenceSourceCountTooHigh:    {"REFERENCE_SOURCE_COUNT_TOO_HIGH", 400, "select at most 20 messages"},
	ErrReferenceSourceDuplicate:       {"REFERENCE_SOURCE_DUPLICATE", 400, "the selection contains a duplicate message id"},
	ErrReferenceSourceInvalid:         {"REFERENCE_SOURCE_INVALID", 400, "source_node_ids must contain valid UUIDs"},
	ErrReferenceSourceNotFound:        {"REFERENCE_SOURCE_NOT_FOUND", 404, "a selected message does not exist in this tree"},
	ErrReferenceSourceDeleted:         {"REFERENCE_SOURCE_DELETED", 410, "a selected message has been deleted"},
	ErrReferenceSourceSystemForbidden: {"REFERENCE_SOURCE_SYSTEM_FORBIDDEN", 400, "system nodes cannot be selected"},
	ErrReferenceTreeMismatch:          {"REFERENCE_TREE_MISMATCH", 400, "a selected message belongs to another tree"},
	ErrReferencePrimaryNotSelected:    {"REFERENCE_PRIMARY_NOT_SELECTED", 400, "primary_source_id must be one of source_node_ids"},
	ErrReferenceContextBudgetExceeded: {"REFERENCE_CONTEXT_BUDGET_EXCEEDED", 422, "profile budget cannot reserve 256 tokens per selected message"},
	ErrReferenceSelectionTokenInvalid: {"REFERENCE_SELECTION_TOKEN_INVALID", 400, "selection token is invalid"},
	ErrReferenceSelectionTokenExpired: {"REFERENCE_SELECTION_TOKEN_EXPIRED", 410, "selection token has expired"},
	ErrReferenceSelectionStale:        {"REFERENCE_SELECTION_STALE", 409, "a selected message changed or was deleted since preflight"},
	ErrReferenceParentInvariant:       {"REFERENCE_PARENT_INVARIANT", 409, "reference parent invariant violated"},
	ErrReferenceRequestIDConflict:     {"REFERENCE_REQUEST_ID_CONFLICT", 409, "request_id was reused with a different payload"},
	ErrReferenceContextNotFound:       {"REFERENCE_CONTEXT_NOT_FOUND", 404, "target node is not a multi-reference reply"},
}

// ReferenceAPIError is a rejected reference operation with its §9.4
// identity. errors.Is(err, Err<Sentinel>) still matches through Unwrap.
type ReferenceAPIError struct {
	Code    string
	Status  int
	Message string
	Err     error
}

// Error implements the error interface with the catalog message.
func (e *ReferenceAPIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

// Unwrap exposes the sentinel so errors.Is classification keeps working.
func (e *ReferenceAPIError) Unwrap() error { return e.Err }

// newReferenceAPIError builds the catalog-shaped error for a sentinel. An
// empty message keeps the catalog's default.
func newReferenceAPIError(sentinel error, message string) error {
	spec, ok := referenceErrorCatalog[sentinel]
	if !ok {
		return fmt.Errorf("%w: %s", sentinel, message)
	}
	if message == "" {
		message = spec.message
	}
	return &ReferenceAPIError{Code: spec.code, Status: spec.status, Message: message, Err: sentinel}
}

// ReferenceErrorFrom extracts the §9.4 identity from an error chain.
func ReferenceErrorFrom(err error) (*ReferenceAPIError, bool) {
	var apiErr *ReferenceAPIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

// NewReferenceAPIError is the exported form of the catalog constructor, for
// callers (HTTP handlers) that reject a request before the service runs.
func NewReferenceAPIError(sentinel error, message string) error {
	return newReferenceAPIError(sentinel, message)
}

// --- Requester context -------------------------------------------------------

type referenceRequesterKey struct{}

// WithRequester attaches the authenticated requester's identity to ctx.
//
// SPEC-PL-06 §11 requires the selection token to carry the authenticated
// requester id, and §9.2 makes the caller the created node's author — both
// services take no explicit identity parameter in the spec's signatures, so
// the handler injects it here ("The caller identity is the created node
// author", §9.2; "Authenticate caller", §13.1 step 1).
func WithRequester(ctx context.Context, userID uuid.UUID) context.Context {
	return context.WithValue(ctx, referenceRequesterKey{}, userID)
}

// requesterFromContext returns the requester injected by WithRequester, or
// uuid.Nil when the caller did not authenticate.
func requesterFromContext(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(referenceRequesterKey{}).(uuid.UUID)
	return id
}

// --- Selection token ---------------------------------------------------------

// ReferenceSelectionClaims are the signed claims of a preflight token
// (SPEC-PL-06 §4.3: tree, ordered sources with hashes, primary source,
// expiry, budget, requester). The token carries no answer content.
type ReferenceSelectionClaims struct {
	Version         int         `json:"v"`
	TreeID          uuid.UUID   `json:"tree_id"`
	RequesterID     uuid.UUID   `json:"requester_id"`
	PrimarySourceID uuid.UUID   `json:"primary_source_id"`
	SourceIDs       []uuid.UUID `json:"source_ids"`
	SourceHashes    []string    `json:"source_hashes"`
	Budget          int         `json:"budget"`
	ExpiresAt       int64       `json:"exp"`
}

// ReferenceSelectionSigner signs and verifies preflight tokens with an
// HMAC-SHA256 server secret (§11: "The HMAC signing key is a server
// secret").
type ReferenceSelectionSigner struct {
	secret []byte
	now    func() time.Time
}

// NewReferenceSelectionSigner wires the signer. now defaults to time.Now;
// pass nil in production and a fixed clock in tests.
func NewReferenceSelectionSigner(secret string, now func() time.Time) *ReferenceSelectionSigner {
	if now == nil {
		now = time.Now
	}
	return &ReferenceSelectionSigner{secret: []byte(secret), now: now}
}

// configured reports whether a usable signing secret is present.
func (s *ReferenceSelectionSigner) configured() bool {
	return s != nil && len(s.secret) > 0
}

// base64url encodes without padding, per the token's mrs.v1.<payload>.<sig>
// shape in §9.1.
func base64url(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

// Sign mints a token for the claims, stamping the five-minute expiry.
func (s *ReferenceSelectionSigner) Sign(claims ReferenceSelectionClaims) (string, error) {
	claims.Version = 1
	claims.ExpiresAt = s.now().Add(ReferenceSelectionTTL).Unix()
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("reference selection: encode claims: %w", err)
	}
	encoded := base64url(payload)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(encoded))
	return referenceSelectionTokenPrefix + "." + encoded + "." + base64url(mac.Sum(nil)), nil
}

// Verify checks the signature and expiry of a token and returns its claims.
// A tampered signature, malformed token, or different-requester replay is
// REFERENCE_SELECTION_TOKEN_INVALID; an elapsed token is
// REFERENCE_SELECTION_TOKEN_EXPIRED (§9.4, §15 scenarios 17/33/34).
func (s *ReferenceSelectionSigner) Verify(token string, callerID uuid.UUID) (*ReferenceSelectionClaims, error) {
	if !s.configured() {
		return nil, newReferenceAPIError(ErrReferenceSelectionUnconfigured, "")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != "mrs" || parts[1] != "v1" {
		return nil, newReferenceAPIError(ErrReferenceSelectionTokenInvalid, "")
	}
	encodedPayload, encodedSig := parts[2], parts[3]
	sig, err := base64.RawURLEncoding.DecodeString(encodedSig)
	if err != nil {
		return nil, newReferenceAPIError(ErrReferenceSelectionTokenInvalid, "")
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(encodedPayload))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, newReferenceAPIError(ErrReferenceSelectionTokenInvalid, "")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return nil, newReferenceAPIError(ErrReferenceSelectionTokenInvalid, "")
	}
	var claims ReferenceSelectionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, newReferenceAPIError(ErrReferenceSelectionTokenInvalid, "")
	}
	if claims.Version != 1 || claims.TreeID == uuid.Nil || len(claims.SourceIDs) == 0 {
		return nil, newReferenceAPIError(ErrReferenceSelectionTokenInvalid, "")
	}
	if claims.RequesterID != callerID {
		// §15 scenario 33: another profile may not replay a token.
		return nil, newReferenceAPIError(ErrReferenceSelectionTokenInvalid,
			"selection token was issued to a different caller")
	}
	if s.now().Unix() > claims.ExpiresAt {
		return nil, newReferenceAPIError(ErrReferenceSelectionTokenExpired, "")
	}
	return &claims, nil
}

// --- Wire types (SPEC-PL-06 §9.1, §11) ---------------------------------------

// ReferenceSelectionInput is the preflight request body (§9.1).
type ReferenceSelectionInput struct {
	SourceNodeIDs        []uuid.UUID `json:"source_node_ids"`
	PrimarySourceID      *uuid.UUID  `json:"primary_source_id,omitempty"`
	ProfileContextBudget int         `json:"profile_context_budget,omitempty"`
}

// ReferenceSelectionResult is the §9.1 success envelope.
type ReferenceSelectionResult struct {
	SelectionToken        string                  `json:"selection_token"`
	ExpiresAt             time.Time               `json:"expires_at"`
	TreeID                uuid.UUID               `json:"tree_id"`
	CanonicalSourceIDs    []uuid.UUID             `json:"canonical_source_ids"`
	PrimarySourceID       uuid.UUID               `json:"primary_source_id"`
	IsSyntheticMergePoint bool                    `json:"is_synthetic_merge_point"`
	BranchSpan            *BranchSpanMetadata     `json:"branch_span,omitempty"`
	ContextBudget         ReferenceContextPreview `json:"context_budget"`
	Sources               []ReferenceParent       `json:"sources"`
}

// ReferenceContextPreview is the §9.1 context_budget object.
type ReferenceContextPreview struct {
	AvailableTokens       int  `json:"available_tokens"`
	MinimumRequiredTokens int  `json:"minimum_required_tokens"`
	EstimatedTokens       int  `json:"estimated_tokens"`
	Fits                  bool `json:"fits"`
}

// ReferenceParent describes one selected source on the HTTP boundary (§11).
// The JSON tags are snake_case because §9 says HTTP boundaries use
// snake_case.
type ReferenceParent struct {
	NodeID         uuid.UUID `json:"node_id"`
	SourceLabel    string    `json:"source_label"`
	ColorKey       string    `json:"color_key"`
	BranchRootID   uuid.UUID `json:"branch_root_id"`
	ContentHash    string    `json:"content_hash"`
	SequenceNum    int64     `json:"sequence_num"`
	ContentPreview string    `json:"content_preview"`
}

// BranchSpanMetadata is the §9.1/§8.3 branch_span wire shape.
type BranchSpanMetadata struct {
	CommonAncestorID uuid.UUID               `json:"common_ancestor_id"`
	SourceBranches   []ReferenceBranchSource `json:"source_branches"`
}

// ReferenceBranchSource is one source's divergence point on the wire.
type ReferenceBranchSource struct {
	SourceID         uuid.UUID `json:"source_id"`
	BranchRootID     uuid.UUID `json:"branch_root_id"`
	DistanceFromRoot int       `json:"distance_from_root"`
}

// --- Source loading / shared validation --------------------------------------

// referenceQuerier is the query seam shared by the pool (preflight) and a
// transaction (creation), so both endpoints run the SAME validator (§9.1
// rules applied in §13.1 step 4).
type referenceQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// referenceLoadMode selects the catalog code a missing/deleted/relocated
// source maps onto. Preflight reports the precise source problem (§9.1);
// creation reports a stale selection, because the signed snapshot no longer
// matches live state (§4.3, §13.1 step 4, §15 scenarios 8-9).
type referenceLoadMode int

const (
	referenceLoadPreflight referenceLoadMode = iota
	referenceLoadCreation
)

// referenceSource is one resolved source snapshot.
type referenceSource struct {
	ID          uuid.UUID
	TreeID      uuid.UUID
	NodeType    string
	ParentID    *uuid.UUID
	Content     string
	ContentHash string
	SequenceNum int64
	Deleted     bool
}

// loadReferenceSources resolves the ordered selection and applies the shared
// §9.1 validator: tree scope, node existence, not-deleted, and the
// system-node exclusion. Canonical (request) order is preserved.
func loadReferenceSources(ctx context.Context, q referenceQuerier, treeID uuid.UUID, orderedIDs []uuid.UUID, mode referenceLoadMode) ([]referenceSource, error) {
	if len(orderedIDs) < referenceSourceMinCount {
		return nil, newReferenceAPIError(ErrReferenceSourceCountTooLow, "")
	}
	if len(orderedIDs) > referenceSourceMaxCount {
		return nil, newReferenceAPIError(ErrReferenceSourceCountTooHigh, "")
	}
	seen := make(map[uuid.UUID]struct{}, len(orderedIDs))
	raw := make([]string, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		if id == uuid.Nil {
			return nil, newReferenceAPIError(ErrReferenceSourceInvalid, "")
		}
		if _, dup := seen[id]; dup {
			// §2 decision 9, §15 scenario 4: duplicates are rejected, never
			// silently deduplicated (that would change user intent/order).
			return nil, newReferenceAPIError(ErrReferenceSourceDuplicate, "")
		}
		seen[id] = struct{}{}
		raw = append(raw, id.String())
	}

	rows, err := q.Query(ctx, `
        SELECT id, tree_id, node_type, parent_id, content, content_hash, sequence_num,
               (deleted_at IS NOT NULL) AS deleted
        FROM nodes
        WHERE id = ANY($1::uuid[])`, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: load reference sources: %v", ErrDatabaseUnavailable, err)
	}
	defer rows.Close()

	found := make(map[uuid.UUID]referenceSource, len(orderedIDs))
	for rows.Next() {
		var s referenceSource
		if err := rows.Scan(&s.ID, &s.TreeID, &s.NodeType, &s.ParentID, &s.Content,
			&s.ContentHash, &s.SequenceNum, &s.Deleted); err != nil {
			return nil, fmt.Errorf("%w: scan reference source: %v", ErrDatabaseUnavailable, err)
		}
		found[s.ID] = s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate reference sources: %v", ErrDatabaseUnavailable, err)
	}

	out := make([]referenceSource, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		src, ok := found[id]
		if !ok {
			if mode == referenceLoadCreation {
				return nil, newReferenceAPIError(ErrReferenceSelectionStale,
					"a selected message no longer exists")
			}
			return nil, newReferenceAPIError(ErrReferenceSourceNotFound, "")
		}
		if src.Deleted {
			if mode == referenceLoadCreation {
				return nil, newReferenceAPIError(ErrReferenceSelectionStale,
					"a selected message was deleted since preflight")
			}
			return nil, newReferenceAPIError(ErrReferenceSourceDeleted, "")
		}
		if src.TreeID != treeID {
			if mode == referenceLoadCreation {
				return nil, newReferenceAPIError(ErrReferenceSelectionStale,
					"a selected message left this tree since preflight")
			}
			return nil, newReferenceAPIError(ErrReferenceTreeMismatch, "")
		}
		if src.NodeType == db.NodeTypeSystem {
			// §2 decision 6 / §5.1: server events never acquire
			// user-selected conversational provenance.
			return nil, newReferenceAPIError(ErrReferenceSourceSystemForbidden, "")
		}
		out = append(out, src)
	}
	return out, nil
}

// --- Budget helpers (§6.2) ---------------------------------------------------

// estimateSourceTokens is the deterministic 4-chars-per-token rule used by
// internal/context (SPEC-TM-004 determinism).
func estimateSourceTokens(content string) int {
	rc := len([]rune(content))
	if rc == 0 {
		return 0
	}
	return (rc + 3) / 4
}

// referenceAvailableBudget is min(floor(profileBudget * 0.50), 16384) (§6.2).
func referenceAvailableBudget(profileBudget int) int {
	if profileBudget <= 0 {
		profileBudget = referenceDefaultProfileBudget
	}
	available := int(float64(profileBudget) * referenceBudgetShare)
	if available > referenceMaxBudget {
		available = referenceMaxBudget
	}
	return available
}

// referenceEstimatedTokens sums each source's estimate after the 2,048-token
// per-source cap (§6.2).
func referenceEstimatedTokens(sources []referenceSource) int {
	total := 0
	for _, s := range sources {
		est := estimateSourceTokens(s.Content)
		if est > referenceMaxSourceTokens {
			est = referenceMaxSourceTokens
		}
		total += est
	}
	return total
}

// referenceBudgetPreview builds the §9.1 context_budget object and rejects a
// selection the budget cannot fund (§6.2, §15 scenario 16 — no selective
// omission).
func referenceBudgetPreview(sources []referenceSource, profileBudget int) (ReferenceContextPreview, int, error) {
	available := referenceAvailableBudget(profileBudget)
	minimum := len(sources) * referenceMinSourceTokens
	if available < minimum {
		return ReferenceContextPreview{}, 0, newReferenceAPIError(ErrReferenceContextBudgetExceeded, "")
	}
	estimated := referenceEstimatedTokens(sources)
	return ReferenceContextPreview{
		AvailableTokens:       available,
		MinimumRequiredTokens: minimum,
		EstimatedTokens:       estimated,
		Fits:                  estimated <= available,
	}, available, nil
}

// referenceManifestHash hashes the canonical (tree, primary, ordered source
// snapshot, budget) tuple that binds a node to the context it was created
// from (§3.3 contextManifestHash, §9.2 manifest_hash; 64 hex chars).
func referenceManifestHash(treeID, primarySourceID uuid.UUID, sources []referenceSource, budget int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "canopy-multi-reference|v1|%s|%s|%d", treeID, primarySourceID, budget)
	for i, s := range sources {
		fmt.Fprintf(&b, "|%d|%s|%s|%d", i, s.ID, s.ContentHash, s.SequenceNum)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// --- Branch span (§8.1) ------------------------------------------------------

// displayChain is one source's parent_id chain ordered from the tree root
// down to the source.
type displayChain struct {
	sourceID uuid.UUID
	fromRoot []uuid.UUID
}

// loadDisplayChains walks each source's display-parent chain in one query.
// parent_id is a single pointer, so the chains form a forest and the deepest
// node shared by every chain is the selection's nearest common display
// ancestor.
func loadDisplayChains(ctx context.Context, q referenceQuerier, treeID uuid.UUID, sourceIDs []uuid.UUID) ([]displayChain, error) {
	raw := make([]string, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		raw = append(raw, id.String())
	}
	rows, err := q.Query(ctx, `
        WITH RECURSIVE chain(src_id, node_id, parent_id, depth) AS (
            SELECT n.id, n.id, n.parent_id, 0
            FROM nodes n
            WHERE n.id = ANY($1::uuid[]) AND n.tree_id = $2 AND n.deleted_at IS NULL
            UNION ALL
            SELECT c.src_id, p.id, p.parent_id, c.depth + 1
            FROM nodes p
            JOIN chain c ON p.id = c.parent_id
            WHERE p.deleted_at IS NULL AND p.tree_id = $2 AND c.depth < 10000
        )
        SELECT src_id, node_id, depth FROM chain ORDER BY src_id, depth ASC`,
		raw, treeID)
	if err != nil {
		return nil, fmt.Errorf("%w: load display chains: %v", ErrDatabaseUnavailable, err)
	}
	defer rows.Close()

	bySource := make(map[uuid.UUID][]uuid.UUID, len(sourceIDs))
	for rows.Next() {
		var srcID, nodeID uuid.UUID
		var depth int
		if err := rows.Scan(&srcID, &nodeID, &depth); err != nil {
			return nil, fmt.Errorf("%w: scan display chain: %v", ErrDatabaseUnavailable, err)
		}
		bySource[srcID] = append(bySource[srcID], nodeID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate display chains: %v", ErrDatabaseUnavailable, err)
	}

	chains := make([]displayChain, 0, len(sourceIDs))
	for _, srcID := range sourceIDs {
		nodes := bySource[srcID]
		if len(nodes) == 0 {
			return nil, newReferenceAPIError(ErrReferenceSourceNotFound,
				"a selected message has no display chain in this tree")
		}
		fromRoot := make([]uuid.UUID, len(nodes))
		for i, n := range nodes {
			// The query walks source → root, so reverse it.
			fromRoot[len(nodes)-1-i] = n
		}
		chains = append(chains, displayChain{sourceID: srcID, fromRoot: fromRoot})
	}
	return chains, nil
}

// computeBranchSpan implements §8.1: the deepest display ancestor shared by
// every selected source, plus each source's first divergent child. When a
// source IS the common ancestor its branch_root_id is the common ancestor
// itself (§15 scenario 14).
func computeBranchSpan(ctx context.Context, q referenceQuerier, treeID uuid.UUID, sourceIDs []uuid.UUID) (*BranchSpanMetadata, error) {
	chains, err := loadDisplayChains(ctx, q, treeID, sourceIDs)
	if err != nil {
		return nil, err
	}
	if len(chains) == 0 {
		return nil, nil
	}

	// Depth from the root of every node in the first chain, then keep only
	// the nodes present in every other chain.
	depthFromRoot := make(map[uuid.UUID]int, len(chains[0].fromRoot))
	for depth, id := range chains[0].fromRoot {
		depthFromRoot[id] = depth
	}
	commonDepth := -1
	var commonAncestor uuid.UUID
	for _, id := range chains[0].fromRoot {
		inAll := true
		for _, c := range chains[1:] {
			if !containsUUID(c.fromRoot, id) {
				inAll = false
				break
			}
		}
		if !inAll {
			continue
		}
		if d := depthFromRoot[id]; d > commonDepth {
			commonDepth = d
			commonAncestor = id
		}
	}
	if commonDepth < 0 {
		// §15 scenario 15: valid nodes in a valid tree share a root; no
		// shared display ancestor means the graph is corrupt or the
		// selection spans roots.
		return nil, newReferenceAPIError(ErrReferenceTreeMismatch,
			"selected messages share no common display ancestor")
	}

	span := &BranchSpanMetadata{
		CommonAncestorID: commonAncestor,
		SourceBranches:   make([]ReferenceBranchSource, 0, len(chains)),
	}
	for _, c := range chains {
		sourceDepth := len(c.fromRoot) - 1
		branchRoot := commonAncestor
		distance := 0
		if sourceDepth > commonDepth {
			// First node after the common ancestor on this source's chain.
			branchRoot = c.fromRoot[commonDepth+1]
			distance = sourceDepth - (commonDepth + 1)
		}
		span.SourceBranches = append(span.SourceBranches, ReferenceBranchSource{
			SourceID:         c.sourceID,
			BranchRootID:     branchRoot,
			DistanceFromRoot: distance,
		})
	}
	return span, nil
}

// containsUUID reports whether ids holds id.
func containsUUID(ids []uuid.UUID, id uuid.UUID) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// IsSyntheticMergePoint reports whether the selection spans divergent
// display branches: §8.1's `same_branch = all selected sources have the same
// branch_root_id`, inverted.
func IsSyntheticMergePoint(span *BranchSpanMetadata) bool {
	if span == nil || len(span.SourceBranches) < 2 {
		return false
	}
	first := span.SourceBranches[0].BranchRootID
	for _, sb := range span.SourceBranches[1:] {
		if sb.BranchRootID != first {
			return true
		}
	}
	return false
}

// --- TreeService reference methods -------------------------------------------

// WithReferenceSelection configures the §4.3 selection-token signer and the
// fallback profile context budget used when a preflight request omits
// profile_context_budget. Without a signer the reference endpoints answer 503
// (the feature is unconfigured, never silently permissive).
func (s *TreeServiceImpl) WithReferenceSelection(signer *ReferenceSelectionSigner, defaultProfileBudget int) *TreeServiceImpl {
	s.refSigner = signer
	if defaultProfileBudget > 0 {
		s.refBudget = defaultProfileBudget
	}
	return s
}

// VerifyReferenceSelectionToken checks a signed selection token for a caller
// (§13.1 step 3). Exposed on TreeService so NodeService can resolve the
// snapshot claims without owning the signing key.
func (s *TreeServiceImpl) VerifyReferenceSelectionToken(_ context.Context, token string, callerID uuid.UUID) (*ReferenceSelectionClaims, error) {
	if !s.refSigner.configured() {
		return nil, newReferenceAPIError(ErrReferenceSelectionUnconfigured, "")
	}
	return s.refSigner.Verify(token, callerID)
}

// ValidateReferenceSelection validates an ordered selection and returns a
// signed five-minute token plus a preview manifest. It writes no graph rows
// (SPEC-PL-06 §9.1, §11, §4.3).
func (s *TreeServiceImpl) ValidateReferenceSelection(ctx context.Context, treeID uuid.UUID, input ReferenceSelectionInput) (*ReferenceSelectionResult, error) {
	// Configuration before connectivity: an unwired signer is a 503 feature
	// gap, never a permissive fallback.
	if !s.refSigner.configured() {
		return nil, newReferenceAPIError(ErrReferenceSelectionUnconfigured, "")
	}
	if s.pool == nil {
		return nil, ErrDatabaseUnavailable
	}
	if treeID == uuid.Nil {
		return nil, newReferenceAPIError(ErrReferenceTreeMismatch, "tree_id is required")
	}
	requesterID := requesterFromContext(ctx)
	if requesterID == uuid.Nil {
		return nil, ErrNotTreeMember
	}

	ordered := make([]uuid.UUID, len(input.SourceNodeIDs))
	copy(ordered, input.SourceNodeIDs)

	// Normalise the primary source to index 0 BEFORE computing labels,
	// branch metadata, hashes, and the signature (§11).
	primary := ordered[0]
	if input.PrimarySourceID != nil && *input.PrimarySourceID != uuid.Nil {
		primary = *input.PrimarySourceID
		found := false
		normalised := make([]uuid.UUID, 0, len(ordered))
		normalised = append(normalised, primary)
		for _, id := range ordered {
			if id == primary {
				found = true
				continue
			}
			normalised = append(normalised, id)
		}
		if !found {
			return nil, newReferenceAPIError(ErrReferencePrimaryNotSelected, "")
		}
		ordered = normalised
	}

	sources, err := loadReferenceSources(ctx, s.pool, treeID, ordered, referenceLoadPreflight)
	if err != nil {
		return nil, err
	}

	preview, available, err := referenceBudgetPreview(sources, s.referenceBudget(input.ProfileContextBudget))
	if err != nil {
		return nil, err
	}

	span, err := computeBranchSpan(ctx, s.pool, treeID, ordered)
	if err != nil {
		return nil, err
	}

	hashes := make([]string, 0, len(sources))
	branchRoots := make(map[uuid.UUID]uuid.UUID, len(ordered))
	if span != nil {
		for _, sb := range span.SourceBranches {
			branchRoots[sb.SourceID] = sb.BranchRootID
		}
	}
	parents := make([]ReferenceParent, 0, len(sources))
	for i, src := range sources {
		hashes = append(hashes, src.ContentHash)
		parents = append(parents, ReferenceParent{
			NodeID:         src.ID,
			SourceLabel:    db.ReferenceSourceLabel(i),
			ColorKey:       db.ReferenceColorKey(treeID, uuid.Nil, src.ID),
			BranchRootID:   branchRoots[src.ID],
			ContentHash:    src.ContentHash,
			SequenceNum:    src.SequenceNum,
			ContentPreview: previewOf(src.Content),
		})
	}

	token, err := s.refSigner.Sign(ReferenceSelectionClaims{
		TreeID:          treeID,
		RequesterID:     requesterID,
		PrimarySourceID: primary,
		SourceIDs:       ordered,
		SourceHashes:    hashes,
		Budget:          available,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: sign selection token: %v", ErrDatabaseUnavailable, err)
	}

	return &ReferenceSelectionResult{
		SelectionToken:        token,
		ExpiresAt:             time.Unix(s.refSigner.claimedExpiry(token), 0).UTC(),
		TreeID:                treeID,
		CanonicalSourceIDs:    ordered,
		PrimarySourceID:       primary,
		IsSyntheticMergePoint: IsSyntheticMergePoint(span),
		BranchSpan:            span,
		ContextBudget:         preview,
		Sources:               parents,
	}, nil
}

// claimedExpiry re-reads the expiry the signer just stamped, so the response
// and the token can never disagree.
func (s *ReferenceSelectionSigner) claimedExpiry(token string) int64 {
	parts := strings.Split(token, ".")
	if len(parts) != 4 {
		return s.now().Add(ReferenceSelectionTTL).Unix()
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return s.now().Add(ReferenceSelectionTTL).Unix()
	}
	var claims ReferenceSelectionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return s.now().Add(ReferenceSelectionTTL).Unix()
	}
	return claims.ExpiresAt
}

// referenceBudget resolves the profile budget used for preflight estimation:
// the request value, else the configured default (§9.1 field rules).
func (s *TreeServiceImpl) referenceBudget(requested int) int {
	if requested > 0 {
		return requested
	}
	if s.refBudget > 0 {
		return s.refBudget
	}
	return referenceDefaultProfileBudget
}

// GetReferenceParents returns a multi-reference target's active reference
// parents in canonical selection order (§11).
func (s *TreeServiceImpl) GetReferenceParents(ctx context.Context, treeID, nodeID uuid.UUID) ([]ReferenceParent, error) {
	if s.pool == nil {
		return nil, ErrDatabaseUnavailable
	}
	var (
		nodeTreeID uuid.UUID
		parentMode string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT tree_id, parent_mode FROM nodes WHERE id = $1 AND deleted_at IS NULL`,
		nodeID).Scan(&nodeTreeID, &parentMode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTreeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read reference target: %v", ErrDatabaseUnavailable, err)
	}
	if nodeTreeID != treeID {
		return nil, newReferenceAPIError(ErrReferenceTreeMismatch, "")
	}
	if parentMode != string(db.ParentModeMultiReference) {
		return []ReferenceParent{}, nil
	}

	rows, err := s.pool.Query(ctx, `
        SELECT e.source_id, n.content_hash, n.sequence_num
        FROM edges e
        JOIN nodes n ON n.id = e.source_id
        WHERE e.target_id = $1
          AND e.edge_type = $2
          AND e.deleted_at IS NULL
          AND n.deleted_at IS NULL
        ORDER BY COALESCE((e.metadata->>'selection_order')::int, 2147483647) ASC,
                 e.sequence_num ASC`,
		nodeID, db.EdgeTypeReference)
	if err != nil {
		return nil, fmt.Errorf("%w: read reference parents: %v", ErrDatabaseUnavailable, err)
	}
	defer rows.Close()

	out := make([]ReferenceParent, 0, referenceSourceMaxCount)
	idx := 0
	for rows.Next() {
		var (
			sourceID    uuid.UUID
			contentHash string
			seq         int64
		)
		if err := rows.Scan(&sourceID, &contentHash, &seq); err != nil {
			return nil, fmt.Errorf("%w: scan reference parent: %v", ErrDatabaseUnavailable, err)
		}
		out = append(out, ReferenceParent{
			NodeID:      sourceID,
			SourceLabel: db.ReferenceSourceLabel(idx),
			ColorKey:    db.ReferenceColorKey(treeID, nodeID, sourceID),
			ContentHash: contentHash,
			SequenceNum: seq,
		})
		idx++
	}
	return out, rows.Err()
}

// AnalyzeReferenceBranchSpan finds the nearest shared display ancestor and
// each source's first divergent child (§8.1, §11). Deterministic for the
// same source order.
func (s *TreeServiceImpl) AnalyzeReferenceBranchSpan(ctx context.Context, treeID uuid.UUID, sourceIDs []uuid.UUID) (*BranchSpanMetadata, error) {
	if s.pool == nil {
		return nil, ErrDatabaseUnavailable
	}
	if len(sourceIDs) < 2 {
		return nil, newReferenceAPIError(ErrReferenceSourceCountTooLow, "")
	}
	return computeBranchSpan(ctx, s.pool, treeID, sourceIDs)
}

// previewOf trims a source's content to the §9.1 content_preview.
func previewOf(content string) string {
	runes := []rune(content)
	if len(runes) <= referencePreviewRunes {
		return content
	}
	return string(runes[:referencePreviewRunes]) + "…"
}

// sortedUUIDStrings returns ids as strings in canonical UUID order — used by
// tests and by lock ordering (§5.3 step 1).
func sortedUUIDStrings(ids []uuid.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	sort.Strings(out)
	return out
}
