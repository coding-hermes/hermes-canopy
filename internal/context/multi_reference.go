package context

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// Multi-reference context compilation (SPEC-PL-06 §6).
//
// Given a preflight-validated selection (phase 1 wrote the node + reference
// edges; phase 2 serves the read route), this file turns the persisted
// selection into the §6.1 provenance block that the model receives, plus the
// §6.3 structured context and manifest entry that the user sees.
//
// Two rules shape everything here:
//
//   - All-or-nothing (§6.4): a selection is either compiled in full or the
//     compilation fails. There is no selective omission, ever — no partial
//     source set, and never a block plus an error.
//   - Selected content is DATA, never instructions (§6.4): a source may not
//     terminate the block, and nothing in a source is treated as a directive
//     merely because it was selected.

// --- Spec constants (§6.2) ---------------------------------------------------

// These mirror the unexported constants in internal/service/multi_reference.go
// (same SPEC-PL-06 §6.2 values). They are intentionally duplicated: the import
// direction between internal/context and internal/service must stay one-way, so
// neither package may adopt the other's constants. Change both together.
const (
	// referenceBudgetShare is the share of the profile's turn context budget
	// the selected-source block may consume.
	referenceBudgetShare = 0.50
	// referenceMaxBudget caps the whole selected-source budget.
	referenceMaxBudget = 16384
	// referenceMinSourceTokens is the per-source floor and therefore the
	// minimum the profile budget must be able to reserve for every source.
	referenceMinSourceTokens = 256
	// referenceMaxSourceTokens is the per-source ceiling.
	referenceMaxSourceTokens = 2048
	// referenceDefaultProfileBudget is used when ProfileBudget <= 0.
	referenceDefaultProfileBudget = 8000
	// referenceCharsPerToken is the deterministic 4-chars-per-token rule
	// shared with NewTokenEstimator.
	referenceCharsPerToken = 4
)

const (
	// referenceMinSourceCount and referenceMaxSourceCount bound an ordered
	// selection (§6.4, §15.1 scenarios 6 and 21).
	referenceMinSourceCount = 2
	referenceMaxSourceCount = 20

	// blockOpen / blockClose delimit the §6.1 block.
	blockOpen  = `<canopy_multi_reference version="1"`
	blockClose = "</canopy_multi_reference"

	// blockTerminatorEscaped replaces any literal blockClose inside source
	// content so a selected message can never terminate the block (§6.4).
	blockTerminatorEscaped = `<\/canopy_multi_reference`
)

// §9.4 wire codes. Declared as plain strings in this package because
// internal/context must not import internal/service (one-way import
// direction). mirrors SPEC-PL-06 §9.4.
const (
	CodeReferenceSourceCountTooLow     = "REFERENCE_SOURCE_COUNT_TOO_LOW"
	CodeReferenceSourceCountTooHigh    = "REFERENCE_SOURCE_COUNT_TOO_HIGH"
	CodeReferenceContextBudgetExceeded = "REFERENCE_CONTEXT_BUDGET_EXCEEDED"
	CodeReferenceSourceNotFound        = "REFERENCE_SOURCE_NOT_FOUND"
	CodeReferenceSelectionStale        = "REFERENCE_SELECTION_STALE"
)

// --- Failure rules (§6.4) ----------------------------------------------------

// Sentinel errors for multi-reference compilation failures. Every failure is
// total: no partial source set is ever returned.
var (
	ErrMultiReferenceBudgetExceeded = errors.New("context: multi-reference context budget exceeded")
	ErrMultiReferenceSelectionStale = errors.New("context: multi-reference selection is stale")
	ErrMultiReferenceSourceCount    = errors.New("context: multi-reference source count out of range")
	ErrMultiReferenceSourceNotFound = errors.New("context: multi-reference source could not be loaded")
)

// MultiReferenceError is a multi-reference compilation failure carrying the
// exact §9.4 wire code the API layer must surface. Unwrap returns the package
// sentinel so errors.Is works without importing internal/service.
type MultiReferenceError struct {
	Code    string // exact §9.4 wire code
	Message string // human-readable detail (never source content)

	cause error
}

