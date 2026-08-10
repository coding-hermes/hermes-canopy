// Package db — topic search repository.
// Implements search.TopicSearchRepo against PostgreSQL with pgx.
// Spec: SPEC-TM-03 §4.1, §4.4.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/search"
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

// SearchTopics performs FTS across topics title/description (search_vector)
// AND node content (topic_node_content_search.content_vector).
// Uses ts_headline for snippet generation with <mark> highlighting.
func (r *PGTopicSearchRepo) SearchTopics(ctx context.Context, treeID uuid.UUID, opts search.SearchOptions) ([]search.TopicSearchResult, int, error) {
	// Use plainto_tsquery for safety — it handles arbitrary user input
	// without syntax errors (unlike to_tsquery which interprets & | ! etc).
	// The raw query is passed as $2 to plainto_tsquery in the SQL.

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
                ts_rank(t.search_vector, plainto_tsquery('english', $2)) AS relevance,
                ts_headline('english',
                    COALESCE(t.title,'') || ' ' || COALESCE(t.description,''),
                    plainto_tsquery('english', $2),
                    'StartSel=<mark>, StopSel=</mark>, MaxWords=35, MinWords=15'
                ) AS snippet
            FROM topics t
            WHERE t.tree_id = $1
              AND t.search_vector @@ plainto_tsquery('english', $2)
              %s
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
                0.5 * MAX(ts_rank(tncs.content_vector, plainto_tsquery('english', $2))) AS relevance,
                ts_headline('english',
                    string_agg(tncs.content_text, ' '),
                    plainto_tsquery('english', $2),
                    'StartSel=<mark>, StopSel=</mark>, MaxWords=35, MinWords=15'
                ) AS snippet
            FROM topics t
            JOIN topic_node_content_search tncs ON tncs.topic_id = t.id
            WHERE t.tree_id = $1
              AND tncs.content_vector @@ plainto_tsquery('english', $2)
              %s
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
        %s
        LIMIT $3 OFFSET $4`,
		statusClause, statusClause, orderBy)

	limit := opts.MaxResults
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	rows, err := r.pool.Query(ctx, query, treeID, opts.Query, limit, opts.Offset)
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
                WHERE t.tree_id = $1 AND t.search_vector @@ plainto_tsquery('english', $2) %s
            ),
            content_matches AS (
                SELECT DISTINCT t.id FROM topics t
                JOIN topic_node_content_search tncs ON tncs.topic_id = t.id
                WHERE t.tree_id = $1 AND tncs.content_vector @@ plainto_tsquery('english', $2) %s
            )
            SELECT COUNT(*) FROM (
                SELECT id FROM topic_matches
                UNION
                SELECT id FROM content_matches
            ) AS all_matches`,
			statusClause, statusClause)
		if err := r.pool.QueryRow(ctx, countQuery, treeID, opts.Query).Scan(&total); err != nil {
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
