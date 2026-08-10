// Package db — topic detection repositories.
// Implements TopicProposalRepo, DetectionConfigRepo, and SubjectCooldownRepo
// backed by pgx against PostgreSQL. Spec: SPEC-TM-02 §8.1.
package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// proposalColumns lists all columns in topic_proposals for SELECT.
const proposalColumns = `id, tree_id, root_node_id, title, description,
    detection_type, confidence, subject_key, status, expires_at,
    created_at, resolved_at, evidence`

func scanProposal(row pgx.Row, p *TopicProposal) error {
	return row.Scan(
		&p.ID, &p.TreeID, &p.RootNodeID, &p.Title, &p.Description,
		&p.DetectionType, &p.Confidence, &p.SubjectKey, &p.Status,
		&p.ExpiresAt, &p.CreatedAt, &p.ResolvedAt, &p.Evidence,
	)
}

// ── TopicProposalRepo ───────────────────────────────────────────────────

// TopicProposalRepo handles persistence of topic detection proposals.
type TopicProposalRepo interface {
	Create(ctx context.Context, p *TopicProposal) (*TopicProposal, error)
	GetByID(ctx context.Context, id uuid.UUID) (*TopicProposal, error)
	ListPending(ctx context.Context, treeID uuid.UUID) ([]TopicProposal, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status string, resolvedAt interface{}) error
	ExpirePending(ctx context.Context, treeID uuid.UUID, beforeSeq int64) (int, error)
}

// PGTopicProposalRepo is the pgx-backed implementation.
type PGTopicProposalRepo struct {
	pool *pgxpool.Pool
}

// NewPGTopicProposalRepo wires the repo to a pgxpool.
func NewPGTopicProposalRepo(pool *pgxpool.Pool) *PGTopicProposalRepo {
	return &PGTopicProposalRepo{pool: pool}
}

// Create inserts a new proposal.
func (r *PGTopicProposalRepo) Create(ctx context.Context, p *TopicProposal) (*TopicProposal, error) {
	if p == nil {
		return nil, errors.New("db: proposal is nil")
	}
	evidence := p.Evidence
	if len(evidence) == 0 {
		evidence = json.RawMessage(`{}`)
	}
	row := r.pool.QueryRow(ctx, `
        INSERT INTO topic_proposals
            (tree_id, root_node_id, title, description, detection_type,
             confidence, subject_key, status, expires_at, evidence)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
        RETURNING `+proposalColumns,
		p.TreeID, p.RootNodeID, p.Title, p.Description, p.DetectionType,
		p.Confidence, p.SubjectKey, p.Status, p.ExpiresAt, evidence,
	)
	var out TopicProposal
	if err := scanProposal(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert proposal: %w", err)
	}
	return &out, nil
}

// GetByID retrieves a proposal by ID. Returns ErrNotFound if missing.
func (r *PGTopicProposalRepo) GetByID(ctx context.Context, id uuid.UUID) (*TopicProposal, error) {
	var p TopicProposal
	err := scanProposal(r.pool.QueryRow(ctx,
		`SELECT `+proposalColumns+` FROM topic_proposals WHERE id = $1`, id), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get proposal: %w", err)
	}
	return &p, nil
}