// Error implements error.
func (e *MultiReferenceError) Error() string { return e.Code + ": " + e.Message }

// Unwrap returns the sentinel this failure maps to (errors.Is support).
func (e *MultiReferenceError) Unwrap() error { return e.cause }

func newMultiReferenceError(code string, cause error, format string, args ...any) *MultiReferenceError {
	return &MultiReferenceError{Code: code, Message: fmt.Sprintf(format, args...), cause: cause}
}

// --- Inputs ------------------------------------------------------------------

// MultiReferenceSourceInput is one selected source as loaded from the store,
// in canonical selection order (R1..RN).
type MultiReferenceSourceInput struct {
	NodeID            uuid.UUID
	AuthorID          uuid.UUID
	NodeType          string // "message" | "synthesis" | "system"
	SequenceNum       int64
	CreatedAt         time.Time
	BranchRootID      uuid.UUID
	ContentHash       string // live hash; empty means "could not be loaded"
	SignedContentHash string // hash baked into the signed preflight token
	Content           string
	Label             string // "R1".."RN" as persisted
	ColorKey          string // "ref-1".."ref-6" as persisted
}

// MultiReferenceSelection is a preflight-validated selection ready to compile.
type MultiReferenceSelection struct {
	TreeID        uuid.UUID
	Metadata      db.MultiReferenceMetadata // persisted §3.3 record
	Sources       []MultiReferenceSourceInput
	ProfileBudget int // P, the profile's turn context budget; <=0 -> default 8000
}

// --- Output types (§6.3) -----------------------------------------------------

// MultiReferenceContext is the structured §6.3 view of the compiled
// selected-source block. The compiler only ever CARRIES the manifest hash —
// it is the provenance record the service created when the reply was written
// and must never be recomputed here.
type MultiReferenceContext struct {
	TreeID                uuid.UUID              `json:"treeId"`
	PrimarySourceID       uuid.UUID              `json:"primarySourceId"`
	Sources               []MultiReferenceSource `json:"sources"`
	IsSyntheticMergePoint bool                   `json:"isSyntheticMergePoint"`
	BranchSpan            *db.BranchSpanMetadata `json:"branchSpan,omitempty"`
	TokenBudget           int                    `json:"tokenBudget"`
	TokensUsed            int                    `json:"tokensUsed"`
	ManifestHash          string                 `json:"manifestHash"`
}

// MultiReferenceSource is one compiled source in canonical selection order.
type MultiReferenceSource struct {
	ReferenceIndex int       `json:"referenceIndex"`
	SourceLabel    string    `json:"sourceLabel"`
	ColorKey       string    `json:"colorKey"`
	NodeID         uuid.UUID `json:"nodeId"`
	AuthorID       uuid.UUID `json:"authorId"`
	NodeType       string    `json:"nodeType"`
	SequenceNum    int64     `json:"sequenceNum"`
	CreatedAt      time.Time `json:"createdAt"`
	BranchRootID   uuid.UUID `json:"branchRootId"`
	ContentHash    string    `json:"contentHash"`
	TokenCount     int       `json:"tokenCount"`
	Truncated      bool      `json:"truncated"`
	OmittedTokens  int       `json:"omittedTokens"`
	Content        string    `json:"content"`
}

// MultiReferenceManifestEntry is the auditable record of a compiled
// multi-reference block, attached to the visible context manifest.
type MultiReferenceManifestEntry struct {
	PrimarySourceID  uuid.UUID              `json:"primarySourceId"`
	SourceCount      int                    `json:"sourceCount"`
	ManifestHash     string                 `json:"manifestHash"`
	TokenBudget      int                    `json:"tokenBudget"`
	TokensUsed       int                    `json:"tokensUsed"`
	CommonAncestorID string                 `json:"commonAncestorId,omitempty"`
	Sources          []MultiReferenceSource `json:"sources"`
}

// --- Estimation --------------------------------------------------------------

// referenceEstimator reuses the package's existing deterministic estimator
// (ceil(runes/4)) so §6.2 never grows a second counting rule.
var referenceEstimator = NewTokenEstimator()

