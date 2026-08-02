package duckdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/card"
)

// CardRepo implements card.CardRepository backed by an in-process DuckDB database.
type CardRepo struct {
	db *sql.DB
}

// NewCardRepo creates a new DuckDB-backed card repository.
func NewCardRepo(store *Store) *CardRepo {
	return &CardRepo{db: store.DB()}
}

// Close closes the underlying DuckDB database.
func (r *CardRepo) Close() error {
	return r.db.Close()
}

// Create inserts a new card row and returns the created Card.
func (r *CardRepo) Create(ctx context.Context, input card.CreateCardInput) (*card.Card, error) {
	now := time.Now().UTC()

	actionsJSON := "[]"
	if input.Actions != nil {
		b, err := json.Marshal(input.Actions)
		if err != nil {
			return nil, fmt.Errorf("duckdb: marshal actions: %w", err)
		}
		actionsJSON = string(b)
	}

	dataJSON := "{}"
	if len(input.Data) > 0 {
		dataJSON = string(input.Data)
	}

	_, err := r.db.ExecContext(ctx,
		`INSERT INTO cards (id, tree_id, node_id, app_id, card_type, data, actions, status, context_hash, revision, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'active', ?, 1, ?, ?)`,
		input.ID.String(),
		input.TreeID.String(),
		input.NodeID.String(),
		input.AppID,
		string(input.CardType),
		dataJSON,
		actionsJSON,
		input.ContextHash,
		now,
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("duckdb: insert: %w", err)
	}

	return r.Get(ctx, input.ID)
}

// Get retrieves a card by ID.
func (r *CardRepo) Get(ctx context.Context, id uuid.UUID) (*card.Card, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, tree_id, node_id, app_id, card_type, data, actions, status, context_hash, revision, created_at, updated_at, dismissed_at, archived_at
		 FROM cards WHERE id = ?`, id.String())

	return scanCard(row)
}

// List queries cards with optional filters.
func (r *CardRepo) List(ctx context.Context, options card.ListCardsOptions) ([]card.Card, error) {
	query := `SELECT id, tree_id, node_id, app_id, card_type, data, actions, status, context_hash, revision, created_at, updated_at, dismissed_at, archived_at
		 FROM cards WHERE 1=1`
	args := []any{}

	if options.TreeID != nil {
		query += " AND tree_id = ?"
		args = append(args, options.TreeID.String())
	}
	if options.NodeID != nil {
		query += " AND node_id = ?"
		args = append(args, options.NodeID.String())
	}
	if options.CardType != nil {
		query += " AND card_type = ?"
		args = append(args, string(*options.CardType))
	}
	if options.Status != nil {
		query += " AND status = ?"
		args = append(args, string(*options.Status))
	}
	if options.AppID != "" {
		query += " AND app_id = ?"
		args = append(args, options.AppID)
	}

	query += " ORDER BY updated_at DESC"

	limit := options.Limit
	if limit <= 0 {
		limit = 50
	}
	query += " LIMIT ?"
	args = append(args, limit)

	if options.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, options.Offset)
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("duckdb: list: %w", err)
	}
	defer rows.Close()

	var cards []card.Card
	for rows.Next() {
		c, err := scanCardFromRows(rows)
		if err != nil {
			return nil, err
		}
		cards = append(cards, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("duckdb: list rows: %w", err)
	}
	if cards == nil {
		cards = []card.Card{}
	}
	return cards, nil
}

// Patch updates selected fields and increments revision.
//
// DuckDB does not release the PK index slot within a transaction, so a
// SELECT→DELETE→INSERT cycle inside a single tx fails with a duplicate-key
// error (and parameterized UPDATE is unreliable on indexed tables — it
// internally rewrites to DELETE+INSERT and can hit the same constraint on
// multi-column schemas). We work around this by executing DELETE and INSERT
// as independent statements (no transaction wrapper). Since the database is
// local/in-process, there is no risk of concurrent mutation between the two
// statements.
func (r *CardRepo) Patch(ctx context.Context, id uuid.UUID, expectedRevision int64, input card.PatchCardInput) (*card.Card, error) {
	now := time.Now().UTC()

	// Read current row to check revision and preserve immutable / unpatchable fields.
	current, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Revision != expectedRevision {
		return nil, fmt.Errorf("duckdb: patch %s: revision mismatch or not found", id)
	}

	// Merge input values on top of the current row.
	newRev := current.Revision + 1
	dataJSON := string(current.Data)
	if input.Data != nil {
		dataJSON = string(*input.Data)
	}
	actionsJSON := "[]"
	if input.Actions != nil {
		b, marshalErr := json.Marshal(*input.Actions)
		if marshalErr != nil {
			return nil, fmt.Errorf("duckdb: marshal actions: %w", marshalErr)
		}
		actionsJSON = string(b)
	} else if len(current.Actions) > 0 {
		b, _ := json.Marshal(current.Actions)
		actionsJSON = string(b)
	}
	status := current.Status
	dismissedAt := current.DismissedAt
	archivedAt := current.ArchivedAt
	if input.Status != nil {
		status = *input.Status
		if status == card.CardStatusDismissed {
			dismissedAt = &now
		}
		if status == card.CardStatusArchived {
			archivedAt = &now
		}
	}
	ctxHash := current.ContextHash
	if input.ContextHash != nil {
		ctxHash = *input.ContextHash
	}

	// DELETE the old row first (autonomous statement — releases PK slot).
	if _, delErr := r.db.ExecContext(ctx, "DELETE FROM cards WHERE id = ?", id.String()); delErr != nil {
		return nil, fmt.Errorf("duckdb: patch: delete: %w", delErr)
	}

	// INSERT the updated row (new statement — no PK conflict).
	_, insErr := r.db.ExecContext(ctx,
		`INSERT INTO cards (id, tree_id, node_id, app_id, card_type, data, actions, status, context_hash, revision, created_at, updated_at, dismissed_at, archived_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id.String(),
		current.TreeID.String(),
		current.NodeID.String(),
		current.AppID,
		string(current.CardType),
		dataJSON,
		actionsJSON,
		string(status),
		ctxHash,
		newRev,
		current.CreatedAt,
		now,
		dismissedAt,
		archivedAt,
	)
	if insErr != nil {
		return nil, fmt.Errorf("duckdb: patch: insert: %w", insErr)
	}

	return r.Get(ctx, id)
}

