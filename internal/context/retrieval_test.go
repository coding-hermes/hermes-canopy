package context

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// --- Retrieved-tier stub ---------------------------------------------------

// stubRetrievalReader is a fake RetrievalReader that records calls and can
// be told to fail.
type stubRetrievalReader struct {
	calls     int
	lastTree  uuid.UUID
	lastQuery string
	lastLimit int
	items     []RetrievalItem
	err       error
}

func (s *stubRetrievalReader) Retrieve(ctx context.Context, treeID uuid.UUID, query string, limit int) ([]RetrievalItem, error) {
	s.calls++
	s.lastTree = treeID
	s.lastQuery = query
	s.lastLimit = limit
	if s.err != nil {
		return nil, s.err
	}
	return s.items, nil
}

// retrievalFixture assembles a compile fixture: one current node with the
// given content and an ancestry stub returning just that node. A FIXED
// author id keeps the rendered ancestry byte-stable across the two
// compiles of a parity test.
func retrievalFixture(content string) (CompileRequest, *stubNodeReader) {
	nodeID := uuid.New()
	treeID := uuid.New()
	author := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nodeID: makeNode(nodeID, author, content),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{*makeNode(nodeID, author, content)}, nil
		},
	}
	return CompileRequest{
		TreeID:      treeID,
		NodeID:      nodeID,
		TokenBudget: 10000,
		ResolveRefs: true,
	}, nodes
}

func retrievalItem(id uuid.UUID, slug, title, content string, relevance float64) RetrievalItem {
	return RetrievalItem{ID: id, Slug: slug, Title: title, Content: content, Relevance: relevance}
}

func manifestJSON(t *testing.T, m *Manifest) string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return string(raw)
}

// canonicalForCompare zeroes the per-compile volatile fields (RequestID,
// CompiledAt, ManifestHash) — the same exclusions ManifestDigest documents —
// so two compiles of the same content can be compared byte-for-byte.
func canonicalForCompare(m *Manifest) *Manifest {
	canonical := *m
	canonical.RequestID = ""
	canonical.CompiledAt = time.Time{}
	canonical.ManifestHash = ""
	return &canonical
}

// --- Parity (AC1) -----------------------------------------------------------

// TestRetrieved_UnwiredCompilerIsByteIdentical pins the parity guarantee:
// with the tier unwired, Content, Manifest JSON and ManifestHash are
// byte-identical to the same fixture compiled without any option at all.
func TestRetrieved_UnwiredCompilerIsByteIdentical(t *testing.T) {
	req, nodes := retrievalFixture("hello world of topic search")

	plain := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	// The tier is "unwired" but the option machinery is exercised: build
	// with no-op options to prove the path is inert too.
	unwired := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5,
		WithRetrieval(nil, 5), WithRetrieval(&stubRetrievalReader{}, 0))

	a, err := plain.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("plain compile: %v", err)
	}
	b, err := unwired.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("unwired compile: %v", err)
	}

	if a.Content != b.Content {
		t.Errorf("Content diverged with the tier unwired:\nplain:   %q\nunwired: %q", a.Content, b.Content)
	}
	if a.Manifest.ManifestHash != b.Manifest.ManifestHash {
		t.Errorf("ManifestHash diverged with the tier unwired: %q vs %q",
			a.Manifest.ManifestHash, b.Manifest.ManifestHash)
	}
	// RequestID and CompiledAt are fresh per compile by design (they are
	// the digest's documented volatile exclusions), so byte-identity of the
	// manifest is asserted on the canonical form: same normalization
	// ManifestDigest performs.
	aj := manifestJSON(t, canonicalForCompare(a.Manifest))
	bj := manifestJSON(t, canonicalForCompare(b.Manifest))
	if aj != bj {
		t.Errorf("canonical Manifest JSON diverged with the tier unwired:\n%s\n%s", aj, bj)
	}
	if len(b.Manifest.Retrieved) != 0 {
		t.Errorf("unwired compile has %d retrieved items, want 0", len(b.Manifest.Retrieved))
	}
	if b.Manifest.RetrievalBudget != 0 {
		t.Errorf("unwired compile has RetrievalBudget %d, want 0", b.Manifest.RetrievalBudget)
	}
	for _, w := range b.Manifest.Warnings {
		if strings.Contains(w, "retrieval") {
			t.Errorf("unwired compile emitted retrieval warning %q", w)
		}
	}
}

