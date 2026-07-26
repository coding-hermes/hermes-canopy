// Package db provides the PostgreSQL data layer for Canopy.
// TopicRepo and TopicMemberRepo implement CRUD + search for topics.
// Spec: SPEC-TM-01 §4.3, §4.4.
package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoActiveTopic is returned when a topic has been soft-deleted.
var ErrNoActiveTopic = errors.New("db: topic is soft-deleted or archived")

// topicColumns lists all columns in the topics table for SELECT queries.
const topicColumns = `id, tree_id, root_node_id, title, description, slug,
    parent_topic_id, status, topic_tags, node_count, created_at,
    archived_at, deleted_at`

// scanTopic scans a topic row into a Topic struct.
func scanTopic(row pgx.Row, t *Topic) error {
	return row.Scan(
		&t.ID, &t.TreeID, &t.RootNodeID, &t.Title, &t.Description,
		&t.Slug, &t.ParentTopicID, &t.Status, &t.TopicTags,
		&t.NodeCount, &t.CreatedAt, &t.ArchivedAt, &t.DeletedAt,
	)
}

// scanTopicArray scans multiple topic rows.
func scanTopicArray(rows pgx.Rows) ([]Topic, error) {
	var topics []Topic
	for rows.Next() {
		var t Topic
		if err := scanTopic(rows, &t); err != nil {
			return nil, fmt.Errorf("db: scan topic: %w", err)
		}
		topics = append(topics, t)
	}
	return topics, rows.Err()
}

// ── TopicRepo ──────────────────────────────────────────────────────────

// TopicRepo handles CRUD operations on topics.
type TopicRepo interface {
	Create(ctx context.Context, input TopicCreateInput) (*Topic, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Topic, error)
	GetByTree(ctx context.Context, treeID uuid.UUID, status string) ([]Topic, error)
	GetByRootNode(ctx context.Context, nodeID uuid.UUID) (*Topic, error)
	GetBySlug(ctx context.Context, treeID uuid.UUID, slug string) (*Topic, error)
	Search(ctx context.Context, treeID uuid.UUID, query string, limit, offset int) ([]TopicSummary, int, error)
	Update(ctx context.Context, id uuid.UUID, input TopicUpdateInput) (*Topic, error)
	Archive(ctx context.Context, id uuid.UUID) error
	Restore(ctx context.Context, id uuid.UUID) error
	SoftDelete(ctx context.Context, id uuid.UUID) error
	HardDelete(ctx context.Context, id uuid.UUID) error
	GetParentTopics(ctx context.Context, topicID uuid.UUID) ([]Topic, error)
	GetChildTopics(ctx context.Context, parentTopicID uuid.UUID) ([]Topic, error)
	RefreshNodeCount(ctx context.Context, topicID uuid.UUID) (int32, error)
	GetTopicsForNode(ctx context.Context, nodeID uuid.UUID) ([]Topic, error)
	ListArchived(ctx context.Context, treeID uuid.UUID, limit, offset int) ([]Topic, int, error)
}

// PGTopicRepo is the pgx-backed TopicRepo implementation.
type PGTopicRepo struct {
	pool *pgxpool.Pool
}

// NewPGTopicRepo wires the repo to a pgxpool.
func NewPGTopicRepo(pool *pgxpool.Pool) *PGTopicRepo {
	return &PGTopicRepo{pool: pool}
}

// Create inserts a new topic. Generates slug from title.
func (r *PGTopicRepo) Create(ctx context.Context, input TopicCreateInput) (*Topic, error) {
	slug := generateSlug(input.Title)
	// Default topic_tags to empty slice so PostgreSQL NOT NULL constraint
	// is satisfied even when the caller omits the field.
	tags := input.TopicTags
	if tags == nil {
		tags = []string{}
	}
	row := r.pool.QueryRow(ctx, `
        INSERT INTO topics (tree_id, root_node_id, title, description, slug,
                            parent_topic_id, topic_tags)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
        RETURNING `+topicColumns,
		input.TreeID, input.RootNodeID, input.Title, input.Description,
		slug, input.ParentTopicID, tags,
	)
	var t Topic
	if err := scanTopic(row, &t); err != nil {
		return nil, fmt.Errorf("db: insert topic: %w", err)
	}
	return &t, nil
}

