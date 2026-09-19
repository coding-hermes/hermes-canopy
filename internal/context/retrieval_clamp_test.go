// Retrieved-tier remaining-budget clamp coverage (GAP-080 phase 4a follow-up).
//
// The tier allocation is floor(TokenBudget * RetrievalSharePercent / 100), but
// it must ALSO be clamped by the budget still left after the ancestry and
// reference steps. The dispatched work pinned and implemented that clamp but
// no test distinguished it: deleting the clamp left every test green (found by
// the foreman's independent mutation pass). This test drives the clamp as the
// BINDING constraint — the tier allocation is larger than the remaining budget —
// so removing the clamp fails here:
//
//   - a candidate that fits the tier allocation but not the remaining budget is
//     omitted, and
//   - a smaller candidate inside the remaining budget is still kept, with the
//     tier's total spend never exceeding what was actually left.
package context

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRetrieved_RemainingBudgetClampIsLoadBearing(t *testing.T) {
	content := strings.Repeat("c", 16000)
	req, nodes := retrievalFixture(content)

	// Baseline: tier off, huge budget → the ancestry's token spend.
	plain := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	base, err := plain.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("base compile: %v", err)
	}
	ancestryTokens := base.Manifest.TokensUsed

	// A budget that leaves only `remaining` tokens after the ancestry and a
	// tier allocation comfortably larger than it (the fixture guard below
	// refuses to run a scenario where the clamp could not bind).
	remaining := 400
	req.TokenBudget = ancestryTokens + remaining
	tierAlloc := req.TokenBudget * RetrievalSharePercent / 100
	if tierAlloc <= remaining+40 {
		t.Fatalf("fixture invalid: tier allocation %d must exceed the remaining budget %d by a usable margin (ancestry %d)",
			tierAlloc, remaining, ancestryTokens)
	}

	// Long slug: slugs are never preview-capped (unlike content), so the
	// rendered block is ~400 tokens — it fits the tier allocation but cannot
	// fit the remaining budget.
	bigSlug := strings.Repeat("b", 1600)
	reader := &stubRetrievalReader{items: []RetrievalItem{
		retrievalItem(uuid.New(), bigSlug, "Big", "big", 0.9),
		retrievalItem(uuid.New(), "tiny-topic", "Tiny", "t", 0.1),
	}}
	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5,
		WithRetrieval(reader, 5))
	res, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("wired compile: %v", err)
	}
	if res.Manifest.RetrievalBudget != tierAlloc {
		t.Errorf("RetrievalBudget = %d, want the tier allocation %d", res.Manifest.RetrievalBudget, tierAlloc)
	}
	kept := map[string]int{}
	for _, item := range res.Manifest.Retrieved {
		kept[item.Title] = item.TokenCount
	}
	if n, ok := kept[bigSlug]; ok {
		t.Errorf("Big kept (%d tokens) although the tier allocation %d exceeds the %d tokens actually remaining — the remaining-budget clamp is not binding",
			n, tierAlloc, remaining)
	}
	if _, ok := kept["tiny-topic"]; !ok {
		t.Errorf("Tiny omitted (kept=%v) — a candidate inside the remaining budget must still fit", kept)
	}
	sum := 0
	for _, n := range kept {
		sum += n
	}
	if sum > remaining {
		t.Errorf("retrieved spend %d exceeds the %d tokens remaining after ancestry", sum, remaining)
	}
}
