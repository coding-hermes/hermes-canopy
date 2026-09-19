// Package retrieval adapts the existing topic search service to the context
// compiler's retrieved-tier seam (GAP-080 phase 4a).
//
// It lives OUTSIDE internal/handler deliberately: that package's tests need
// a live PostgreSQL, and this adapter must stay unit-testable without one.
// The adapter is pure translation — no logging, no analytics writes, no
// side effects of any kind (the search service's LogSearch is never called).
//
// GAP-080 phase 4c (DF-HERMES-CANOPY-29) adds the recall fallback: the tier's
// query is the node's WHOLE content, and PostgreSQL's plainto_tsquery ANDs
// every term, so a sentence-shaped message matched nothing at all. The search
// now runs ALL-TERMS first and, only when that returns nothing, retries ONCE
// in ANY-TERM (OR) mode.
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
// Precision first, recall second (DF-HERMES-CANOPY-29): the ALL-TERMS search
// runs first and its result is authoritative WHENEVER IT IS NON-EMPTY — a
// query whose terms all occur in a topic still resolves exactly as it did
// before the fallback existed, on one search. Only a clean EMPTY result
// retries once in ANY-TERM (OR) mode, which is what makes the tier fire on
// natural prose ("zebra migration runbook planning for the zebra cutover"
// cannot match a "Zebra Runbook Cutover" topic under AND semantics).
//
// An ERROR never triggers the fallback: it is returned UNWRAPPED, so the
// compiler's degrade path renders it verbatim in its single
// `retrieval failed: ...` warning. That also covers the stop-words-only
// query, which the search layer reports as an error (ErrSearchStopWordsOnly):
// such a query has no significant term, so an ANY-TERM retry could not match
// anything either and no second search is issued.
//
// A blank query or limit <= 0 means there is nothing to search for: no
// search is issued at all and (nil, nil) is returned.
func (r *TopicRetriever) Retrieve(ctx context.Context, treeID uuid.UUID, query string, limit int) ([]ctxpkg.RetrievalItem, error) {
	if query == "" || limit <= 0 {
		return nil, nil
	}

	results, err := r.search(ctx, treeID, query, limit, false)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		results, err = r.search(ctx, treeID, query, limit, true)
		if err != nil {
			return nil, err
		}
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

// search runs one topic search in the requested match mode and returns its
// results. Errors are handed back UNWRAPPED — the compiler owns the degrade
// path and renders them verbatim.
func (r *TopicRetriever) search(ctx context.Context, treeID uuid.UUID, query string, limit int, matchAnyTerms bool) ([]search.TopicSearchResult, error) {
	results, _, _, err := r.searcher.Search(ctx, treeID, search.SearchOptions{
		Query:         query,
		MaxResults:    limit,
		MatchAnyTerms: matchAnyTerms,
	})
	return results, err
}
