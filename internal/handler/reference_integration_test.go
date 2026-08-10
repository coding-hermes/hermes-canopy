// Package handler — integration tests for TM-04 reference resolution.
// Tests run against real PostgreSQL (testutil pattern). Creates throwaway
// trees/topics/nodes — never touches demo data.
// Spec: SPEC-TM-04 §11.1 scenarios 4-30 (backend), §6 API contract.
package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/reference"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/testutil"
)

// ── Test server ───────────────────────────────────────────────────────────

func newReferenceTestServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, reference.ReferenceService) {
	t.Helper()
	ctx := context.Background()

	// Create sentinel user for FK (profiles.owner_id → users.id).
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, display_name)
		VALUES ('a0000000-0000-0000-0000-000000000001', 'testuser', 'Test User')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("create sentinel user: %v", err)
	}

	searchRepo := db.NewPGTopicSearchRepo(pool)
	refRepo := db.NewPGReferenceRepo(pool)
	svc := reference.NewReferenceService(refRepo, searchRepo)
	hub := sse.NewHub()

	r := chi.NewRouter()
	authMW := AuthMiddleware("canopy-dev-secret")
	refHandler := NewReferenceHandler(svc, hub)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMW)
		r.Mount("/trees/{tree_id}", refHandler.Routes())
	})

	srv := httptest.NewServer(r)
	return srv, svc
}

// ── Helpers ───────────────────────────────────────────────────────────────

func tm04CreateTestTree(t *testing.T, pool *pgxpool.Pool) (treeID, profileID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	// Ensure sentinel user exists for FK (profiles.owner_id → users.id).
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, display_name)
		VALUES ('a0000000-0000-0000-0000-000000000001', 'testuser', 'Test User')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("create sentinel user: %v", err)
	}

	return tm03CreateTestTree(t, pool)
}

func tm04CreateTestNode(t *testing.T, pool *pgxpool.Pool, treeID, authorID uuid.UUID, content string) uuid.UUID {
	return tm03CreateTestNode(t, pool, treeID, authorID, content)
}

