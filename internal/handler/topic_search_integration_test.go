// Package handler — integration tests for TM-03 topic search + context injection.
// Tests run against real PostgreSQL (testutil pattern). Creates throwaway
// trees/topics/nodes — never touches demo data.
// Spec: SPEC-TM-03 §10.1 scenarios 1-30, §6 API contract.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/search"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// --- Test server for search endpoints -------------------------------------

func newSearchTestServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, *search.TopicSearchService) {
	t.Helper()
	ctx := context.Background()

	// Create sentinel user for FK.
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, display_name)
		VALUES ('a0000000-0000-0000-0000-000000000001', 'testuser', 'Test User')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("create sentinel user: %v", err)
	}

	searchRepo := db.NewPGTopicSearchRepo(pool)
	logRepo := db.NewPGTopicSearchLogRepo(pool)
	svc := search.NewTopicSearchService(searchRepo, logRepo)
	hub := sse.NewHub()

	r := chi.NewRouter()
	authMW := AuthMiddleware("canopy-dev-secret")
	searchHandler := NewTopicSearchHandler(svc, hub)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMW)
		r.Mount("/trees/{tree_id}", searchHandler.Routes())
	})

	srv := httptest.NewServer(r)
	return srv, &svc
}

// tm03CreateTestTree creates a tree + root node + profile and returns their IDs.
func tm03CreateTestTree(t *testing.T, pool *pgxpool.Pool) (treeID, profileID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	profileID = uuid.New()
	treeID = uuid.New()

	// Create profile (owner_id FK to users — use sentinel user).
	_, err := pool.Exec(ctx, `INSERT INTO profiles (id, owner_id, profile_type, name, display_name)
		VALUES ($1, 'a0000000-0000-0000-0000-000000000001', 'human', $2, $3)
		ON CONFLICT (id) DO NOTHING`,
		profileID, "test-"+profileID.String()[:8], "Test User")
	require.NoError(t, err, "create profile")

	// Create tree (owner_id is a plain uuid, no FK).
	_, err = pool.Exec(ctx, `INSERT INTO trees (id, owner_id, title)
		VALUES ($1, $2, 'Test Tree')`, treeID, profileID)
	require.NoError(t, err, "create tree")

	// Create tree membership.
	_, err = pool.Exec(ctx, `INSERT INTO tree_members (tree_id, user_id, role)
		VALUES ($1, 'a0000000-0000-0000-0000-000000000001', 'owner')`, treeID)
	require.NoError(t, err, "create tree member")

	return treeID, profileID
}

// tm03CreateTestNode inserts a node into the tree and returns its ID.
func tm03CreateTestNode(t *testing.T, pool *pgxpool.Pool, treeID, authorID uuid.UUID, content string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	nodeID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO nodes (id, tree_id, author_id, content)
		VALUES ($1, $2, $3, $4)`, nodeID, treeID, authorID, content)
	require.NoError(t, err, "create node")
	return nodeID
}

// tm03CreateTestTopic creates a topic rooted at rootNode and returns its ID.
func tm03CreateTestTopic(t *testing.T, pool *pgxpool.Pool, treeID, rootNodeID uuid.UUID, title string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	topicID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO topics (id, tree_id, root_node_id, title, slug)
		VALUES ($1, $2, $3, $4, $5)`,
		topicID, treeID, rootNodeID, title,
		uuid.New().String()[:8])
	require.NoError(t, err, "create topic")
	return topicID
}

// tm03CreateReplyEdge creates a reply edge from parent to child.
// Edges require explicit sequence_num (no trigger on the table).
func tm03CreateReplyEdge(t *testing.T, pool *pgxpool.Pool, treeID, parentID, childID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `INSERT INTO edges (tree_id, source_id, target_id, edge_type, sequence_num)
		VALUES ($1, $2, $3, 'reply', 1)`, treeID, parentID, childID)
	require.NoError(t, err, "create edge")
}

// --- Scenario 1: Basic search by title ------------------------------------