// --- Wired tier (AC2) -------------------------------------------------------

// TestRetrieved_WiredTierPopulatesManifestAndContent: fitting items are
// folded in with kind retrieved_topic, preserved relevance, the specified
// block text, and the tier's manifest allocation is recorded.
func TestRetrieved_WiredTierPopulatesManifestAndContent(t *testing.T) {
	req, nodes := retrievalFixture("tell me about deployment pipelines")

	topicID := uuid.New()
	reader := &stubRetrievalReader{items: []RetrievalItem{
		retrievalItem(topicID, "deploy-pipelines", "Deployment pipelines", "Continuous delivery notes for the fleet", 0.91),
	}}
	// One explicit reference too, so the tier's position relative to the
	// references block is observable in Content.
	refTopic := makeTopic(uuid.New(), "ci-flake", "CI flake", "flake hunting")
	topics := &stubTopicReader{topics: map[uuid.UUID][]db.Topic{req.NodeID: {refTopic}}}

	c := NewCompiler(nodes, topics, &stubCardReader{}, NewTokenEstimator(), 5,
		WithRetrieval(reader, 5))
	result, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	if reader.calls != 1 {
		t.Fatalf("Retrieve called %d times, want 1", reader.calls)
	}
	if len(result.Manifest.Retrieved) != 1 {
		t.Fatalf("manifest.Retrieved has %d items, want 1", len(result.Manifest.Retrieved))
	}
	item := result.Manifest.Retrieved[0]
	if item.ID != topicID {
		t.Errorf("retrieved item ID = %v, want %v", item.ID, topicID)
	}
	if item.Kind != "retrieved_topic" {
		t.Errorf("retrieved item Kind = %q, want retrieved_topic", item.Kind)
	}
	if item.Title != "deploy-pipelines" {
		t.Errorf("retrieved item Title = %q, want the slug", item.Title)
	}
	if item.Relevance != 0.91 {
		t.Errorf("retrieved item Relevance = %v, want 0.91", item.Relevance)
	}
	if item.TokenCount <= 0 {
		t.Errorf("retrieved item TokenCount = %d, want > 0", item.TokenCount)
	}

	wantBlock := fmt.Sprintf("--- retrieved topic deploy-pipelines ---\n%s\n%s",
		"Deployment pipelines", contentPreview("Continuous delivery notes for the fleet", 200))
	if !strings.Contains(result.Content, wantBlock) {
		t.Errorf("Content missing the rendered retrieved block %q; content:\n%s", wantBlock, result.Content)
	}

	// RetrievalBudget recorded: floor(10000 * 12 / 100) = 1200.
	if result.Manifest.RetrievalBudget != 1200 {
		t.Errorf("manifest.RetrievalBudget = %d, want 1200", result.Manifest.RetrievalBudget)
	}

	// Position: the retrieved block must come AFTER the reference boundary.
	refIdx := strings.Index(result.Content, "--- topic boundary: ci-flake ---")
	retIdx := strings.Index(result.Content, wantBlock)
	if refIdx < 0 {
		t.Fatalf("Content missing the reference block; content:\n%s", result.Content)
	}
	if retIdx < refIdx {
		t.Errorf("retrieved block (idx %d) before the references block (idx %d)", retIdx, refIdx)
	}
}