func tm04CreateTestTopicWithSlug(t *testing.T, pool *pgxpool.Pool, treeID, rootNodeID uuid.UUID, title, slug string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	topicID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO topics (id, tree_id, root_node_id, title, slug)
		VALUES ($1, $2, $3, $4, $5)`,
		topicID, treeID, rootNodeID, title, slug)
	require.NoError(t, err, "create topic")
	return topicID
}

func tm04ArchiveTopic(t *testing.T, pool *pgxpool.Pool, topicID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `UPDATE topics SET status = 'archived', archived_at = clock_timestamp() WHERE id = $1`, topicID)
	require.NoError(t, err, "archive topic")
}

func tm04DoRequest(t *testing.T, srv *httptest.Server, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	req := authenticatedRequest(t, srv.URL, method, path, body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, respBody
}

func tm04AutocompletePath(treeID uuid.UUID, prefix string) string {
	return "/api/v1/trees/" + treeID.String() + "/references/autocomplete?prefix=" + prefix
}

func tm04ResolvePath(treeID uuid.UUID) string {
	return "/api/v1/trees/" + treeID.String() + "/references/resolve"
}

func tm04InjectPath(treeID uuid.UUID) string {
	return "/api/v1/trees/" + treeID.String() + "/references/inject"
}

// ── Scenario 4: Autocomplete exact prefix ─────────────────────────────────

func TestTM04_AutocompletePrefix(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newReferenceTestServer(t, pool)

	treeID, profileID := tm04CreateTestTree(t, pool)
	root1 := tm04CreateTestNode(t, pool, treeID, profileID, "Root 1")
	root2 := tm04CreateTestNode(t, pool, treeID, profileID, "Root 2")
	root3 := tm04CreateTestNode(t, pool, treeID, profileID, "Root 3")
	tm04CreateTestTopicWithSlug(t, pool, treeID, root1, "Database Schema", "database-schema")
	tm04CreateTestTopicWithSlug(t, pool, treeID, root2, "Data Model", "data-model")
	tm04CreateTestTopicWithSlug(t, pool, treeID, root3, "Data Flow", "data-flow")

	resp, body := tm04DoRequest(t, srv, "GET", tm04AutocompletePath(treeID, "dat"), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	results := result["results"].([]any)
	assert.Equal(t, 3, len(results), "expected 3 prefix matches")
}

// Scenario 6: Autocomplete excludes archived topics by default.
func TestTM04_AutocompleteExcludesArchived(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newReferenceTestServer(t, pool)

	treeID, profileID := tm04CreateTestTree(t, pool)
	root := tm04CreateTestNode(t, pool, treeID, profileID, "Root")
	topicID := tm04CreateTestTopicWithSlug(t, pool, treeID, root, "Archived Topic", "archived-topic")
	tm04ArchiveTopic(t, pool, topicID)

	// include=active (default) → excluded
	resp, body := tm04DoRequest(t, srv, "GET", tm04AutocompletePath(treeID, "arch"), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	results := result["results"].([]any)
	assert.Equal(t, 0, len(results), "archived topic should be excluded by default")

	// include=all → included
	resp, body = tm04DoRequest(t, srv, "GET",
		"/api/v1/trees/"+treeID.String()+"/references/autocomplete?prefix=arch&include=all", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.Unmarshal(body, &result))
	results = result["results"].([]any)
	assert.Equal(t, 1, len(results), "archived topic should be included with include=all")
}

// Scenario 7: Resolve existing reference.
func TestTM04_ResolveExisting(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newReferenceTestServer(t, pool)

	treeID, profileID := tm04CreateTestTree(t, pool)
	root := tm04CreateTestNode(t, pool, treeID, profileID, "Root")
	tm04CreateTestTopicWithSlug(t, pool, treeID, root, "Database Schema", "database-schema")

	resp, body := tm04DoRequest(t, srv, "POST", tm04ResolvePath(treeID), map[string]any{
		"content": "See #database-schema",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	refs := result["references"].([]any)
	assert.Equal(t, 1, len(refs), "expected 1 resolved reference")
	nf := result["not_found"]
	assert.Nil(t, nf, "expected no not_found")
}

// Scenario 8: Resolve non-existent reference.
func TestTM04_ResolveNotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newReferenceTestServer(t, pool)

	treeID, _ := tm04CreateTestTree(t, pool)

	resp, body := tm04DoRequest(t, srv, "POST", tm04ResolvePath(treeID), map[string]any{
		"content": "See #missing-topic",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	nf := result["not_found"].([]any)
	assert.Equal(t, 1, len(nf), "expected 1 not_found")
	refs := result["references"].([]any)
	assert.Equal(t, 0, len(refs), "expected 0 resolved")
}

// Scenario 9: Resolve mixed valid/invalid.
func TestTM04_ResolveMixed(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newReferenceTestServer(t, pool)

	treeID, profileID := tm04CreateTestTree(t, pool)
	root := tm04CreateTestNode(t, pool, treeID, profileID, "Root")
	tm04CreateTestTopicWithSlug(t, pool, treeID, root, "Database Schema", "database-schema")

	resp, body := tm04DoRequest(t, srv, "POST", tm04ResolvePath(treeID), map[string]any{
		"content": "#database-schema and #missing-topic",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	refs := result["references"].([]any)
	nf := result["not_found"].([]any)
	assert.Equal(t, 1, len(refs))
	assert.Equal(t, 1, len(nf))
	assert.Equal(t, false, result["too_many"])
}

// Scenario 10: Resolve over hard cap (>10 references) → 400 error.
func TestTM04_ResolveOverHardCap(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newReferenceTestServer(t, pool)

	treeID, _ := tm04CreateTestTree(t, pool)

	// Create 12 distinct references.
	content := ""
	for i := 0; i < 12; i++ {
		if i > 0 {
			content += " "
		}
		content += "#topic" + string(rune('a'+i))
	}

	resp, body := tm04DoRequest(t, srv, "POST", tm04ResolvePath(treeID), map[string]any{
		"content": content,
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)

	var errResp map[string]any
	require.NoError(t, json.Unmarshal(body, &errResp))
	errObj := errResp["error"].(map[string]any)
	assert.Contains(t, errObj["code"], "REFERENCES_TOO_MANY")
}

// Scenario 13: Inject with references only.
func TestTM04_InjectReferencesOnly(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newReferenceTestServer(t, pool)

	treeID, profileID := tm04CreateTestTree(t, pool)
	root := tm04CreateTestNode(t, pool, treeID, profileID, "Root")
	tm04CreateTestTopicWithSlug(t, pool, treeID, root, "Database Schema", "database-schema")

	resp, body := tm04DoRequest(t, srv, "POST", tm04InjectPath(treeID), map[string]any{
		"references": []string{"#database-schema"},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	ctx := result["context"].(map[string]any)
	topics := ctx["topics"].([]any)
	assert.Equal(t, 1, len(topics), "expected 1 topic in context")
}

// Scenario 15: Inject over 5-topic limit.
func TestTM04_InjectOverTopicLimit(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newReferenceTestServer(t, pool)

	treeID, profileID := tm04CreateTestTree(t, pool)

	// Create 6 topics.
	topicIDs := make([]string, 6)
	for i := 0; i < 6; i++ {
		root := tm04CreateTestNode(t, pool, treeID, profileID, "Root "+string(rune('a'+i)))
		id := tm04CreateTestTopicWithSlug(t, pool, treeID, root, "Topic "+string(rune('A'+i)), "topic-"+string(rune('a'+i)))
		topicIDs[i] = id.String()
	}

	resp, body := tm04DoRequest(t, srv, "POST", tm04InjectPath(treeID), map[string]any{
		"topic_ids": topicIDs,
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)
}

// Scenario 15b: Invalid input — no topic_ids or references.
func TestTM04_InjectInvalidInput(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newReferenceTestServer(t, pool)

	treeID, _ := tm04CreateTestTree(t, pool)

	resp, body := tm04DoRequest(t, srv, "POST", tm04InjectPath(treeID), map[string]any{})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)

	var errResp map[string]any
	require.NoError(t, json.Unmarshal(body, &errResp))
	errObj := errResp["error"].(map[string]any)
	assert.Contains(t, errObj["code"], "REFERENCES_INVALID_INPUT")
}

// Scenario 19: Reference resolution log is recorded.
func TestTM04_ReferenceLogRecorded(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newReferenceTestServer(t, pool)

	treeID, profileID := tm04CreateTestTree(t, pool)
	root := tm04CreateTestNode(t, pool, treeID, profileID, "Root")
	tm04CreateTestTopicWithSlug(t, pool, treeID, root, "Database Schema", "database-schema")

	resp, _ := tm04DoRequest(t, srv, "POST", tm04ResolvePath(treeID), map[string]any{
		"content": "See #database-schema and #missing",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify log entries were written.
	ctx := context.Background()
	var logCount int
	err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reference_resolution_log WHERE tree_id = $1`, treeID).Scan(&logCount)
	require.NoError(t, err)
	assert.Equal(t, 2, logCount, "expected 2 log entries (resolved + not_found)")

	// Verify the statuses.
	var resolvedCount, notFoundCount int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reference_resolution_log WHERE tree_id = $1 AND status = 'resolved'`, treeID).Scan(&resolvedCount)
	require.NoError(t, err)
	assert.Equal(t, 1, resolvedCount)

	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reference_resolution_log WHERE tree_id = $1 AND status = 'not_found'`, treeID).Scan(&notFoundCount)
	require.NoError(t, err)
	assert.Equal(t, 1, notFoundCount)
}