// GetByID retrieves a topic by ID. Returns nil if not found or deleted.
func (r *PGTopicRepo) GetByID(ctx context.Context, id uuid.UUID) (*Topic, error) {
	var t Topic
	err := scanTopic(r.pool.QueryRow(ctx, `
        SELECT `+topicColumns+` FROM topics WHERE id = $1 AND deleted_at IS NULL`, id), &t)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select topic: %w", err)
	}
	return &t, nil
}

// GetByTree returns all topics in a tree, optionally filtered by status.
func (r *PGTopicRepo) GetByTree(ctx context.Context, treeID uuid.UUID, status string) ([]Topic, error) {
	var rows pgx.Rows
	var err error
	if status != "" {
		rows, err = r.pool.Query(ctx, `
            SELECT `+topicColumns+` FROM topics
            WHERE tree_id = $1 AND status = $2 AND deleted_at IS NULL
            ORDER BY created_at DESC`, treeID, status)
	} else {
		rows, err = r.pool.Query(ctx, `
            SELECT `+topicColumns+` FROM topics
            WHERE tree_id = $1 AND deleted_at IS NULL
            ORDER BY created_at DESC`, treeID)
	}
	if err != nil {
		return nil, fmt.Errorf("db: list topics: %w", err)
	}
	defer rows.Close()
	return scanTopicArray(rows)
}

// GetByRootNode returns the topic rooted at the given node.
func (r *PGTopicRepo) GetByRootNode(ctx context.Context, nodeID uuid.UUID) (*Topic, error) {
	var t Topic
	err := scanTopic(r.pool.QueryRow(ctx, `
        SELECT `+topicColumns+` FROM topics
        WHERE root_node_id = $1 AND deleted_at IS NULL
        LIMIT 1`, nodeID), &t)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select topic by root node: %w", err)
	}
	return &t, nil
}

// GetBySlug retrieves a topic by tree_id + slug.
func (r *PGTopicRepo) GetBySlug(ctx context.Context, treeID uuid.UUID, slug string) (*Topic, error) {
	var t Topic
	err := scanTopic(r.pool.QueryRow(ctx, `
        SELECT `+topicColumns+` FROM topics
        WHERE tree_id = $1 AND slug = $2 AND deleted_at IS NULL`, treeID, slug), &t)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select topic by slug: %w", err)
	}
	return &t, nil
}

// Search performs full-text search across topics in a tree.
func (r *PGTopicRepo) Search(ctx context.Context, treeID uuid.UUID, query string, limit, offset int) ([]TopicSummary, int, error) {
	tsQuery := strings.Join(strings.Fields(query), " & ")
	totalQuery := `SELECT COUNT(*) FROM topics
        WHERE tree_id = $1 AND deleted_at IS NULL
        AND search_vector @@ to_tsquery('english', $2)`

	dataQuery := `SELECT id, tree_id, title, slug, description, status,
        node_count, topic_tags, created_at, archived_at
        FROM topics
        WHERE tree_id = $1 AND deleted_at IS NULL
        AND search_vector @@ to_tsquery('english', $2)
        ORDER BY ts_rank(search_vector, to_tsquery('english', $2)) DESC
        LIMIT $3 OFFSET $4`

	var total int
	if err := r.pool.QueryRow(ctx, totalQuery, treeID, tsQuery).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("db: topic search count: %w", err)
	}

	rows, err := r.pool.Query(ctx, dataQuery, treeID, tsQuery, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("db: topic search: %w", err)
	}
	defer rows.Close()

	var summaries []TopicSummary
	for rows.Next() {
		var s TopicSummary
		if err := rows.Scan(&s.ID, &s.TreeID, &s.Title, &s.Slug,
			&s.Description, &s.Status, &s.NodeCount, &s.TopicTags,
			&s.CreatedAt, &s.ArchivedAt); err != nil {
			return nil, 0, fmt.Errorf("db: scan topic summary: %w", err)
		}
		summaries = append(summaries, s)
	}
	return summaries, total, rows.Err()
}

// generateSlug builds a URL-safe slug from a title.
func generateSlug(title string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(title), " ", "-"))
}

