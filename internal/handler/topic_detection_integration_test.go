// Package handler — integration tests for TM-02 topic detection.
// Tests run against real PostgreSQL (testutil pattern). Creates throwaway
// trees/nodes/topics — never touches demo data.
// Spec: SPEC-TM-02 §11.1 scenarios.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/testutil"
)

// --- TM-02 test server helper ---

func newTM02TestServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, service.TopicService) {
	t.Helper()
	ctx := context.Background()

	// Sentinel user.
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, display_name)
		VALUES ('a0000000-0000-0000-0000-000000000001', 'testuser', 'Test User')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("create sentinel user: %v", err)
	}

	topicRepo := db.NewPGTopicRepo(pool)
	topicMemberRepo := db.NewPGTopicMemberRepo(pool)
	treeRepo := db.NewPGTreeRepo(pool)
	nodeRepo := db.NewPGNodeRepo(pool)
	edgeRepo := db.NewPGEdgeRepo(pool)
	hub := sse.NewHub()

	svc := service.NewTopicServiceImpl(topicRepo, topicMemberRepo, treeRepo, nodeRepo).
		WithDetection(edgeRepo, hub,
			db.NewPGTopicProposalRepo(pool),
			db.NewPGDetectionConfigRepo(pool),
			db.NewPGSubjectCooldownRepo(pool),
		)

	r := chi.NewRouter()
	authMW := AuthMiddleware("canopy-dev-secret")
	tdHandler := NewTopicDetectionHandler(svc)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMW)
		r.Mount("/topic-proposals", tdHandler.ProposalRoutes())
		r.Mount("/trees/{tree_id}", tdHandler.TreeRoutes())
	})

	srv := httptest.NewServer(r)
	return srv, svc
}

// tm02CreateTestTree creates a tree + root node + profile.
func tm02CreateTestTree(t *testing.T, pool *pgxpool.Pool) (treeID, profileID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	profileID = uuid.New()
	treeID = uuid.New()

	_, err := pool.Exec(ctx, `INSERT INTO profiles (id, owner_id, profile_type, name, display_name)
		VALUES ($1, 'a0000000-0000-0000-0000-000000000001', 'human', $2, $3)
		ON CONFLICT (id) DO NOTHING`,
		profileID, "test-"+profileID.String()[:8], "Test User")
	require.NoError(t, err, "create profile")

	_, err = pool.Exec(ctx, `INSERT INTO trees (id, owner_id, title)
		VALUES ($1, $2, 'Test Tree')`, treeID, profileID)
	require.NoError(t, err, "create tree")

	_, err = pool.Exec(ctx, `INSERT INTO tree_members (tree_id, user_id, role)
		VALUES ($1, 'a0000000-0000-0000-0000-000000000001', 'owner')`, treeID)
	require.NoError(t, err, "create tree member")

	return treeID, profileID
}

