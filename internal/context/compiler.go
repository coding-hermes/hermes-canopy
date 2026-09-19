package context

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// --- Compiler implementation -------------------------------------------------

// compilerImpl is the default Compiler, backed by repository interfaces.
// Stateless — safe for concurrent use.
type compilerImpl struct {
	nodes   NodeReader
	topics  TopicReader
	cards   CardReader
	est     TokenEstimator
	maxRefs int // soft cap for references (hard cap = 2x)

	// Retrieved tier (GAP-080 phase 4a). retrieval == nil or retrievalMax
	// <= 0 means the tier is disabled and Compile is byte-identical to the
	// pre-4a compiler.
	retrieval    RetrievalReader
	retrievalMax int
}

// isPinned reports whether a node's metadata JSON marks the node pinned: an
// OBJECT with `"pinned": true` (exact boolean true).
//
// Every other shape is NOT pinned and is never an error: absent metadata, nil,
// empty bytes, `{}`, `{"pinned": false}`, a non-boolean value
// (`{"pinned": "yes"}`), a JSON array or scalar, and malformed JSON all return
// false. A pinned node's content is never dropped by the budget walk
// (GAP-080 phase 1).
func isPinned(metadata []byte) bool {
	if len(metadata) == 0 {
		return false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &obj); err != nil {
		return false // malformed JSON, array, or scalar — not an object
	}
	raw, ok := obj["pinned"]
	if !ok {
		return false
	}
	var pinned bool
	if err := json.Unmarshal(raw, &pinned); err != nil {
		return false // present but not a JSON boolean
	}
	return pinned
}

// RetrievalSharePercent is the retrieved tier's share of the compile's
// TokenBudget (GAP-080 phase 4a — the vision's tier share, the audit's 12%
// figure). The tier allocation is floor(TokenBudget * RetrievalSharePercent
// / 100).
const RetrievalSharePercent = 12

// Option configures a compilerImpl built by NewCompiler.
type Option func(*compilerImpl)

// WithRetrieval enables the retrieved tier (GAP-080 phase 4a). A nil reader
// or max <= 0 is a no-op (the tier stays disabled) — never a panic.
func WithRetrieval(reader RetrievalReader, max int) Option {
	return func(c *compilerImpl) {
		if reader == nil || max <= 0 {
			return
		}
		c.retrieval = reader
		c.retrievalMax = max
	}
}