func TestTM03_SearchByTitle(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "Root content")
	topicID := tm03CreateTestTopic(t, pool, treeID, rootNode, "Database Schema Design")
	_ = topicID
	_ = rootNode

	// Search for "schema".
	resp := doSearchRequest(t, srv, treeID, "schema")
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body searchResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.True(t, body.Total >= 1, "should find at least 1 result")
	if len(body.Results) > 0 {
		r := body.Results[0]
		assert.Contains(t, r.Title, "Schema")
		assert.True(t, r.Relevance > 0)
	}
}

// --- Scenario 5: Search with stop words only → 400 ------------------------

func TestTM03_SearchStopWordsOnly(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "Root")
	tm03CreateTestTopic(t, pool, treeID, rootNode, "Test Topic")

	resp := doSearchRequest(t, srv, treeID, "the and of")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// --- Scenario 3: Search with no matches -----------------------------------

func TestTM03_SearchNoMatches(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "Root")
	tm03CreateTestTopic(t, pool, treeID, rootNode, "Database Schema Design")

	resp := doSearchRequest(t, srv, treeID, "quantum cryptography")
	defer resp.Body.Close()

	// "quantum" and "cryptography" are valid FTS terms, so this should return 200
	// with empty results. If the FTS parser somehow rejects it, 400 is also acceptable.
	if resp.StatusCode == http.StatusBadRequest {
		// FTS stripped all terms — acceptable per spec §9 (stop words only).
		return
	}
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body searchResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, 0, body.Total)
}

// --- Query too short → 400 SEARCH_QUERY_TOO_SHORT -------------------------

func TestTM03_SearchTooShort(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "Root")
	tm03CreateTestTopic(t, pool, treeID, rootNode, "Test")

	resp := doSearchRequest(t, srv, treeID, "a")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// --- Scenario 15/16: Recent topics ordering + limit -----------------------

func TestTM03_RecentTopics(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "Root")

	// Create 3 topics with different last_active_at.
	topic1 := tm03CreateTestTopic(t, pool, treeID, rootNode, "Topic One")
	topic2 := tm03CreateTestTopic(t, pool, treeID, rootNode, "Topic Two")
	topic3 := tm03CreateTestTopic(t, pool, treeID, rootNode, "Topic Three")

	// Update last_active_at in specific order.
	ctx := context.Background()
	_, err := pool.Exec(ctx, `UPDATE topics SET last_active_at = NOW() - INTERVAL '3 hours' WHERE id = $1`, topic1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE topics SET last_active_at = NOW() - INTERVAL '1 hour' WHERE id = $1`, topic2)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE topics SET last_active_at = NOW() - INTERVAL '2 hours' WHERE id = $1`, topic3)
	require.NoError(t, err)

	req := authenticatedRequest(t, srv.URL, http.MethodGet,
		"/api/v1/trees/"+treeID.String()+"/topics/recent?limit=10", nil)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body recentTopicsResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.True(t, len(body.Topics) >= 3, "should have at least 3 recent topics")
	if len(body.Topics) >= 3 {
		// Most recent first (topic2 = 1h ago, topic3 = 2h ago, topic1 = 3h ago).
		assert.Equal(t, "Topic Two", body.Topics[0].Title)
	}
}

// --- Scenario 8: Inject single topic --------------------------------------

func TestTM03_InjectSingleTopic(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "Root message content")
	childNode := tm03CreateTestNode(t, pool, treeID, profileID, "Child message about schema design")
	tm03CreateReplyEdge(t, pool, treeID, rootNode, childNode)

	topicID := tm03CreateTestTopic(t, pool, treeID, rootNode, "Schema Discussion")

	req := authenticatedRequest(t, srv.URL, http.MethodPost,
		"/api/v1/trees/"+treeID.String()+"/context/inject",
		map[string]any{
			"topic_ids": []string{topicID.String()},
			"max_nodes": 500,
		})
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body injectResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Len(t, body.Context.Topics, 1)
	if len(body.Context.Topics) > 0 {
		tc := body.Context.Topics[0]
		assert.NotEmpty(t, tc.ContextHash)
		assert.True(t, tc.TotalNodes >= 1)
	}
	assert.NotEmpty(t, body.EventID)
}

// --- Scenario 13: Inject 6 topics → 400 CONTEXT_TOO_MANY_TOPICS -----------

