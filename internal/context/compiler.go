package context

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
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
}

// NewCompiler wires repositories + estimator into a Compiler.
func NewCompiler(
	nodes NodeReader,
	topics TopicReader,
	cards CardReader,
	est TokenEstimator,
	maxRefs int,
) Compiler {
	return &compilerImpl{
		nodes:   nodes,
		topics:  topics,
		cards:   cards,
		est:     est,
		maxRefs: maxRefs,
	}
}

// Compile implements Compiler.
func (c *compilerImpl) Compile(ctx context.Context, req CompileRequest) (*CompiledContext, error) {
	// Validate budget
	if req.TokenBudget < 1 {
		return nil, ErrInvalidBudget
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
	}

	// ── Step 4: Budget application ──────────────────────────────────────
	// Remove oldest-first until content fits budget
	remainingBudget := req.TokenBudget
	var keptContent []string
	var keptItems []ManifestItem
	totalOmittedByBudget := 0

	for i := 0; i < len(ancestryContent); i++ {
		tokens := c.est.Estimate(ancestryContent[i])
		if remainingBudget >= tokens {
			remainingBudget -= tokens
			keptContent = append(keptContent, ancestryContent[i])
			keptItems = append(keptItems, ancestryItems[i])
		} else {
			// Drop this (oldest) and all remaining older ones
			totalOmittedByBudget += len(ancestryContent) - i
			// still include at least the NEWEST node (the last one)
			if len(keptContent) == 0 && i == len(ancestryContent)-1 {
				// budget too small for even one node — keep the single newest anyway
				keptContent = append(keptContent, ancestryContent[i])
				keptItems = append(keptItems, ancestryItems[i])
				manifest.Warnings = append(manifest.Warnings, "budget too small for single node")
			}
			break
		}
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

	// ── Step 5: References ──────────────────────────────────────────────
	refContent, refItems, refWarnings := c.compileReferences(ctx, req, remainingBudget)
	manifest.References = refItems
	manifest.Warnings = append(manifest.Warnings, refWarnings...)

	// Deduct reference tokens from budget
	for _, item := range refItems {
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
	if len(cardContent) > 0 {
		finalContent += "\n\n" + joinSections(cardContent)
	}

	manifest.TokensUsed = c.est.Estimate(finalContent)

	// Defensive: if tokens used > budget, warn (shouldn't happen with budget loop)
	if manifest.TokensUsed > req.TokenBudget {
		manifest.Warnings = append(manifest.Warnings,
			fmt.Sprintf("tokens used (%d) exceeds budget (%d)", manifest.TokensUsed, req.TokenBudget))
	}

	return &CompiledContext{
		Content:  finalContent,
		Manifest: manifest,
	}, nil
}

// compileReferences resolves topic references and renders them, budget-gated.
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
