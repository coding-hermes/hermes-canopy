package context

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/card"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

// --- Stub readers ---------------------------------------------------------

type stubNodeReader struct {
	nodes     map[uuid.UUID]*db.Node
	getByIDFn func(ctx context.Context, id uuid.UUID) (*db.Node, error)
	getAncFn  func(ctx context.Context, id uuid.UUID) ([]db.Node, error)
}

func (s *stubNodeReader) GetByID(ctx context.Context, id uuid.UUID) (*db.Node, error) {
	if s.getByIDFn != nil {
		return s.getByIDFn(ctx, id)
	}
	n, ok := s.nodes[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	return n, nil
}

func (s *stubNodeReader) GetAncestors(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
	if s.getAncFn != nil {
		return s.getAncFn(ctx, id)
	}
	return nil, nil
}

type stubTopicReader struct {
	topics      map[uuid.UUID][]db.Topic
	getTopicsFn func(ctx context.Context, nodeID uuid.UUID) ([]db.Topic, error)
	// resolvedTopics simulates node_resolved_refs topics for the compiler merge test.
	resolvedTopics      map[uuid.UUID][]db.Topic
	getResolvedTopicsFn func(ctx context.Context, nodeID uuid.UUID) ([]db.Topic, error)
}

func (s *stubTopicReader) GetBySlug(ctx context.Context, treeID uuid.UUID, slug string) (*db.Topic, error) {
	return nil, errors.New("not found")
}

func (s *stubTopicReader) GetTopicsForNode(ctx context.Context, nodeID uuid.UUID) ([]db.Topic, error) {
	if s.getTopicsFn != nil {
		return s.getTopicsFn(ctx, nodeID)
	}
	return s.topics[nodeID], nil
}

func (s *stubTopicReader) GetResolvedTopicsForNode(ctx context.Context, nodeID uuid.UUID) ([]db.Topic, error) {
	if s.getResolvedTopicsFn != nil {
		return s.getResolvedTopicsFn(ctx, nodeID)
	}
	if s.resolvedTopics != nil {
		return s.resolvedTopics[nodeID], nil
	}
	return nil, nil
}

type stubCardReader struct {
	cards map[string][]card.Card
}

func (s *stubCardReader) GetByContextHash(ctx context.Context, contextHash string) ([]card.Card, error) {
	return s.cards[contextHash], nil
}

// --- Helpers --------------------------------------------------------------

func makeNode(id, author uuid.UUID, content string) *db.Node {
	return &db.Node{
		ID:       id,
		AuthorID: author,
		Content:  content,
	}
}

func makeTopic(id uuid.UUID, slug, title, desc string) db.Topic {
	return db.Topic{
		ID:          id,
		Slug:        slug,
		Title:       title,
		Description: desc,
	}
}

func makeCard(id uuid.UUID, cardType card.CardType, appID string) card.Card {
	return card.Card{
		ID:       id,
		CardType: cardType,
		AppID:    appID,
	}
}

// --- Tests ----------------------------------------------------------------

// 1. Ancestry chain 3 nodes, budget 10000
func TestCompile_ThreeNodes_PlentyBudget(t *testing.T) {
	n1 := uuid.New()
	n2 := uuid.New()
	n3 := uuid.New()
	author := uuid.New()

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			n1: makeNode(n1, author, "root message"),
			n2: makeNode(n2, author, "second message"),
			n3: makeNode(n3, author, "third message"),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{
				*makeNode(n3, author, "third message"),
				*makeNode(n2, author, "second message"),
				*makeNode(n1, author, "root message"),
			}, nil
		},
	}

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:       n3,
		TokenBudget:  10000,
		MaxAncestors: 50,
		ResolveRefs:  false,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(result.Manifest.Ancestry) != 3 {
		t.Errorf("expected 3 ancestry items, got %d", len(result.Manifest.Ancestry))
	}
	if result.Manifest.OmittedCount != 0 {
		t.Errorf("expected 0 omitted, got %d", result.Manifest.OmittedCount)
	}
	if !strings.Contains(result.Content, "root message") {
		t.Error("content missing root message")
	}
	if !strings.Contains(result.Content, "third message") {
		t.Error("content missing third message")
	}
}