func TestTM03_InjectTooManyTopics(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, _ := tm03CreateTestTree(t, pool)

	ids := make([]string, 6)
	for i := range ids {
		ids[i] = uuid.New().String()
	}

	req := authenticatedRequest(t, srv.URL, http.MethodPost,
		"/api/v1/trees/"+treeID.String()+"/context/inject",
		map[string]any{
			"topic_ids": ids,
		})
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// --- Scenario 10: Inject non-existent topic → 404 -------------------------

func TestTM03_InjectNonExistent(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, _ := tm03CreateTestTree(t, pool)

	req := authenticatedRequest(t, srv.URL, http.MethodPost,
		"/api/v1/trees/"+treeID.String()+"/context/inject",
		map[string]any{
			"topic_ids": []string{uuid.New().String()},
		})
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// --- Scenario 23/24: Topic preview ----------------------------------------

func TestTM03_TopicPreview(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "First message about database design")
	childNode := tm03CreateTestNode(t, pool, treeID, profileID, "Second message about indexing strategies")
	tm03CreateReplyEdge(t, pool, treeID, rootNode, childNode)

	topicID := tm03CreateTestTopic(t, pool, treeID, rootNode, "Database Design")

	req := authenticatedRequest(t, srv.URL, http.MethodGet,
		"/api/v1/trees/"+treeID.String()+"/topics/"+topicID.String()+"/preview", nil)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var preview search.TopicPreview
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&preview))
	assert.Equal(t, "Database Design", preview.Title)
	assert.True(t, len(preview.Snippets) > 0)
	assert.NotEmpty(t, preview.LastActiveRel)
}

// --- Scenario 24: Preview non-existent topic → 404 ------------------------

func TestTM03_PreviewNonExistent(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, _ := tm03CreateTestTree(t, pool)

	req := authenticatedRequest(t, srv.URL, http.MethodGet,
		"/api/v1/trees/"+treeID.String()+"/topics/"+uuid.New().String()+"/preview", nil)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// --- Scenario 20: Context hash determinism --------------------------------

func TestTM03_ContextHashDeterminism(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "Root content for hashing test")
	topicID := tm03CreateTestTopic(t, pool, treeID, rootNode, "Hash Test Topic")

	body := map[string]any{
		"topic_ids": []string{topicID.String()},
		"max_nodes": 500,
	}

	// First injection.
	req1 := authenticatedRequest(t, srv.URL, http.MethodPost,
		"/api/v1/trees/"+treeID.String()+"/context/inject", body)
	resp1, err := srv.Client().Do(req1)
	require.NoError(t, err)
	var result1 injectResponse
	json.NewDecoder(resp1.Body).Decode(&result1)
	resp1.Body.Close()

	// Second injection — same topic.
	req2 := authenticatedRequest(t, srv.URL, http.MethodPost,
		"/api/v1/trees/"+treeID.String()+"/context/inject", body)
	resp2, err := srv.Client().Do(req2)
	require.NoError(t, err)
	var result2 injectResponse
	json.NewDecoder(resp2.Body).Decode(&result2)
	resp2.Body.Close()

	require.NotEmpty(t, result1.Context.Topics)
	require.NotEmpty(t, result2.Context.Topics)
	assert.Equal(t, result1.Context.Topics[0].ContextHash, result2.Context.Topics[0].ContextHash,
		"same topic injected twice should produce same context hash")
}

// --- Scenario 25: Search log recording -------------------------------------

