// DF-HERMES-CANOPY-29 — the retrieved tier END TO END over real PostgreSQL.
//
// Wires the production chain the compiler actually runs: the context compiler
// → this package's bridge → search.TopicSearchService → db.PGTopicSearchRepo
// → real SQL, with a real node row for the current node. The board's live
// probe is reproduced at the compile level: prose content that retrieves
// NOTHING under ALL-TERMS semantics now folds the topic in via the ONE
// ANY-TERM retry, while the short keyword content keeps resolving on the
// first (ALL-TERMS) search.
//
// Run: CANOPY_TEST_ALLOW_SHARED_DB=1 go test -count=1 -run 'TestDF29' ./internal/retrieval/
package retrieval

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/card"
	ctxpkg "github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/search"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

const (
	df29Prose      = "zebra migration runbook planning for the zebra cutover"
	df29Short      = "zebra runbook cutover"
	df29ZebraSlug  = "zebra-runbook-cutover"
	df29KitchenSlu = "kitchen-recipes"
)

// --- test doubles -----------------------------------------------------------

// countingSearcher wraps the real search service and records the match mode
// of every call, so a test can PROVE how many searches ran and in which mode
// (the bridge's whole contract is "ALL-TERMS first, ONE ANY-TERM retry").
type countingSearcher struct {
	inner TopicSearcher
	modes []bool
}

func (c *countingSearcher) Search(ctx context.Context, treeID uuid.UUID, opts search.SearchOptions) ([]search.TopicSearchResult, int, time.Duration, error) {
	c.modes = append(c.modes, opts.MatchAnyTerms)
	return c.inner.Search(ctx, treeID, opts)
}

// refusingLogRepo asserts the bridge writes NOTHING to the analytics log —
// the adapter's documented "no side effects" contract, now proven on the PG
// path too.
type refusingLogRepo struct{ t *testing.T }

func (r refusingLogRepo) InsertSearchLog(ctx context.Context, entry search.SearchLogEntry) error {
	r.t.Errorf("the retrieval bridge must not write the search analytics log (query %q)", entry.QueryText)
	return nil
}

// stubTopicReader satisfies ctxpkg.TopicReader; the fixtures compile with
// ResolveRefs=false, so the reference step never queries it.
type stubTopicReader struct{}

func (stubTopicReader) GetBySlug(ctx context.Context, treeID uuid.UUID, slug string) (*db.Topic, error) {
	return nil, db.ErrNotFound
}

func (stubTopicReader) GetTopicsForNode(ctx context.Context, nodeID uuid.UUID) ([]db.Topic, error) {
	return nil, nil
}

func (stubTopicReader) GetResolvedTopicsForNode(ctx context.Context, nodeID uuid.UUID) ([]db.Topic, error) {
	return nil, nil
}

// stubCardReader satisfies ctxpkg.CardReader; IncludeCards=false means the
// cards step never runs.
type stubCardReader struct{}

func (stubCardReader) GetByContextHash(ctx context.Context, contextHash string) ([]card.Card, error) {
	return nil, nil
}

// --- fixtures ---------------------------------------------------------------

func df29Tree(t *testing.T, pool *pgxpool.Pool, title string) uuid.UUID {
	t.Helper()
	treeID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO trees (id, owner_id, title) VALUES ($1, $2, $3)`, treeID, uuid.New(), title)
	require.NoError(t, err, "insert tree")
	return treeID
}

func df29Node(t *testing.T, pool *pgxpool.Pool, treeID uuid.UUID, content string) uuid.UUID {
	t.Helper()
	nodeID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO nodes (id, tree_id, author_id, content, node_type) VALUES ($1, $2, $3, $4, 'message')`,
		nodeID, treeID, uuid.New(), content)
	require.NoError(t, err, "insert node")
	return nodeID
}