// 2. Budget 500, chain 10 nodes
func TestCompile_TenNodes_TightBudget(t *testing.T) {
	author := uuid.New()
	var ids []uuid.UUID
	for i := 0; i < 10; i++ {
		ids = append(ids, uuid.New())
	}

	nodeMap := make(map[uuid.UUID]*db.Node)
	ancList := make([]db.Node, 10)
	for i := 0; i < 10; i++ {
		n := makeNode(ids[i], author, strings.Repeat("x", 200)) // ~200 chars = 50 tokens
		nodeMap[ids[i]] = n
		ancList[9-i] = *n // reverse: self first
	}

	lastID := ids[9]
	nodes := &stubNodeReader{
		nodes: nodeMap,
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return ancList, nil
		},
	}

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:       lastID,
		TokenBudget:  500,
		MaxAncestors: 50,
		ResolveRefs:  false,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.Manifest.OmittedCount < 1 {
		t.Error("expected some nodes omitted by budget")
	}
	if len(result.Manifest.TruncationMarkers) == 0 {
		t.Error("expected truncation markers")
	}
	// Newest node must be present
	if !strings.Contains(result.Content, ids[9].String()) {
		t.Error("newest node not present in content")
	}
}

// 3. Node with 2 references resolved
func TestCompile_TwoReferences(t *testing.T) {
	nid := uuid.New()
	t1 := makeTopic(uuid.New(), "topic-one", "Topic One", "Description one")
	t2 := makeTopic(uuid.New(), "topic-two", "Topic Two", "Description two")

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nid: makeNode(nid, uuid.New(), "message with #references"),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{*makeNode(nid, uuid.New(), "message with #references")}, nil
		},
	}

	topics := &stubTopicReader{
		topics: map[uuid.UUID][]db.Topic{
			nid: {t1, t2},
		},
	}

	c := NewCompiler(nodes, topics, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      nid,
		TokenBudget: 10000,
		ResolveRefs: true,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(result.Manifest.References) != 2 {
		t.Errorf("expected 2 references, got %d", len(result.Manifest.References))
	}
	if !strings.Contains(result.Content, "topic boundary: topic-one") {
		t.Error("content missing topic-one boundary")
	}
	if !strings.Contains(result.Content, "topic boundary: topic-two") {
		t.Error("content missing topic-two boundary")
	}
}

// 4. 12 references, MaxRefs=5
func TestCompile_ManyReferences_SoftCap(t *testing.T) {
	nid := uuid.New()
	var allTopics []db.Topic
	for i := 0; i < 12; i++ {
		slug := "topic-" + string(rune('a'+i))
		allTopics = append(allTopics, makeTopic(uuid.New(), slug, "Title", "Desc"))
	}

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nid: makeNode(nid, uuid.New(), "msg"),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{*makeNode(nid, uuid.New(), "msg")}, nil
		},
	}

	topics := &stubTopicReader{
		topics: map[uuid.UUID][]db.Topic{nid: allTopics},
	}

	c := NewCompiler(nodes, topics, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      nid,
		TokenBudget: 100000,
		ResolveRefs: true,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Hard cap = 10 — only 10 rendered
	if len(result.Manifest.References) > 10 {
		t.Errorf("expected ≤10 references, got %d", len(result.Manifest.References))
	}
	// Must have soft cap warning
	foundFocusWarning := false
	foundLimitWarning := false
	for _, w := range result.Manifest.Warnings {
		if strings.Contains(w, "context becoming unfocused") {
			foundFocusWarning = true
		}
		if strings.Contains(w, "reference limit reached") {
			foundLimitWarning = true
		}
	}
	if !foundFocusWarning {
		t.Error("missing soft cap warning")
	}
	if !foundLimitWarning {
		t.Error("missing hard cap warning")
	}
}

// 5. Reference topic missing
func TestCompile_ReferenceTopicMissing(t *testing.T) {
	nid := uuid.New()

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nid: makeNode(nid, uuid.New(), "msg"),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{*makeNode(nid, uuid.New(), "msg")}, nil
		},
	}

	topics := &stubTopicReader{
		getTopicsFn: func(ctx context.Context, nodeID uuid.UUID) ([]db.Topic, error) {
			return nil, errors.New("some db error")
		},
	}

	c := NewCompiler(nodes, topics, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      nid,
		TokenBudget: 10000,
		ResolveRefs: true,
	})
	if err != nil {
		t.Fatalf("expected no error (partial failure), got %v", err)
	}

	// Must succeed, must have warning about resolution failure
	foundWarning := false
	for _, w := range result.Manifest.Warnings {
		if strings.Contains(w, "reference resolution failed") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("expected warning about reference resolution failure, got: %v", result.Manifest.Warnings)
	}
}