// estimateReferenceTokens is the §6.2 "estimated_source_tokens" rule.
func estimateReferenceTokens(content string) int {
	return referenceEstimator.Estimate(content)
}

// contributionTokens is the §6.2 "estimated_source_tokens" of a source as it is
// actually PLACED in the block: §6.4 escapes a literal block terminator inside
// selected content before rendering, which can add a token to a source that is
// short enough for one character to matter. Budgeting the escaped text is what
// keeps a source that fits its allocation from being truncated by the escape
// itself (and from rendering a bogus one-token omission marker).
func contributionTokens(content string) int {
	escaped, _ := escapeBlockTerminator(content)
	return estimateReferenceTokens(escaped)
}

// --- Budget allocation (§6.2) ------------------------------------------------

// AllocateMultiReferenceBudget returns the per-source token allocation and the
// total reference budget (§6.2).
//
//	reference_budget = min(floor(P*0.50), 16384)
//	every source starts at 256
//	weighted_need[i] = min(2048, estimated_source_tokens[i]) - 256
//	remaining is handed out proportionally by weighted_need using the
//	largest-remainder (Hamilton) method with integer arithmetic, ties broken
//	by lower selection index; no source ever exceeds 2048
//	a source that cannot use its whole allocation hands the surplus over
//	once, in canonical order, to the sources below the cap that still have
//	content to place, and settles down to what it actually contributes (see
//	redistributeUnused)
//
// minimum_required = source_count * 256 is an ADMISSION rule: it is what the
// profile budget must be able to reserve for the selection to be compiled at
// all. It is not an end state the vector must still hold once the pass has
// run — 256 is where every source STARTS.
//
// Invariants that hold after that single pass:
//
//	allocation[i] >= min(estimated_source_tokens[i], 256) and <= 2048 — a
//	source whose content is shorter than the 256-token start settles BELOW
//	256, because it contributes only its actual tokens
//	sum(allocations) <= reference_budget, always
//	sum(allocations) == reference_budget whenever a source below the 2,048
//	cap still has unmet demand (no stranded budget)
//
// The vector the pass returns is what the sources actually place, so a source
// the budget cannot satisfy keeps a shortfall instead of a padded allocation.
//
// The algorithm is pure integer arithmetic and deterministic: the same input
// always produces the same allocation.
func AllocateMultiReferenceBudget(profileBudget int, sources []MultiReferenceSourceInput) ([]int, int, error) {
	profileBudget = referenceProfileBudget(profileBudget)
	referenceBudget := referenceAvailableBudget(profileBudget)

	count := len(sources)
	minimumRequired := count * referenceMinSourceTokens
	if referenceBudget < minimumRequired {
		return nil, referenceBudget, newMultiReferenceError(CodeReferenceContextBudgetExceeded,
			ErrMultiReferenceBudgetExceeded,
			"%d sources require %d tokens but a profile budget of %d only reserves %d",
			count, minimumRequired, profileBudget, referenceBudget)
	}

	allocations := make([]int, count)
	for i := range allocations {
		allocations[i] = referenceMinSourceTokens
	}
	if count == 0 {
		return allocations, referenceBudget, nil
	}

	remaining := referenceBudget - minimumRequired
	need := make([]int, count)
	totalNeed := 0
	for i, src := range sources {
		estimated := contributionTokens(src.Content)
		if estimated > referenceMaxSourceTokens {
			estimated = referenceMaxSourceTokens
		}
		if estimated > referenceMinSourceTokens {
			need[i] = estimated - referenceMinSourceTokens
		}
		totalNeed += need[i]
	}

	if remaining > 0 && totalNeed > 0 {
		allocateRemaining(allocations, need, totalNeed, remaining)
	}

	redistributeUnused(allocations, sources)
	return allocations, referenceBudget, nil
}

