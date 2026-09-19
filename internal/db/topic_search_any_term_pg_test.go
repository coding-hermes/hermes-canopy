// DF-HERMES-CANOPY-29 — the ANY-TERM (OR) match mode against REAL PostgreSQL.
//
// The board's live probe: the tier's query is the node's whole content and
// plainto_tsquery ANDs every term, so "zebra migration runbook planning for
// the zebra cutover" matched NOTHING while "zebra runbook cutover" matched
// the seeded topic "Zebra Runbook Cutover". These tests pin both halves
// against the real statement/planner: ALL-TERMS semantics unchanged, ANY-TERM
// as the bounded, sanitized, tree-scoped recall fallback.
//
// Run: CANOPY_TEST_ALLOW_SHARED_DB=1 go test -count=1 -run 'TestDF29' ./internal/db/
package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/search"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// Prose (the sentence shape the board's live probe used) and the short
// keyword shape that ALL-TERMS already resolves.
const (
	df29Prose   = "zebra migration runbook planning for the zebra cutover"
	df29Short   = "zebra runbook cutover"
	df29Zebra   = "zebra-runbook-cutover"
	df29Migrat  = "migration-notes"
	df29Kitchen = "kitchen-recipes"
)

func df29Tree(t *testing.T, pool *pgxpool.Pool, title string) uuid.UUID {
	t.Helper()
	treeID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO trees (id, owner_id, title) VALUES ($1, $2, $3)`,
		treeID, uuid.New(), title)
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
	// The search_vector is computed by the trg_topic_search_vector trigger
	// from title + description, i.e. exactly what the topic arm of the query
	// reads.
	_, err := pool.Exec(context.Background(),
		`INSERT INTO topics (id, tree_id, root_node_id, title, slug) VALUES ($1, $2, $3, $4, $5)`,
		topicID, treeID, rootNodeID, title, slug)
	require.NoError(t, err, "insert topic")
	return topicID
}

// df29TopicContent indexes node content under a topic, the way
// refresh_topic_node_content_index() does (content_text + content_vector).
func df29TopicContent(t *testing.T, pool *pgxpool.Pool, topicID, nodeID, treeID uuid.UUID, text string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO topic_node_content_search (topic_id, node_id, tree_id, content_text, content_vector)
		 VALUES ($1, $2, $3, $4, to_tsvector('english', $4))`,
		topicID, nodeID, treeID, text)
	require.NoError(t, err, "insert topic_node_content_search")
}

// df29Fixture seeds one tree with: a title-arm topic ("Zebra Runbook
// Cutover"), a content-arm topic whose TITLE carries none of the prose terms
// ("Migration Notes" + indexed node content), and a decoy topic sharing no
// significant term with the prose.
func df29Fixture(t *testing.T, pool *pgxpool.Pool) (treeID uuid.UUID, topicRoot, proseNode uuid.UUID) {
	t.Helper()
	treeID = df29Tree(t, pool, "DF-29 retrieval tree")

	topicRoot = df29Node(t, pool, treeID, "topic root node")
	proseNode = df29Node(t, pool, treeID, df29Prose)

	df29Topic(t, pool, treeID, topicRoot, "Zebra Runbook Cutover", df29Zebra)

	contentRoot := df29Node(t, pool, treeID, "migration window notes")
	contentTopic := df29Topic(t, pool, treeID, contentRoot, "Migration Notes", df29Migrat)
	df29TopicContent(t, pool, contentTopic, contentRoot, treeID, "zebra cutover planning for the migration window")

	decoyRoot := df29Node(t, pool, treeID, "unrelated root")
	df29Topic(t, pool, treeID, decoyRoot, "Kitchen Recipes", df29Kitchen)

	return treeID, topicRoot, proseNode
}

func df29Slugs(results []search.TopicSearchResult) []string {
	slugs := make([]string, 0, len(results))
	for _, r := range results {
		slugs = append(slugs, r.Slug)
	}
	return slugs
}