// 6. IncludeCards=true + card found by hash
func TestCompile_IncludeCards_Found(t *testing.T) {
	nid := uuid.New()
	content := "node content for hashing"

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nid: makeNode(nid, uuid.New(), content),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{*makeNode(nid, uuid.New(), content)}, nil
		},
	}

	ch := ContextHash(content)
	cd := makeCard(uuid.New(), card.CardTypeCompact, "test-app")
	cards := &stubCardReader{
		cards: map[string][]card.Card{ch: {cd}},
	}

	c := NewCompiler(nodes, &stubTopicReader{}, cards, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:       nid,
		TokenBudget:  10000,
		IncludeCards: true,
		ResolveRefs:  false,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(result.Manifest.Cards) != 1 {
		t.Errorf("expected 1 card, got %d", len(result.Manifest.Cards))
	}
	if !strings.Contains(result.Content, "--- card compact") {
		t.Error("content missing card section")
	}
}

// 7. IncludeCards=false
func TestCompile_IncludeCards_False(t *testing.T) {
	nid := uuid.New()
	content := "node content"

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nid: makeNode(nid, uuid.New(), content),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{*makeNode(nid, uuid.New(), content)}, nil
		},
	}

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:       nid,
		TokenBudget:  10000,
		IncludeCards: false,
		ResolveRefs:  false,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(result.Manifest.Cards) != 0 {
		t.Errorf("expected 0 cards, got %d", len(result.Manifest.Cards))
	}
}

// 8. Node not found
func TestCompile_NodeNotFound(t *testing.T) {
	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{},
	}
	nid := uuid.New()

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	_, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      nid,
		TokenBudget: 10000,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrNodeNotFound) {
		t.Errorf("expected ErrNodeNotFound, got %v", err)
	}
}

// 9. Budget < 1
func TestCompile_InvalidBudget(t *testing.T) {
	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			uuid.Nil: makeNode(uuid.Nil, uuid.New(), "x"),
		},
	}

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	_, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      uuid.New(),
		TokenBudget: 0,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrInvalidBudget) {
		t.Errorf("expected ErrInvalidBudget, got %v", err)
	}
}

// 10. Root node (no ancestors)
func TestCompile_RootNode(t *testing.T) {
	nid := uuid.New()
	author := uuid.New()

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nid: makeNode(nid, author, "root message"),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{*makeNode(nid, author, "root message")}, nil
		},
	}

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      nid,
		TokenBudget: 10000,
		ResolveRefs: false,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(result.Manifest.Ancestry) != 1 {
		t.Errorf("expected 1 ancestry item, got %d", len(result.Manifest.Ancestry))
	}
	if !strings.Contains(result.Content, "root message") {
		t.Error("content missing root message")
	}
}

// 11. Determinism
func TestCompile_Determinism(t *testing.T) {
	nid := uuid.New()
	author := uuid.New()

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nid: makeNode(nid, author, "hello world"),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{*makeNode(nid, author, "hello world")}, nil
		},
	}

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)

	req := CompileRequest{
		NodeID:      nid,
		TokenBudget: 10000,
		ResolveRefs: false,
	}

	r1, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("first compile: %v", err)
	}
	r2, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("second compile: %v", err)
	}

	if r1.Content != r2.Content {
		t.Errorf("non-deterministic content:\n--- r1 ---\n%s\n--- r2 ---\n%s", r1.Content, r2.Content)
	}
	if r1.Manifest.TokensUsed != r2.Manifest.TokensUsed {
		t.Errorf("non-deterministic tokens used: %d vs %d", r1.Manifest.TokensUsed, r2.Manifest.TokensUsed)
	}
}

// 12. Empty content node
func TestCompile_EmptyContent(t *testing.T) {
	nid := uuid.New()
	author := uuid.New()

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nid: {ID: nid, AuthorID: author, Content: ""},
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{{ID: nid, AuthorID: author, Content: ""}}, nil
		},
	}

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      nid,
		TokenBudget: 10000,
		ResolveRefs: false,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Content == "" {
		t.Error("expected non-empty content even for empty node")
	}
}

