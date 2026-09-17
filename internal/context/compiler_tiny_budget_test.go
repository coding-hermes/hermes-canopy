package context

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// ── DF-HERMES-CANOPY-19 ──────────────────────────────────────────────────────
//
// SPEC-IMPL-GAP-001 §7: "Budget smaller than one node: include the single
// newest node regardless, TokensUsed may exceed budget — append warning
// "budget too small for single node". Never return empty Content when the node
// exists."
//
// Step 3 renders the ancestry chain NEWEST-FIRST, so index 0 of the slice the
// budget walk (step 4) iterates is the NEWEST node. The pre-fix escape hatch
// tested `i == len(ancestryContent)-1` — the OLDEST item — so it only ever
// fired on a one-element chain; on any longer chain the walk dropped
// everything and Content came back empty (live: tick 484, 4-node chain,
// budget=30 → {"content":"","manifest":{"ancestry":[]}}).

const (
	tinyRootID   = "11111111-1111-4111-8111-111111111101"
	tinyMidID    = "11111111-1111-4111-8111-111111111102"
	tinyMid2ID   = "11111111-1111-4111-8111-111111111103"
	tinyNewestID = "11111111-1111-4111-8111-11111111110a"
	tinyAuthorID = "22222222-2222-4222-8222-222222222201"
)

// The rendered sections are written out literally (i.e. not built from the
// compiler's own format string) so a change to the rendering format cannot
// silently move the expectations with it. tinyBudgetAssertRendering below
// pins the literals against the production format.
const (
	tinyNewestSection = "--- node 11111111-1111-4111-8111-11111111110a (22222222-2222-4222-8222-222222222201) ---\nnewest message content for the tiny-budget regression"
	tinyMid2Section   = "--- node 11111111-1111-4111-8111-111111111103 (22222222-2222-4222-8222-222222222201) ---\nthird message content for the tiny-budget regression"
	tinyMidSection    = "--- node 11111111-1111-4111-8111-111111111102 (22222222-2222-4222-8222-222222222201) ---\nsecond message content for the tiny-budget regression"
	tinyRootSection   = "--- node 11111111-1111-4111-8111-111111111101 (22222222-2222-4222-8222-222222222201) ---\nroot message content for the tiny-budget regression"
)

// tinyBudgetHasWarning reports whether any warning contains substr.
func tinyBudgetHasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// renderNodeSection mirrors compiler step 3's rendering.
func renderNodeSection(n db.Node) string {
	return fmt.Sprintf("--- node %s (%s) ---\n%s", n.ID, n.AuthorID, n.Content)
}

// tinyBudgetChain builds the fixed 4-node chain (root → mid → mid2 → newest)
// used by both the tiny-budget regression and its normal-budget parity
// control, and returns the stub reader, the requested (newest) node ID and the
// rendered sections in the order the compiler sees them (index 0 = newest).
func tinyBudgetChain(t *testing.T) (*stubNodeReader, uuid.UUID, []string) {
	t.Helper()

	author := uuid.MustParse(tinyAuthorID)
	ids := []string{tinyRootID, tinyMidID, tinyMid2ID, tinyNewestID}
	contents := []string{
		"root message content for the tiny-budget regression",
		"second message content for the tiny-budget regression",
		"third message content for the tiny-budget regression",
		"newest message content for the tiny-budget regression",
	}

	nodes := make(map[uuid.UUID]*db.Node, len(ids))
	for i, raw := range ids {
		id := uuid.MustParse(raw)
		nodes[id] = makeNode(id, author, contents[i])
	}

	// GetAncestors contract: [self, parent, ..., root] — newest first.
	ancestors := make([]db.Node, 0, len(ids))
	rendered := make([]string, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		node := *nodes[uuid.MustParse(ids[i])]
		ancestors = append(ancestors, node)
		rendered = append(rendered, renderNodeSection(node))
	}

	nodes2 := &stubNodeReader{
		nodes: nodes,
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return ancestors, nil
		},
	}
	return nodes2, uuid.MustParse(tinyNewestID), rendered
}

// tinyBudgetAssertRendering pins the literal section constants against the
// production rendering so the expectations cannot go vacuous.
func tinyBudgetAssertRendering(t *testing.T, rendered []string) {
	t.Helper()
	want := []string{tinyNewestSection, tinyMid2Section, tinyMidSection, tinyRootSection}
	if len(rendered) != len(want) {
		t.Fatalf("fixture rendering: got %d sections, want %d", len(rendered), len(want))
	}
	for i := range want {
		if rendered[i] != want[i] {
			t.Fatalf("sections[%d] literal is stale:\n got %q\nwant %q", i, rendered[i], want[i])
		}
	}
}