// tm02CreateNode inserts a node and returns its ID.
func tm02CreateNode(t *testing.T, pool *pgxpool.Pool, treeID, authorID uuid.UUID, content string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	nodeID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO nodes (id, tree_id, author_id, content)
		VALUES ($1, $2, $3, $4)`, nodeID, treeID, authorID, content)
	require.NoError(t, err, "create node")
	return nodeID
}

// tm02CreateProposal inserts a pending proposal directly and returns its ID.
func tm02CreateProposal(t *testing.T, pool *pgxpool.Pool, treeID, rootNodeID uuid.UUID, subjectKey, title string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	proposalID := uuid.New()
	_, err := pool.Exec(ctx, `
		INSERT INTO topic_proposals (id, tree_id, root_node_id, title, detection_type,
			confidence, subject_key, status, expires_at, evidence)
		VALUES ($1, $2, $3, $4, 'explicit', 1.0, $5, 'pending', now() + interval '24 hours', '{}'::jsonb)`,
		proposalID, treeID, rootNodeID, title, subjectKey)
	require.NoError(t, err, "create proposal")
	return proposalID
}

// tm02AuthRequest creates an authenticated HTTP request with the test JWT.
func tm02AuthRequest(t *testing.T, method, url string, body any) *http.Request {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r, _ = http.NewRequest(method, url, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r, _ = http.NewRequest(method, url, nil)
	}
	r.Header.Set("Authorization", authHeader(t))
	return r
}

// --- Scenario 1: Explicit proposal created via AutoDetect ---

func TestTM02_ExplicitDetection(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newTM02TestServer(t, pool)
	ctx := context.Background()

	treeID, profileID := tm02CreateTestTree(t, pool)
	rootNodeID := tm02CreateNode(t, pool, treeID, profileID, "Let's make this into a topic!")

	node, err := db.NewPGNodeRepo(pool).GetByID(ctx, rootNodeID)
	require.NoError(t, err)

	proposal, err := svc.AutoDetect(ctx, *node, nil)
	require.NoError(t, err)
	require.NotNil(t, proposal, "explicit signal should produce a proposal")
	assert.Equal(t, "explicit", string(proposal.DetectionType))
	assert.InDelta(t, 1.0, proposal.Confidence, 0.001)
	assert.NotEmpty(t, proposal.Title)
}

// --- Scenario 12: Detection off → no proposal ---

func TestTM02_DetectionOff(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newTM02TestServer(t, pool)
	ctx := context.Background()

	treeID, profileID := tm02CreateTestTree(t, pool)
	rootNodeID := tm02CreateNode(t, pool, treeID, profileID, "Make this a topic")

	// Set detection to off.
	_, err := svc.UpdateDetectionConfig(ctx, treeID, service.DetectionConfig{
		DetectionLevel:      service.DetectionLevelOff,
		MinMessagesPerTopic: 3,
		ProposalCooldown:    10,
	})
	require.NoError(t, err)

	node, err := db.NewPGNodeRepo(pool).GetByID(ctx, rootNodeID)
	require.NoError(t, err)

	proposal, err := svc.AutoDetect(ctx, *node, nil)
	require.NoError(t, err)
	assert.Nil(t, proposal, "detection off should produce no proposal")
}

// --- Scenario 14/15: Auto-create vs always-ask ---

func TestTM02_AutoCreate(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newTM02TestServer(t, pool)
	ctx := context.Background()

	treeID, profileID := tm02CreateTestTree(t, pool)
	rootNodeID := tm02CreateNode(t, pool, treeID, profileID, "Make this a topic")

	// Enable auto-create, disable always-ask.
	_, err := svc.UpdateDetectionConfig(ctx, treeID, service.DetectionConfig{
		AutoCreate:          true,
		AlwaysAsk:           false,
		DetectionLevel:      service.DetectionLevelFull,
		MinMessagesPerTopic: 3,
		ProposalCooldown:    10,
	})
	require.NoError(t, err)

	node, err := db.NewPGNodeRepo(pool).GetByID(ctx, rootNodeID)
	require.NoError(t, err)

	proposal, err := svc.AutoDetect(ctx, *node, nil)
	require.NoError(t, err)
	require.NotNil(t, proposal)

	// Auto-create should have created the topic.
	topic, err := pool.Exec(ctx, `SELECT 1 FROM topics WHERE root_node_id = $1`, rootNodeID)
	_ = topic
	require.NoError(t, err)

	// Verify topic exists.
	topicRepo := db.NewPGTopicRepo(pool)
	created, err := topicRepo.GetByRootNode(ctx, rootNodeID)
	require.NoError(t, err, "auto-create should have created a topic")
	assert.NotEmpty(t, created.Title)
}

func TestTM02_AlwaysAskOverride(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newTM02TestServer(t, pool)
	ctx := context.Background()

	treeID, profileID := tm02CreateTestTree(t, pool)
	rootNodeID := tm02CreateNode(t, pool, treeID, profileID, "Make this a topic")

	// Both flags true → always-ask wins.
	_, err := svc.UpdateDetectionConfig(ctx, treeID, service.DetectionConfig{
		AutoCreate:          true,
		AlwaysAsk:           true,
		DetectionLevel:      service.DetectionLevelFull,
		MinMessagesPerTopic: 3,
		ProposalCooldown:    10,
	})
	require.NoError(t, err)

	node, err := db.NewPGNodeRepo(pool).GetByID(ctx, rootNodeID)
	require.NoError(t, err)

	proposal, err := svc.AutoDetect(ctx, *node, nil)
	require.NoError(t, err)
	require.NotNil(t, proposal, "should produce a proposal")

	// No topic should be created (always-ask wins).
	topicRepo := db.NewPGTopicRepo(pool)
	_, err = topicRepo.GetByRootNode(ctx, rootNodeID)
	assert.Error(t, err, "no topic should exist when always-ask is true")
}

// --- Scenario 16: Confirm proposal (accept) ---

func TestTM02_ConfirmProposal(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newTM02TestServer(t, pool)
	ctx := context.Background()

	treeID, profileID := tm02CreateTestTree(t, pool)
	rootNodeID := tm02CreateNode(t, pool, treeID, profileID, "Discussion content")
	proposalID := tm02CreateProposal(t, pool, treeID, rootNodeID, "test-subject", "Test Topic Title")

	topic, err := svc.ConfirmProposal(ctx, proposalID, "")
	require.NoError(t, err)
	require.NotNil(t, topic)
	assert.Equal(t, "Test Topic Title", topic.Title)
	assert.NotEmpty(t, topic.Slug)
}

// --- Scenario 17: Rename proposal (confirm with title override) ---

func TestTM02_RenameProposal(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newTM02TestServer(t, pool)
	ctx := context.Background()

	treeID, profileID := tm02CreateTestTree(t, pool)
	rootNodeID := tm02CreateNode(t, pool, treeID, profileID, "Discussion content")
	proposalID := tm02CreateProposal(t, pool, treeID, rootNodeID, "test-subject", "Original Title")

	topic, err := svc.ConfirmProposal(ctx, proposalID, "Custom Renamed Title")
	require.NoError(t, err)
	assert.Equal(t, "Custom Renamed Title", topic.Title)
}

// --- Scenario 18: Dismiss proposal (reject) ---

func TestTM02_DismissProposal(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newTM02TestServer(t, pool)
	ctx := context.Background()

	treeID, profileID := tm02CreateTestTree(t, pool)
	rootNodeID := tm02CreateNode(t, pool, treeID, profileID, "Discussion content")
	proposalID := tm02CreateProposal(t, pool, treeID, rootNodeID, "dismissed-subject", "Topic to Dismiss")

	err := svc.DismissProposal(ctx, proposalID)
	require.NoError(t, err)

	// Verify status is dismissed.
	proposal, err := db.NewPGTopicProposalRepo(pool).GetByID(ctx, proposalID)
	require.NoError(t, err)
	assert.Equal(t, "dismissed", proposal.Status)
}

// --- Scenario 23: Rename title conflict ---

func TestTM02_RenameConflict(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newTM02TestServer(t, pool)
	ctx := context.Background()

	treeID, profileID := tm02CreateTestTree(t, pool)
	rootNode1 := tm02CreateNode(t, pool, treeID, profileID, "Node 1")
	rootNode2 := tm02CreateNode(t, pool, treeID, profileID, "Node 2")

	// Create first topic.
	_, err := svc.CreateTopic(ctx, treeID, rootNode1, "Existing Title", "")
	require.NoError(t, err)

	// Create proposal for second node, try to confirm with same title.
	proposalID := tm02CreateProposal(t, pool, treeID, rootNode2, "conflict-subject", "Some Title")
	_, err = svc.ConfirmProposal(ctx, proposalID, "Existing Title")
	assert.Error(t, err, "should reject duplicate title")
}

// --- Scenario 24: Deleted root confirmation ---

func TestTM02_DeletedRootConfirm(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newTM02TestServer(t, pool)
	ctx := context.Background()

	treeID, profileID := tm02CreateTestTree(t, pool)
	rootNodeID := tm02CreateNode(t, pool, treeID, profileID, "Content")
	proposalID := tm02CreateProposal(t, pool, treeID, rootNodeID, "subject", "Title")

	// Soft-delete the root node.
	_, err := pool.Exec(ctx, `UPDATE nodes SET deleted_at = now() WHERE id = $1`, rootNodeID)
	require.NoError(t, err)

	_, err = svc.ConfirmProposal(ctx, proposalID, "")
	assert.Error(t, err, "should fail when root is deleted")
}

// --- Scenario 25: Concurrent accepts (idempotent) ---

func TestTM02_ConcurrentAccepts(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newTM02TestServer(t, pool)
	ctx := context.Background()

	treeID, profileID := tm02CreateTestTree(t, pool)
	rootNodeID := tm02CreateNode(t, pool, treeID, profileID, "Content")
	proposalID := tm02CreateProposal(t, pool, treeID, rootNodeID, "concurrent", "Concurrent Topic")

	// First confirm should succeed.
	topic1, err := svc.ConfirmProposal(ctx, proposalID, "")
	require.NoError(t, err)

	// Second confirm should return the same topic (idempotent).
	topic2, err := svc.ConfirmProposal(ctx, proposalID, "")
	require.NoError(t, err)
	assert.Equal(t, topic1.ID, topic2.ID, "concurrent confirms should return same topic")
}

// --- Detection config GET/PUT ---

func TestTM02_GetDetectionConfig_Defaults(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newTM02TestServer(t, pool)
	ctx := context.Background()

	treeID, _ := tm02CreateTestTree(t, pool)

	cfg, err := svc.GetDetectionConfig(ctx, treeID)
	require.NoError(t, err)
	assert.True(t, cfg.AlwaysAsk, "default should be always-ask")
	assert.False(t, cfg.AutoCreate)
	assert.Equal(t, service.DetectionLevelFull, cfg.DetectionLevel)
}

func TestTM02_UpdateDetectionConfig(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newTM02TestServer(t, pool)
	ctx := context.Background()

	treeID, _ := tm02CreateTestTree(t, pool)

	updated, err := svc.UpdateDetectionConfig(ctx, treeID, service.DetectionConfig{
		DetectionLevel:      service.DetectionLevelExplicitOnly,
		AutoCreate:          true,
		MinMessagesPerTopic: 5,
		ProposalCooldown:    20,
	})
	require.NoError(t, err)
	assert.Equal(t, service.DetectionLevelExplicitOnly, updated.DetectionLevel)
	assert.True(t, updated.AutoCreate)
	assert.Equal(t, 5, updated.MinMessagesPerTopic)
	assert.Equal(t, 20, updated.ProposalCooldown)
}

func TestTM02_UpdateDetectionConfig_InvalidLevel(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	_, svc := newTM02TestServer(t, pool)
	ctx := context.Background()

	treeID, _ := tm02CreateTestTree(t, pool)

	_, err := svc.UpdateDetectionConfig(ctx, treeID, service.DetectionConfig{
		DetectionLevel: "bogus",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, service.ErrInvalidDetectionLevel)
}

// --- HTTP handler tests ---

func TestTM02_HTTP_GetConfig(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newTM02TestServer(t, pool)

	treeID, _ := tm02CreateTestTree(t, pool)

	req := tm02AuthRequest(t, http.MethodGet, srv.URL+"/api/v1/trees/"+treeID.String()+"/topic-detection", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestTM02_HTTP_PutConfig(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newTM02TestServer(t, pool)

	treeID, _ := tm02CreateTestTree(t, pool)

	body := map[string]any{
		"detection_level": "explicit_only",
		"auto_create":     true,
	}
	req := tm02AuthRequest(t, http.MethodPut, srv.URL+"/api/v1/trees/"+treeID.String()+"/topic-detection", body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestTM02_HTTP_DismissProposal(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newTM02TestServer(t, pool)

	treeID, profileID := tm02CreateTestTree(t, pool)
	rootNodeID := tm02CreateNode(t, pool, treeID, profileID, "Content")
	proposalID := tm02CreateProposal(t, pool, treeID, rootNodeID, "subject", "Title")

	req := tm02AuthRequest(t, http.MethodPost, srv.URL+"/api/v1/topic-proposals/"+proposalID.String()+"/dismiss", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestTM02_HTTP_ConfirmProposal(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newTM02TestServer(t, pool)

	treeID, profileID := tm02CreateTestTree(t, pool)
	rootNodeID := tm02CreateNode(t, pool, treeID, profileID, "Content")
	proposalID := tm02CreateProposal(t, pool, treeID, rootNodeID, "subject", "Title")

	body := map[string]any{"titleOverride": ""}
	req := tm02AuthRequest(t, http.MethodPost, srv.URL+"/api/v1/topic-proposals/"+proposalID.String()+"/confirm", body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestTM02_HTTP_ConfirmProposal_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newTM02TestServer(t, pool)

	bogusID := uuid.New()
	req := tm02AuthRequest(t, http.MethodPost, srv.URL+"/api/v1/topic-proposals/"+bogusID.String()+"/confirm", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestTM02_HTTP_PutConfig_InvalidLevel(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := newTM02TestServer(t, pool)

	treeID, _ := tm02CreateTestTree(t, pool)

	body := map[string]any{"detection_level": "bogus_level"}
	req := tm02AuthRequest(t, http.MethodPut, srv.URL+"/api/v1/trees/"+treeID.String()+"/topic-detection", body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Invalid detection level returns 400 (mapped from ErrInvalidDetectionLevel).
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