// ListPending returns all pending proposals for a tree, ordered newest-first.
func (r *PGTopicProposalRepo) ListPending(ctx context.Context, treeID uuid.UUID) ([]TopicProposal, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+proposalColumns+` FROM topic_proposals
         WHERE tree_id = $1 AND status = 'pending'
         ORDER BY created_at DESC`, treeID)
	if err != nil {
		return nil, fmt.Errorf("db: list pending proposals: %w", err)
	}
	defer rows.Close()
	var out []TopicProposal
	for rows.Next() {
		var p TopicProposal
		if err := scanProposal(rows, &p); err != nil {
			return nil, fmt.Errorf("db: scan proposal: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateStatus sets the status and resolved_at timestamp for a proposal.
// resolvedAt may be nil (sets NULL), a time.Time, or *time.Time.
func (r *PGTopicProposalRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status string, resolvedAt interface{}) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE topic_proposals SET status = $2, resolved_at = $3 WHERE id = $1`,
		id, status, resolvedAt)
	if err != nil {
		return fmt.Errorf("db: update proposal status: %w", err)
	}
	return nil
}

// ExpirePending marks all pending proposals in a tree whose root node has
// a sequence number below beforeSeq as expired. Returns the count expired.
// Used by the expiry sweep when new messages arrive.
func (r *PGTopicProposalRepo) ExpirePending(ctx context.Context, treeID uuid.UUID, beforeSeq int64) (int, error) {
	tag, err := r.pool.Exec(ctx, `
        UPDATE topic_proposals SET status = 'expired',
               resolved_at = clock_timestamp()
        WHERE tree_id = $1 AND status = 'pending'
          AND root_node_id IN (
              SELECT id FROM nodes WHERE tree_id = $1 AND sequence_num < $2
          )`, treeID, beforeSeq)
	if err != nil {
		return 0, fmt.Errorf("db: expire proposals: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ── DetectionConfigRepo ─────────────────────────────────────────────────

// DetectionConfigRepo manages per-tree detection configuration.
type DetectionConfigRepo interface {
	Get(ctx context.Context, treeID uuid.UUID) (*DetectionConfigRecord, error)
	Upsert(ctx context.Context, cfg DetectionConfigRecord) (*DetectionConfigRecord, error)
	UpdateProposalTracking(ctx context.Context, treeID uuid.UUID, lastProposalSeq int64, messagesSince int) error
}

// PGDetectionConfigRepo is the pgx-backed implementation.
type PGDetectionConfigRepo struct {
	pool *pgxpool.Pool
}

// NewPGDetectionConfigRepo wires the repo to a pgxpool.
func NewPGDetectionConfigRepo(pool *pgxpool.Pool) *PGDetectionConfigRepo {
	return &PGDetectionConfigRepo{pool: pool}
}

// Get retrieves the config row for a tree. If no row exists (e.g. tree
// created before the migration), a default row is inserted and returned.
func (r *PGDetectionConfigRepo) Get(ctx context.Context, treeID uuid.UUID) (*DetectionConfigRecord, error) {
	// Ensure a default row exists, then read it.
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO topic_detection_config (tree_id) VALUES ($1) ON CONFLICT (tree_id) DO NOTHING`,
		treeID); err != nil {
		return nil, fmt.Errorf("db: ensure detection config: %w", err)
	}
	var c DetectionConfigRecord
	err := r.pool.QueryRow(ctx, `
        SELECT tree_id, auto_create, always_ask, detection_level,
               min_messages_per_topic, proposal_cooldown,
               last_proposal_seq, messages_since_proposal, updated_at
        FROM topic_detection_config WHERE tree_id = $1`, treeID).Scan(
		&c.TreeID, &c.AutoCreate, &c.AlwaysAsk, &c.DetectionLevel,
		&c.MinMessagesPerTopic, &c.ProposalCooldown,
		&c.LastProposalSeq, &c.MessagesSinceProposal, &c.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("db: get detection config: %w", err)
	}
	return &c, nil
}

// Upsert inserts or updates a detection config row.
func (r *PGDetectionConfigRepo) Upsert(ctx context.Context, cfg DetectionConfigRecord) (*DetectionConfigRecord, error) {
	var c DetectionConfigRecord
	err := r.pool.QueryRow(ctx, `
        INSERT INTO topic_detection_config
            (tree_id, auto_create, always_ask, detection_level,
             min_messages_per_topic, proposal_cooldown)
        VALUES ($1, $2, $3, $4, $5, $6)
        ON CONFLICT (tree_id) DO UPDATE SET
            auto_create = EXCLUDED.auto_create,
            always_ask = EXCLUDED.always_ask,
            detection_level = EXCLUDED.detection_level,
            min_messages_per_topic = EXCLUDED.min_messages_per_topic,
            proposal_cooldown = EXCLUDED.proposal_cooldown,
            updated_at = clock_timestamp()
        RETURNING tree_id, auto_create, always_ask, detection_level,
                  min_messages_per_topic, proposal_cooldown,
                  last_proposal_seq, messages_since_proposal, updated_at`,
		cfg.TreeID, cfg.AutoCreate, cfg.AlwaysAsk, cfg.DetectionLevel,
		cfg.MinMessagesPerTopic, cfg.ProposalCooldown,
	).Scan(
		&c.TreeID, &c.AutoCreate, &c.AlwaysAsk, &c.DetectionLevel,
		&c.MinMessagesPerTopic, &c.ProposalCooldown,
		&c.LastProposalSeq, &c.MessagesSinceProposal, &c.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("db: upsert detection config: %w", err)
	}
	return &c, nil
}

// UpdateProposalTracking records the last proposal sequence number and
// messages-since counter after a proposal is emitted.
func (r *PGDetectionConfigRepo) UpdateProposalTracking(ctx context.Context, treeID uuid.UUID, lastProposalSeq int64, messagesSince int) error {
	_, err := r.pool.Exec(ctx, `
        UPDATE topic_detection_config
        SET last_proposal_seq = $2, messages_since_proposal = $3,
            updated_at = clock_timestamp()
        WHERE tree_id = $1`, treeID, lastProposalSeq, messagesSince)
	if err != nil {
		return fmt.Errorf("db: update proposal tracking: %w", err)
	}
	return nil
}

// ── SubjectCooldownRepo ─────────────────────────────────────────────────

// SubjectCooldownRepo manages per-tree subject-key rejection cooldowns.
type SubjectCooldownRepo interface {
	Add(ctx context.Context, sc SubjectCooldown) error
	IsActive(ctx context.Context, treeID uuid.UUID, subjectKey string) (bool, error)
	CleanExpired(ctx context.Context) (int, error)
}

// PGSubjectCooldownRepo is the pgx-backed implementation.
type PGSubjectCooldownRepo struct {
	pool *pgxpool.Pool
}

// NewPGSubjectCooldownRepo wires the repo to a pgxpool.
func NewPGSubjectCooldownRepo(pool *pgxpool.Pool) *PGSubjectCooldownRepo {
	return &PGSubjectCooldownRepo{pool: pool}
}

// Add inserts or replaces a cooldown for the given tree + subject key.
func (r *PGSubjectCooldownRepo) Add(ctx context.Context, sc SubjectCooldown) error {
	_, err := r.pool.Exec(ctx, `
        INSERT INTO subject_cooldowns (tree_id, subject_key, cooldown_until)
        VALUES ($1, $2, $3)
        ON CONFLICT (tree_id, subject_key) DO UPDATE SET
            cooldown_until = EXCLUDED.cooldown_until,
            created_at = clock_timestamp()`,
		sc.TreeID, sc.SubjectKey, sc.CooldownUntil)
	if err != nil {
		return fmt.Errorf("db: add subject cooldown: %w", err)
	}
	return nil
}

// IsActive returns true if an active (non-expired) cooldown exists for the
// subject key in the given tree.
func (r *PGSubjectCooldownRepo) IsActive(ctx context.Context, treeID uuid.UUID, subjectKey string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM subject_cooldowns
          WHERE tree_id = $1 AND subject_key = $2 AND cooldown_until > now())`,
		treeID, subjectKey).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("db: check subject cooldown: %w", err)
	}
	return exists, nil
}

// CleanExpired removes all expired cooldown rows. Returns the count deleted.
func (r *PGSubjectCooldownRepo) CleanExpired(ctx context.Context) (int, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM subject_cooldowns WHERE cooldown_until <= now()`)
	if err != nil {
		return 0, fmt.Errorf("db: clean expired cooldowns: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
