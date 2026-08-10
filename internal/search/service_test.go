// Package search — tests for the context compiler and service logic.
// Spec: SPEC-TM-03 §4.5, §10.1 scenarios.
package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Context Compiler Tests (spec §4.5, scenarios 18-20) ------------------

func TestCompileMultiTopicContext_BoundaryMarkers(t *testing.T) {
	contexts := []TopicContext{
		{
			TopicID:    uuid.New(),
			Title:      "Topic A",
			Slug:       "topic-a",
			RootNodeID: uuid.New(),
			Nodes: []ContextNode{
				{ID: uuid.New(), Content: "Hello from topic A", CreatedAt: time.Now()},
			},
			TotalNodes: 1,
			HasMore:    false,
			ContextHash: "abc123",
		},
		{
			TopicID:    uuid.New(),
			Title:      "Topic B",
			Slug:       "topic-b",
			RootNodeID: uuid.New(),
			Nodes: []ContextNode{
				{ID: uuid.New(), Content: "Hello from topic B", CreatedAt: time.Now()},
			},
			TotalNodes: 1,
			HasMore:    false,
			ContextHash: "def456",
		},
	}

	result := compileMultiTopicContext(contexts, GlobalMaxNodes)

	assert.Contains(t, result.MergedText, "--- topic boundary: topic-a")
	assert.Contains(t, result.MergedText, "--- topic boundary: topic-b")
	assert.Contains(t, result.MergedText, "Topic: Topic A")
	assert.Contains(t, result.MergedText, "Topic: Topic B")
	assert.Equal(t, 2, result.TotalNodes)
	assert.False(t, result.Truncated)
}

func TestCompileMultiTopicContext_Truncation(t *testing.T) {
	// Global budget of 1, but 2 nodes total → truncated.
	contexts := []TopicContext{
		{
			TopicID:    uuid.New(),
			Title:      "Topic A",
			Slug:       "topic-a",
			RootNodeID: uuid.New(),
			Nodes: []ContextNode{
				{ID: uuid.New(), Content: "node 1", CreatedAt: time.Now()},
				{ID: uuid.New(), Content: "node 2", CreatedAt: time.Now()},
			},
			TotalNodes: 2,
		},
	}

	result := compileMultiTopicContext(contexts, 1) // global max = 1

	assert.True(t, result.Truncated)
	assert.Equal(t, 1, result.TotalNodes)
	assert.Contains(t, result.MergedText, "CONTEXT WARNING")
}

// --- Context Hash Tests (scenario 20) -------------------------------------

func TestContextHash_Deterministic(t *testing.T) {
	id1 := uuid.New()
	id2 := uuid.New()
	now := time.Now()

	nodes := []ContextNode{
		{ID: id1, CreatedAt: now},
		{ID: id2, CreatedAt: now.Add(time.Second)},
	}

	hash1 := contextHash(nodes)
	hash2 := contextHash(nodes) // same input
	assert.Equal(t, hash1, hash2, "same nodes should produce same hash")

	// Different order should still produce same hash (sorted internally).
	reversed := []ContextNode{nodes[1], nodes[0]}
	hash3 := contextHash(reversed)
	assert.Equal(t, hash1, hash3, "hash should be order-independent")
}

func TestContextHash_DifferentNodes(t *testing.T) {
	nodes1 := []ContextNode{{ID: uuid.New(), CreatedAt: time.Now()}}
	nodes2 := []ContextNode{{ID: uuid.New(), CreatedAt: time.Now()}}

	h1 := contextHash(nodes1)
	h2 := contextHash(nodes2)
	assert.NotEqual(t, h1, h2)
}

// --- Relative Time Formatting ----------------------------------------------

func TestFormatRelativeTime(t *testing.T) {
	assert.Equal(t, "just now", formatRelativeTime(time.Now()))
	assert.Equal(t, "30m ago", formatRelativeTime(time.Now().Add(-30*time.Minute)))
	assert.Equal(t, "2h ago", formatRelativeTime(time.Now().Add(-2*time.Hour)))
}

// --- Truncate Snippet ------------------------------------------------------

