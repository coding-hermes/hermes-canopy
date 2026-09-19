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
//
// Results are per MATCH MODE so the ALL-TERMS call and the ANY-TERM fallback
// can be told apart (DF-HERMES-CANOPY-29): `results`/`err` answer the default
// ALL-TERMS search, `anyTermResults`/`anyTermErr` answer the ANY-TERM one.
type stubTopicSearcher struct {
	calls      int
	lastOpts   search.SearchOptions
	lastTree   uuid.UUID
	lastQuery  string
	allOpts    []search.SearchOptions
	allTrees   []uuid.UUID
	results    []search.TopicSearchResult
	total      int
	took       time.Duration
	err        error
	anyResults []search.TopicSearchResult
	anyTotal   int
	anyErr     error
}

func (s *stubTopicSearcher) Search(ctx context.Context, treeID uuid.UUID, opts search.SearchOptions) ([]search.TopicSearchResult, int, time.Duration, error) {
	s.calls++
	s.lastOpts = opts
	s.lastTree = treeID
	s.lastQuery = opts.Query
	s.allOpts = append(s.allOpts, opts)
	s.allTrees = append(s.allTrees, treeID)
	if opts.MatchAnyTerms {
		return s.anyResults, s.anyTotal, s.took, s.anyErr
	}
	return s.results, s.total, s.took, s.err
}

// matchModes returns the mode of every Search call in order.
func (s *stubTopicSearcher) matchModes() []bool {
	modes := make([]bool, 0, len(s.allOpts))
	for _, o := range s.allOpts {
		modes = append(modes, o.MatchAnyTerms)
	}
	return modes
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
		t.Fatalf("Search called %d times, want 1 (a non-empty ALL-TERMS result needs no fallback)", searcher.calls)
	}
	if searcher.lastOpts.MatchAnyTerms {
		t.Errorf("Search ran in ANY-TERM mode for a query the ALL-TERMS search resolved: %+v", searcher.lastOpts)
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
	searcher := &stubTopicSearcher{results: []search.TopicSearchResult{{TopicID: uuid.New()}}}
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
	searcher := &stubTopicSearcher{err: boom, anyResults: []search.TopicSearchResult{{TopicID: uuid.New()}}}
	r := NewTopicRetriever(searcher)

	items, err := r.Retrieve(context.Background(), uuid.New(), "query", 5)
	if !errors.Is(err, boom) {
		t.Fatalf("Retrieve error = %v, want the searcher error verbatim (unwrapped)", err)
	}
	if items != nil {
		t.Errorf("Retrieve returned items alongside an error: %v", items)
	}
	// An ERROR is not an empty result: the ANY-TERM fallback must NOT run,
	// otherwise the failing call would be retried and its warning doubled.
	if searcher.calls != 1 {
		t.Errorf("Search called %d times after an error, want 1 (no fallback on an error)", searcher.calls)
	}
}

// TestTopicRetriever_StopWordsOnlyIssuesNoFallback pins the degrade contract
// for a query with no significant term: the search layer reports it as
// ErrSearchStopWordsOnly, which is an ERROR (never an empty success), so no
// ANY-TERM retry is issued — such a query has no lexemes to OR either.
func TestTopicRetriever_StopWordsOnlyIssuesNoFallback(t *testing.T) {
	searcher := &stubTopicSearcher{
		err:        search.ErrSearchStopWordsOnly,
		anyResults: []search.TopicSearchResult{{TopicID: uuid.New(), Slug: "should-not-appear"}},
	}
	r := NewTopicRetriever(searcher)

	items, err := r.Retrieve(context.Background(), uuid.New(), "the and of the", 5)
	if !errors.Is(err, search.ErrSearchStopWordsOnly) {
		t.Fatalf("Retrieve error = %v, want ErrSearchStopWordsOnly verbatim", err)
	}
	if items != nil {
		t.Errorf("Retrieve returned items for a stop-words-only query: %v", items)
	}
	if searcher.calls != 1 {
		t.Errorf("Search called %d times, want 1 (no ANY-TERM retry for a stop-words-only query)", searcher.calls)
	}
}

