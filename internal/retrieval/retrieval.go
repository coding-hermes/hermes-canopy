// Package retrieval adapts the existing topic search service to the context
// compiler's retrieved-tier seam (GAP-080 phase 4a).
//
// It lives OUTSIDE internal/handler deliberately: that package's tests need
// a live PostgreSQL, and this adapter must stay unit-testable without one.
// The adapter is pure translation — no logging, no analytics writes, no
// side effects of any kind (the search service's LogSearch is never called).
package retrieval

import (
	"context"
	"time"

	"github.com/google/uuid"

	ctxpkg "github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/search"
)

// TopicSearcher is the narrow slice of search.TopicSearchService the adapter
// needs. TopicSearchService satisfies it; tests provide a stub.
type TopicSearcher interface {
	Search(ctx context.Context, treeID uuid.UUID, opts search.SearchOptions) ([]search.TopicSearchResult, int, time.Duration, error)
}

// TopicRetriever implements ctxpkg.RetrievalReader over topic FTS search.
type TopicRetriever struct {
	searcher TopicSearcher
}

// NewTopicRetriever wraps a TopicSearcher as a context.RetrievalReader.
func NewTopicRetriever(searcher TopicSearcher) *TopicRetriever {
	return &TopicRetriever{searcher: searcher}
}

// Retrieve maps a topic search to retrieval candidates, preserving the
// searcher's result order (the compiler owns ordering and dedupe).
//
// A blank query or limit <= 0 means there is nothing to search for: no
// search is issued and (nil, nil) is returned. A searcher error is
// returned UNWRAPPED — the compiler owns the degrade path and renders it in
// its warning verbatim.
func (r *TopicRetriever) Retrieve(ctx context.Context, treeID uuid.UUID, query string, limit int) ([]ctxpkg.RetrievalItem, error) {
	if query == "" || limit <= 0 {
		return nil, nil
	}
	results, _, _, err := r.searcher.Search(ctx, treeID, search.SearchOptions{
		Query:      query,
		MaxResults: limit,
	})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	items := make([]ctxpkg.RetrievalItem, 0, len(results))
	for _, res := range results {
		items = append(items, ctxpkg.RetrievalItem{
			ID:        res.TopicID,
			Slug:      res.Slug,
			Title:     res.Title,
			Content:   res.Snippet,
			Relevance: res.Relevance,
		})
	}
	return items, nil
}