// Update modifies a topic's title, description, and/or tags.
func (r *PGTopicRepo) Update(ctx context.Context, id uuid.UUID, input TopicUpdateInput) (*Topic, error) {
	setClauses := []string{}
	args := []any{}
	argIdx := 1

	if input.Title != nil {
		slug := generateSlug(*input.Title)
		setClauses = append(setClauses, fmt.Sprintf("title = $%d", argIdx))
		args = append(args, *input.Title)
		argIdx++
		setClauses = append(setClauses, fmt.Sprintf("slug = $%d", argIdx))
		args = append(args, slug)
		argIdx++
	}
	if input.Description != nil {
		setClauses = append(setClauses, fmt.Sprintf("description = $%d", argIdx))
		args = append(args, *input.Description)
		argIdx++
	}
	if input.TopicTags != nil {
		setClauses = append(setClauses, fmt.Sprintf("topic_tags = $%d", argIdx))
		args = append(args, *input.TopicTags)
		argIdx++
	}

	if len(setClauses) == 0 {
		return r.GetByID(ctx, id)
	}

	args = append(args, id)
	query := fmt.Sprintf(`
        UPDATE topics SET %s
        WHERE id = $%d AND deleted_at IS NULL
        RETURNING `+topicColumns,
		strings.Join(setClauses, ", "), argIdx)

	var t Topic
	if err := scanTopic(r.pool.QueryRow(ctx, query, args...), &t); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("db: update topic: %w", err)
	}
	return &t, nil
}

// Archive marks a topic as archived.
func (r *PGTopicRepo) Archive(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
        UPDATE topics SET status = 'archived', archived_at = clock_timestamp()
        WHERE id = $1 AND deleted_at IS NULL`, id)
	return err
}

// Restore changes a topic from archived back to active.
func (r *PGTopicRepo) Restore(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
        UPDATE topics SET status = 'active', archived_at = NULL
        WHERE id = $1 AND deleted_at IS NULL`, id)
	return err
}

// SoftDelete marks a topic as deleted.
func (r *PGTopicRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
        UPDATE topics SET status = 'deleted', deleted_at = clock_timestamp()
        WHERE id = $1 AND deleted_at IS NULL`, id)
	return err
}

// HardDelete permanently removes a topic.
func (r *PGTopicRepo) HardDelete(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM topics WHERE id = $1`, id)
	return err
}

// GetParentTopics returns all ancestor topics for a given topic.
func (r *PGTopicRepo) GetParentTopics(ctx context.Context, topicID uuid.UUID) ([]Topic, error) {
	rows, err := r.pool.Query(ctx, `
        WITH RECURSIVE ancestors AS (
            SELECT t.* FROM topics t WHERE t.id = $1
            UNION ALL
            SELECT t.* FROM topics t
            JOIN ancestors a ON t.id = a.parent_topic_id
            WHERE a.parent_topic_id IS NOT NULL
        )
        SELECT `+topicColumns+` FROM ancestors
        WHERE id != $1  -- exclude the topic itself
        ORDER BY created_at ASC`, topicID)
	if err != nil {
		return nil, fmt.Errorf("db: parent topics: %w", err)
	}
	defer rows.Close()
	return scanTopicArray(rows)
}

// GetChildTopics returns direct child topics.
func (r *PGTopicRepo) GetChildTopics(ctx context.Context, parentTopicID uuid.UUID) ([]Topic, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+topicColumns+` FROM topics
        WHERE parent_topic_id = $1 AND deleted_at IS NULL
        ORDER BY created_at ASC`, parentTopicID)
	if err != nil {
		return nil, fmt.Errorf("db: child topics: %w", err)
	}
	defer rows.Close()
	return scanTopicArray(rows)
}

// RefreshNodeCount recalculates node_count for a topic.
func (r *PGTopicRepo) RefreshNodeCount(ctx context.Context, topicID uuid.UUID) (int32, error) {
	var count int32
	err := r.pool.QueryRow(ctx, `SELECT refresh_topic_node_count($1)`, topicID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("db: refresh node count: %w", err)
	}
	return count, nil
}

// GetTopicsForNode returns all topics containing the given node in their scope.
func (r *PGTopicRepo) GetTopicsForNode(ctx context.Context, nodeID uuid.UUID) ([]Topic, error) {
	// Use t. prefix to avoid ambiguous column references when joining
	// with topic_member_nodes (both tables have tree_id).
	rows, err := r.pool.Query(ctx, `
        SELECT DISTINCT t.id, t.tree_id, t.root_node_id, t.title, t.description,
            t.slug, t.parent_topic_id, t.status, t.topic_tags,
            t.node_count, t.created_at,
            t.archived_at, t.deleted_at
        FROM topics t
        JOIN topic_member_nodes tmn ON tmn.topic_id = t.id
        WHERE tmn.node_id = $1 AND t.deleted_at IS NULL`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("db: topics for node: %w", err)
	}
	defer rows.Close()
	return scanTopicArray(rows)
}