// Scenario 20: Reference count denormalization — ref_count on topics.
func TestTM04_RefCountDenormalization(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	treeID, profileID := tm04CreateTestTree(t, pool)
	root := tm04CreateTestNode(t, pool, treeID, profileID, "Root")
	topicID := tm04CreateTestTopicWithSlug(t, pool, treeID, root, "Database Schema", "database-schema")

	node1 := tm04CreateTestNode(t, pool, treeID, profileID, "Node 1")

	refRepo := db.NewPGReferenceRepo(pool)
	_, err := refRepo.InsertResolvedRef(context.Background(), reference.ResolvedReferenceLink{
		NodeID:     node1,
		TreeID:     treeID,
		TopicID:    topicID,
		RawRef:     "#database-schema",
		Slug:       "database-schema",
		ResolvedBy: profileID,
	})
	require.NoError(t, err)

	// topics.ref_count should be 1 (trigger fires).
	ctx := context.Background()
	var refCount int
	err = pool.QueryRow(ctx, `SELECT ref_count FROM topics WHERE id = $1`, topicID).Scan(&refCount)
	require.NoError(t, err)
	assert.Equal(t, 1, refCount, "topics.ref_count should be 1 after insert")

	// nodes.resolved_ref_count should be 1 too.
	var nodeRefCount int
	err = pool.QueryRow(ctx, `SELECT resolved_ref_count FROM nodes WHERE id = $1`, node1).Scan(&nodeRefCount)
	require.NoError(t, err)
	assert.Equal(t, 1, nodeRefCount, "nodes.resolved_ref_count should be 1")

	// Delete → count back to 0.
	err = refRepo.DeleteResolvedRefsForNode(ctx, node1)
	require.NoError(t, err)

	err = pool.QueryRow(ctx, `SELECT ref_count FROM topics WHERE id = $1`, topicID).Scan(&refCount)
	require.NoError(t, err)
	assert.Equal(t, 0, refCount, "topics.ref_count should be 0 after delete")

	err = pool.QueryRow(ctx, `SELECT resolved_ref_count FROM nodes WHERE id = $1`, node1).Scan(&nodeRefCount)
	require.NoError(t, err)
	assert.Equal(t, 0, nodeRefCount, "nodes.resolved_ref_count should be 0 after delete")
}