// TestRetrieved_PositionBetweenReferencesAndCards proves the tier sits
// between the references block and the cards block in Content.
func TestRetrieved_PositionBetweenReferencesAndCards(t *testing.T) {
	content := "query about topic search"
	req, nodes := retrievalFixture(content)

	reader := &stubRetrievalReader{items: []RetrievalItem{
		retrievalItem(uuid.New(), "alpha-topic", "Alpha", "alpha snippet", 0.5),
	}}
	refTopic := makeTopic(uuid.New(), "beta-ref", "Beta ref", "beta description")
	topics := &stubTopicReader{topics: map[uuid.UUID][]db.Topic{req.NodeID: {refTopic}}}

	// One card keyed by the context hash of the node content.
	cardID := uuid.New()
	cards := &stubCardReader{cards: map[string][]card.Card{
		ContextHash(content): {makeCard(cardID, card.CardTypeCompact, "app-1")},
	}}

	c := NewCompiler(nodes, topics, cards, NewTokenEstimator(), 5,
		WithRetrieval(reader, 5))
	req.IncludeCards = true
	result, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	refIdx := strings.Index(result.Content, "--- topic boundary: beta-ref ---")
	retIdx := strings.Index(result.Content, "--- retrieved topic alpha-topic ---")
	cardIdx := strings.Index(result.Content, fmt.Sprintf("--- card %s ---", card.CardTypeCompact))
	if refIdx < 0 || retIdx < 0 || cardIdx < 0 {
		t.Fatalf("missing blocks: ref=%d ret=%d card=%d; content:\n%s", refIdx, retIdx, cardIdx, result.Content)
	}
	if retIdx < refIdx {
		t.Errorf("retrieved block (idx %d) before references (idx %d)", retIdx, refIdx)
	}
	if cardIdx < retIdx {
		t.Errorf("cards block (idx %d) before the retrieved block (idx %d) — payload order must be ancestry → references → retrieved → cards", cardIdx, retIdx)
	}
}

// --- Ordering (AC3) ---------------------------------------------------------

// TestRetrieved_DeterministicOrdering: a scrambled input list comes back
// sorted by relevance DESC, then slug ASC, then ID string ASC.
func TestRetrieved_DeterministicOrdering(t *testing.T) {
	req, nodes := retrievalFixture("ordering fixture")

	idA, idB := uuid.New(), uuid.New()
	// Make the slug-tie ID order explicit instead of relying on uuid.New's
	// randomness: swap so idLow is whichever ID string sorts first.
	idLow, idHigh := idA, idB
	if idLow.String() > idHigh.String() {
		idLow, idHigh = idHigh, idLow
	}
	reader := &stubRetrievalReader{items: []RetrievalItem{
		retrievalItem(uuid.New(), "zeta", "Zeta", "z", 0.1),   // lowest relevance
		retrievalItem(idHigh, "same", "Same high", "h", 0.5),  // slug tie, higher ID
		retrievalItem(idLow, "same", "Same low", "l", 0.5),    // slug tie, lower ID
		retrievalItem(uuid.New(), "alpha", "Alpha", "a", 0.9), // highest relevance
		retrievalItem(uuid.New(), "mid", "Mid", "m", 0.5),     // slug "mid" > "same"
	}}

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5,
		WithRetrieval(reader, 10))
	result, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	wantSlugs := []string{"alpha", "mid", "same", "same", "zeta"}
	if len(result.Manifest.Retrieved) != len(wantSlugs) {
		t.Fatalf("retrieved %d items, want %d", len(result.Manifest.Retrieved), len(wantSlugs))
	}
	for i, it := range result.Manifest.Retrieved {
		if it.Title != wantSlugs[i] {
			t.Fatalf("retrieved order = %v, want %v (relevance DESC, then slug ASC)",
				slugsOf(result.Manifest.Retrieved), wantSlugs)
		}
	}
	// The "same"/"same" slug+relevance tie: the SMALLER ID string must come
	// first.
	lowIdx, highIdx := -1, -1
	for i, it := range result.Manifest.Retrieved {
		if it.ID == idLow {
			lowIdx = i
		}
		if it.ID == idHigh {
			highIdx = i
		}
	}
	if lowIdx < 0 || highIdx < 0 {
		t.Fatalf("slug-tie items missing from the retrieved set")
	}
	if lowIdx > highIdx {
		t.Errorf("slug+relevance tie broken wrongly: lower ID string at %d, higher at %d — ID ASC must decide", lowIdx, highIdx)
	}
}