// 13. MaxAncestors=2, chain 10
func TestCompile_MaxAncestors(t *testing.T) {
	author := uuid.New()
	var ids []uuid.UUID
	for i := 0; i < 10; i++ {
		ids = append(ids, uuid.New())
	}

	nodeMap := make(map[uuid.UUID]*db.Node)
	ancList := make([]db.Node, 10)
	for i := 0; i < 10; i++ {
		n := makeNode(ids[i], author, "msg "+string(rune('a'+i)))
		nodeMap[ids[i]] = n
		ancList[9-i] = *n // self first
	}

	lastID := ids[9]
	nodes := &stubNodeReader{
		nodes: nodeMap,
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return ancList, nil
		},
	}

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:       lastID,
		TokenBudget:  100000,
		MaxAncestors: 2,
		ResolveRefs:  false,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(result.Manifest.Ancestry) != 2 {
		t.Errorf("expected 2 ancestry items, got %d", len(result.Manifest.Ancestry))
	}
	if result.Manifest.OmittedReason != "depth" {
		t.Errorf("expected OmittedReason=depth, got %q", result.Manifest.OmittedReason)
	}
	if result.Manifest.OmittedCount != 8 {
		t.Errorf("expected OmittedCount=8, got %d", result.Manifest.OmittedCount)
	}
}

// 14. Duplicate refs
func TestCompile_DuplicateRefs(t *testing.T) {
	nid := uuid.New()
	topicID := uuid.New()
	t1 := makeTopic(topicID, "dup-topic", "Dup Topic", "Desc")
	t2 := makeTopic(topicID, "dup-topic", "Dup Topic", "Desc") // same ID

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nid: makeNode(nid, uuid.New(), "msg"),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{*makeNode(nid, uuid.New(), "msg")}, nil
		},
	}

	topics := &stubTopicReader{
		topics: map[uuid.UUID][]db.Topic{nid: {t1, t2}},
	}

	c := NewCompiler(nodes, topics, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      nid,
		TokenBudget: 10000,
		ResolveRefs: true,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(result.Manifest.References) != 1 {
		t.Errorf("expected 1 deduped reference, got %d", len(result.Manifest.References))
	}
}

// 15. Budget too small for one node
func TestCompile_BudgetTooSmall(t *testing.T) {
	nid := uuid.New()
	author := uuid.New()

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nid: makeNode(nid, author, "hello"),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{*makeNode(nid, author, "hello")}, nil
		},
	}

	c := NewCompiler(nodes, &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      nid,
		TokenBudget: 2, // very small
		ResolveRefs: false,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result.Content == "" {
		t.Error("expected non-empty Content even with tiny budget")
	}
	foundWarning := false
	for _, w := range result.Manifest.Warnings {
		if strings.Contains(w, "budget too small for single node") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("expected 'budget too small' warning, got: %v", result.Manifest.Warnings)
	}
}

// 16. Compiler merge: scope-membership topics + node_resolved_refs topics
// are deduplicated and merged (spec §8.1).
func TestCompile_ReferenceMerge_ScopeAndResolved(t *testing.T) {
	nid := uuid.New()
	// t1 appears in BOTH scope-membership and resolved refs → must dedupe to 1.
	t1 := makeTopic(uuid.New(), "shared-topic", "Shared", "Desc shared")
	// t2 is ONLY in scope-membership.
	t2 := makeTopic(uuid.New(), "scope-topic", "Scope", "Desc scope")
	// t3 is ONLY in node_resolved_refs (the author wrote #ref-only-topic).
	t3 := makeTopic(uuid.New(), "ref-only-topic", "RefOnly", "Desc ref")

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nid: makeNode(nid, uuid.New(), "message with #ref-only-topic"),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{*makeNode(nid, uuid.New(), "message with #ref-only-topic")}, nil
		},
	}

	topics := &stubTopicReader{
		topics:         map[uuid.UUID][]db.Topic{nid: {t1, t2}},
		resolvedTopics: map[uuid.UUID][]db.Topic{nid: {t1, t3}},
	}

	c := NewCompiler(nodes, topics, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      nid,
		TokenBudget: 10000,
		ResolveRefs: true,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Merged set: t1, t2, t3 — 3 unique topics (t1 deduped).
	if len(result.Manifest.References) != 3 {
		t.Errorf("expected 3 merged references (scope+resolved, deduped), got %d", len(result.Manifest.References))
	}

	// All three topics must appear in the content.
	if !strings.Contains(result.Content, "shared-topic") {
		t.Error("content missing shared-topic (should appear once after dedup)")
	}
	if !strings.Contains(result.Content, "scope-topic") {
		t.Error("content missing scope-topic (scope-membership only)")
	}
	if !strings.Contains(result.Content, "ref-only-topic") {
		t.Error("content missing ref-only-topic (node_resolved_refs only)")
	}
}