// smallChain builds a chain of n nodes (root → ... → newest) and returns the
// stub reader, the requested (newest) node ID, and every rendered section in
// the order the compiler sees them (index 0 = newest). Node contents are
// unique per position so Content assertions can name the newest node.
func smallChain(n int) (*stubNodeReader, uuid.UUID, []string) {
	author := uuid.New()
	ids := make([]uuid.UUID, n)
	nodes := make(map[uuid.UUID]*db.Node, n)
	for i := 0; i < n; i++ {
		ids[i] = uuid.New()
		nodes[ids[i]] = makeNode(ids[i], author, fmt.Sprintf("chain node %d content", i))
	}

	ancestors := make([]db.Node, 0, n)
	rendered := make([]string, 0, n)
	for i := n - 1; i >= 0; i-- {
		node := *nodes[ids[i]]
		ancestors = append(ancestors, node)
		rendered = append(rendered, renderNodeSection(node))
	}

	return &stubNodeReader{
		nodes: nodes,
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return ancestors, nil
		},
	}, ids[n-1], rendered
}

// AC2/AC3/AC4: a 4-node chain (the live probe's shape) with a budget smaller
// than ONE rendered node keeps the newest node and its content.
func TestCompile_MultiNodeChain_TinyBudget_KeepsNewest(t *testing.T) {
	nodes, newestID, rendered := tinyBudgetChain(t)
	tinyBudgetAssertRendering(t, rendered)

	est := NewTokenEstimator()
	const budget = 30 // the budget the tick-484 live probe used

	// Premise: the budget really is smaller than one rendered node — assert it
	// instead of trusting the comment.
	single := est.Estimate(tinyNewestSection)
	if single <= budget {
		t.Fatalf("premise: newest rendered node is %d tokens, must exceed the %d-token budget", single, budget)
	}

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, est, 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      newestID,
		TokenBudget: budget,
		ResolveRefs: false,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Manifest == nil {
		t.Fatal("expected a manifest, got nil")
	}

	// AC2: non-empty Content carrying the requested (newest) node's text.
	if result.Content == "" {
		t.Fatalf("expected non-empty Content for a multi-node chain with a %d-token budget, got empty", budget)
	}
	if !strings.Contains(result.Content, "newest message content for the tiny-budget regression") {
		t.Errorf("Content does not carry the requested node's text, got: %q", result.Content)
	}
	if result.Content != tinyNewestSection {
		t.Errorf("Content is not exactly the newest node's section:\n got %q\nwant %q", result.Content, tinyNewestSection)
	}

	// AC3: exactly one manifest ancestry item — the requested node.
	if len(result.Manifest.Ancestry) != 1 {
		t.Fatalf("expected exactly 1 ancestry item, got %d (%v)", len(result.Manifest.Ancestry), result.Manifest.Ancestry)
	}
	if result.Manifest.Ancestry[0].ID != newestID {
		t.Errorf("ancestry item is not the requested node: got %s, want %s", result.Manifest.Ancestry[0].ID, newestID)
	}
	if got := result.Manifest.Ancestry[0].TokenCount; got != single {
		t.Errorf("ancestry TokenCount: got %d, want %d", got, single)
	}

	// OmittedCount is 4 = the 3 older nodes genuinely dropped + the forced-kept
	// newest node, which the pre-existing accounting counts as omitted at the
	// point the prefix phase ends (the same single-node behaviour
	// TestCompile_BudgetTooSmall has always had: 1 kept, OmittedCount 1). That
	// accounting is explicitly out of scope here (DF-HERMES-CANOPY-19 AC7).
	if result.Manifest.OmittedCount != 4 {
		t.Errorf("expected OmittedCount=4 (3 dropped + 1 forced-kept, pre-existing accounting), got %d", result.Manifest.OmittedCount)
	}
	if result.Manifest.OmittedReason != "budget" {
		t.Errorf("expected OmittedReason=budget, got %q", result.Manifest.OmittedReason)
	}
	if !tinyBudgetHasWarning(result.Manifest.TruncationMarkers, "messages omitted") {
		t.Errorf("expected a truncation marker, got %v", result.Manifest.TruncationMarkers)
	}

	// AC4: the §7 warning is present; the pinned-overage warning is NOT (this
	// fixture has no pins — a bogus overage warning would be a regression).
	if !tinyBudgetHasWarning(result.Manifest.Warnings, "budget too small for single node") {
		t.Errorf("expected warning %q, got %v", "budget too small for single node", result.Manifest.Warnings)
	}
	if tinyBudgetHasWarning(result.Manifest.Warnings, "pinned nodes exceed the token budget") {
		t.Errorf("no node is pinned in this fixture, but the pinned-overage warning fired: %v", result.Manifest.Warnings)
	}
	if result.Manifest.PinnedCount != 0 {
		t.Errorf("expected PinnedCount=0, got %d", result.Manifest.PinnedCount)
	}
	if result.Manifest.TokensUsed != est.Estimate(result.Content) {
		t.Errorf("TokensUsed %d does not match the estimate of Content (%d)",
			result.Manifest.TokensUsed, est.Estimate(result.Content))
	}

	t.Logf("tiny budget: Content len=%d tokensUsed=%d warnings=%v omitted=%d",
		len(result.Content), result.Manifest.TokensUsed, result.Manifest.Warnings, result.Manifest.OmittedCount)
}