func slugsOf(items []ManifestItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}

// TestRetrieved_ReferenceWinsOverRetrievalHit: a hit whose ID is already a
// reference is dropped from the retrieved tier.
func TestRetrieved_ReferenceWinsOverRetrievalHit(t *testing.T) {
	req, nodes := retrievalFixture("dedupe me")

	sharedID := uuid.New()
	refTopic := makeTopic(sharedID, "shared-topic", "Shared", "already a reference")
	topics := &stubTopicReader{topics: map[uuid.UUID][]db.Topic{req.NodeID: {refTopic}}}
	reader := &stubRetrievalReader{items: []RetrievalItem{
		retrievalItem(sharedID, "shared-topic", "Shared", "duplicate hit", 0.99),
		retrievalItem(uuid.New(), "fresh-topic", "Fresh", "new hit", 0.4),
	}}

	c := NewCompiler(nodes, topics, &stubCardReader{}, NewTokenEstimator(), 5,
		WithRetrieval(reader, 5))
	result, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	if len(result.Manifest.Retrieved) != 1 {
		t.Fatalf("retrieved has %d items, want 1 (the reference duplicate dropped)", len(result.Manifest.Retrieved))
	}
	if result.Manifest.Retrieved[0].Title != "fresh-topic" {
		t.Errorf("surviving retrieved item = %q, want fresh-topic", result.Manifest.Retrieved[0].Title)
	}
	if strings.Count(result.Content, "--- retrieved topic shared-topic ---") != 0 {
		t.Error("Content contains a retrieved block for the reference-duplicate topic")
	}
}

// TestRetrieved_IntraSetDedupe: duplicate IDs within the retrieved set
// collapse to the first occurrence.
func TestRetrieved_IntraSetDedupe(t *testing.T) {
	req, nodes := retrievalFixture("dupes")

	dup := uuid.New()
	reader := &stubRetrievalReader{items: []RetrievalItem{
		retrievalItem(dup, "dup", "Dup", "first snippet", 0.9),
		retrievalItem(dup, "dup", "Dup", "second snippet", 0.9),
	}}

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5,
		WithRetrieval(reader, 5))
	result, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(result.Manifest.Retrieved) != 1 {
		t.Fatalf("retrieved has %d items, want 1 (intra-set dedupe)", len(result.Manifest.Retrieved))
	}
	if !strings.Contains(result.Content, "first snippet") {
		t.Error("dedupe kept the wrong occurrence — Content lost the first item's snippet")
	}
}

// --- Budget (AC4) -----------------------------------------------------------

