// Package db — reference resolution repository.
// Implements reference.ReferenceRepo against PostgreSQL.
// Spec: SPEC-TM-04 §3 (DDL), §4.4 (repo interface), §6 (autocomplete ranking).
package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/reference"
)

// Ensure PGReferenceRepo satisfies the interface.
var _ reference.ReferenceRepo = (*PGReferenceRepo)(nil)

// PGReferenceRepo is the pgx-backed reference repo.
type PGReferenceRepo struct {
	pool *pgxpool.Pool
}

// NewPGReferenceRepo wires the repo to a pgxpool.
func NewPGReferenceRepo(pool *pgxpool.Pool) *PGReferenceRepo {
	return &PGReferenceRepo{pool: pool}
}

// ── Resolved Reference Links ──────────────────────────────────────────────

// InsertResolvedRef persists a single resolved reference link.
func (r *PGReferenceRepo) InsertResolvedRef(ctx context.Context, link reference.ResolvedReferenceLink) (*reference.ResolvedReferenceLink, error) {
	var out reference.ResolvedReferenceLink
	err := r.pool.QueryRow(ctx, `
        INSERT INTO node_resolved_refs (node_id, tree_id, topic_id, raw_ref, slug, resolved_by, context_hash)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
        ON CONFLICT (node_id, topic_id) DO NOTHING
        RETURNING id, node_id, tree_id, topic_id, raw_ref, slug, resolved_at, resolved_by, context_hash`,
		link.NodeID, link.TreeID, link.TopicID, link.RawRef, link.Slug, link.ResolvedBy, link.ContextHash,
	).Scan(&out.ID, &out.NodeID, &out.TreeID, &out.TopicID, &out.RawRef,
		&out.Slug, &out.ResolvedAt, &out.ResolvedBy, &out.ContextHash)
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT: row already existed; return the input as-is.
		return &link, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: insert resolved ref: %w", err)
	}
	return &out, nil
}