func TestTruncateSnippet(t *testing.T) {
	short := "Hello world"
	assert.Equal(t, short, truncateSnippet(short, 100))

	long := "This is a very long snippet that exceeds the maximum length allowed for display purposes"
	result := truncateSnippet(long, 30)
	assert.True(t, len(result) <= 33) // 30 + "..."
	assert.True(t, result != long)
}

// --- StripMarkdown ---------------------------------------------------------

func TestStripMarkdown(t *testing.T) {
	assert.Equal(t, "Hello world", stripMarkdown("**Hello** world"))
	assert.Equal(t, "Heading", stripMarkdown("# Heading"))
	assert.Equal(t, "code here", stripMarkdown("`code here`"))
}

// --- Mock repo for service tests ------------------------------------------

type mockSearchRepo struct {
	searchResults  []TopicSearchResult
	searchTotal    int
	searchErr      error
	recentResults  []TopicSearchResult
	recentErr      error
	injectMeta     *TopicInjectMeta
	injectMetaErr  error
	topicNodes     []ContextNode
	topicNodesErr  error
	totalNodes     int
	hasMore        bool
	previewNodes   []ContextNode
	previewMeta    *TopicPreviewMeta
	previewMetaErr error
	refreshCount   int
	refreshErr     error
}

func (m *mockSearchRepo) SearchTopics(_ context.Context, _ uuid.UUID, _ SearchOptions) ([]TopicSearchResult, int, error) {
	return m.searchResults, m.searchTotal, m.searchErr
}
func (m *mockSearchRepo) GetRecentTopics(_ context.Context, _ uuid.UUID, _ int) ([]TopicSearchResult, error) {
	return m.recentResults, m.recentErr
}
func (m *mockSearchRepo) GetTopicNodes(_ context.Context, _ uuid.UUID, _ int) ([]ContextNode, int, bool, error) {
	return m.topicNodes, m.totalNodes, m.hasMore, m.topicNodesErr
}
func (m *mockSearchRepo) GetTopicForInject(_ context.Context, _ uuid.UUID) (*TopicInjectMeta, error) {
	return m.injectMeta, m.injectMetaErr
}
func (m *mockSearchRepo) GetTopicPreviewNodes(_ context.Context, _ uuid.UUID, _ int) ([]ContextNode, error) {
	return m.previewNodes, nil
}
func (m *mockSearchRepo) GetTopicPreviewMeta(_ context.Context, _ uuid.UUID) (*TopicPreviewMeta, error) {
	return m.previewMeta, m.previewMetaErr
}
func (m *mockSearchRepo) RefreshNodeContentIndex(_ context.Context, _ uuid.UUID, _ []uuid.UUID) (int, error) {
	return m.refreshCount, m.refreshErr
}

type mockLogRepo struct {
	entries []SearchLogEntry
	err     error
}

func (m *mockLogRepo) InsertSearchLog(_ context.Context, entry SearchLogEntry) error {
	if m.err != nil {
		return m.err
	}
	m.entries = append(m.entries, entry)
	return nil
}

// --- Service-level validation tests (scenarios 5, 13) ---------------------

func TestService_Search_TooShort(t *testing.T) {
	svc := NewTopicSearchService(&mockSearchRepo{}, &mockLogRepo{})
	_, _, _, err := svc.Search(context.Background(), uuid.New(), SearchOptions{Query: "a"})
	assert.True(t, errors.Is(err, ErrSearchQueryTooShort))
}

func TestService_Search_TooLong(t *testing.T) {
	long := string(make([]byte, 201))
	for i := range long {
		long = long[:i] + "x" + long[i+1:]
	}
	svc := NewTopicSearchService(&mockSearchRepo{}, &mockLogRepo{})
	_, _, _, err := svc.Search(context.Background(), uuid.New(), SearchOptions{Query: long})
	assert.True(t, errors.Is(err, ErrSearchQueryTooLong))
}

func TestService_Search_InvalidSort(t *testing.T) {
	svc := NewTopicSearchService(&mockSearchRepo{}, &mockLogRepo{})
	_, _, _, err := svc.Search(context.Background(), uuid.New(), SearchOptions{
		Query:  "test",
		SortBy: "invalid",
	})
	assert.True(t, errors.Is(err, ErrSearchInvalidSort))
}