// allocateRemaining hands out the tokens above the floor proportionally by
// weighted need, using the largest-remainder method: integer division first,
// then one unit at a time to the largest fractional remainder (ties to the
// lower selection index). Sources whose weighted need is zero are never
// allocated above the floor, and no source passes the per-source cap.
func allocateRemaining(allocations, need []int, totalNeed, remaining int) {
	count := len(need)
	remainders := make([]int, count)
	distributed := 0
	for i := range need {
		if need[i] == 0 {
			continue
		}
		numerator := remaining * need[i]
		share := numerator / totalNeed
		// The proportional share may not push a source past the cap.
		if headroom := referenceMaxSourceTokens - allocations[i]; share > headroom {
			share = headroom
		}
		allocations[i] += share
		distributed += share
		remainders[i] = numerator - share*totalNeed
	}

	leftover := remaining - distributed
	if leftover <= 0 {
		return
	}

	order := make([]int, 0, count)
	for i := range need {
		if need[i] > 0 {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if remainders[ia] != remainders[ib] {
			return remainders[ia] > remainders[ib]
		}
		return ia < ib // ties break toward the lower selection index
	})

	for leftover > 0 {
		progressed := false
		for _, i := range order {
			if leftover == 0 {
				break
			}
			if allocations[i] >= referenceMaxSourceTokens {
				continue
			}
			allocations[i]++
			leftover--
			progressed = true
		}
		if !progressed {
			return // every candidate is at the cap; the rest stays unused
		}
	}
}

// redistributeUnused settles the §6.2 redistribution pass and makes the
// allocation vector equal to what the sources actually place.
//
// §6.2 fixes the reading this pass implements:
//
//	allocation[i] starts at 256 tokens for every source
//	A source shorter than its allocation contributes its actual tokens.
//	Unused tokens are redistributed once, in canonical order, to sources below
//	their 2,048-token cap. If all sources fit below the budget, the compiler
//	reports unused selected-source budget and does not pad context.
//
// 256 is therefore the STARTING allocation, not a floor every source must
// still hold once the pass has run: a source whose content is shorter than
// 256 cannot fill its start value, contributes its actual tokens, and ends
// BELOW 256.
//
// The pass runs once, in four steps:
//
//  1. contribution[i] = min(allocation[i], estimated_source_tokens[i]) — the
//     tokens the source actually places.
//
//  2. pool = Σ (allocation[i] - contribution[i]) — every unused token,
//     including the ones that sit inside the 256-token start value.
//
//  3. the pool is handed out ONCE, in canonical order, to the sources still
//     below the 2,048-token cap that have content left to place:
//     take = min(pool, headroom, wanted). A receiver takes at most what its
//     own content still needs, so the pass never pads context; and a donor —
//     a source already allocated more than its content can use — can never
//     receive in the same pass.
//
//  4. the donors settle, in canonical order, down to contribution[i]. How far
//     a donor settles depends on where its unused tokens sit:
//
//     · Tokens inside the 256-token start value are not a reservation a source
//     may hold on to. §6.2 makes 256 the starting allocation and a source
//     shorter than its allocation contributes its actual tokens, so a donor
//     whose content is shorter than 256 settles at its contribution
//     unconditionally — even when no source below the cap can take those
//     tokens, which then surface as unused selected-source budget.
//
//     · Tokens ABOVE the 256-token start value were allocated proportionally
//     by weighted need: a donor gives up exactly what the pass placed with a
//     receiver, never below its contribution. When no source below the cap
//     can use them they stay with the source that earned them, so the pass
//     can neither push the total past the reference budget nor spend the
//     same token twice.
//
// Whatever is not placed is unused selected-source budget (§6.2); context is
// never padded.
func redistributeUnused(allocations []int, sources []MultiReferenceSourceInput) {
	// Step 1 + 2: what each source actually places, and the unused tokens.
	givable := make([]int, len(sources))
	belowStart := make([]bool, len(sources))
	pool := 0
	for i, src := range sources {
		contribution := contributionTokens(src.Content)
		if contribution >= allocations[i] {
			continue // the source places its whole allocation: nothing unused
		}
		givable[i] = allocations[i] - contribution
		belowStart[i] = contribution < referenceMinSourceTokens
		pool += givable[i]
	}
	if pool == 0 {
		return
	}

	// Step 3: hand the pool out once, in canonical order, to the sources below
	// the cap that still have content to place.
	unplaced := pool
	for i, src := range sources {
		if unplaced == 0 {
			break
		}
		headroom := referenceMaxSourceTokens - allocations[i]
		wanted := contributionTokens(src.Content) - allocations[i]
		if headroom <= 0 || wanted <= 0 {
			continue // at the cap, or nothing left to place
		}
		take := unplaced
		if take > headroom {
			take = headroom
		}
		if take > wanted {
			take = wanted
		}
		allocations[i] += take
		unplaced -= take
	}

	// Step 4: settle the donors in canonical order, down to contribution[i].
	placed := pool - unplaced
	for i := range sources {
		if givable[i] == 0 {
			continue
		}
		give := givable[i]
		if !belowStart[i] {
			if give > placed {
				give = placed
			}
			placed -= give
		}
		allocations[i] -= give
	}
}