// TestDF29_AllTermsProseMatchesNothing_AnyTermFindsIt is the board's exact
// probe, reproduced at the SQL layer: the same statement that returns NOTHING
// for the prose content in the default mode returns the topic in ANY-TERM
// mode, while the short keyword content keeps working WITHOUT the fallback.
func TestDF29_AllTermsProseMatchesNothing_AnyTermFindsIt(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicSearchRepo(pool)
	treeID, _, _ := df29Fixture(t, pool)

	// ALL-TERMS + prose: nothing matches (every term is required).
	allTerms, total, err := repo.SearchTopics(ctx, treeID, search.SearchOptions{Query: df29Prose, MaxResults: 10})
	require.NoError(t, err)
	assert.Empty(t, allTerms, "ALL-TERMS matched the prose, so the defect this row fixes is not reproducible")
	assert.Equal(t, 0, total)

	// ANY-TERM + the SAME prose: the title-arm topic AND the content-arm topic.
	anyTerm, total, err := repo.SearchTopics(ctx, treeID, search.SearchOptions{Query: df29Prose, MaxResults: 10, MatchAnyTerms: true})
	require.NoError(t, err)
	slugs := df29Slugs(anyTerm)
	assert.GreaterOrEqual(t, len(anyTerm), 2, "ANY-TERM returned %v, want the title-arm and content-arm topics", slugs)
	assert.Contains(t, slugs, df29Zebra, "the title-arm topic must match in ANY-TERM mode")
	assert.Contains(t, slugs, df29Migrat, "the content-arm topic must match in ANY-TERM mode")
	assert.NotContains(t, slugs, df29Kitchen, "a topic sharing no significant term must never match")
	assert.GreaterOrEqual(t, total, 2)

	// Precision unchanged: the short keyword content still resolves in the
	// DEFAULT mode — the fallback is a rescue, not the only path.
	shortDefault, _, err := repo.SearchTopics(ctx, treeID, search.SearchOptions{Query: df29Short, MaxResults: 10})
	require.NoError(t, err)
	assert.Contains(t, df29Slugs(shortDefault), df29Zebra,
		"ALL-TERMS must still resolve %q without any ANY-TERM fallback", df29Short)
	shortAny, _, err := repo.SearchTopics(ctx, treeID, search.SearchOptions{Query: df29Short, MaxResults: 10, MatchAnyTerms: true})
	require.NoError(t, err)
	assert.Contains(t, df29Slugs(shortAny), df29Zebra)
}

// TestDF29_AnyTermRanksByRelevanceAndStaysTreeScoped covers the ordering
// contract and the scope rule on real SQL: the merged statement orders by the
// mode's own relevance, and a topic in ANOTHER tree is never returned.
func TestDF29_AnyTermRanksByRelevanceAndStaysTreeScoped(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicSearchRepo(pool)

	treeA, _, _ := df29Fixture(t, pool)

	// The SAME title/slug in a different tree — a cross-tree leak would show
	// up as a duplicate slug with a foreign topic id.
	treeB := df29Tree(t, pool, "DF-29 decoy tree")
	decoyRootB := df29Node(t, pool, treeB, "decoy root")
	decoyTopicB := df29Topic(t, pool, treeB, decoyRootB, "Zebra Runbook Cutover", df29Zebra)

	results, _, err := repo.SearchTopics(ctx, treeA, search.SearchOptions{Query: df29Prose, MaxResults: 10, MatchAnyTerms: true})
	require.NoError(t, err)
	for _, r := range results {
		assert.Equal(t, treeA, r.TreeID, "result %q came from tree %v, want only the searched tree %v", r.Slug, r.TreeID, treeA)
		assert.NotEqual(t, decoyTopicB, r.TopicID, "the other tree's topic leaked into the result set")
	}
	// Relevance DESC (the default ORDER BY) — ranked, best match first.
	for i := 1; i < len(results); i++ {
		assert.LessOrEqual(t, results[i].Relevance, results[i-1].Relevance,
			"results are not ordered by relevance DESC: %v", df29Slugs(results))
	}
	// No duplicate topics in the merged set.
	seen := map[uuid.UUID]bool{}
	for _, r := range results {
		assert.False(t, seen[r.TopicID], "topic %v appeared twice in the merged result set", r.TopicID)
		seen[r.TopicID] = true
	}

	// And the same query scoped to tree B returns only tree B's topic.
	other, _, err := repo.SearchTopics(ctx, treeB, search.SearchOptions{Query: df29Prose, MaxResults: 10, MatchAnyTerms: true})
	require.NoError(t, err)
	require.Len(t, other, 1)
	assert.Equal(t, decoyTopicB, other[0].TopicID)
	assert.Equal(t, treeB, other[0].TreeID)
}