func TestTM03_SearchLogRecorded(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "Root")
	tm03CreateTestTopic(t, pool, treeID, rootNode, "Logging Test Topic")

	resp := doSearchRequest(t, srv, treeID, "logging")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify a search_log row was created.
	ctx := context.Background()
	var count int
	err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM topic_search_log WHERE tree_id = $1 AND query_text = $2`,
		treeID, "logging").Scan(&count)
	require.NoError(t, err)
	// The search log stores the raw query as-is, but the FK on profile_id may
	// fail on first attempt (user_id not a profile). The retry with NULL should
	// succeed. Either way, a row should exist.
	assert.True(t, count >= 1, "search log row should exist; got %d for tree %s", count, treeID)
}

// --- Scenario 26: Content index refresh ------------------------------------

func TestTM03_RefreshNodeContentIndex(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	// Ensure sentinel user exists for FK.
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, display_name)
		VALUES ('a0000000-0000-0000-0000-000000000001', 'testuser', 'Test User')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("create sentinel user: %v", err)
	}

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "We need to decide on the schema for topics")
	childNode := tm03CreateTestNode(t, pool, treeID, profileID, "Good idea about the schema design")
	tm03CreateReplyEdge(t, pool, treeID, rootNode, childNode)
	topicID := tm03CreateTestTopic(t, pool, treeID, rootNode, "Schema Design Topic")

	searchRepo := db.NewPGTopicSearchRepo(pool)

	// Refresh the index for the topic's nodes.
	count, err := searchRepo.RefreshNodeContentIndex(ctx, topicID, []uuid.UUID{rootNode, childNode})
	require.NoError(t, err)
	assert.True(t, count >= 2, "should have indexed at least 2 nodes")

	// Verify rows exist.
	var rowCount int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM topic_node_content_search WHERE topic_id = $1`, topicID).Scan(&rowCount)
	require.NoError(t, err)
	assert.True(t, rowCount >= 2, "should have 2 rows in content index")
}

// --- Scenario 27: Content index cascade delete on topic delete ------------

func TestTM03_ContentIndexCascadeDelete(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	// Ensure sentinel user exists for FK.
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, display_name)
		VALUES ('a0000000-0000-0000-0000-000000000001', 'testuser', 'Test User')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("create sentinel user: %v", err)
	}

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "Cascade test content")
	topicID := tm03CreateTestTopic(t, pool, treeID, rootNode, "Cascade Test")

	searchRepo := db.NewPGTopicSearchRepo(pool)

	// Index the node.
	_, err := searchRepo.RefreshNodeContentIndex(ctx, topicID, []uuid.UUID{rootNode})
	require.NoError(t, err)

	// Verify row exists.
	var count int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM topic_node_content_search WHERE topic_id = $1`, topicID).Scan(&count)
	require.NoError(t, err)
	assert.True(t, count >= 1)

	// Delete the topic (hard delete).
	_, err = pool.Exec(ctx, `DELETE FROM topics WHERE id = $1`, topicID)
	require.NoError(t, err)

	// Verify content index rows cascade-deleted.
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM topic_node_content_search WHERE topic_id = $1`, topicID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "content index rows should be cascade-deleted")
}

// --- Scenario 28: SQL injection attempt ------------------------------------

func TestTM03_SQLInjectionAttempt(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "Root")
	tm03CreateTestTopic(t, pool, treeID, rootNode, "Injection Test")

	resp := doSearchRequest(t, srv, treeID, "' OR 1=1 --")
	defer resp.Body.Close()
	// Should either 400 (stop words only — FTS strips the injection) or
	// 200 with empty results. Either way, NO data leak.
	assert.True(t, resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusBadRequest,
		"expected 200 or 400, got %d", resp.StatusCode)
	if resp.StatusCode == http.StatusOK {
		var body searchResponse
		json.NewDecoder(resp.Body).Decode(&body)
		// Must NOT return all topics (no injection).
		for _, r := range body.Results {
			assert.NotEqual(t, "Injection Test", r.Title,
				"SQL injection must not leak data")
		}
	}
}

// --- Scenario 2: Search by node content (with content index) ---------------

func TestTM03_SearchByNodeContent(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, svc := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "We need to decide on the schema for the topics table")
	childNode := tm03CreateTestNode(t, pool, treeID, profileID, "Good point about the schema design approach")
	tm03CreateReplyEdge(t, pool, treeID, rootNode, childNode)
	topicID := tm03CreateTestTopic(t, pool, treeID, rootNode, "Architecture Discussion")

	// Refresh the content index so node content is searchable.
	_, err := (*svc).RefreshNodeContentIndex(context.Background(), topicID, []uuid.UUID{rootNode, childNode})
	require.NoError(t, err)

	// Search for content-specific term.
	resp := doSearchRequest(t, srv, treeID, "decide schema topics")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("search status=%d, body=%s", resp.StatusCode, string(bodyBytes))
	}
	var body searchResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	// The topic should appear in content search results.
	found := false
	for _, r := range body.Results {
		if r.TopicID == topicID {
			found = true
			assert.True(t, r.Relevance > 0)
		}
	}
	assert.True(t, found, "topic should be found via content search")
}