// referenceProfileBudget substitutes the default when a caller passes no
// usable profile budget.
func referenceProfileBudget(profileBudget int) int {
	if profileBudget <= 0 {
		return referenceDefaultProfileBudget
	}
	return profileBudget
}

// referenceAvailableBudget is min(floor(P*0.50), 16384) (§6.2).
func referenceAvailableBudget(profileBudget int) int {
	available := int(float64(referenceProfileBudget(profileBudget)) * referenceBudgetShare)
	if available > referenceMaxBudget {
		available = referenceMaxBudget
	}
	return available
}

// --- Compilation -------------------------------------------------------------

// multiReferenceCompilation is the full result of compiling a selection: the
// §6.3 context, the §6.1 block text, and the facts the manifest needs.
type multiReferenceCompilation struct {
	Context *MultiReferenceContext
	Block   string
	// AllFit reports that no source was truncated, which is the condition for
	// reporting unused selected-source budget (§6.2).
	AllFit bool
	// Escaped lists the labels of sources whose content contained the block
	// terminator and was escaped (§6.4).
	Escaped []string
}

// CompileMultiReference validates a selection against its signed hashes and
// renders the §6.1 block. It returns the structured §6.3 context, the exact
// text to prepend to the compiled context, and an error carrying the §9.4
// wire code when the selection cannot be compiled.
//
// A nil selection compiles to nothing.
func CompileMultiReference(sel *MultiReferenceSelection) (*MultiReferenceContext, string, error) {
	compiled, err := compileMultiReference(sel)
	if err != nil {
		return nil, "", err
	}
	if compiled == nil {
		return nil, "", nil
	}
	return compiled.Context, compiled.Block, nil
}

