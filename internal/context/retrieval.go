package context

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// compileRetrieved is Step 5c (GAP-080 phase 4a): fold topic-search results
// into the payload between the references block and the cards block.
//
// The tier is OPTIONAL: when it is unwired (retrieval == nil or
// retrievalMax <= 0) this returns nil everything with NO side effect — no
// search, no warning, no manifest field — so `Content`, the `Manifest` JSON
// and `ManifestHash` are byte-identical to the pre-4a compiler (parity
// guarantee, AC1).
//
// The step never fails the compile: a reader error appends one
// `retrieval failed: ...` warning and nothing else (same partial-failure
// contract as references and cards).
//
// It records manifest.RetrievalBudget (the tier's allocation) once the
// search was ISSUED — an empty result still counts as a run search — and
// mutates nothing else on the manifest. Kept tokens are DEDUCTED from the
// shared remaining budget by the caller (Compile, Step 5c block), so the
// later cards step sees the reduced budget.
func (c *compilerImpl) compileRetrieved(
	ctx context.Context,
	req CompileRequest,
	currentNode *db.Node,
	manifest *Manifest,
	remainingBudget int,
) (sections []string, items []ManifestItem, warnings []string) {
	// Tier disabled (or wired with a no-op option): no side effect at all.
	if c.retrieval == nil || c.retrievalMax <= 0 {
		return nil, nil, nil
	}

	// Query = the current node's content, whitespace-collapsed, capped at
	// 300 runes. An empty collapsed query means there is nothing to search
	// for — skip silently (no search issued).
	query := strings.Join(strings.Fields(currentNode.Content), " ")
	if runes := []rune(query); len(runes) > 300 {
		query = string(runes[:300])
	}
	if query == "" {
		return nil, nil, nil
	}

	// Tier allocation: floor(TokenBudget * RetrievalSharePercent / 100),
	// clamped by whatever budget is actually left. No budget → skip
	// silently, WITHOUT a warning: the audit already shows the budget, and
	// a warning on every pinned-overage compile would be noise.
	tierBudget := req.TokenBudget * RetrievalSharePercent / 100
	effective := tierBudget
	if remainingBudget < effective {
		effective = remainingBudget
	}
	if effective <= 0 {
		return nil, nil, nil
	}

	retrieved, err := c.retrieval.Retrieve(ctx, req.TreeID, query, c.retrievalMax)
	// The search was issued (result nil, empty or not) — record the
	// allocation on the manifest regardless of the outcome below.
	manifest.RetrievalBudget = tierBudget
	if err != nil {
		// Degrade, never fail: one warning, no retrieved items.
		warnings = append(warnings, fmt.Sprintf("retrieval failed: %v", err))
		return nil, nil, warnings
	}

	// Dedupe: a reference WINS over a retrieval hit (the topic is already
	// in the payload as an explicit reference), then first occurrence wins
	// within the retrieved set.
	seen := make(map[uuid.UUID]bool, len(retrieved))
	for _, ref := range manifest.References {
		seen[ref.ID] = true
	}
	var candidates []RetrievalItem
	for _, item := range retrieved {
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		candidates = append(candidates, item)
	}

	// Deterministic order — never trust the reader's ordering: relevance
	// DESC, then slug ASC (byte compare), then id ASC.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Relevance != candidates[j].Relevance {
			return candidates[i].Relevance > candidates[j].Relevance
		}
		if candidates[i].Slug != candidates[j].Slug {
			return candidates[i].Slug < candidates[j].Slug
		}
		return candidates[i].ID.String() < candidates[j].ID.String()
	})

	// Greedy fill in that order: an item that fits is kept; an item that
	// does not fit is omitted and the walk CONTINUES (a smaller later
	// candidate may still fit).
	local := effective
	omitted := 0
	for _, item := range candidates {
		text := fmt.Sprintf("--- retrieved topic %s ---\n%s\n%s", item.Slug, item.Title, contentPreview(item.Content, 200))
		tokens := c.est.Estimate(text)
		if tokens > local {
			omitted++
			continue
		}
		local -= tokens
		sections = append(sections, text)
		items = append(items, ManifestItem{
			ID:         item.ID,
			Kind:       "retrieved_topic",
			Title:      item.Slug,
			TokenCount: tokens,
			Relevance:  item.Relevance,
		})
	}
	if omitted > 0 {
		warnings = append(warnings, fmt.Sprintf("%d retrieved items omitted (budget)", omitted))
	}

	return sections, items, warnings
}