// AC2/AC3/AC4 for every chain length, including the one-element chain whose
// behaviour must not move.
func TestCompile_MultiNodeChain_TinyBudget_EveryChainLength(t *testing.T) {
	const budget = 10 // smaller than any rendered node in the fixture
	est := NewTokenEstimator()

	for _, n := range []int{1, 2, 3, 4, 5} {
		t.Run(fmt.Sprintf("chain_%d", n), func(t *testing.T) {
			nodes, newestID, rendered := smallChain(n)

			if got := est.Estimate(rendered[0]); got <= budget {
				t.Fatalf("premise: newest rendered node is %d tokens, must exceed the %d-token budget", got, budget)
			}

			c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, est, 5)
			result, err := c.Compile(context.Background(), CompileRequest{
				NodeID:      newestID,
				TokenBudget: budget,
				ResolveRefs: false,
			})
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}

			if result.Content == "" {
				t.Fatalf("empty Content on a %d-node chain with a %d-token budget", n, budget)
			}
			if !strings.Contains(result.Content, fmt.Sprintf("chain node %d content", n-1)) {
				t.Errorf("Content does not carry the newest node's text, got %q", result.Content)
			}
			if result.Content != rendered[0] {
				t.Errorf("Content is not exactly the newest section:\n got %q\nwant %q", result.Content, rendered[0])
			}
			if len(result.Manifest.Ancestry) != 1 {
				t.Fatalf("expected exactly 1 ancestry item, got %d", len(result.Manifest.Ancestry))
			}
			if result.Manifest.Ancestry[0].ID != newestID {
				t.Errorf("ancestry item: got %s, want %s", result.Manifest.Ancestry[0].ID, newestID)
			}
			if !tinyBudgetHasWarning(result.Manifest.Warnings, "budget too small for single node") {
				t.Errorf("missing %q warning: %v", "budget too small for single node", result.Manifest.Warnings)
			}
			if tinyBudgetHasWarning(result.Manifest.Warnings, "pinned nodes exceed the token budget") {
				t.Errorf("pinned-overage warning fired with no pins: %v", result.Manifest.Warnings)
			}
			// Every element is counted by the walk: the forced-kept newest plus
			// the n-1 older nodes (pre-existing accounting, AC7 scope).
			if result.Manifest.OmittedCount != n {
				t.Errorf("expected OmittedCount=%d, got %d", n, result.Manifest.OmittedCount)
			}
		})
	}
}

// AC6: the SAME 4-node fixture with a budget that fits must render exactly what
// it rendered before the §7 fix — all four nodes newest-first, nothing omitted,
// no warnings.
func TestCompile_MultiNodeChain_NormalBudget_ParityControl(t *testing.T) {
	nodes, newestID, rendered := tinyBudgetChain(t)
	tinyBudgetAssertRendering(t, rendered)

	est := NewTokenEstimator()
	const budget = 10000

	wantContent := strings.Join([]string{
		tinyNewestSection, tinyMid2Section, tinyMidSection, tinyRootSection,
	}, "\n\n")

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, est, 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      newestID,
		TokenBudget: budget,
		ResolveRefs: false,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	sum := sha256.Sum256([]byte(result.Content))
	t.Logf("PARITY content sha256=%x len=%d tokensUsed=%d omitted=%d warnings=%v ancestry=%d",
		sum, len(result.Content), result.Manifest.TokensUsed, result.Manifest.OmittedCount,
		result.Manifest.Warnings, len(result.Manifest.Ancestry))

	if result.Content != wantContent {
		t.Errorf("normal-budget Content changed:\n got %q\nwant %q", result.Content, wantContent)
	}
	if got := est.Estimate(wantContent); result.Manifest.TokensUsed != got {
		t.Errorf("TokensUsed: got %d, want %d", result.Manifest.TokensUsed, got)
	}

	wantIDs := []uuid.UUID{
		uuid.MustParse(tinyNewestID),
		uuid.MustParse(tinyMid2ID),
		uuid.MustParse(tinyMidID),
		uuid.MustParse(tinyRootID),
	}
	if len(result.Manifest.Ancestry) != len(wantIDs) {
		t.Fatalf("expected %d ancestry items, got %d", len(wantIDs), len(result.Manifest.Ancestry))
	}
	for i, want := range wantIDs {
		if result.Manifest.Ancestry[i].ID != want {
			t.Errorf("ancestry[%d]: got %s, want %s", i, result.Manifest.Ancestry[i].ID, want)
		}
	}
	if result.Manifest.OmittedCount != 0 {
		t.Errorf("expected OmittedCount=0 on a fitting budget, got %d", result.Manifest.OmittedCount)
	}
	if result.Manifest.OmittedReason != "" {
		t.Errorf("expected empty OmittedReason, got %q", result.Manifest.OmittedReason)
	}
	if len(result.Manifest.TruncationMarkers) != 0 {
		t.Errorf("expected no truncation markers, got %v", result.Manifest.TruncationMarkers)
	}
	if len(result.Manifest.Warnings) != 0 {
		t.Errorf("expected no warnings on a fitting budget, got %v", result.Manifest.Warnings)
	}
	if result.Manifest.PinnedCount != 0 {
		t.Errorf("expected PinnedCount=0, got %d", result.Manifest.PinnedCount)
	}
}