func TestRetrieved_BudgetCapAndGreedyFill(t *testing.T) {
	// TokenBudget 800 → tier allocation floor(800*12/100) = 96 tokens. The
	// estimator is ceil(runes/4). Craft items so the FIRST (highest
	// relevance) does not fit into 96, a LATER smaller one does — proving
	// greedy continue — and the kept item's spend never exceeds 96. The
	// oversized item is oversized via its SLUG (slugs are never
	// preview-capped, unlike content).
	req, nodes := retrievalFixture("budget walk")
	req.TokenBudget = 800 // tier allocation floor(800*12/100) = 96

	bigSlug := strings.Repeat("s", 500) // block text ~150 tokens — never fits in 96
	small := strings.Repeat("y", 40)
	reader := &stubRetrievalReader{items: []RetrievalItem{
		retrievalItem(uuid.New(), bigSlug, "Big", "big snippet", 0.99),
		retrievalItem(uuid.New(), "small-topic", "Small", small, 0.4),
	}}

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5,
		WithRetrieval(reader, 5))
	result, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	if len(result.Manifest.Retrieved) != 1 {
		t.Fatalf("retrieved has %d items, want 1 (big omitted, small kept)", len(result.Manifest.Retrieved))
	}
	if result.Manifest.Retrieved[0].Title != "small-topic" {
		t.Errorf("kept item = %q, want small-topic (greedy fill continues past a non-fitting candidate)",
			result.Manifest.Retrieved[0].Title)
	}
	if result.Manifest.Retrieved[0].TokenCount > 96 {
		t.Errorf("kept item spent %d tokens, tier cap is 96", result.Manifest.Retrieved[0].TokenCount)
	}
	found := false
	for _, w := range result.Manifest.Warnings {
		if w == "1 retrieved items omitted (budget)" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing omission warning; warnings: %v", result.Manifest.Warnings)
	}
	if result.Manifest.RetrievalBudget != 96 {
		t.Errorf("RetrievalBudget = %d, want 96", result.Manifest.RetrievalBudget)
	}
}

func TestRetrieved_KeptTokensReduceCardsBudget(t *testing.T) {
	// A card that fits with the tier OFF must be squeezed out with the tier
	// ON: kept retrieval tokens are deducted from the shared remaining
	// budget the cards step sees.
	content := "card budget interaction"
	req, nodes := retrievalFixture(content)
	req.IncludeCards = true
	req.TokenBudget = 60 // tier allocation floor(60*12/100) = 7

	reader := &stubRetrievalReader{items: []RetrievalItem{
		// Tiny item: the block text ("--- retrieved topic r ---\nT\n")
		// estimates to exactly 7 tokens, so it fits the floor(60*12/100)=7
		// tier allocation and its 7 tokens are deducted from the cards'
		// budget.
		retrievalItem(uuid.New(), "r", "T", "", 0.9),
	}}
	// Several small cards under the content hash: the baseline keeps them
	// all (they fit in what ancestry leaves), the wired compile must keep
	// FEWER — the retrieval deduction shrinks what the cards step sees.
	var cardList []card.Card
	for i := 0; i < 6; i++ {
		cardList = append(cardList, makeCard(uuid.New(), card.CardTypeCompact, "app-1"))
	}
	cards := &stubCardReader{cards: map[string][]card.Card{
		ContextHash(content): cardList,
	}}

	// Baseline: tier off → every card fits (fixture validity).
	plain := NewCompiler(nodes, &stubTopicReader{}, cards, NewTokenEstimator(), 5)
	base, err := plain.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("base compile: %v", err)
	}
	if len(base.Manifest.Cards) == 0 {
		t.Fatalf("base (tier off) kept 0 cards — fixture invalid")
	}

	// Tier on: the retrieval item's tokens come out of the same budget, so
	// fewer cards fit. With tier=7 and the retrieval item at 16 tokens, the
	// cards see 7 fewer tokens than baseline — enough to drop the LAST
	// card that fit before.
	wired := NewCompiler(nodes, &stubTopicReader{}, cards, NewTokenEstimator(), 5,
		WithRetrieval(reader, 5))
	wiredResult, err := wired.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("wired compile: %v", err)
	}
	if len(wiredResult.Manifest.Retrieved) != 1 {
		t.Fatalf("wired compile kept %d retrieved items, want 1", len(wiredResult.Manifest.Retrieved))
	}
	if len(wiredResult.Manifest.Cards) >= len(base.Manifest.Cards) {
		t.Fatalf("wired compile kept %d cards, want FEWER than the %d the baseline kept — retrieval tokens must reduce the cards budget",
			len(wiredResult.Manifest.Cards), len(base.Manifest.Cards))
	}
}

// --- Degrade (AC5) ----------------------------------------------------------