// AppendEvent inserts a new event row and returns the created event.
func (r *CardRepo) AppendEvent(ctx context.Context, cardID uuid.UUID, input card.AppendEventInput) (*card.CardEvent, error) {
	now := time.Now().UTC()

	payloadJSON := "{}"
	if len(input.Payload) > 0 {
		payloadJSON = string(input.Payload)
	}

	// Use RETURNING to get the auto-generated sequence value.
	row := r.db.QueryRowContext(ctx,
		`INSERT INTO events (event_id, card_id, event_type, actor_kind, actor_id, payload, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 RETURNING sequence`,
		input.EventID.String(),
		cardID.String(),
		string(input.EventType),
		string(input.ActorKind),
		input.ActorID,
		payloadJSON,
		now,
	)

	var seq int64
	if err := row.Scan(&seq); err != nil {
		return nil, fmt.Errorf("duckdb: append event: %w", err)
	}

	return &card.CardEvent{
		Sequence:  seq,
		EventID:   input.EventID,
		CardID:    cardID,
		EventType: input.EventType,
		ActorKind: input.ActorKind,
		ActorID:   input.ActorID,
		Payload:   json.RawMessage(payloadJSON),
		CreatedAt: now,
	}, nil
}

// ListEvents returns events for a card after a given sequence.
func (r *CardRepo) ListEvents(ctx context.Context, cardID uuid.UUID, afterSequence int64, limit int) ([]card.CardEvent, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT sequence, event_id, card_id, event_type, actor_kind, actor_id, payload, created_at
		 FROM events WHERE card_id = ? AND sequence > ? ORDER BY sequence ASC LIMIT ?`,
		cardID.String(), afterSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("duckdb: list events: %w", err)
	}
	defer rows.Close()

	var events []card.CardEvent
	for rows.Next() {
		e, err := scanEventFromRows(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("duckdb: list events rows: %w", err)
	}
	if events == nil {
		events = []card.CardEvent{}
	}
	return events, nil
}

// MaxSequence returns the highest event sequence for a card.
func (r *CardRepo) MaxSequence(ctx context.Context, cardID uuid.UUID) (int64, error) {
	var seq sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT MAX(sequence) FROM events WHERE card_id = ?`, cardID.String()).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("duckdb: max sequence: %w", err)
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.Int64, nil
}