// ListArchived returns all archived topics in a tree.
func (r *PGTopicRepo) ListArchived(ctx context.Context, treeID uuid.UUID, limit, offset int) ([]Topic, int, error) {
	var total int
	if err := r.pool.QueryRow(ctx, `
        SELECT COUNT(*) FROM topics
        WHERE tree_id = $1 AND status = 'archived' AND deleted_at IS NULL`, treeID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("db: count archived: %w", err)
	}

	rows, err := r.pool.Query(ctx, `
        SELECT `+topicColumns+` FROM topics
        WHERE tree_id = $1 AND status = 'archived' AND deleted_at IS NULL
        ORDER BY archived_at DESC
        LIMIT $2 OFFSET $3`, treeID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("db: list archived: %w", err)
	}
	defer rows.Close()
	topics, err := scanTopicArray(rows)
	if err != nil {
		return nil, 0, err
	}
	return topics, total, nil
}

// ── TopicMemberRepo ─────────────────────────────────────────────────────

// TopicMemberRepo handles CRUD operations on topic memberships.
type TopicMemberRepo interface {
	AddMember(ctx context.Context, topicID, profileID uuid.UUID, role string) (*TopicMember, error)
	RemoveMember(ctx context.Context, topicID, profileID uuid.UUID) error
	UpdateRole(ctx context.Context, topicID, profileID uuid.UUID, role string) error
	GetMembers(ctx context.Context, topicID uuid.UUID) ([]TopicMember, error)
	GetTopicsForProfile(ctx context.Context, profileID uuid.UUID) ([]Topic, error)
}

// PGTopicMemberRepo is the pgx-backed TopicMemberRepo implementation.
type PGTopicMemberRepo struct {
	pool *pgxpool.Pool
}

// NewPGTopicMemberRepo wires the repo to a pgxpool.
func NewPGTopicMemberRepo(pool *pgxpool.Pool) *PGTopicMemberRepo {
	return &PGTopicMemberRepo{pool: pool}
}

// AddMember adds a profile to a topic with the given role.
func (r *PGTopicMemberRepo) AddMember(ctx context.Context, topicID, profileID uuid.UUID, role string) (*TopicMember, error) {
	var m TopicMember
	err := r.pool.QueryRow(ctx, `
        INSERT INTO topic_members (topic_id, profile_id, role)
        VALUES ($1, $2, $3)
        RETURNING topic_id, profile_id, role, joined_at`,
		topicID, profileID, role).Scan(&m.TopicID, &m.ProfileID, &m.Role, &m.JoinedAt)
	if err != nil {
		return nil, fmt.Errorf("db: add topic member: %w", err)
	}
	return &m, nil
}

// RemoveMember removes a profile from a topic.
func (r *PGTopicMemberRepo) RemoveMember(ctx context.Context, topicID, profileID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
        DELETE FROM topic_members WHERE topic_id = $1 AND profile_id = $2`,
		topicID, profileID)
	return err
}

// UpdateRole changes a member's role in a topic.
func (r *PGTopicMemberRepo) UpdateRole(ctx context.Context, topicID, profileID uuid.UUID, role string) error {
	_, err := r.pool.Exec(ctx, `
        UPDATE topic_members SET role = $1
        WHERE topic_id = $2 AND profile_id = $3`,
		role, topicID, profileID)
	return err
}

// GetMembers returns all members of a topic.
func (r *PGTopicMemberRepo) GetMembers(ctx context.Context, topicID uuid.UUID) ([]TopicMember, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT topic_id, profile_id, role, joined_at
        FROM topic_members WHERE topic_id = $1
        ORDER BY joined_at ASC`, topicID)
	if err != nil {
		return nil, fmt.Errorf("db: get topic members: %w", err)
	}
	defer rows.Close()

	var members []TopicMember
	for rows.Next() {
		var m TopicMember
		if err := rows.Scan(&m.TopicID, &m.ProfileID, &m.Role, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("db: scan topic member: %w", err)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// GetTopicsForProfile returns all topics a profile is a member of.
func (r *PGTopicMemberRepo) GetTopicsForProfile(ctx context.Context, profileID uuid.UUID) ([]Topic, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+topicColumns+`
        FROM topics t
        JOIN topic_members tm ON tm.topic_id = t.id
        WHERE tm.profile_id = $1 AND t.deleted_at IS NULL
        ORDER BY t.created_at DESC`, profileID)
	if err != nil {
		return nil, fmt.Errorf("db: topics for profile: %w", err)
	}
	defer rows.Close()
	return scanTopicArray(rows)
}