func TestRetrieved_ReaderErrorDegradesToWarning(t *testing.T) {
	req, nodes := retrievalFixture("failure path")

	boom := errors.New("search backend exploded")
	reader := &stubRetrievalReader{err: boom}
	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5,
		WithRetrieval(reader, 5))
	result, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("compile failed on a reader error: %v (retrieval must never fail the compile)", err)
	}
	if len(result.Manifest.Retrieved) != 0 {
		t.Errorf("retrieved items present after a reader error: %d", len(result.Manifest.Retrieved))
	}
	want := fmt.Sprintf("retrieval failed: %v", boom)
	found := false
	for _, w := range result.Manifest.Warnings {
		if w == want {
			found = true
		}
	}
	if !found {
		t.Errorf("missing warning %q; warnings: %v", want, result.Manifest.Warnings)
	}
	if result.Manifest.RetrievalBudget != 1200 {
		t.Errorf("RetrievalBudget = %d, want 1200 (recorded once the search was issued)", result.Manifest.RetrievalBudget)
	}
}

func TestRetrieved_EmptyResultIsSilentAndRecordsBudget(t *testing.T) {
	req, nodes := retrievalFixture("empty results")

	reader := &stubRetrievalReader{items: nil}
	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5,
		WithRetrieval(reader, 5))
	result, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("Retrieve called %d times, want 1", reader.calls)
	}
	if len(result.Manifest.Retrieved) != 0 {
		t.Errorf("retrieved items present on an empty result: %d", len(result.Manifest.Retrieved))
	}
	if result.Manifest.RetrievalBudget != 1200 {
		t.Errorf("RetrievalBudget = %d, want 1200 (an empty search still ran)", result.Manifest.RetrievalBudget)
	}
	for _, w := range result.Manifest.Warnings {
		if strings.Contains(w, "retrieval") {
			t.Errorf("unexpected retrieval warning on an empty result: %q", w)
		}
	}
}

func TestRetrieved_NoBudgetSkipsSearchSilently(t *testing.T) {
	// TokenBudget 8 → tier allocation floor(8*12/100) = 0 → no search, no
	// warning, no RetrievalBudget.
	req, nodes := retrievalFixture("no budget for the tier")
	req.TokenBudget = 8

	reader := &stubRetrievalReader{items: []RetrievalItem{
		retrievalItem(uuid.New(), "x", "X", "x", 0.9),
	}}
	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5,
		WithRetrieval(reader, 5))
	result, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if reader.calls != 0 {
		t.Fatalf("Retrieve called %d times with a zero tier budget, want 0", reader.calls)
	}
	if result.Manifest.RetrievalBudget != 0 {
		t.Errorf("RetrievalBudget = %d with a zero tier budget, want 0", result.Manifest.RetrievalBudget)
	}
	if len(result.Manifest.Retrieved) != 0 {
		t.Errorf("retrieved items with a zero tier budget: %d", len(result.Manifest.Retrieved))
	}
	for _, w := range result.Manifest.Warnings {
		if strings.Contains(w, "retrieval") {
			t.Errorf("retrieval warning with a zero tier budget: %q", w)
		}
	}
}

func TestRetrieved_ExhaustedRemainingBudgetSkipsSearch(t *testing.T) {
	// Pinned ancestry consumes the whole budget: remainingBudget <= 0 → no
	// search even though the tier allocation itself is positive.
	nodeID := uuid.New()
	pinned := []byte(`{"pinned": true}`)
	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nodeID: {ID: nodeID, AuthorID: uuid.New(), Content: "current", Metadata: pinned},
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{
				{ID: nodeID, AuthorID: uuid.New(), Content: "current", Metadata: pinned},
				{ID: uuid.New(), AuthorID: uuid.New(), Content: strings.Repeat("p ", 200), Metadata: pinned},
			}, nil
		},
	}
	req := CompileRequest{TreeID: uuid.New(), NodeID: nodeID, TokenBudget: 20, ResolveRefs: false}

	reader := &stubRetrievalReader{}
	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5,
		WithRetrieval(reader, 5))
	result, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if reader.calls != 0 {
		t.Fatalf("Retrieve called %d times with an exhausted budget, want 0", reader.calls)
	}
	if result.Manifest.RetrievalBudget != 0 {
		t.Errorf("RetrievalBudget = %d with an exhausted budget, want 0", result.Manifest.RetrievalBudget)
	}
}