// GetByContextHash finds all cards with the given context hash.
func (r *CardRepo) GetByContextHash(ctx context.Context, contextHash string) ([]card.Card, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, tree_id, node_id, app_id, card_type, data, actions, status, context_hash, revision, created_at, updated_at, dismissed_at, archived_at
		 FROM cards WHERE context_hash = ?`, contextHash)
	if err != nil {
		return nil, fmt.Errorf("duckdb: get by context hash: %w", err)
	}
	defer rows.Close()

	var cards []card.Card
	for rows.Next() {
		c, err := scanCardFromRows(rows)
		if err != nil {
			return nil, err
		}
		cards = append(cards, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("duckdb: get by context hash rows: %w", err)
	}
	if cards == nil {
		cards = []card.Card{}
	}
	return cards, nil
}

// ── Internal scanners ─────────────────────────────────────────────────

func scanCard(row *sql.Row) (*card.Card, error) {
	var (
		id, treeID, nodeID, appID, cardTypeStr, dataJSON, actionsJSON, status, contextHash string
		revision                                                                           int64
		createdAt, updatedAt                                                               time.Time
		dismissedAt, archivedAt                                                            sql.NullTime
	)

	err := row.Scan(&id, &treeID, &nodeID, &appID, &cardTypeStr, &dataJSON, &actionsJSON, &status,
		&contextHash, &revision, &createdAt, &updatedAt, &dismissedAt, &archivedAt)
	if err != nil {
		return nil, fmt.Errorf("duckdb: scan: %w", err)
	}

	return parseRow(id, treeID, nodeID, appID, cardTypeStr, dataJSON, actionsJSON, status,
		contextHash, revision, createdAt, updatedAt, dismissedAt, archivedAt)
}

func scanCardFromRows(rows *sql.Rows) (*card.Card, error) {
	var (
		id, treeID, nodeID, appID, cardTypeStr, dataJSON, actionsJSON, status, contextHash string
		revision                                                                           int64
		createdAt, updatedAt                                                               time.Time
		dismissedAt, archivedAt                                                            sql.NullTime
	)

	err := rows.Scan(&id, &treeID, &nodeID, &appID, &cardTypeStr, &dataJSON, &actionsJSON, &status,
		&contextHash, &revision, &createdAt, &updatedAt, &dismissedAt, &archivedAt)
	if err != nil {
		return nil, fmt.Errorf("duckdb: scan: %w", err)
	}

	return parseRow(id, treeID, nodeID, appID, cardTypeStr, dataJSON, actionsJSON, status,
		contextHash, revision, createdAt, updatedAt, dismissedAt, archivedAt)
}

func scanEventFromRows(rows *sql.Rows) (*card.CardEvent, error) {
	var (
		seq                                                           int64
		eventIDStr, cardIDStr, eventType, actorKind, actorID, payload string
		createdAt                                                     time.Time
	)

	err := rows.Scan(&seq, &eventIDStr, &cardIDStr, &eventType, &actorKind, &actorID, &payload, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("duckdb: scan event: %w", err)
	}

	eid, err := uuid.Parse(eventIDStr)
	if err != nil {
		return nil, fmt.Errorf("duckdb: parse event id: %w", err)
	}
	cid, err := uuid.Parse(cardIDStr)
	if err != nil {
		return nil, fmt.Errorf("duckdb: parse card id: %w", err)
	}

	return &card.CardEvent{
		Sequence:  seq,
		EventID:   eid,
		CardID:    cid,
		EventType: card.CardEventType(eventType),
		ActorKind: card.CardActorKind(actorKind),
		ActorID:   actorID,
		Payload:   json.RawMessage(payload),
		CreatedAt: createdAt.UTC(),
	}, nil
}

func parseRow(
	id, treeIDStr, nodeIDStr, appID, cardTypeStr, dataJSON, actionsJSON, status string,
	contextHash string, revision int64,
	createdAt, updatedAt time.Time,
	dismissedAt, archivedAt sql.NullTime,
) (*card.Card, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("duckdb: parse id: %w", err)
	}

	treeUID := uuid.Nil
	if treeIDStr != "" {
		treeUID, _ = uuid.Parse(treeIDStr)
	}
	nodeUID := uuid.Nil
	if nodeIDStr != "" {
		nodeUID, _ = uuid.Parse(nodeIDStr)
	}

	var actions []card.CardAction
	if actionsJSON != "" && actionsJSON != "[]" {
		if err := json.Unmarshal([]byte(actionsJSON), &actions); err != nil {
			actions = []card.CardAction{}
		}
	}

	var dismissedAtPtr *time.Time
	if dismissedAt.Valid {
		t := dismissedAt.Time.UTC()
		dismissedAtPtr = &t
	}
	var archivedAtPtr *time.Time
	if archivedAt.Valid {
		t := archivedAt.Time.UTC()
		archivedAtPtr = &t
	}

	return &card.Card{
		ID:          uid,
		TreeID:      treeUID,
		NodeID:      nodeUID,
		AppID:       appID,
		CardType:    card.CardType(cardTypeStr),
		Data:        json.RawMessage(dataJSON),
		Actions:     actions,
		Status:      card.CardStatus(status),
		ContextHash: contextHash,
		Revision:    revision,
		CreatedAt:   createdAt.UTC(),
		UpdatedAt:   updatedAt.UTC(),
		DismissedAt: dismissedAtPtr,
		ArchivedAt:  archivedAtPtr,
	}, nil
}