// TestTopicRetriever_EmptyResultRetriesOnceInAnyTermMode is the DF-29 core:
// an empty ALL-TERMS result triggers EXACTLY ONE ANY-TERM retry, and the
// candidate set comes from that retry.
func TestTopicRetriever_EmptyResultRetriesOnceInAnyTermMode(t *testing.T) {
	treeID := uuid.New()
	fallbackID := uuid.New()
	searcher := &stubTopicSearcher{
		results: nil, // ALL-TERMS: nothing matched (the prose case)
		anyResults: []search.TopicSearchResult{
			{TopicID: fallbackID, TreeID: treeID, Title: "Zebra Runbook Cutover", Slug: "zebra-runbook-cutover", Snippet: "cutover notes", Relevance: 0.55},
		},
	}
	r := NewTopicRetriever(searcher)

	items, err := r.Retrieve(context.Background(), treeID, "zebra migration runbook planning for the zebra cutover", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if searcher.calls != 2 {
		t.Fatalf("Search called %d times, want 2 (ALL-TERMS then ONE ANY-TERM retry)", searcher.calls)
	}
	if got := searcher.matchModes(); len(got) != 2 || got[0] || !got[1] {
		t.Errorf("match modes = %v, want [false true] (ALL-TERMS first, ANY-TERM second)", got)
	}
	// Both searches carry the same tree, query, limit — only the mode differs.
	if searcher.allTrees[0] != treeID || searcher.allTrees[1] != treeID {
		t.Errorf("search trees = %v, want both %v (scope is never widened by the fallback)", searcher.allTrees, treeID)
	}
	for i, o := range searcher.allOpts {
		if o.Query != searcher.lastQuery || o.MaxResults != 5 || o.Offset != 0 || o.StatusFilter != "" || o.SortBy != "" {
			t.Errorf("call %d opts = %+v, want the query/limit forwarded verbatim and no other opinion", i, o)
		}
	}
	if len(items) != 1 {
		t.Fatalf("Retrieve returned %d items, want 1 (from the ANY-TERM retry)", len(items))
	}
	if items[0].ID != fallbackID || items[0].Slug != "zebra-runbook-cutover" {
		t.Errorf("item = %+v, want the fallback hit zebra-runbook-cutover", items[0])
	}
}

// TestTopicRetriever_AllTermsMatchSkipsFallback: when the ALL-TERMS search
// already yields candidates, the ANY-TERM retry must NOT run — the fallback
// is a recall rescue, never a re-ranking of a working query.
func TestTopicRetriever_AllTermsMatchSkipsFallback(t *testing.T) {
	treeID := uuid.New()
	allTermsID := uuid.New()
	searcher := &stubTopicSearcher{
		results: []search.TopicSearchResult{
			{TopicID: allTermsID, TreeID: treeID, Slug: "zebra-runbook-cutover", Relevance: 0.9},
		},
		anyResults: []search.TopicSearchResult{
			{TopicID: uuid.New(), TreeID: treeID, Slug: "unrelated-noise", Relevance: 0.1},
		},
	}
	r := NewTopicRetriever(searcher)

	items, err := r.Retrieve(context.Background(), treeID, "zebra runbook cutover", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if searcher.calls != 1 {
		t.Fatalf("Search called %d times, want 1 (no fallback once ALL-TERMS matched)", searcher.calls)
	}
	if len(items) != 1 || items[0].ID != allTermsID {
		t.Errorf("items = %+v, want only the ALL-TERMS hit %v", items, allTermsID)
	}
}

// TestTopicRetriever_BothModesEmptyReturnsNilNil: an empty ALL-TERMS result
// AND an empty ANY-TERM result still maps to (nil, nil) — the compiler
// records the allocation and folds nothing in, and no third search happens.
func TestTopicRetriever_BothModesEmptyReturnsNilNil(t *testing.T) {
	searcher := &stubTopicSearcher{results: nil, anyResults: nil}
	r := NewTopicRetriever(searcher)

	items, err := r.Retrieve(context.Background(), uuid.New(), "query", 5)
	if err != nil {
		t.Fatalf("Retrieve on an empty result: %v", err)
	}
	if items != nil {
		t.Errorf("Retrieve on an empty result returned %v, want nil", items)
	}
	if searcher.calls != 2 {
		t.Errorf("Search called %d times, want 2 (ALL-TERMS, then exactly one ANY-TERM retry)", searcher.calls)
	}
}

// TestTopicRetriever_FallbackErrorPropagatesUnwrapped: when the retry itself
// fails, that error is returned unwrapped (the compiler renders it in its one
// `retrieval failed: ...` warning) and no items are returned.
func TestTopicRetriever_FallbackErrorPropagatesUnwrapped(t *testing.T) {
	boom := errors.New("fts index unavailable")
	searcher := &stubTopicSearcher{results: nil, anyErr: boom}
	r := NewTopicRetriever(searcher)

	items, err := r.Retrieve(context.Background(), uuid.New(), "prose query", 5)
	if !errors.Is(err, boom) {
		t.Fatalf("Retrieve error = %v, want the fallback's error verbatim (unwrapped)", err)
	}
	if items != nil {
		t.Errorf("Retrieve returned items alongside a fallback error: %v", items)
	}
	if searcher.calls != 2 {
		t.Errorf("Search called %d times, want 2 (the retry failed, it is not retried again)", searcher.calls)
	}
}

// TestTopicRetriever_ImplementsCompilerSeam pins the wiring contract: the
// adapter must satisfy the compiler's RetrievalReader interface.
func TestTopicRetriever_ImplementsCompilerSeam(t *testing.T) {
	var _ ctxpkg.RetrievalReader = (*TopicRetriever)(nil)
}