func TestRetrieved_BlankQuerySkipsSearch(t *testing.T) {
	// Current-node content is whitespace only → the collapsed query is
	// empty → no search issued, silently.
	req, nodes := retrievalFixture("   \n\t  ")

	reader := &stubRetrievalReader{items: []RetrievalItem{
		retrievalItem(uuid.New(), "x", "X", "x", 0.9),
	}}
	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5,
		WithRetrieval(reader, 5))
	result, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if reader.calls != 0 {
		t.Fatalf("Retrieve called %d times on a blank query, want 0", reader.calls)
	}
	if result.Manifest.RetrievalBudget != 0 {
		t.Errorf("RetrievalBudget = %d on a blank query, want 0", result.Manifest.RetrievalBudget)
	}
	if len(result.Manifest.Retrieved) != 0 {
		t.Errorf("retrieved items on a blank query: %d", len(result.Manifest.Retrieved))
	}
}

func TestRetrieved_QueryIsCollapsedAndCapped(t *testing.T) {
	// The query handed to the reader is whitespace-collapsed and truncated
	// to 300 runes; the limit and treeID are forwarded verbatim.
	long := strings.Repeat("w", 500)
	req, nodes := retrievalFixture("  hello   world\n\n" + long)

	reader := &stubRetrievalReader{}
	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5,
		WithRetrieval(reader, 5))
	if _, err := c.Compile(context.Background(), req); err != nil {
		t.Fatalf("compile: %v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("Retrieve called %d times, want 1", reader.calls)
	}
	got := reader.lastQuery
	if strings.Contains(got, "\n") || strings.Contains(got, "  ") {
		t.Errorf("query was not whitespace-collapsed: %q", got)
	}
	if runes := []rune(got); len(runes) != 300 {
		t.Errorf("query is %d runes, want the 300-rune cap", len(runes))
	}
	if !strings.HasPrefix(got, "hello world ") {
		t.Errorf("query = %q, want the collapsed content prefix", got)
	}
	if reader.lastLimit != 5 {
		t.Errorf("Retrieve limit = %d, want 5 (retrievalMax forwarded)", reader.lastLimit)
	}
	if reader.lastTree != req.TreeID {
		t.Errorf("Retrieve treeID = %v, want %v", reader.lastTree, req.TreeID)
	}
}

// --- Manifest JSON shape ----------------------------------------------------

func TestRetrieved_ManifestJSONFields(t *testing.T) {
	// Retrieved + RetrievalBudget appear in the manifest JSON when
	// populated; relevance rides on retrieved items only and the unwired
	// manifest carries none of the new keys (omitempty parity).
	req, nodes := retrievalFixture("json shape")

	reader := &stubRetrievalReader{items: []RetrievalItem{
		retrievalItem(uuid.New(), "json-topic", "JSON topic", "snippet", 0.75),
	}}
	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5,
		WithRetrieval(reader, 5))
	result, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	raw := manifestJSON(t, result.Manifest)
	for _, key := range []string{`"retrieved"`, `"retrievalBudget"`, `"retrieved_topic"`, `"relevance":0.75`} {
		if !strings.Contains(raw, key) {
			t.Errorf("manifest JSON missing %s:\n%s", key, raw)
		}
	}

	plain := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	plainResult, err := plain.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("plain compile: %v", err)
	}
	plainJSONStr := manifestJSON(t, plainResult.Manifest)
	for _, key := range []string{`"retrieved"`, `"retrievalBudget"`, `"relevance"`} {
		if strings.Contains(plainJSONStr, key) {
			t.Errorf("unwired manifest JSON carries %s:\n%s", key, plainJSONStr)
		}
	}
}
