// Package search — concrete service implementation.
// Backed by TopicSearchRepo + TopicSearchLogRepo.
package search

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// topicSearchService is the real TopicSearchService implementation.
type topicSearchService struct {
	repo    TopicSearchRepo
	logRepo TopicSearchLogRepo
}

// NewTopicSearchService creates a TopicSearchService backed by the given repos.
func NewTopicSearchService(repo TopicSearchRepo, logRepo TopicSearchLogRepo) TopicSearchService {
	return &topicSearchService{repo: repo, logRepo: logRepo}
}

// ensure topicSearchService satisfies the interface.
var _ TopicSearchService = (*topicSearchService)(nil)

// Search performs FTS across topics in a tree (spec §6.1).
func (s *topicSearchService) Search(ctx context.Context, treeID uuid.UUID, opts SearchOptions) ([]TopicSearchResult, int, time.Duration, error) {
	query := strings.TrimSpace(opts.Query)
	if len(query) < 2 {
		return nil, 0, 0, ErrSearchQueryTooShort
	}
	if len(query) > 200 {
		return nil, 0, 0, ErrSearchQueryTooLong
	}

	switch opts.SortBy {
	case "", "relevance", "last_active", "title":
	default:
		return nil, 0, 0, ErrSearchInvalidSort
	}

	if opts.MaxResults <= 0 {
		opts.MaxResults = 20
	}
	if opts.MaxResults > 100 {
		return nil, 0, 0, ErrSearchInvalidLimit
	}

	start := time.Now()
	results, total, err := s.repo.SearchTopics(ctx, treeID, opts)
	elapsed := time.Since(start)
	if err != nil {
		if err == ErrSearchStopWordsOnly {
			return nil, 0, 0, ErrSearchStopWordsOnly
		}
		return nil, 0, 0, fmt.Errorf("search topics: %w", err)
	}

	return results, total, elapsed, nil
}

// GetRecent returns the most recently active topics in a tree (spec §6.2).
func (s *topicSearchService) GetRecent(ctx context.Context, treeID uuid.UUID, limit int) ([]TopicSearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	return s.repo.GetRecentTopics(ctx, treeID, limit)
}

// InjectContext compiles and returns the merged context (spec §6.4).
func (s *topicSearchService) InjectContext(ctx context.Context, treeID uuid.UUID, req InjectContextRequest) (*MultiTopicContext, error) {
	if len(req.TopicIDs) == 0 {
		return nil, ErrContextTooManyTopics
	}
	if len(req.TopicIDs) > MaxTopics {
		return nil, ErrContextTooManyTopics
	}

	merged, _, err := CompileInjectContext(ctx, s.repo, treeID, req)
	if err != nil {
		return nil, err
	}
	return merged, nil
}

// GetTopicPreview returns a lightweight preview for hover tooltips (spec §6.3).
func (s *topicSearchService) GetTopicPreview(ctx context.Context, topicID uuid.UUID, snippetCount int) (*TopicPreview, error) {
	if snippetCount <= 0 {
		snippetCount = 3
	}

	meta, err := s.repo.GetTopicPreviewMeta(ctx, topicID)
	if err != nil {
		return nil, err
	}
	if meta.Status == "deleted" || meta.ID == uuid.Nil {
		return nil, ErrTopicNotFound
	}

	nodes, err := s.repo.GetTopicPreviewNodes(ctx, topicID, snippetCount)
	if err != nil {
		return nil, fmt.Errorf("get preview nodes: %w", err)
	}

	snippets := make([]string, 0, len(nodes))
	for _, n := range nodes {
		text := truncateSnippet(stripMarkdown(n.Content), 120)
		if text != "" {
			snippets = append(snippets, text)
		}
	}

	return &TopicPreview{
		TopicID:          meta.ID,
		Title:            meta.Title,
		Snippets:         snippets,
		ParticipantCount: meta.ParticipantCount,
		NodeCount:        meta.NodeCount,
		LastActive:       meta.LastActive,
		LastActiveRel:    formatRelativeTime(meta.LastActive),
	}, nil
}

// RefreshNodeContentIndex re-indexes node content for a topic.
func (s *topicSearchService) RefreshNodeContentIndex(ctx context.Context, topicID uuid.UUID, nodeIDs []uuid.UUID) (int, error) {
	count, err := s.repo.RefreshNodeContentIndex(ctx, topicID, nodeIDs)
	if err != nil {
		log.Warn().Err(err).Msg("search: refresh node content index failed")
		return 0, err
	}
	return count, nil
}

// LogSearch records a search event in the analytics log.
func (s *topicSearchService) LogSearch(ctx context.Context, entry SearchLogEntry) error {
	return s.logRepo.InsertSearchLog(ctx, entry)
}
