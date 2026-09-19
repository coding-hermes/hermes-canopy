// Package db — topic search repository.
// Implements search.TopicSearchRepo against PostgreSQL with pgx.
// Spec: SPEC-TM-03 §4.1, §4.4.
package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/search"
)

// Ensure PGTopicSearchRepo satisfies the search.TopicSearchRepo interface.
var _ search.TopicSearchRepo = (*PGTopicSearchRepo)(nil)

// PGTopicSearchRepo is the pgx-backed topic search repo.
type PGTopicSearchRepo struct {
	pool *pgxpool.Pool
}

// NewPGTopicSearchRepo wires the repo to a pgxpool.
func NewPGTopicSearchRepo(pool *pgxpool.Pool) *PGTopicSearchRepo {
	return &PGTopicSearchRepo{pool: pool}
}

// scanContextNode scans a minimal node row into search.ContextNode.
func scanContextNode(row pgx.Row) (search.ContextNode, error) {
	var n search.ContextNode
	var contentFormat, nodeType string
	var metadata []byte
	var editedAt, deletedAt *time.Time // not used but needed for scan alignment
	_ = editedAt
	_ = deletedAt
	_ = contentFormat
	_ = nodeType
	_ = metadata
	return n, row.Scan(
		&n.ID, &n.TreeID, &n.AuthorID, &n.Content,
		&contentFormat, &nodeType, &n.SequenceNum, &metadata,
		&n.CreatedAt, &editedAt, &deletedAt,
	)
}

// We need time import for the scan helper.
// The import is already at the top of this file via the std lib.