// compileMultiReference is the internal implementation; it keeps the
// manifest-only facts (all-fit, escaped labels) that are not part of the
// §6.3 wire shape.
func compileMultiReference(sel *MultiReferenceSelection) (*multiReferenceCompilation, error) {
	if sel == nil {
		return nil, nil
	}

	count := len(sel.Sources)
	switch {
	case count < referenceMinSourceCount:
		return nil, newMultiReferenceError(CodeReferenceSourceCountTooLow, ErrMultiReferenceSourceCount,
			"a multi-reference selection needs at least %d sources, got %d", referenceMinSourceCount, count)
	case count > referenceMaxSourceCount:
		return nil, newMultiReferenceError(CodeReferenceSourceCountTooHigh, ErrMultiReferenceSourceCount,
			"a multi-reference selection allows at most %d sources, got %d", referenceMaxSourceCount, count)
	}

	// §6.4: verify the whole selection against the signed token BEFORE any
	// content is rendered. One unusable source fails the entire compilation.
	for i, src := range sel.Sources {
		label := sourceLabel(src, i)
		switch {
		case src.SignedContentHash != "" && src.ContentHash == "":
			// Signed, but gone now: the snapshot the user chose no longer
			// exists, so answering would mix snapshots.
			return nil, newMultiReferenceError(CodeReferenceSelectionStale, ErrMultiReferenceSelectionStale,
				"source %s was part of the signed selection but can no longer be loaded", label)
		case src.ContentHash == "":
			return nil, newMultiReferenceError(CodeReferenceSourceNotFound, ErrMultiReferenceSourceNotFound,
				"source %s could not be loaded", label)
		case src.SignedContentHash != "" && src.ContentHash != src.SignedContentHash:
			return nil, newMultiReferenceError(CodeReferenceSelectionStale, ErrMultiReferenceSelectionStale,
				"source %s changed after the selection was signed", label)
		}
	}

	allocations, referenceBudget, err := AllocateMultiReferenceBudget(sel.ProfileBudget, sel.Sources)
	if err != nil {
		return nil, err
	}

	ctx := &MultiReferenceContext{
		TreeID:                sel.TreeID,
		PrimarySourceID:       sel.Metadata.PrimarySourceID,
		Sources:               make([]MultiReferenceSource, 0, count),
		IsSyntheticMergePoint: sel.Metadata.IsSyntheticMergePoint,
		BranchSpan:            sel.Metadata.BranchSpan,
		TokenBudget:           referenceBudget,
		ManifestHash:          sel.Metadata.ContextManifestHash,
	}

	compiled := &multiReferenceCompilation{Context: ctx, AllFit: true}
	for i, src := range sel.Sources {
		label := sourceLabel(src, i)
		content, escaped := escapeBlockTerminator(src.Content)
		if escaped {
			compiled.Escaped = append(compiled.Escaped, label)
		}
		text, truncated, omitted, used := renderSourceContent(content, allocations[i], label)
		if truncated {
			compiled.AllFit = false
		}
		ctx.TokensUsed += used
		ctx.Sources = append(ctx.Sources, MultiReferenceSource{
			ReferenceIndex: i + 1,
			SourceLabel:    label,
			ColorKey:       src.ColorKey,
			NodeID:         src.NodeID,
			AuthorID:       src.AuthorID,
			NodeType:       src.NodeType,
			SequenceNum:    src.SequenceNum,
			CreatedAt:      src.CreatedAt.UTC(),
			BranchRootID:   src.BranchRootID,
			ContentHash:    src.ContentHash,
			TokenCount:     used,
			Truncated:      truncated,
			OmittedTokens:  omitted,
			Content:        text,
		})
	}

	compiled.Block = renderMultiReferenceBlock(sel, ctx)
	return compiled, nil
}

// multiRefManifestEntry builds the visible audit record for a compiled
// multi-reference block. common_ancestor_id is omitted when the selection
// carries no branch span, mirroring the §6.1 block.
func multiRefManifestEntry(ctx *MultiReferenceContext) *MultiReferenceManifestEntry {
	entry := &MultiReferenceManifestEntry{
		PrimarySourceID: ctx.PrimarySourceID,
		SourceCount:     len(ctx.Sources),
		ManifestHash:    ctx.ManifestHash,
		TokenBudget:     ctx.TokenBudget,
		TokensUsed:      ctx.TokensUsed,
		Sources:         ctx.Sources,
	}
	if ctx.BranchSpan != nil {
		entry.CommonAncestorID = ctx.BranchSpan.CommonAncestorID.String()
	}
	return entry
}

// renderMultiReferenceBlock renders the §6.1 block. The header attributes are
// one per line; common_ancestor_id is omitted entirely when the selection
// carries no branch span.
func renderMultiReferenceBlock(sel *MultiReferenceSelection, ctx *MultiReferenceContext) string {
	attrs := []string{
		"  target_tree_id=" + strconv.Quote(sel.TreeID.String()),
		"  primary_source_id=" + strconv.Quote(ctx.PrimarySourceID.String()),
		"  source_count=" + strconv.Quote(strconv.Itoa(len(ctx.Sources))),
		"  synthetic_context_merge=" + strconv.Quote(strconv.FormatBool(ctx.IsSyntheticMergePoint)),
	}
	if sel.Metadata.BranchSpan != nil {
		attrs = append(attrs, "  common_ancestor_id="+strconv.Quote(sel.Metadata.BranchSpan.CommonAncestorID.String()))
	}

	header := blockOpen + "\n"
	for i, attr := range attrs {
		header += attr
		if i == len(attrs)-1 {
			header += ">"
		}
		header += "\n"
	}

	sections := make([]string, 0, len(ctx.Sources)+2)
	sections = append(sections, header)
	for _, src := range ctx.Sources {
		sections = append(sections, sourceHeader(src)+"\n"+src.Content)
	}
	sections = append(sections, blockClose+">")
	return strings.Join(sections, "\n\n")
}