// 17. Compiler includes resolved ref with boundary marker (scenario 23).
func TestCompile_ResolvedRef_HasBoundaryMarker(t *testing.T) {
	nid := uuid.New()
	refTopic := makeTopic(uuid.New(), "db-schema", "Database Schema", "Schema docs")

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nid: makeNode(nid, uuid.New(), "See #db-schema"),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{*makeNode(nid, uuid.New(), "See #db-schema")}, nil
		},
	}

	topics := &stubTopicReader{
		topics:         map[uuid.UUID][]db.Topic{}, // no scope-membership
		resolvedTopics: map[uuid.UUID][]db.Topic{nid: {refTopic}},
	}

	c := NewCompiler(nodes, topics, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      nid,
		TokenBudget: 10000,
		ResolveRefs: true,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(result.Manifest.References) != 1 {
		t.Fatalf("expected 1 resolved reference, got %d", len(result.Manifest.References))
	}
	if result.Manifest.References[0].Title != "db-schema" {
		t.Errorf("expected ref slug 'db-schema', got %q", result.Manifest.References[0].Title)
	}
	if !strings.Contains(result.Content, "topic boundary: db-schema") {
		t.Error("content missing boundary marker for resolved ref topic")
	}
}

// 18. Budget truncation: more topics than budget allows → remaining dropped.
func TestCompile_ResolvedRefs_BudgetTruncation(t *testing.T) {
	nid := uuid.New()
	var refTopics []db.Topic
	for i := 0; i < 5; i++ {
		// Long descriptions to consume budget.
		desc := strings.Repeat("desc-content-"+string(rune('a'+i))+" ", 30)
		refTopics = append(refTopics, makeTopic(uuid.New(), "topic-"+string(rune('a'+i)), "Title", desc))
	}

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nid: makeNode(nid, uuid.New(), "msg"),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{*makeNode(nid, uuid.New(), "msg")}, nil
		},
	}

	topics := &stubTopicReader{
		resolvedTopics: map[uuid.UUID][]db.Topic{nid: refTopics},
	}

	c := NewCompiler(nodes, topics, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      nid,
		TokenBudget: 200, // tight budget — ancestry takes most of it
		ResolveRefs: true,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// At least one topic should be dropped by budget. The exact count depends
	// on the estimator, but with budget=200 and 5 large topics, we expect
	// fewer than 5 references.
	if len(result.Manifest.References) >= 5 {
		t.Errorf("expected budget truncation (<5 refs), got %d", len(result.Manifest.References))
	}
}

// 19. GetResolvedTopicsForNode failure → warning, scope topics still work.
func TestCompile_ResolvedRefLookupFailure_PartialDegrade(t *testing.T) {
	nid := uuid.New()
	scopeTopic := makeTopic(uuid.New(), "scope-only", "Scope", "Desc")

	nodes := &stubNodeReader{
		nodes: map[uuid.UUID]*db.Node{
			nid: makeNode(nid, uuid.New(), "msg"),
		},
		getAncFn: func(ctx context.Context, id uuid.UUID) ([]db.Node, error) {
			return []db.Node{*makeNode(nid, uuid.New(), "msg")}, nil
		},
	}

	topics := &stubTopicReader{
		topics: map[uuid.UUID][]db.Topic{nid: {scopeTopic}},
		getResolvedTopicsFn: func(ctx context.Context, nodeID uuid.UUID) ([]db.Topic, error) {
			return nil, errors.New("simulated db failure")
		},
	}

	c := NewCompiler(nodes, topics, &stubCardReader{}, NewTokenEstimator(), 5)
	result, err := c.Compile(context.Background(), CompileRequest{
		NodeID:      nid,
		TokenBudget: 10000,
		ResolveRefs: true,
	})
	if err != nil {
		t.Fatalf("expected no error (partial degrade), got %v", err)
	}

	// Scope topic still included.
	if len(result.Manifest.References) != 1 {
		t.Errorf("expected 1 scope reference despite resolved-ref failure, got %d", len(result.Manifest.References))
	}

	// Warning about resolved-reference failure.
	foundWarning := false
	for _, w := range result.Manifest.Warnings {
		if strings.Contains(w, "resolved-reference lookup failed") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("expected resolved-reference lookup failure warning, got: %v", result.Manifest.Warnings)
	}
}