// collectContextNodes drains pgx.Rows into []search.ContextNode.
func collectContextNodes(rows pgx.Rows) ([]search.ContextNode, error) {
	var out []search.ContextNode
	for rows.Next() {
		n, err := scanContextNode(rows)
		if err != nil {
			return nil, fmt.Errorf("db: scan context node: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// --- Match modes (DF-HERMES-CANOPY-29) -------------------------------------
//
// ALL-TERMS is the default and is unchanged: the RAW query is handed to
// plainto_tsquery, which parses it for operators and ANDs the remaining
// lexemes, so a topic must contain EVERY term to match.
//
// ANY-TERM is the opt-in mode (SearchOptions.MatchAnyTerms): the query is
// sanitized into at most maxAnyTermLexemes significant terms, joined with
// ` | `, and a topic matches when it contains AT LEAST ONE of them. The
// context compiler's retrieved tier falls back to it because AND semantics
// make a sentence-shaped node content match nothing at all
// (DF-HERMES-CANOPY-29).

const (
	// plainTSQueryExpr is the ALL-TERMS tsquery expression: the raw query
	// text, parsed by plainto_tsquery (operators ignored, no syntax errors on
	// arbitrary input).
	plainTSQueryExpr = "plainto_tsquery('english', $2)"

	// anyTermTSQueryExpr is the ANY-TERM tsquery expression. $2 carries the
	// SANITIZED `a | b | c` operand string built by buildAnyTermQuery — never
	// the raw query, because to_tsquery INTERPRETS the operators it is given.
	anyTermTSQueryExpr = "to_tsquery('english', $2)"

	// maxAnyTermLexemes bounds how many significant terms an ANY-TERM query
	// may carry. The terms are OR'd, so each extra term widens the match set;
	// the bound keeps natural prose from matching half the tree.
	maxAnyTermLexemes = 12

	// minAnyTermRunes is the shortest token buildAnyTermQuery keeps.
	// Single-character lexemes are REAL in english tsvectors (the parser
	// splits "d'Artagnan" into `d` + `artagnan`), and an OR over single
	// letters matches almost any topic — pure noise for a recall fallback.
	minAnyTermRunes = 2
)

// buildAnyTermQuery turns free text into the operand string of an ANY-TERM
// tsquery: the significant terms, lowercased and deduplicated in
// first-occurrence order, joined with ` | `.
//
// Sanitization is BY CONSTRUCTION, not by escaping. PostgreSQL's tsquery text
// parser does not treat quotes the way a SQL literal does — on PG 16.14 both
// to_tsquery('english', quote_literal('a | b')) and its bare form parse the
// `|` as the OR OPERATOR and yield the lexeme 'b' — so quoting or escaping a
// raw query protects nothing. The only safe operand is one that cannot
// express an operator: every rune that is not a letter or a digit acts as a
// SEPARATOR, tokens shorter than minAnyTermRunes are dropped, duplicates
// collapse to their first occurrence, and the walk stops at maxTerms.
// Nothing else ever reaches to_tsquery.
//
// Stop words are deliberately NOT filtered here: to_tsquery runs the same
// 'english' configuration the indexes were built with, so it applies the
// stemmer and the stop-word list to each operand, and the resulting query
// matches exactly the lexemes the tsvector columns store.
//
// Returns "" when no term survives — the caller maps that to the same
// "nothing to search for" outcome as a stop-words-only query.
func buildAnyTermQuery(text string, maxTerms int) string {
	if maxTerms <= 0 || text == "" {
		return ""
	}
	terms := make([]string, 0, maxTerms)
	seen := make(map[string]bool, maxTerms)
	var token []rune

	flush := func() {
		if len(token) == 0 {
			return
		}
		term := strings.ToLower(string(token))
		token = token[:0]
		if len(terms) >= maxTerms || utf8.RuneCountInString(term) < minAnyTermRunes || seen[term] {
			return
		}
		seen[term] = true
		terms = append(terms, term)
	}

	for _, r := range text {
		if len(terms) >= maxTerms {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			token = append(token, r)
			continue
		}
		flush()
	}
	flush()

	return strings.Join(terms, " | ")
}

// tsQueryExprFor resolves the requested match mode into the tsquery SQL
// expression and the value bound to $2. ok=false means the mode has no usable
// term to search for: ANY-TERM with nothing left after sanitization.
//
// ALL-TERMS (the zero value of SearchOptions.MatchAnyTerms) returns the raw
// query UNTOUCHED — the exact bytes the pre-DF-29 code bound to $2 — so the
// default statement text and every existing caller are unaffected.
func tsQueryExprFor(matchAny bool, query string) (expr, arg string, ok bool) {
	if !matchAny {
		return plainTSQueryExpr, query, true
	}
	arg = buildAnyTermQuery(query, maxAnyTermLexemes)
	if arg == "" {
		return anyTermTSQueryExpr, "", false
	}
	return anyTermTSQueryExpr, arg, true
}

// SearchTopics performs FTS across topics title/description (search_vector)
// AND node content (topic_node_content_search.content_vector).
// Uses ts_headline for snippet generation with <mark> highlighting.
//
// The tsquery expression in both statements below is supplied through the
// %[1]s positional verb (ALL-TERMS: plainto_tsquery over the raw query;
// ANY-TERM: to_tsquery over the sanitized operand string), %[2]s is the
// status clause and %[3]s the ORDER BY. Positional verbs keep the statement
// text independent of the order the arguments are passed in — the ALL-TERMS
// rendering is byte-identical to the pre-DF-29 statement text.
func (r *PGTopicSearchRepo) SearchTopics(ctx context.Context, treeID uuid.UUID, opts search.SearchOptions) ([]search.TopicSearchResult, int, error) {
	// Use plainto_tsquery for safety — it handles arbitrary user input
	// without syntax errors (unlike to_tsquery which interprets & | ! etc).
	// The raw query is passed as $2 to plainto_tsquery in the SQL; in the
	// opt-in ANY-TERM mode $2 carries the sanitized operand string instead
	// (see tsQueryExprFor).

	// Check if the tsquery is empty after FTS parsing (stop words only).
	var tsQueryValid bool
	err := r.pool.QueryRow(ctx,
		`SELECT plainto_tsquery('english', $1) != to_tsquery('english', '')`,
		opts.Query).Scan(&tsQueryValid)
	if err != nil {
		return nil, 0, fmt.Errorf("db: check tsquery: %w", err)
	}
	if !tsQueryValid {
		return nil, 0, search.ErrSearchStopWordsOnly
	}

	// Match mode (DF-HERMES-CANOPY-29). ALL-TERMS is the default and binds the
	// RAW query to $2; ANY-TERM binds the sanitized `a | b | c` operand string.
	// The parameter COUNT is the same in both modes, so the statement shape
	// does not change with the mode — only the expression and $2's value do.
	tsExpr, queryArg, ok := tsQueryExprFor(opts.MatchAnyTerms, opts.Query)
	if !ok {
		// ANY-TERM with no significant term left after sanitization (every
		// token was a single character or a stop word): the same "nothing to
		// search for" outcome as a stop-words-only query.
		return nil, 0, search.ErrSearchStopWordsOnly
	}

	// Status filter.
	statusClause := "AND t.status = 'active'"
	switch opts.StatusFilter {
	case "all":
		statusClause = "AND t.status != 'deleted'"
	case "archived":
		statusClause = "AND t.status = 'archived'"
	}

	// ORDER BY clause.
	var orderBy string
	switch opts.SortBy {
	case "last_active":
		orderBy = "ORDER BY last_active_at DESC"
	case "title":
		orderBy = "ORDER BY title ASC"
	default:
		orderBy = "ORDER BY relevance DESC"
	}

	// Combined search: topic-level matches (search_vector) +
	// content-level matches (content_vector), merged by topic.
	query := fmt.Sprintf(`
        WITH topic_matches AS (
            SELECT
                t.id AS topic_id,
                t.tree_id,
                t.title,
                t.slug,
                t.status,
                t.node_count,
                t.last_active_at,
                ts_rank(t.search_vector, %[1]s) AS relevance,
                ts_headline('english',
                    COALESCE(t.title,'') || ' ' || COALESCE(t.description,''),
                    %[1]s,
                    'StartSel=<mark>, StopSel=</mark>, MaxWords=35, MinWords=15'
                ) AS snippet
            FROM topics t
            WHERE t.tree_id = $1
              AND t.search_vector @@ %[1]s
              %[2]s
        ),
        content_matches AS (
            SELECT
                t.id AS topic_id,
                t.tree_id,
                t.title,
                t.slug,
                t.status,
                t.node_count,
                t.last_active_at,
                0.5 * MAX(ts_rank(tncs.content_vector, %[1]s)) AS relevance,
                ts_headline('english',
                    string_agg(tncs.content_text, ' '),
                    %[1]s,
                    'StartSel=<mark>, StopSel=</mark>, MaxWords=35, MinWords=15'
                ) AS snippet
            FROM topics t
            JOIN topic_node_content_search tncs ON tncs.topic_id = t.id
            WHERE t.tree_id = $1
              AND tncs.content_vector @@ %[1]s
              %[2]s
            GROUP BY t.id, t.tree_id, t.title, t.slug, t.status, t.node_count, t.last_active_at
        ),
        combined AS (
            SELECT * FROM topic_matches
            UNION ALL
            SELECT * FROM content_matches
        ),
        merged AS (
            SELECT
                topic_id,
                tree_id,
                title,
                slug,
                status,
                node_count,
                last_active_at,
                MAX(relevance) AS relevance,
                MAX(snippet) AS snippet
            FROM combined
            GROUP BY topic_id, tree_id, title, slug, status, node_count, last_active_at
        )
        SELECT COUNT(*) OVER() AS total, * FROM merged
        %[3]s
        LIMIT $3 OFFSET $4`,
		tsExpr, statusClause, orderBy)

	limit := opts.MaxResults
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	rows, err := r.pool.Query(ctx, query, treeID, queryArg, limit, opts.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("db: search topics: %w", err)
	}
	defer rows.Close()

	var results []search.TopicSearchResult
	total := 0
	for rows.Next() {
		var sr search.TopicSearchResult
		if err := rows.Scan(&total, &sr.TopicID, &sr.TreeID, &sr.Title, &sr.Slug,
			&sr.Status, &sr.NodeCount, &sr.LastActive, &sr.Relevance, &sr.Snippet); err != nil {
			return nil, 0, fmt.Errorf("db: scan search result: %w", err)
		}
		results = append(results, sr)
	}

	if len(results) == 0 {
		countQuery := fmt.Sprintf(`
            WITH topic_matches AS (
                SELECT t.id FROM topics t
                WHERE t.tree_id = $1 AND t.search_vector @@ %[1]s %[2]s
            ),
            content_matches AS (
                SELECT DISTINCT t.id FROM topics t
                JOIN topic_node_content_search tncs ON tncs.topic_id = t.id
                WHERE t.tree_id = $1 AND tncs.content_vector @@ %[1]s %[2]s
            )
            SELECT COUNT(*) FROM (
                SELECT id FROM topic_matches
                UNION
                SELECT id FROM content_matches
            ) AS all_matches`,
			tsExpr, statusClause)
		if err := r.pool.QueryRow(ctx, countQuery, treeID, queryArg).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("db: count search results: %w", err)
		}
	}

	return results, total, rows.Err()
}

// GetRecentTopics returns topics ordered by last_active_at DESC, excluding deleted.
func (r *PGTopicSearchRepo) GetRecentTopics(ctx context.Context, treeID uuid.UUID, limit int) ([]search.TopicSearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
        SELECT
            t.id, t.tree_id, t.title, t.slug,
            COALESCE(LEFT(t.description, 200), '') AS snippet,
            t.status, t.node_count, t.last_active_at, 0.0 AS relevance
        FROM topics t
        WHERE t.tree_id = $1 AND t.status != 'deleted'
        ORDER BY t.last_active_at DESC NULLS LAST
        LIMIT $2`, treeID, limit)
	if err != nil {
		return nil, fmt.Errorf("db: recent topics: %w", err)
	}
	defer rows.Close()

	var results []search.TopicSearchResult
	for rows.Next() {
		var sr search.TopicSearchResult
		if err := rows.Scan(&sr.TopicID, &sr.TreeID, &sr.Title, &sr.Slug,
			&sr.Snippet, &sr.Status, &sr.NodeCount, &sr.LastActive, &sr.Relevance); err != nil {
			return nil, fmt.Errorf("db: scan recent topic: %w", err)
		}
		results = append(results, sr)
	}
	return results, rows.Err()
}

// GetTopicNodes returns nodes in a topic's scope (via topic_member_nodes view),
// ordered by sequence_num, up to maxNodes.
func (r *PGTopicSearchRepo) GetTopicNodes(ctx context.Context, topicID uuid.UUID, maxNodes int) ([]search.ContextNode, int, bool, error) {
	if maxNodes <= 0 {
		maxNodes = 500
	}

	var total int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM topic_member_nodes WHERE topic_id = $1`,
		topicID).Scan(&total)
	if err != nil {
		return nil, 0, false, fmt.Errorf("db: count topic nodes: %w", err)
	}

	rows, err := r.pool.Query(ctx, `
        SELECT n.id, n.tree_id, n.author_id, n.content,
               n.content_format, n.node_type, n.sequence_num, n.metadata,
               n.created_at, n.edited_at, n.deleted_at
        FROM topic_member_nodes tmn
        JOIN nodes n ON n.id = tmn.node_id
        WHERE tmn.topic_id = $1 AND n.deleted_at IS NULL
        ORDER BY n.sequence_num ASC
        LIMIT $2`, topicID, maxNodes)
	if err != nil {
		return nil, 0, false, fmt.Errorf("db: get topic nodes: %w", err)
	}
	defer rows.Close()

	nodes, err := collectContextNodes(rows)
	if err != nil {
		return nil, 0, false, err
	}

	hasMore := total > maxNodes
	return nodes, total, hasMore, nil
}

// GetTopicForInject returns topic metadata for injection validation.
func (r *PGTopicSearchRepo) GetTopicForInject(ctx context.Context, topicID uuid.UUID) (*search.TopicInjectMeta, error) {
	var meta search.TopicInjectMeta
	err := r.pool.QueryRow(ctx, `
        SELECT id, title, slug, root_node_id, status
        FROM topics
        WHERE id = $1`, topicID).Scan(
		&meta.ID, &meta.Title, &meta.Slug, &meta.RootNodeID, &meta.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, search.ErrTopicNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get topic for inject: %w", err)
	}
	return &meta, nil
}

// GetTopicPreviewNodes returns the first N nodes in a topic for preview snippets.
func (r *PGTopicSearchRepo) GetTopicPreviewNodes(ctx context.Context, topicID uuid.UUID, limit int) ([]search.ContextNode, error) {
	if limit <= 0 {
		limit = 3
	}
	rows, err := r.pool.Query(ctx, `
        SELECT n.id, n.tree_id, n.author_id, n.content,
               n.content_format, n.node_type, n.sequence_num, n.metadata,
               n.created_at, n.edited_at, n.deleted_at
        FROM topic_member_nodes tmn
        JOIN nodes n ON n.id = tmn.node_id
        WHERE tmn.topic_id = $1 AND n.deleted_at IS NULL
        ORDER BY n.sequence_num ASC
        LIMIT $2`, topicID, limit)
	if err != nil {
		return nil, fmt.Errorf("db: get preview nodes: %w", err)
	}
	defer rows.Close()
	return collectContextNodes(rows)
}

// GetTopicPreviewMeta returns topic metadata for the preview endpoint.
func (r *PGTopicSearchRepo) GetTopicPreviewMeta(ctx context.Context, topicID uuid.UUID) (*search.TopicPreviewMeta, error) {
	var meta search.TopicPreviewMeta
	err := r.pool.QueryRow(ctx, `
        SELECT
            t.id,
            t.title,
            t.status,
            t.node_count,
            COALESCE(t.last_active_at, t.created_at),
            COALESCE((
                SELECT COUNT(DISTINCT n.author_id)
                FROM topic_member_nodes tmn
                JOIN nodes n ON n.id = tmn.node_id
                WHERE tmn.topic_id = t.id AND n.deleted_at IS NULL
            ), 0)
        FROM topics t
        WHERE t.id = $1`, topicID).Scan(
		&meta.ID, &meta.Title, &meta.Status, &meta.NodeCount, &meta.LastActive, &meta.ParticipantCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, search.ErrTopicNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get preview meta: %w", err)
	}
	return &meta, nil
}

// RefreshNodeContentIndex calls the PL/pgSQL refresh function.
func (r *PGTopicSearchRepo) RefreshNodeContentIndex(ctx context.Context, topicID uuid.UUID, nodeIDs []uuid.UUID) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT refresh_topic_node_content_index($1, $2)`,
		topicID, nodeIDs).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("db: refresh node content index: %w", err)
	}
	return count, nil
}