// sourceHeader renders the two-line per-source header (§6.1).
func sourceHeader(src MultiReferenceSource) string {
	return fmt.Sprintf(
		"[Source %s | color=%s | node_id=%s | branch_root=%s | author=%s |\n"+
			" sequence=%d | created_at=%s | node_type=%s | tokens=%d | truncated=%t]",
		src.SourceLabel, src.ColorKey, src.NodeID.String(), src.BranchRootID.String(), src.AuthorID.String(),
		src.SequenceNum, src.CreatedAt.UTC().Format(time.RFC3339), src.NodeType, src.TokenCount, src.Truncated)
}

// renderSourceContent applies the §6.1 head+tail rule against a source's
// allocation and returns the text to place in the block, whether content was
// actually cut, how many tokens were dropped, and the tokens the source used.
//
// The token count reported for a truncated source is its full allocation; the
// retained head, marker and tail together stay within that allocation, so a
// rendered source can never exceed its cap.
func renderSourceContent(content string, allocation int, label string) (text string, truncated bool, omitted int, used int) {
	if allocation <= 0 {
		allocation = referenceMinSourceTokens
	}
	total := estimateReferenceTokens(content)
	if total <= allocation {
		return content, false, 0, total
	}
	retained, omitted := truncateReferenceContent(content, allocation, total, label)
	return retained, true, omitted, allocation
}

// truncateReferenceContent keeps a head and a tail around an explicit omission
// marker. The retained head, tail and marker together fit the allowance, and
// the marker names the source label and the true number of dropped tokens.
func truncateReferenceContent(content string, allocation, total int, label string) (string, int) {
	runes := []rune(content)

	// Bound the retained text by the allowance minus the marker, so the
	// rendered source (marker included) never exceeds the allowance. The
	// marker is sized from the largest number it could carry.
	largestMarker := omittedMarker(total, label)
	keepChars := allocation*referenceCharsPerToken - len([]rune(largestMarker)) - 2 // two newlines
	if keepChars < 2 {
		keepChars = 2
	}
	if keepChars > len(runes) {
		keepChars = len(runes)
	}

	head := keepChars / 2
	tail := keepChars - head
	retainedTokens := (keepChars + referenceCharsPerToken - 1) / referenceCharsPerToken
	omitted := total - retainedTokens
	if omitted < 1 {
		omitted = 1
	}

	marker := omittedMarker(omitted, label)
	return string(runes[:head]) + "\n" + marker + "\n" + string(runes[len(runes)-tail:]), omitted
}

// omittedMarker renders the §6.1 omission marker with a comma-grouped count.
func omittedMarker(omitted int, label string) string {
	return fmt.Sprintf("[... %s tokens omitted from source %s ...]", groupThousands(omitted), label)
}

// groupThousands formats n with comma thousands separators (1,346).
func groupThousands(n int) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	digits := strconv.Itoa(n)
	if len(digits) <= 3 {
		return sign + digits
	}
	groups := make([]string, 0, len(digits)/3+1)
	for len(digits) > 3 {
		groups = append([]string{digits[len(digits)-3:]}, groups...)
		digits = digits[:len(digits)-3]
	}
	groups = append([]string{digits}, groups...)
	return sign + strings.Join(groups, ",")
}

// escapeBlockTerminator neutralizes any literal block terminator inside
// selected content (§6.4 — selected content is data, never instructions).
func escapeBlockTerminator(content string) (string, bool) {
	if !strings.Contains(content, blockClose) {
		return content, false
	}
	return strings.ReplaceAll(content, blockClose, blockTerminatorEscaped), true
}

// sourceLabel returns the persisted R-label, falling back to the canonical
// position when a caller did not carry one.
func sourceLabel(src MultiReferenceSourceInput, index int) string {
	if src.Label != "" {
		return src.Label
	}
	return fmt.Sprintf("R%d", index+1)
}