// NewCompiler wires repositories + estimator into a Compiler.
func NewCompiler(
	nodes NodeReader,
	topics TopicReader,
	cards CardReader,
	est TokenEstimator,
	maxRefs int,
	opts ...Option,
) Compiler {
	c := &compilerImpl{
		nodes:   nodes,
		topics:  topics,
		cards:   cards,
		est:     est,
		maxRefs: maxRefs,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// Compile implements Compiler.
func (c *compilerImpl) Compile(ctx context.Context, req CompileRequest) (*CompiledContext, error) {
	// Validate budget
	if req.TokenBudget < 1 {
		return nil, ErrInvalidBudget
	}

	// ── Step 0: Multi-reference block (SPEC-PL-06 §6) ───────────────────
	// The selected-source block is created before the agent's response and is
	// the highest-priority block of the turn: every selected message
	// participates. It is compiled FIRST, and a selection that cannot be
	// compiled in full fails the whole compilation — no partial source sets,
	// never a block plus an error (§6.4).
	multiRef, err := compileMultiReference(req.MultiReference)
	if err != nil {
		return nil, err
	}

	// Defaults
	maxAncestors := req.MaxAncestors
	if maxAncestors <= 0 {
		maxAncestors = 50
	}
	// Note: resolveRefs defaults to true — the zero-value for bool is false,
	// but the spec says "default true". The HTTP handler will set ResolveRefs=true
	// when the query param is absent (or "true"). The struct default is false
	// but the HTTP handler controls the semantics. For the Compiler, we treat
	// the field as-is from the caller.

	manifest := &Manifest{
		RequestID:   uuid.New().String(),
		NodeID:      req.NodeID,
		CompiledAt:  time.Now().UTC(),
		TokenBudget: req.TokenBudget,
	}

	// ── Step 1: Load current node ──────────────────────────────────────
	currentNode, err := c.nodes.GetByID(ctx, req.NodeID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, fmt.Errorf("%w: %v", ErrNodeNotFound, err)
		}
		return nil, fmt.Errorf("%w: get current node: %v", ErrDatabaseUnavailable, err)
	}

	// ── Step 2: Ancestry chain ─────────────────────────────────────────
	ancestors, err := c.nodes.GetAncestors(ctx, req.NodeID)
	if err != nil {
		return nil, fmt.Errorf("%w: get ancestors: %v", ErrDatabaseUnavailable, err)
	}

	// Reverse to oldest→newest. GetAncestors returns [self, parent, ..., root].
	// We need oldest→newest for budget dropping (oldest-first).
	reversed := make([]db.Node, len(ancestors))
	for i, n := range ancestors {
		reversed[len(ancestors)-1-i] = n
	}
	ancestors = reversed

	// If len > MaxAncestors, keep the NEWEST MaxAncestors, set OmittedReason="depth"
	omittedByDepth := 0
	if len(ancestors) > maxAncestors {
		omittedByDepth = len(ancestors) - maxAncestors
		ancestors = ancestors[omittedByDepth:] // keep newest
		manifest.OmittedCount = omittedByDepth
		manifest.OmittedReason = "depth"
	}

	// ── Step 3: Render ancestry newest-first ────────────────────────────
	// The ancestry is oldest→newest now. Render newest-first.
	var ancestryContent []string
	var ancestryItems []ManifestItem
	var ancestryPinned []bool
	for i := len(ancestors) - 1; i >= 0; i-- {
		node := ancestors[i]
		text := fmt.Sprintf("--- node %s (%s) ---\n%s", node.ID, node.AuthorID, node.Content)
		ancestryContent = append(ancestryContent, text)
		ancestryItems = append(ancestryItems, ManifestItem{
			ID:         node.ID,
			Kind:       "node",
			Title:      contentPreview(node.Content, 120),
			TokenCount: c.est.Estimate(text),
		})
		ancestryPinned = append(ancestryPinned, isPinned(node.Metadata))
	}

	// ── Step 4: Budget application ──────────────────────────────────────
	// Walk newest→oldest. An item that fits is kept and its tokens deducted.
	// An item that does NOT fit is kept anyway when it is PINNED (the running
	// budget may go negative — that is the documented overage) and the walk
	// continues. The first item that does not fit and is NOT pinned ends the
	// "prefix" phase: that item is omitted, and for every older item the walk
	// keeps ONLY pinned ones and omits the rest. With zero pins the tail
	// contributes nothing, so the result is byte-identical to the pre-GAP-080
	// behaviour (GAP-080 phase 1).
	remainingBudget := req.TokenBudget
	var keptContent []string
	var keptItems []ManifestItem
	totalOmittedByBudget := 0
	pinnedKept := 0
	tailPhase := false

	for i := 0; i < len(ancestryContent); i++ {
		tokens := c.est.Estimate(ancestryContent[i])
		pinned := ancestryPinned[i]

		if tailPhase && !pinned {
			// Older unpinned item after the prefix ended — omit, never count
			// a pinned (kept) item as omitted.
			totalOmittedByBudget++
			continue
		}

		if remainingBudget >= tokens || pinned {
			remainingBudget -= tokens
			item := ancestryItems[i]
			if pinned {
				item.Pinned = true
				pinnedKept++
			}
			keptContent = append(keptContent, ancestryContent[i])
			keptItems = append(keptItems, item)
			continue
		}

		// Unpinned and does not fit: the prefix phase ends here.
		tailPhase = true
		totalOmittedByBudget++
	}

	// SPEC-IMPL-GAP-001 §7 — "Budget smaller than one node": when the walk kept
	// NOTHING at all, the newest node is included regardless and `TokensUsed`
	// may exceed the budget. Step 3 renders the chain newest-first, so the
	// newest node is index 0 — the pre-fix code tested `i == len-1` (the
	// OLDEST item) and therefore only ever fired on a one-element chain, so any
	// longer chain came back with empty Content.
	//
	// The floor is applied only when nothing was kept: when older PINNED nodes
	// were kept (GAP-080 phase 1), Content is already non-empty and the
	// unpinned newest node stays subject to the ordinary budget rule.
	//
	// The forced node is not deducted from remainingBudget — the same
	// no-deduction the pre-fix single-node path had — so the pinned-overage
	// warning cannot be tripped by it (see AC7).
	if len(keptContent) == 0 && len(ancestryContent) > 0 {
		// budget too small for even one node — keep the single newest anyway.
		//
		// The walk above counted that same node as omitted at the iteration
		// that ended the prefix phase (index 0: keptContent is empty only when
		// the NEWEST unpinned item did not fit), so undo its count here. The
		// invariant this restores: a node that ends up in manifest.Ancestry is
		// never counted in manifest.OmittedCount — OmittedCount is "nodes
		// dropped by budget" (types.go), and the frontend renders it as
		// "N items omitted". The guard keeps the total from going negative.
		if totalOmittedByBudget > 0 {
			totalOmittedByBudget--
		}
		keptContent = append(keptContent, ancestryContent[0])
		keptItems = append(keptItems, ancestryItems[0])
		manifest.Warnings = append(manifest.Warnings, "budget too small for single node")
	}

	// Pinned content alone can push the accounted tokens past the budget: the
	// overage is reported, and no pinned node is ever dropped to hide it.
	if remainingBudget < 0 {
		manifest.Warnings = append(manifest.Warnings,
			fmt.Sprintf("pinned nodes exceed the token budget by %d tokens", -remainingBudget))
	}

	if totalOmittedByBudget > 0 {
		manifest.OmittedCount += totalOmittedByBudget
		if manifest.OmittedReason == "" {
			manifest.OmittedReason = "budget"
		}
		manifest.TruncationMarkers = append(manifest.TruncationMarkers,
			fmt.Sprintf("%d messages omitted", totalOmittedByBudget))
	}

	ancestryContent = keptContent
	ancestryItems = keptItems
	manifest.Ancestry = ancestryItems
	manifest.PinnedCount = pinnedKept

	// ── Step 5: References ──────────────────────────────────────────────
	refContent, refItems, refWarnings := c.compileReferences(ctx, req, remainingBudget)
	manifest.References = refItems
	manifest.Warnings = append(manifest.Warnings, refWarnings...)

	// Deduct reference tokens from budget
	for _, item := range refItems {
		remainingBudget -= item.TokenCount
	}

	// ── Step 5b: Multi-reference accounting (SPEC-PL-06 §6.3) ───────────
	// Each selected source is one visible reference in the manifest, and the
	// block's own budget accounting is recorded alongside it.
	if multiRef != nil {
		for _, src := range multiRef.Context.Sources {
			manifest.References = append(manifest.References, ManifestItem{
				ID:         src.NodeID,
				Kind:       "reference",
				Title:      src.SourceLabel,
				TokenCount: src.TokenCount,
				Truncated:  src.Truncated,
			})
		}
		manifest.MultiReference = multiRefManifestEntry(multiRef.Context)

		// Every source fit inside its allocation: report the unused
		// selected-source budget. Context is never padded to fill it.
		if multiRef.AllFit {
			manifest.Warnings = append(manifest.Warnings, fmt.Sprintf(
				"multi-reference: %d sources used %d of %d allocated tokens (%d unused, no padding)",
				len(multiRef.Context.Sources), multiRef.Context.TokensUsed, multiRef.Context.TokenBudget,
				multiRef.Context.TokenBudget-multiRef.Context.TokensUsed))
		}
		for _, label := range multiRef.Escaped {
			manifest.Warnings = append(manifest.Warnings, fmt.Sprintf(
				"multi-reference: source %s contained the block terminator; escaped", label))
		}
	}

	// ── Step 5c: Retrieved tier (GAP-080 phase 4a) ─────────────────────
	// Folds topic-search results into the payload between the references
	// block and the cards block. Optional and disabled unless the compiler
	// was built WithRetrieval — with the tier unwired this step is a no-op
	// and the payload is byte-identical to the pre-4a compiler.
	retContent, retItems, retWarnings := c.compileRetrieved(ctx, req, currentNode, manifest, remainingBudget)
	manifest.Retrieved = retItems
	manifest.Warnings = append(manifest.Warnings, retWarnings...)
	for _, item := range retItems {
		remainingBudget -= item.TokenCount
	}

	// ── Step 6: Cards ───────────────────────────────────────────────────
	cardContent, cardItems, cardWarnings := c.compileCards(ctx, req, currentNode.Content, remainingBudget)
	manifest.Cards = cardItems
	manifest.Warnings = append(manifest.Warnings, cardWarnings...)

	// ── Step 7: Assemble ────────────────────────────────────────────────
	var finalContent string
	finalContent += joinSections(ancestryContent)
	if len(refContent) > 0 {
		finalContent += "\n\n" + joinSections(refContent)
	}
	if len(retContent) > 0 {
		finalContent += "\n\n" + joinSections(retContent)
	}
	if len(cardContent) > 0 {
		finalContent += "\n\n" + joinSections(cardContent)
	}

	// The selected-source block is the highest-priority block of the turn, so
	// it is prepended to whatever the ordinary compilation assembled.
	if multiRef != nil {
		if finalContent == "" {
			finalContent = multiRef.Block
		} else {
			finalContent = multiRef.Block + "\n\n" + finalContent
		}
	}

	// The block is part of Content, so the estimate below already accounts for
	// the selected sources' used tokens (mirrored per source in
	// Manifest.MultiReference.TokensUsed).
	manifest.TokensUsed = c.est.Estimate(finalContent)

	// Defensive: if tokens used > budget, warn (shouldn't happen with budget loop)
	if manifest.TokensUsed > req.TokenBudget {
		manifest.Warnings = append(manifest.Warnings,
			fmt.Sprintf("tokens used (%d) exceeds budget (%d)", manifest.TokensUsed, req.TokenBudget))
	}

	// ── Step 8: Manifest digest (GAP-080 phase 5a) ──────────────────────
	// Every compile path returns through here — ordinary, pinned, truncated,
	// multi-reference and degraded (nil MultiReference) alike — so the digest
	// is sealed once, over the finished manifest, after every content-bearing
	// field has been set. It excludes the volatile RequestID/CompiledAt so two
	// compiles of the same content compare equal, and it excludes itself so
	// the value stays recomputable from the record's JSON.
	manifest.ManifestHash = ManifestDigest(*manifest)

	return &CompiledContext{
		Content:  finalContent,
		Manifest: manifest,
	}, nil
}

// compileReferences resolves topic references and renders them, budget-gated.
// Per spec §8.1, the merged topic set includes BOTH:
//   - scope-membership topics (GetTopicsForNode — topic_member_nodes)
//   - explicitly referenced topics (GetResolvedTopicsForNode — node_resolved_refs)
//
// The two sets are deduplicated by topic ID before cap/budget application.
func (c *compilerImpl) compileReferences(
	ctx context.Context,
	req CompileRequest,
	budget int,
) (sections []string, items []ManifestItem, warnings []string) {
	if !req.ResolveRefs {
		return nil, nil, nil
	}

	topics, err := c.topics.GetTopicsForNode(ctx, req.NodeID)
	if err != nil {
		// Partial failure: add warning, don't fail
		warnings = append(warnings, fmt.Sprintf("reference resolution failed: %v", err))
		return nil, nil, warnings
	}

	// Spec §8.1: also include topics explicitly referenced via node_resolved_refs.
	// These are #topic-slug references the author wrote, resolved at send time.
	resolvedTopics, err := c.topics.GetResolvedTopicsForNode(ctx, req.NodeID)
	if err != nil {
		// Partial failure — proceed with scope-membership topics only.
		warnings = append(warnings, fmt.Sprintf("resolved-reference lookup failed: %v", err))
	} else {
		topics = mergeTopics(topics, resolvedTopics)
	}

	// Deduplicate by topic ID
	seen := make(map[uuid.UUID]bool)
	var deduped []db.Topic
	for _, t := range topics {
		if !seen[t.ID] {
			seen[t.ID] = true
			deduped = append(deduped, t)
		}
	}
	topics = deduped

	// Soft cap: maxRefs, hard cap: 2x maxRefs
	softCap := c.maxRefs
	hardCap := softCap * 2
	if softCap <= 0 {
		softCap = 5
		hardCap = 10
	}

	if len(topics) > softCap {
		warnings = append(warnings,
			fmt.Sprintf("%d references: context becoming unfocused", len(topics)))
	}
	if len(topics) > hardCap {
		omitted := len(topics) - hardCap
		topics = topics[:hardCap]
		warnings = append(warnings,
			fmt.Sprintf("reference limit reached; %d references omitted", omitted))
	}

	remainingBudget := budget
	for _, topic := range topics {
		text := fmt.Sprintf("--- topic boundary: %s ---\n%s\n%.200s", topic.Slug, topic.Title, topic.Description)
		tokens := c.est.Estimate(text)
		if remainingBudget >= tokens {
			remainingBudget -= tokens
			sections = append(sections, text)
			items = append(items, ManifestItem{
				ID:         topic.ID,
				Kind:       "topic",
				Title:      topic.Slug,
				TokenCount: tokens,
			})
		} else {
			// Drop oldest-first — already in order, so skip remaining
			break
		}
	}

	return sections, items, warnings
}

// mergeTopics appends src topics to dst, skipping duplicates by topic ID.
// The caller is expected to have already populated dst; this does a simple
// linear append for each unique src entry. Final dedup is handled by the
// caller's seen-map, so we don't need to check dst here — just avoid adding
// the same topic from src twice.
func mergeTopics(dst, src []db.Topic) []db.Topic {
	srcSeen := make(map[uuid.UUID]bool, len(src))
	for _, t := range src {
		if !srcSeen[t.ID] {
			srcSeen[t.ID] = true
			dst = append(dst, t)
		}
	}
	return dst
}

// compileCards fetches and renders cards, budget-gated.
func (c *compilerImpl) compileCards(
	ctx context.Context,
	req CompileRequest,
	nodeContent string,
	budget int,
) (sections []string, items []ManifestItem, warnings []string) {
	if !req.IncludeCards {
		return nil, nil, nil
	}

	ch := ContextHash(nodeContent)
	cards, err := c.cards.GetByContextHash(ctx, ch)
	if err != nil {
		// Partial failure
		warnings = append(warnings, fmt.Sprintf("card lookup failed: %v", err))
		return nil, nil, warnings
	}

	remainingBudget := budget
	for _, cd := range cards {
		cardType := string(cd.CardType)
		summary := ""
		if len(cd.Data) > 0 {
			summary = string(cd.Data)
			if len(summary) > 200 {
				summary = summary[:200]
			}
		}
		text := fmt.Sprintf("--- card %s ---\n%s\n%s", cardType, cd.AppID, summary)
		tokens := c.est.Estimate(text)
		if remainingBudget >= tokens {
			remainingBudget -= tokens
			sections = append(sections, text)
			items = append(items, ManifestItem{
				ID:         cd.ID,
				Kind:       "card",
				Title:      cardType,
				TokenCount: tokens,
			})
		} else {
			break
		}
	}

	return sections, items, warnings
}

// joinSections joins rendered sections with double newlines.
func joinSections(sections []string) string {
	if len(sections) == 0 {
		return ""
	}
	result := sections[0]
	for i := 1; i < len(sections); i++ {
		result += "\n\n" + sections[i]
	}
	return result
}

// Ensure compilerImpl satisfies Compiler.
var _ Compiler = (*compilerImpl)(nil)