func df29Topic(t *testing.T, pool *pgxpool.Pool, treeID, rootNodeID uuid.UUID, title, slug string) uuid.UUID {
	t.Helper()
	topicID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO topics (id, tree_id, root_node_id, title, slug) VALUES ($1, $2, $3, $4, $5)`,
		topicID, treeID, rootNodeID, title, slug)
	require.NoError(t, err, "insert topic")
	return topicID
}

// df29Compiler wires the production chain with a counting wrapper (and no
// analytics writes) around the real search service.
func df29Compiler(t *testing.T, pool *pgxpool.Pool) (ctxpkg.Compiler, *countingSearcher) {
	t.Helper()
	counter := &countingSearcher{
		inner: search.NewTopicSearchService(db.NewPGTopicSearchRepo(pool), refusingLogRepo{t: t}),
	}
	compiler := ctxpkg.NewCompiler(
		db.NewPGNodeRepo(pool),
		stubTopicReader{},
		stubCardReader{},
		ctxpkg.NewTokenEstimator(),
		5,
		ctxpkg.WithRetrieval(NewTopicRetriever(counter), 5),
	)
	return compiler, counter
}

func df29RetrievedSlugs(items []ctxpkg.ManifestItem) []string {
	slugs := make([]string, 0, len(items))
	for _, it := range items {
		slugs = append(slugs, it.Title)
	}
	return slugs
}

// --- AC1: the board's probe, end to end ------------------------------------

// TestDF29ProseRetrievalRetrievesTheTopic is AC1: a PG-backed compile of a
// node whose content is the prose sentence retrieves the seeded topic
// "Zebra Runbook Cutover" through the ONE ANY-TERM fallback, scoped to the
// node's own tree (req.TreeID carries a DIFFERENT tree on purpose).
func TestDF29ProseRetrievalRetrievesTheTopic(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	treeT := df29Tree(t, pool, "DF-29 retrieval tree")
	proseNode := df29Node(t, pool, treeT, df29Prose)
	topicRoot := df29Node(t, pool, treeT, "topic root (not an ancestor of the compiled node)")
	zebraTopic := df29Topic(t, pool, treeT, topicRoot, "Zebra Runbook Cutover", df29ZebraSlug)

	// A second, content-indexed topic in the same tree: the OR branch must
	// reach the content arm too.
	contentRoot := df29Node(t, pool, treeT, "migration window notes")
	contentTopic := df29Topic(t, pool, treeT, contentRoot, "Migration Notes", "migration-notes")
	_, err := pool.Exec(ctx,
		`INSERT INTO topic_node_content_search (topic_id, node_id, tree_id, content_text, content_vector)
		 VALUES ($1, $2, $3, $4, to_tsvector('english', $4))`,
		contentTopic, contentRoot, treeT, "zebra cutover planning for the migration window")
	require.NoError(t, err)

	// Precision decoys: a same-tree topic sharing no significant term, and a
	// same-title topic in ANOTHER tree (a cross-tree leak would surface here).
	decoyRoot := df29Node(t, pool, treeT, "unrelated root")
	df29Topic(t, pool, treeT, decoyRoot, "Kitchen Recipes", df29KitchenSlu)

	treeU := df29Tree(t, pool, "DF-29 decoy tree")
	decoyRootU := df29Node(t, pool, treeU, "decoy root")
	decoyTopicU := df29Topic(t, pool, treeU, decoyRootU, "Zebra Runbook Cutover", df29ZebraSlug)

	compiler, counter := df29Compiler(t, pool)
	result, err := compiler.Compile(ctx, ctxpkg.CompileRequest{
		// Deliberately the WRONG tree: the tier is scoped to the CURRENT
		// NODE's tree, so only treeT's topics may come back.
		TreeID:      treeU,
		NodeID:      proseNode,
		TokenBudget: 10000,
		ResolveRefs: false,
	})
	require.NoError(t, err)

	// The tier ran ALL-TERMS first and retried exactly ONCE in ANY-TERM mode.
	assert.Equal(t, []bool{false, true}, counter.modes,
		"search modes = %v, want [ALL-TERMS ANY-TERM] (one retry, not more)", counter.modes)

	items := result.Manifest.Retrieved
	require.GreaterOrEqual(t, len(items), 1, "manifest.Retrieved is empty — the prose content retrieved nothing")
	slugs := df29RetrievedSlugs(items)
	assert.Contains(t, slugs, df29ZebraSlug, "the seeded topic must be retrieved (got %v)", slugs)
	assert.Contains(t, slugs, "migration-notes", "the content-arm topic must be retrieved (got %v)", slugs)
	assert.NotContains(t, slugs, df29KitchenSlu, "an unrelated topic must never be retrieved (got %v)", slugs)

	for _, it := range items {
		assert.Equal(t, "retrieved_topic", it.Kind, "manifest item kind")
		assert.Greater(t, it.TokenCount, 0, "manifest item token count")
		assert.NotEqual(t, decoyTopicU, it.ID, "the OTHER tree's topic leaked into the manifest")
	}
	// The title-arm topic carries relevance; ordering is relevance DESC.
	for i := 1; i < len(items); i++ {
		assert.LessOrEqual(t, items[i].Relevance, items[i-1].Relevance, "retrieved ordering must be relevance DESC")
	}
	// The retrieved item is THIS tree's topic id, not the other tree's topic
	// that happens to carry the same title/slug.
	matchedID := false
	for _, it := range items {
		if it.ID == zebraTopic {
			matchedID = true
		}
	}
	assert.True(t, matchedID, "the retrieved item ids %v do not include the seeded topic %v", items, zebraTopic)

	// Budget accounting and the payload block are unchanged by the fallback:
	// floor(10000 * 12 / 100) = 1200, recorded because the search was issued.
	assert.Equal(t, 1200, result.Manifest.RetrievalBudget, "RetrievalBudget")
	for _, w := range result.Manifest.Warnings {
		assert.NotContains(t, strings.ToLower(w), "retrieval", "unexpected retrieval warning %q", w)
	}
	for _, slug := range slugs {
		assert.Contains(t, result.Content, fmt.Sprintf("--- retrieved topic %s ---", slug),
			"the payload must carry the rendered block for %s", slug)
	}
}

// --- AC2: precision preserved ----------------------------------------------

// TestDF29ShortContentResolvesOnTheFirstAllTermsSearch: content whose terms
// ALL occur in the topic is resolved by the first (ALL-TERMS) search — the
// fallback must not fire, and the tier must not change what it retrieves.
func TestDF29ShortContentResolvesOnTheFirstAllTermsSearch(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	treeT := df29Tree(t, pool, "DF-29 short-content tree")
	shortNode := df29Node(t, pool, treeT, df29Short)
	topicRoot := df29Node(t, pool, treeT, "topic root")
	df29Topic(t, pool, treeT, topicRoot, "Zebra Runbook Cutover", df29ZebraSlug)

	compiler, counter := df29Compiler(t, pool)
	result, err := compiler.Compile(ctx, ctxpkg.CompileRequest{
		NodeID:      shortNode,
		TokenBudget: 10000,
	})
	require.NoError(t, err)

	assert.Equal(t, []bool{false}, counter.modes,
		"search modes = %v, want one ALL-TERMS search and no fallback", counter.modes)
	require.Len(t, result.Manifest.Retrieved, 1)
	assert.Equal(t, df29ZebraSlug, result.Manifest.Retrieved[0].Title)
	assert.Equal(t, 1200, result.Manifest.RetrievalBudget)
}

// TestDF29UnrelatedTopicIsNeverRetrieved: a topic sharing NO significant term
// with the node content is absent from the compiled payload in either mode —
// including when the ANY-TERM fallback is what produced the candidate set.
func TestDF29UnrelatedTopicIsNeverRetrieved(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	treeT := df29Tree(t, pool, "DF-29 precision tree")
	proseNode := df29Node(t, pool, treeT, df29Prose)
	decoyRoot := df29Node(t, pool, treeT, "unrelated root")
	df29Topic(t, pool, treeT, decoyRoot, "Kitchen Recipes", df29KitchenSlu)

	compiler, counter := df29Compiler(t, pool)
	result, err := compiler.Compile(ctx, ctxpkg.CompileRequest{NodeID: proseNode, TokenBudget: 10000})
	require.NoError(t, err)

	// The fallback DID run (the fixture has no matching topic at all) …
	assert.Equal(t, []bool{false, true}, counter.modes)
	// … and still retrieved nothing unrelated.
	assert.Empty(t, result.Manifest.Retrieved, "retrieved %v, want nothing for an unrelated-only tree", df29RetrievedSlugs(result.Manifest.Retrieved))
	assert.NotContains(t, result.Content, "--- retrieved topic ", "the payload must carry no retrieved block")
	assert.Equal(t, 1200, result.Manifest.RetrievalBudget, "a run search still records the allocation")
}

// TestDF29StopWordsOnlyContentIssuesOneSearchAndDegrades covers the
// stop-words-only arm of AC2 plus the degrade contract (AC5): the search
// layer reports it as an ERROR, so exactly ONE search runs (no ANY-TERM
// retry — such a query has no lexeme to OR), the compile still succeeds, the
// allocation is recorded because the search was issued, and the single
// warning is the compiler's `retrieval failed: ...` line.
func TestDF29StopWordsOnlyContentIssuesOneSearchAndDegrades(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	treeT := df29Tree(t, pool, "DF-29 stop-words tree")
	node := df29Node(t, pool, treeT, "the and of the")
	topicRoot := df29Node(t, pool, treeT, "topic root")
	df29Topic(t, pool, treeT, topicRoot, "Zebra Runbook Cutover", df29ZebraSlug)

	compiler, counter := df29Compiler(t, pool)
	result, err := compiler.Compile(ctx, ctxpkg.CompileRequest{NodeID: node, TokenBudget: 10000})
	require.NoError(t, err, "a stop-words-only query must never fail the compile")

	assert.Equal(t, []bool{false}, counter.modes,
		"search modes = %v, want ONE ALL-TERMS search (no ANY-TERM retry for a stop-words-only query)", counter.modes)
	assert.Empty(t, result.Manifest.Retrieved)
	assert.Equal(t, 1200, result.Manifest.RetrievalBudget, "the allocation is recorded once the search was issued")

	want := fmt.Sprintf("retrieval failed: %v", search.ErrSearchStopWordsOnly)
	retrievalWarnings := 0
	for _, w := range result.Manifest.Warnings {
		if strings.Contains(strings.ToLower(w), "retrieval") {
			retrievalWarnings++
			assert.Equal(t, want, w, "degrade warning text")
		}
	}
	assert.Equal(t, 1, retrievalWarnings, "want exactly one retrieval warning (warnings: %v)", result.Manifest.Warnings)
}