// InsertResolvedRefs persists multiple resolved reference links.
func (r *PGReferenceRepo) InsertResolvedRefs(ctx context.Context, links []reference.ResolvedReferenceLink) error {
	if len(links) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, link := range links {
		_, err := tx.Exec(ctx, `
            INSERT INTO node_resolved_refs (node_id, tree_id, topic_id, raw_ref, slug, resolved_by, context_hash)
            VALUES ($1, $2, $3, $4, $5, $6, $7)
            ON CONFLICT (node_id, topic_id) DO NOTHING`,
			link.NodeID, link.TreeID, link.TopicID, link.RawRef, link.Slug, link.ResolvedBy, link.ContextHash)
		if err != nil {
			return fmt.Errorf("db: insert resolved ref batch: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// GetResolvedRefsForNode returns all resolved references for a node.
func (r *PGReferenceRepo) GetResolvedRefsForNode(ctx context.Context, nodeID uuid.UUID) ([]reference.ResolvedReferenceLink, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT id, node_id, tree_id, topic_id, raw_ref, slug, resolved_at, resolved_by, context_hash
        FROM node_resolved_refs
        WHERE node_id = $1
        ORDER BY resolved_at ASC`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("db: get resolved refs for node: %w", err)
	}
	defer rows.Close()

	var links []reference.ResolvedReferenceLink
	for rows.Next() {
		var l reference.ResolvedReferenceLink
		if err := rows.Scan(&l.ID, &l.NodeID, &l.TreeID, &l.TopicID, &l.RawRef,
			&l.Slug, &l.ResolvedAt, &l.ResolvedBy, &l.ContextHash); err != nil {
			return nil, fmt.Errorf("db: scan resolved ref: %w", err)
		}
		links = append(links, l)
	}
	return links, rows.Err()
}

// DeleteResolvedRefsForNode removes all resolved references for a node.
func (r *PGReferenceRepo) DeleteResolvedRefsForNode(ctx context.Context, nodeID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM node_resolved_refs WHERE node_id = $1`, nodeID)
	if err != nil {
		return fmt.Errorf("db: delete resolved refs for node: %w", err)
	}
	return nil
}

// GetTopicReferenceCount returns the number of nodes referencing a topic.
func (r *PGReferenceRepo) GetTopicReferenceCount(ctx context.Context, topicID uuid.UUID) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM node_resolved_refs WHERE topic_id = $1`, topicID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("db: get topic ref count: %w", err)
	}
	return count, nil
}

// ── Autocomplete (spec §6.1 ranking rules) ────────────────────────────────
//
// Ranking order:
// 1. Exact slug prefix match first
// 2. Title prefix match second
// 3. Slug/title contains match third
// Within each tier: last_active_at DESC, node_count DESC, title ASC.

func (r *PGReferenceRepo) AutocompleteTopics(ctx context.Context, treeID uuid.UUID, prefix, include string, limit int) ([]reference.ReferenceAutocompleteResult, error) {
	if limit <= 0 {
		limit = 10
	}

	statusFilter := "AND t.status = 'active'"
	switch include {
	case "archived":
		statusFilter = "AND t.status IN ('active', 'archived')"
	case "all":
		statusFilter = "AND t.status != 'deleted'"
	}

	// Use ILIKE for prefix/contains matching; lower() for case-insensitivity.
	// The pattern is escaped for LIKE special characters (%, _, \).
	likePrefix := escapeLike(prefix) + "%"
	likeContains := "%" + escapeLike(prefix) + "%"

	rows, err := r.pool.Query(ctx, `
        SELECT
            t.slug,
            t.title,
            CASE
                WHEN t.slug ILIKE $2 THEN 'prefix'
                WHEN t.title ILIKE $2 THEN 'prefix'
                ELSE 'contains'
            END AS match_type,
            t.status,
            t.node_count
        FROM topics t
        WHERE t.tree_id = $1
          `+statusFilter+`
          AND (t.slug ILIKE $2 OR t.slug ILIKE $3 OR t.title ILIKE $2 OR t.title ILIKE $3)
        ORDER BY
            CASE
                WHEN t.slug ILIKE $2 THEN 0
                WHEN t.title ILIKE $2 THEN 1
                ELSE 2
            END,
            t.last_active_at DESC NULLS LAST,
            t.node_count DESC,
            t.title ASC
        LIMIT $4`,
		treeID, likePrefix, likeContains, limit)
	if err != nil {
		return nil, fmt.Errorf("db: autocomplete topics: %w", err)
	}
	defer rows.Close()

	var results []reference.ReferenceAutocompleteResult
	for rows.Next() {
		var res reference.ReferenceAutocompleteResult
		if err := rows.Scan(&res.Slug, &res.Title, &res.MatchType, &res.Status, &res.NodeCount); err != nil {
			return nil, fmt.Errorf("db: scan autocomplete result: %w", err)
		}
		results = append(results, res)
	}
	return results, rows.Err()
}

// ── Topic Lookup ──────────────────────────────────────────────────────────

// GetTopicBySlug returns a topic by tree_id + slug.
func (r *PGReferenceRepo) GetTopicBySlug(ctx context.Context, treeID uuid.UUID, slug string) (*reference.Topic, error) {
	var t reference.Topic
	var parentTopicID *uuid.UUID
	var archivedAt *time.Time
	err := r.pool.QueryRow(ctx, `
        SELECT id, tree_id, root_node_id, title, description, slug,
               parent_topic_id, status, topic_tags, node_count, created_at, archived_at
        FROM topics
        WHERE tree_id = $1 AND slug = $2 AND deleted_at IS NULL`,
		treeID, slug).Scan(
		&t.ID, &t.TreeID, &t.RootNodeID, &t.Title, &t.Description, &t.Slug,
		&parentTopicID, &t.Status, &t.TopicTags, &t.NodeCount, &t.CreatedAt, &archivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: get topic by slug: %w", err)
	}
	t.ParentTopicID = parentTopicID
	t.ArchivedAt = archivedAt
	return &t, nil
}

// ── Cache (spec §3.2, §8.4) ───────────────────────────────────────────────

func (r *PGReferenceRepo) UpsertReferenceCache(ctx context.Context, topicID, treeID uuid.UUID, contextHash string, nodeCount int, payload json.RawMessage) error {
	_, err := r.pool.Exec(ctx, `
        INSERT INTO reference_resolution_cache (topic_id, tree_id, context_hash, node_count, payload)
        VALUES ($1, $2, $3, $4, $5)
        ON CONFLICT (topic_id)
        DO UPDATE SET
            tree_id = EXCLUDED.tree_id,
            context_hash = EXCLUDED.context_hash,
            node_count = EXCLUDED.node_count,
            payload = EXCLUDED.payload,
            created_at = clock_timestamp(),
            expires_at = clock_timestamp() + interval '24 hours',
            hit_count = 0`,
		topicID, treeID, contextHash, nodeCount, payload)
	if err != nil {
		return fmt.Errorf("db: upsert reference cache: %w", err)
	}
	return nil
}

func (r *PGReferenceRepo) GetReferenceCache(ctx context.Context, topicID uuid.UUID) (*reference.ReferenceCacheEntry, error) {
	var entry reference.ReferenceCacheEntry
	err := r.pool.QueryRow(ctx, `
        UPDATE reference_resolution_cache
        SET hit_count = hit_count + 1
        WHERE topic_id = $1 AND expires_at > clock_timestamp()
        RETURNING id, topic_id, tree_id, context_hash, node_count, payload, created_at, expires_at, hit_count`,
		topicID).Scan(
		&entry.ID, &entry.TopicID, &entry.TreeID, &entry.ContextHash, &entry.NodeCount,
		&entry.Payload, &entry.CreatedAt, &entry.ExpiresAt, &entry.HitCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: get reference cache: %w", err)
	}
	return &entry, nil
}

func (r *PGReferenceRepo) DeleteReferenceCache(ctx context.Context, topicID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM reference_resolution_cache WHERE topic_id = $1`, topicID)
	if err != nil {
		return fmt.Errorf("db: delete reference cache: %w", err)
	}
	return nil
}

// ── Log (spec §3.3) ───────────────────────────────────────────────────────

func (r *PGReferenceRepo) InsertReferenceLog(ctx context.Context, entry reference.ReferenceLogEntry) error {
	// Handle nullable profile_id (may be uuid.Nil from auth context).
	var profileID any
	if entry.ProfileID != uuid.Nil {
		profileID = entry.ProfileID
	}

	_, err := r.pool.Exec(ctx, `
        INSERT INTO reference_resolution_log (tree_id, node_id, profile_id, raw_ref, slug,
                                                topic_id, status, error_code, duration_ms)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		entry.TreeID, nullableUUID(entry.NodeID), profileID,
		entry.RawRef, entry.Slug, nullableUUID(entry.TopicID),
		entry.Status, entry.ErrorCode, entry.DurationMs)
	if err != nil {
		// Retry with NULL profile_id (FK on profiles might fail).
		if profileID != nil {
			_, err = r.pool.Exec(ctx, `
                INSERT INTO reference_resolution_log (tree_id, node_id, profile_id, raw_ref, slug,
                                                        topic_id, status, error_code, duration_ms)
                VALUES ($1, $2, NULL, $3, $4, $5, $6, $7, $8)`,
				entry.TreeID, nullableUUID(entry.NodeID),
				entry.RawRef, entry.Slug, nullableUUID(entry.TopicID),
				entry.Status, entry.ErrorCode, entry.DurationMs)
		}
		if err != nil {
			return fmt.Errorf("db: insert reference log: %w", err)
		}
	}
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────

// escapeLike escapes LIKE pattern special characters (%, _, \).
func escapeLike(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '%', '_', '\\':
			b = append(b, '\\')
		}
		b = append(b, s[i])
	}
	return string(b)
}

// nullableUUID converts a *uuid.UUID to interface{} for pgx, returning nil for Nil values.
func nullableUUID(u *uuid.UUID) any {
	if u == nil || *u == uuid.Nil {
		return nil
	}
	return *u
}