// Scenario 11: ResolveAtSend persists links + bumps ref counts.
func TestTM04_ResolveAtSendPersists(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newReferenceTestServer(t, pool)

	treeID, profileID := tm04CreateTestTree(t, pool)
	root := tm04CreateTestNode(t, pool, treeID, profileID, "Root")
	topicID := tm04CreateTestTopicWithSlug(t, pool, treeID, root, "Database Schema", "database-schema")
	nodeID := tm04CreateTestNode(t, pool, treeID, profileID, "Message with #database-schema")

	result, err := svc.ResolveAtSend(context.Background(), treeID, nodeID, "Message with #database-schema", profileID)
	require.NoError(t, err)
	assert.Equal(t, 1, len(result.References))
	assert.Equal(t, 0, len(result.NotFound))

	// Verify node_resolved_refs row exists.
	ctx := context.Background()
	var nrrCount int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM node_resolved_refs WHERE node_id = $1 AND topic_id = $2`,
		nodeID, topicID).Scan(&nrrCount)
	require.NoError(t, err)
	assert.Equal(t, 1, nrrCount, "node_resolved_refs should have 1 row")

	// Verify topics.ref_count was bumped.
	var refCount int
	err = pool.QueryRow(ctx, `SELECT ref_count FROM topics WHERE id = $1`, topicID).Scan(&refCount)
	require.NoError(t, err)
	assert.Equal(t, 1, refCount, "topics.ref_count should be 1")
}

// Scenario 12: ResolveAtSend deduplicates.
func TestTM04_ResolveAtSendDedupes(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newReferenceTestServer(t, pool)

	treeID, profileID := tm04CreateTestTree(t, pool)
	root := tm04CreateTestNode(t, pool, treeID, profileID, "Root")
	topicID := tm04CreateTestTopicWithSlug(t, pool, treeID, root, "Database Schema", "database-schema")
	nodeID := tm04CreateTestNode(t, pool, treeID, profileID, "Message")

	content := "#database-schema twice #database-schema"
	result, err := svc.ResolveAtSend(context.Background(), treeID, nodeID, content, profileID)
	require.NoError(t, err)
	assert.Equal(t, 1, len(result.References), "expected 1 deduped reference")

	// Only 1 node_resolved_refs row.
	ctx := context.Background()
	var nrrCount int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM node_resolved_refs WHERE node_id = $1`, nodeID).Scan(&nrrCount)
	require.NoError(t, err)
	assert.Equal(t, 1, nrrCount, "should have 1 deduped row")

	// ref_count should be 1, not 2.
	var refCount int
	err = pool.QueryRow(ctx, `SELECT ref_count FROM topics WHERE id = $1`, topicID).Scan(&refCount)
	require.NoError(t, err)
	assert.Equal(t, 1, refCount)
}

// Scenario 14: Inject with topic IDs and references dedupes.
func TestTM04_InjectDedupeTopicAndRef(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newReferenceTestServer(t, pool)

	treeID, profileID := tm04CreateTestTree(t, pool)
	root := tm04CreateTestNode(t, pool, treeID, profileID, "Root")
	topicID := tm04CreateTestTopicWithSlug(t, pool, treeID, root, "Database Schema", "database-schema")

	merged, err := svc.InjectWithReferences(context.Background(), treeID, reference.InjectWithReferencesRequest{
		TopicIDs:   []uuid.UUID{topicID},
		References: []string{"#database-schema"},
	}, profileID)
	require.NoError(t, err)
	assert.Equal(t, 1, len(merged.Topics), "should dedupe topic_id + reference to same topic")
}

