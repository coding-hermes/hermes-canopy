// Package db — topic search log repository.
// Implements search.TopicSearchLogRepo against PostgreSQL.
// Spec: SPEC-TM-03 §3.1 (topic_search_log table).
package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/search"
)

// Ensure PGTopicSearchLogRepo satisfies the interface.
var _ search.TopicSearchLogRepo = (*PGTopicSearchLogRepo)(nil)

// PGTopicSearchLogRepo is the pgx-backed search log repo.
type PGTopicSearchLogRepo struct {
	pool *pgxpool.Pool
}

// NewPGTopicSearchLogRepo wires the repo to a pgxpool.
func NewPGTopicSearchLogRepo(pool *pgxpool.Pool) *PGTopicSearchLogRepo {
	return &PGTopicSearchLogRepo{pool: pool}
}

// InsertSearchLog records a search analytics entry.
// profile_id is nullable — if the provided ID doesn't reference a valid
// profile (common when using user_id from auth), the insert retries with NULL.
func (r *PGTopicSearchLogRepo) InsertSearchLog(ctx context.Context, entry search.SearchLogEntry) error {
	filters := entry.FiltersApplied
	if len(filters) == 0 {
		filters = json.RawMessage(`{}`)
	}

	// Determine profile_id: use NULL when Nil.
	var profileID any
	if entry.ProfileID != uuid.Nil {
		profileID = entry.ProfileID
	}

	_, err := r.pool.Exec(ctx, `
        INSERT INTO topic_search_log (tree_id, profile_id, query_text, result_count,
                                       filters_applied, injected_count, search_duration_ms)
        VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		entry.TreeID, profileID, entry.QueryText, entry.ResultCount,
		filters, entry.InjectedCount, entry.SearchDurationMs)
	if err != nil {
		// FK violation on profile_id — retry with NULL (analytics is best-effort).
		if profileID != nil {
			_, err = r.pool.Exec(ctx, `
				INSERT INTO topic_search_log (tree_id, profile_id, query_text, result_count,
				                               filters_applied, injected_count, search_duration_ms)
				VALUES ($1, NULL, $2, $3, $4, $5, $6)`,
				entry.TreeID, entry.QueryText, entry.ResultCount,
				filters, entry.InjectedCount, entry.SearchDurationMs)
		}
		if err != nil {
			return fmt.Errorf("db: insert search log: %w", err)
		}
	}
	return nil
}