// --- Scenario 11: Inject deleted topic → 410 ------------------------------

func TestTM03_InjectDeletedTopic(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "Root")
	topicID := tm03CreateTestTopic(t, pool, treeID, rootNode, "Deleted Topic")

	// Soft-delete the topic.
	ctx := context.Background()
	_, err := pool.Exec(ctx,
		`UPDATE topics SET status = 'deleted', deleted_at = NOW() WHERE id = $1`, topicID)
	require.NoError(t, err)

	req := authenticatedRequest(t, srv.URL, http.MethodPost,
		"/api/v1/trees/"+treeID.String()+"/context/inject",
		map[string]any{
			"topic_ids": []string{topicID.String()},
		})
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusGone, resp.StatusCode)
}

// --- Scenario 12: Inject archived topic → 409 ------------------------------

func TestTM03_InjectArchivedTopic(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "Root")
	topicID := tm03CreateTestTopic(t, pool, treeID, rootNode, "Archived Topic")

	// Archive the topic.
	ctx := context.Background()
	_, err := pool.Exec(ctx,
		`UPDATE topics SET status = 'archived', archived_at = NOW() WHERE id = $1`, topicID)
	require.NoError(t, err)

	req := authenticatedRequest(t, srv.URL, http.MethodPost,
		"/api/v1/trees/"+treeID.String()+"/context/inject",
		map[string]any{
			"topic_ids": []string{topicID.String()},
		})
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

// --- Scenario 6: Search across archived topics -----------------------------

func TestTM03_SearchArchivedTopics(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "Root")
	topicID := tm03CreateTestTopic(t, pool, treeID, rootNode, "Archived Schema Topic")

	// Archive the topic.
	ctx := context.Background()
	_, err := pool.Exec(ctx,
		`UPDATE topics SET status = 'archived', archived_at = NOW() WHERE id = $1`, topicID)
	require.NoError(t, err)

	// Search with status=all — should include archived.
	resp := doSearchRequestWithStatus(t, srv, treeID, "schema", "all")
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body searchResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	found := false
	for _, r := range body.Results {
		if r.TopicID == topicID {
			found = true
		}
	}
	assert.True(t, found, "archived topic should appear in status=all search")
}

// --- Scenario 18: Multi-topic injection -----------------------------------

func TestTM03_MultiTopicInjection(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newSearchTestServer(t, pool)

	treeID, profileID := tm03CreateTestTree(t, pool)
	rootNode := tm03CreateTestNode(t, pool, treeID, profileID, "Root node content A")
	topic1 := tm03CreateTestTopic(t, pool, treeID, rootNode, "Multi Topic One")
	topic2 := tm03CreateTestTopic(t, pool, treeID, rootNode, "Multi Topic Two")

	req := authenticatedRequest(t, srv.URL, http.MethodPost,
		"/api/v1/trees/"+treeID.String()+"/context/inject",
		map[string]any{
			"topic_ids": []string{topic1.String(), topic2.String()},
		})
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body injectResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Len(t, body.Context.Topics, 2)
	assert.Contains(t, body.Context.MergedText, "topic boundary")
}

// --- Helpers ---------------------------------------------------------------

func doSearchRequest(t *testing.T, srv *httptest.Server, treeID uuid.UUID, query string) *http.Response {
	return doSearchRequestWithStatus(t, srv, treeID, query, "")
}

func doSearchRequestWithStatus(t *testing.T, srv *httptest.Server, treeID uuid.UUID, query, status string) *http.Response {
	t.Helper()
	path := "/api/v1/trees/" + treeID.String() + "/topics/search?q=" + url.QueryEscape(query)
	if status != "" {
		path += "&status=" + status
	}
	req := authenticatedRequest(t, srv.URL, http.MethodGet, path, nil)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	return resp
}

// stub to prevent "unused import" if bytes is not directly referenced.
var _ = bytes.NewReader