// Autocomplete prefix too short → 400.
func TestTM04_AutocompletePrefixTooShort(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newReferenceTestServer(t, pool)

	treeID, _ := tm04CreateTestTree(t, pool)

	resp, body := tm04DoRequest(t, srv, "GET",
		"/api/v1/trees/"+treeID.String()+"/references/autocomplete", nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)

	var errResp map[string]any
	require.NoError(t, json.Unmarshal(body, &errResp))
	errObj := errResp["error"].(map[string]any)
	assert.Equal(t, "REFERENCE_PREFIX_TOO_SHORT", errObj["code"])
}

// Autocomplete invalid include → 400.
func TestTM04_AutocompleteInvalidInclude(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newReferenceTestServer(t, pool)

	treeID, _ := tm04CreateTestTree(t, pool)

	resp, _ := tm04DoRequest(t, srv, "GET",
		"/api/v1/trees/"+treeID.String()+"/references/autocomplete?prefix=test&include=invalid", nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// Cache: get on a non-existent cache returns nil, not error.
func TestTM04_CacheMissReturnsNil(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	refRepo := db.NewPGReferenceRepo(pool)

	entry, err := refRepo.GetReferenceCache(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Nil(t, entry, "cache miss should return nil, not error")
}

// Cache: upsert + get works.
func TestTM04_CacheUpsertAndGet(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	refRepo := db.NewPGReferenceRepo(pool)

	treeID, _ := tm04CreateTestTree(t, pool)
	root := tm04CreateTestNode(t, pool, treeID, uuid.New(), "Root")
	topicID := tm04CreateTestTopicWithSlug(t, pool, treeID, root, "Test Topic", "test-topic")

	payload := json.RawMessage(`{"topic_id":"` + topicID.String() + `"}`)
	err := refRepo.UpsertReferenceCache(context.Background(), topicID, treeID, "hash123", 5, payload)
	require.NoError(t, err)

	entry, err := refRepo.GetReferenceCache(context.Background(), topicID)
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "hash123", entry.ContextHash)
	assert.Equal(t, 5, entry.NodeCount)
	assert.Equal(t, 1, entry.HitCount, "hit_count should be 1 after first get")
}

// Topic by slug lookup.
func TestTM04_GetTopicBySlug(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	refRepo := db.NewPGReferenceRepo(pool)

	treeID, _ := tm04CreateTestTree(t, pool)
	root := tm04CreateTestNode(t, pool, treeID, uuid.New(), "Root")
	topicID := tm04CreateTestTopicWithSlug(t, pool, treeID, root, "Database Schema", "db-schema")

	topic, err := refRepo.GetTopicBySlug(context.Background(), treeID, "db-schema")
	require.NoError(t, err)
	require.NotNil(t, topic)
	assert.Equal(t, topicID, topic.ID)
	assert.Equal(t, "Database Schema", topic.Title)

	// Non-existent slug returns nil.
	missing, err := refRepo.GetTopicBySlug(context.Background(), treeID, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

// Resolved refs for node.
func TestTM04_GetResolvedRefsForNode(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	refRepo := db.NewPGReferenceRepo(pool)

	treeID, profileID := tm04CreateTestTree(t, pool)
	root1 := tm04CreateTestNode(t, pool, treeID, profileID, "Root 1")
	root2 := tm04CreateTestNode(t, pool, treeID, profileID, "Root 2")
	topic1 := tm04CreateTestTopicWithSlug(t, pool, treeID, root1, "Topic 1", "topic-one")
	topic2 := tm04CreateTestTopicWithSlug(t, pool, treeID, root2, "Topic 2", "topic-two")
	nodeID := tm04CreateTestNode(t, pool, treeID, profileID, "Message")

	_, err := refRepo.InsertResolvedRef(context.Background(), reference.ResolvedReferenceLink{
		NodeID: nodeID, TreeID: treeID, TopicID: topic1,
		RawRef: "#topic-one", Slug: "topic-one", ResolvedBy: profileID,
	})
	require.NoError(t, err)
	_, err = refRepo.InsertResolvedRef(context.Background(), reference.ResolvedReferenceLink{
		NodeID: nodeID, TreeID: treeID, TopicID: topic2,
		RawRef: "#topic-two", Slug: "topic-two", ResolvedBy: profileID,
	})
	require.NoError(t, err)

	links, err := refRepo.GetResolvedRefsForNode(context.Background(), nodeID)
	require.NoError(t, err)
	assert.Equal(t, 2, len(links))
}