// TestDF29_AnyTermCapsSignificantTermsAtTwelve proves the cap END TO END: the
// content's 1st term is inside the cap, its 14th is outside it, so a topic
// matching only the 14th term is never retrieved.
func TestDF29_AnyTermCapsSignificantTermsAtTwelve(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicSearchRepo(pool)

	treeID := df29Tree(t, pool, "DF-29 cap tree")
	root := df29Node(t, pool, treeID, "cap root")

	// 20 distinct significant terms; none is an english stop word.
	words := []string{
		"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf",
		"hotel", "india", "juliet", "kilo", "lima", "mike", "november",
		"oscar", "papa", "quebec", "romeo", "sierra", "tango",
	}
	content := strings.Join(words, " ")

	df29Topic(t, pool, treeID, root, "Alpha Notes", "alpha-notes")      // term 1 — inside the cap
	df29Topic(t, pool, treeID, root, "November Report", "november-rep") // term 14 — beyond it

	// The 20-term content cannot match under AND semantics (fixture premise).
	allTerms, _, err := repo.SearchTopics(ctx, treeID, search.SearchOptions{Query: content, MaxResults: 10})
	require.NoError(t, err)
	require.Empty(t, allTerms, "fixture premise broken: no topic carries all 20 terms")

	results, _, err := repo.SearchTopics(ctx, treeID, search.SearchOptions{Query: content, MaxResults: 10, MatchAnyTerms: true})
	require.NoError(t, err)
	slugs := df29Slugs(results)
	assert.Contains(t, slugs, "alpha-notes", "the first term is inside the cap and must match")
	assert.NotContains(t, slugs, "november-rep",
		"the 14th term is beyond the %d-term cap and must not be searched (results %v)", 12, slugs)
}

// TestDF29_AnyTermIsSafeOnHostileInput proves the sanitizer's boundary against
// the REAL planner: operator-bearing and injection-shaped queries never raise
// a syntax error, never destroy anything, and never match everything.
func TestDF29_AnyTermIsSafeOnHostileInput(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicSearchRepo(pool)

	treeID, _, _ := df29Fixture(t, pool)

	hostile := []string{
		`zebra | runbook`,
		`zebra & runbook`,
		`zebra <-> runbook`,
		`zebra:*`,
		`') | ('`,
		`'; DROP TABLE topics; --`,
		`x)) | ((y`,
		`\`,
		`'`,
	}
	for _, q := range hostile {
		for _, mode := range []bool{false, true} {
			results, _, err := repo.SearchTopics(ctx, treeID, search.SearchOptions{Query: q, MaxResults: 10, MatchAnyTerms: mode})
			if err != nil {
				// A query with no lexical content is REPORTED, never a crash.
				assert.True(t, errors.Is(err, search.ErrSearchStopWordsOnly),
					"mode anyTerm=%v query %q errored with %v, want nil or ErrSearchStopWordsOnly", mode, q, err)
				continue
			}
			for _, r := range results {
				assert.Equal(t, treeID, r.TreeID, "query %q leaked out of the tree", q)
				assert.NotEqual(t, df29Kitchen, r.Slug, "query %q matched an unrelated topic", q)
			}
		}
	}

	// The table survived every hostile query.
	var topics int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM topics`).Scan(&topics))
	assert.Equal(t, 3, topics, "the topics table must be intact after the hostile-input battery")
}

// TestDF29_AnyTermWithNoUsableTermIsReported pins the "nothing to search for"
// outcome of the fallback mode: the same sentinel the AND path uses for a
// stop-words-only query.
func TestDF29_AnyTermWithNoUsableTermIsReported(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicSearchRepo(pool)
	treeID, _, _ := df29Fixture(t, pool)

	// Stop words only: the shared guard rejects it before any mode is applied.
	_, _, err := repo.SearchTopics(ctx, treeID, search.SearchOptions{Query: "the and of the", MaxResults: 10, MatchAnyTerms: true})
	assert.True(t, errors.Is(err, search.ErrSearchStopWordsOnly), "stop-words-only error = %v", err)

	// Single-rune terms only: the guard passes (plainto_tsquery keeps "x") but
	// the sanitizer keeps nothing, so the mode has nothing to search for.
	_, _, err = repo.SearchTopics(ctx, treeID, search.SearchOptions{Query: "a i o", MaxResults: 10, MatchAnyTerms: true})
	assert.True(t, errors.Is(err, search.ErrSearchStopWordsOnly), "single-rune-only error = %v", err)
}
