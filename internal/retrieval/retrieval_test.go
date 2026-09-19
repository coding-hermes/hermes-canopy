package retrieval

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	ctxpkg "github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/search"
)

// stubTopicSearcher records Search calls and returns canned results.
type stubTopicSearcher struct {
	calls    int
	lastOpts search.SearchOptions
	lastTree uuid.UUID
	results  []search.TopicSearchResult
	total    int
	took     time.Duration
	err      error
}

func (s *stubTopicSearcher) Search(ctx context.Context, treeID uuid.UUID, opts search.SearchOptions) ([]search.TopicSearchResult, int, time.Duration, error) {
	s.calls++
	s.lastOpts = opts
	s.lastTree = treeID
	return s.results, s.total, s.took, s.err
}

func TestTopicRetriever_MapsResultsToItems(t *testing.T) {
	treeID := uuid.New()
	idA, idB := uuid.New(), uuid.New()
	searcher := &stubTopicSearcher{results: []search.TopicSearchResult{
		{TopicID: idA, TreeID: treeID, Title: "Deployment pipelines", Slug: "deploy-pipelines", Snippet: "snippet a", Relevance: 0.91},
		{TopicID: idB, TreeID: treeID, Title: "CI flake", Slug: "ci-flake", Snippet: "snippet b", Relevance: 0.42},
	}, total: 2}
	r := NewTopicRetriever(searcher)

	items, err := r.Retrieve(context.Background(), treeID, "deployment", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if searcher.calls != 1 {
		t.Fatalf("Search called %d times, want 1", searcher.calls)
	}
	if len(items) != 2 {
		t.Fatalf("Retrieve returned %d items, want 2", len(items))
	}

	// Mapping: ID←TopicID, Slug, Title, Content←Snippet, Relevance — in
	// the searcher's order.
	want := []struct {
		id    uuid.UUID
		slug  string
		title string
		body  string
		rel   float64
	}{
		{idA, "deploy-pipelines", "Deployment pipelines", "snippet a", 0.91},
		{idB, "ci-flake", "CI flake", "snippet b", 0.42},
	}
	for i, w := range want {
		got := items[i]
		if got.ID != w.id || got.Slug != w.slug || got.Title != w.title || got.Content != w.body || got.Relevance != w.rel {
			t.Errorf("items[%d] = {ID:%v Slug:%q Title:%q Content:%q Relevance:%v}, want {ID:%v Slug:%q Title:%q Content:%q Relevance:%v}",
				i, got.ID, got.Slug, got.Title, got.Content, got.Relevance, w.id, w.slug, w.title, w.body, w.rel)
		}
	}
}

func TestTopicRetriever_ForwardsQueryAndLimit(t *testing.T) {
	treeID := uuid.New()
	searcher := &stubTopicSearcher{}
	r := NewTopicRetriever(searcher)

	if _, err := r.Retrieve(context.Background(), treeID, "a query", 7); err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if searcher.calls != 1 {
		t.Fatalf("Search called %d times, want 1", searcher.calls)
	}
	if searcher.lastTree != treeID {
		t.Errorf("Search treeID = %v, want %v", searcher.lastTree, treeID)
	}
	if searcher.lastOpts.Query != "a query" {
		t.Errorf("Search opts.Query = %q, want %q", searcher.lastOpts.Query, "a query")
	}
	if searcher.lastOpts.MaxResults != 7 {
		t.Errorf("Search opts.MaxResults = %d, want 7 (limit forwarded)", searcher.lastOpts.MaxResults)
	}
	// Pagination/filter/sort fields stay zero — the adapter passes no
	// opinion on those.
	if searcher.lastOpts.Offset != 0 || searcher.lastOpts.StatusFilter != "" || searcher.lastOpts.SortBy != "" {
		t.Errorf("Search opts carried unprompted fields: %+v", searcher.lastOpts)
	}
}

func TestTopicRetriever_BlankQueryIssuesNoSearch(t *testing.T) {
	searcher := &stubTopicSearcher{results: []search.TopicSearchResult{{TopicID: uuid.New()}}}
	r := NewTopicRetriever(searcher)

	items, err := r.Retrieve(context.Background(), uuid.New(), "", 5)
	if err != nil {
		t.Fatalf("Retrieve on a blank query: %v", err)
	}
	if items != nil {
		t.Errorf("Retrieve on a blank query returned %v, want nil", items)
	}
	if searcher.calls != 0 {
		t.Errorf("Search called %d times on a blank query, want 0", searcher.calls)
	}
}

func TestTopicRetriever_NonPositiveLimitIssuesNoSearch(t *testing.T) {
	searcher := &stubTopicSearcher{}
	r := NewTopicRetriever(searcher)

	for _, limit := range []int{0, -1} {
		items, err := r.Retrieve(context.Background(), uuid.New(), "query", limit)
		if err != nil {
			t.Fatalf("Retrieve with limit %d: %v", limit, err)
		}
		if items != nil {
			t.Errorf("Retrieve with limit %d returned %v, want nil", limit, items)
		}
		if searcher.calls != 0 {
			t.Errorf("Search called %d times with limit %d, want 0", searcher.calls, limit)
		}
	}
}

func TestTopicRetriever_ErrorPassthroughUnwrapped(t *testing.T) {
	boom := errors.New("fts index unavailable")
	searcher := &stubTopicSearcher{err: boom}
	r := NewTopicRetriever(searcher)

	items, err := r.Retrieve(context.Background(), uuid.New(), "query", 5)
	if !errors.Is(err, boom) {
		t.Fatalf("Retrieve error = %v, want the searcher error verbatim (unwrapped)", err)
	}
	if items != nil {
		t.Errorf("Retrieve returned items alongside an error: %v", items)
	}
}

func TestTopicRetriever_EmptyResultReturnsNilNil(t *testing.T) {
	searcher := &stubTopicSearcher{results: nil, total: 0}
	r := NewTopicRetriever(searcher)

	items, err := r.Retrieve(context.Background(), uuid.New(), "query", 5)
	if err != nil {
		t.Fatalf("Retrieve on an empty result: %v", err)
	}
	if items != nil {
		t.Errorf("Retrieve on an empty result returned %v, want nil", items)
	}
	if searcher.calls != 1 {
		t.Errorf("Search called %d times, want 1 (an empty result is still a run search)", searcher.calls)
	}
}

// TestTopicRetriever_ImplementsCompilerSeam pins the wiring contract: the
// adapter must satisfy the compiler's RetrievalReader interface.
func TestTopicRetriever_ImplementsCompilerSeam(t *testing.T) {
	var _ ctxpkg.RetrievalReader = (*TopicRetriever)(nil)
}