func TestService_Search_StopWordsOnly(t *testing.T) {
	svc := NewTopicSearchService(
		&mockSearchRepo{searchErr: ErrSearchStopWordsOnly},
		&mockLogRepo{},
	)
	_, _, _, err := svc.Search(context.Background(), uuid.New(), SearchOptions{
		Query:  "the and of",
		SortBy: "relevance",
	})
	assert.True(t, errors.Is(err, ErrSearchStopWordsOnly))
}

// --- Injection validation tests (scenarios 13, 10, 11, 12) -----------------

func TestService_Inject_TooManyTopics(t *testing.T) {
	svc := NewTopicSearchService(&mockSearchRepo{}, &mockLogRepo{})
	ids := make([]uuid.UUID, 6)
	for i := range ids {
		ids[i] = uuid.New()
	}
	_, err := svc.InjectContext(context.Background(), uuid.New(), InjectContextRequest{TopicIDs: ids})
	assert.True(t, errors.Is(err, ErrContextTooManyTopics))
}

func TestService_Inject_TopicNotFound(t *testing.T) {
	repo := &mockSearchRepo{injectMetaErr: ErrTopicNotFound}
	svc := NewTopicSearchService(repo, &mockLogRepo{})
	_, err := svc.InjectContext(context.Background(), uuid.New(), InjectContextRequest{
		TopicIDs: []uuid.UUID{uuid.New()},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "topic not found")
}

func TestService_Inject_TopicDeleted(t *testing.T) {
	repo := &mockSearchRepo{
		injectMeta: &TopicInjectMeta{
			ID:     uuid.New(),
			Status: "deleted",
		},
	}
	svc := NewTopicSearchService(repo, &mockLogRepo{})
	_, err := svc.InjectContext(context.Background(), uuid.New(), InjectContextRequest{
		TopicIDs: []uuid.UUID{uuid.New()},
	})
	assert.True(t, errors.Is(err, ErrTopicDeleted))
}

func TestService_Inject_TopicArchived(t *testing.T) {
	repo := &mockSearchRepo{
		injectMeta: &TopicInjectMeta{
			ID:     uuid.New(),
			Status: "archived",
		},
	}
	svc := NewTopicSearchService(repo, &mockLogRepo{})
	_, err := svc.InjectContext(context.Background(), uuid.New(), InjectContextRequest{
		TopicIDs: []uuid.UUID{uuid.New()},
	})
	assert.True(t, errors.Is(err, ErrTopicArchived))
}

// --- Context Too Large (scenario 14) ---------------------------------------

func TestService_Inject_TooLarge(t *testing.T) {
	repo := &mockSearchRepo{
		injectMeta: &TopicInjectMeta{ID: uuid.New(), Status: "active"},
		topicNodes: []ContextNode{},
		totalNodes: GlobalMaxNodes + 1,
	}
	svc := NewTopicSearchService(repo, &mockLogRepo{})
	_, err := svc.InjectContext(context.Background(), uuid.New(), InjectContextRequest{
		TopicIDs: []uuid.UUID{uuid.New()},
	})
	assert.True(t, errors.Is(err, ErrContextTooLarge))
}

// --- GetTopicPreview not found (scenario 24) -------------------------------

func TestService_Preview_NotFound(t *testing.T) {
	repo := &mockSearchRepo{previewMetaErr: ErrTopicNotFound}
	svc := NewTopicSearchService(repo, &mockLogRepo{})
	_, err := svc.GetTopicPreview(context.Background(), uuid.New(), 3)
	assert.True(t, errors.Is(err, ErrTopicNotFound))
}

// --- LogSearch --------------------------------------------------------------

func TestService_LogSearch(t *testing.T) {
	logRepo := &mockLogRepo{}
	svc := NewTopicSearchService(&mockSearchRepo{}, logRepo)
	err := svc.LogSearch(context.Background(), SearchLogEntry{
		TreeID:    uuid.New(),
		ProfileID: uuid.New(),
		QueryText: "schema design",
	})
	require.NoError(t, err)
	assert.Len(t, logRepo.entries, 1)
	assert.Equal(t, "schema design", logRepo.entries[0].QueryText)
}
