package card

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// CardRepository defines the data access contract for cards and events.
type CardRepository interface {
	// Create inserts a new card.
	Create(ctx context.Context, input CreateCardInput) (*Card, error)

	// Get retrieves a single card by ID.
	Get(ctx context.Context, id uuid.UUID) (*Card, error)

	// List queries cards with optional filters.
	List(ctx context.Context, options ListCardsOptions) ([]Card, error)

	// Patch updates selected fields of a card and increments its revision.
	Patch(ctx context.Context, id uuid.UUID, expectedRevision int64, input PatchCardInput) (*Card, error)

	// AppendEvent inserts a new event for a card.
	AppendEvent(ctx context.Context, cardID uuid.UUID, input AppendEventInput) (*CardEvent, error)

	// ListEvents returns events for a card after a given sequence number.
	ListEvents(ctx context.Context, cardID uuid.UUID, afterSequence int64, limit int) ([]CardEvent, error)

	// MaxSequence returns the highest event sequence for a card.
	MaxSequence(ctx context.Context, cardID uuid.UUID) (int64, error)

	// GetByContextHash finds all cards with a given context hash.
	GetByContextHash(ctx context.Context, contextHash string) ([]Card, error)

	// Close releases the underlying database connection.
	Close() error
}

// SQLiteCardRepo implements CardRepository backed by a single card-type SQLite database.
type SQLiteCardRepo struct {
	db *sql.DB
}

// NewSQLiteCardRepo wraps an existing *sql.DB.
func NewSQLiteCardRepo(db *sql.DB) *SQLiteCardRepo {
	return &SQLiteCardRepo{db: db}
}

// Close closes the underlying database.
func (r *SQLiteCardRepo) Close() error {
	return r.db.Close()
}

// Create inserts a new card row and returns the created Card.
func (r *SQLiteCardRepo) Create(ctx context.Context, input CreateCardInput) (*Card, error) {
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)

	actionsJSON := "[]"
	if input.Actions != nil {
		b, err := json.Marshal(input.Actions)
		if err != nil {
			return nil, fmt.Errorf("card: marshal actions: %w", err)
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
		nowStr,
		nowStr,
	)
	if err != nil {
		return nil, fmt.Errorf("card: insert: %w", err)
	}

	return r.Get(ctx, input.ID)
}

// Get retrieves a card by ID.
func (r *SQLiteCardRepo) Get(ctx context.Context, id uuid.UUID) (*Card, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, tree_id, node_id, app_id, card_type, data, actions, status, context_hash, revision, created_at, updated_at, dismissed_at, archived_at
		 FROM cards WHERE id = ?`, id.String())

	return scanCard(row)
}

// List queries cards with optional filters.
func (r *SQLiteCardRepo) List(ctx context.Context, options ListCardsOptions) ([]Card, error) {
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
		return nil, fmt.Errorf("card: list: %w", err)
	}
	defer rows.Close()

	var cards []Card
	for rows.Next() {
		c, err := scanCardFromRows(rows)
		if err != nil {
			return nil, err
		}
		cards = append(cards, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("card: list rows: %w", err)
	}
	if cards == nil {
		cards = []Card{}
	}
	return cards, nil
}

// Patch updates selected fields and increments revision.
func (r *SQLiteCardRepo) Patch(ctx context.Context, id uuid.UUID, expectedRevision int64, input PatchCardInput) (*Card, error) {
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)

	query := "UPDATE cards SET revision = revision + 1, updated_at = ?"
	args := []any{nowStr}

	if input.Data != nil {
		query += ", data = ?"
		args = append(args, string(*input.Data))
	}
	if input.Actions != nil {
		b, err := json.Marshal(*input.Actions)
		if err != nil {
			return nil, fmt.Errorf("card: marshal actions: %w", err)
		}
		query += ", actions = ?"
		args = append(args, string(b))
	}
	if input.Status != nil {
		query += ", status = ?"
		args = append(args, string(*input.Status))
		if *input.Status == CardStatusDismissed {
			query += ", dismissed_at = ?"
			args = append(args, nowStr)
		}
		if *input.Status == CardStatusArchived {
			query += ", archived_at = ?"
			args = append(args, nowStr)
		}
	}
	if input.ContextHash != nil {
		query += ", context_hash = ?"
		args = append(args, *input.ContextHash)
	}

	query += " WHERE id = ? AND revision = ?"
	args = append(args, id.String(), expectedRevision)

	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("card: patch: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("card: patch %s: revision mismatch or not found", id)
	}

	return r.Get(ctx, id)
}

// AppendEvent inserts a new event row.
func (r *SQLiteCardRepo) AppendEvent(ctx context.Context, cardID uuid.UUID, input AppendEventInput) (*CardEvent, error) {
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)

	payloadJSON := "{}"
	if len(input.Payload) > 0 {
		payloadJSON = string(input.Payload)
	}

	res, err := r.db.ExecContext(ctx,
		`INSERT INTO events (event_id, card_id, event_type, actor_kind, actor_id, payload, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		input.EventID.String(),
		cardID.String(),
		string(input.EventType),
		string(input.ActorKind),
		input.ActorID,
		payloadJSON,
		nowStr,
	)
	if err != nil {
		return nil, fmt.Errorf("card: append event: %w", err)
	}

	seq, _ := res.LastInsertId()

	return &CardEvent{
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
func (r *SQLiteCardRepo) ListEvents(ctx context.Context, cardID uuid.UUID, afterSequence int64, limit int) ([]CardEvent, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT sequence, event_id, card_id, event_type, actor_kind, actor_id, payload, created_at
		 FROM events WHERE card_id = ? AND sequence > ? ORDER BY sequence ASC LIMIT ?`,
		cardID.String(), afterSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("card: list events: %w", err)
	}
	defer rows.Close()

	var events []CardEvent
	for rows.Next() {
		e, err := scanEventFromRows(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("card: list events rows: %w", err)
	}
	if events == nil {
		events = []CardEvent{}
	}
	return events, nil
}

// MaxSequence returns the highest event sequence for a card.
func (r *SQLiteCardRepo) MaxSequence(ctx context.Context, cardID uuid.UUID) (int64, error) {
	var seq sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT MAX(sequence) FROM events WHERE card_id = ?`, cardID.String()).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("card: max sequence: %w", err)
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.Int64, nil
}

// GetByContextHash finds all cards with the given context hash.
func (r *SQLiteCardRepo) GetByContextHash(ctx context.Context, contextHash string) ([]Card, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, tree_id, node_id, app_id, card_type, data, actions, status, context_hash, revision, created_at, updated_at, dismissed_at, archived_at
		 FROM cards WHERE context_hash = ?`, contextHash)
	if err != nil {
		return nil, fmt.Errorf("card: get by context hash: %w", err)
	}
	defer rows.Close()

	var cards []Card
	for rows.Next() {
		c, err := scanCardFromRows(rows)
		if err != nil {
			return nil, err
		}
		cards = append(cards, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("card: get by context hash rows: %w", err)
	}
	if cards == nil {
		cards = []Card{}
	}
	return cards, nil
}

// ── Internal scanners ─────────────────────────────────────────────────

func scanCard(row *sql.Row) (*Card, error) {
	var (
		id, treeID, nodeID, appID, cardType, dataJSON, actionsJSON, status, contextHash string
		revision                                                                        int64
		createdAt, updatedAt                                                            string
		dismissedAt, archivedAt                                                         sql.NullString
	)

	err := row.Scan(&id, &treeID, &nodeID, &appID, &cardType, &dataJSON, &actionsJSON, &status,
		&contextHash, &revision, &createdAt, &updatedAt, &dismissedAt, &archivedAt)
	if err != nil {
		return nil, fmt.Errorf("card: scan: %w", err)
	}

	return parseCardRow(id, treeID, nodeID, appID, cardType, dataJSON, actionsJSON, status,
		contextHash, revision, createdAt, updatedAt, dismissedAt, archivedAt)
}

func scanCardFromRows(rows *sql.Rows) (*Card, error) {
	var (
		id, treeID, nodeID, appID, cardType, dataJSON, actionsJSON, status, contextHash string
		revision                                                                        int64
		createdAt, updatedAt                                                            string
		dismissedAt, archivedAt                                                         sql.NullString
	)

	err := rows.Scan(&id, &treeID, &nodeID, &appID, &cardType, &dataJSON, &actionsJSON, &status,
		&contextHash, &revision, &createdAt, &updatedAt, &dismissedAt, &archivedAt)
	if err != nil {
		return nil, fmt.Errorf("card: scan: %w", err)
	}

	return parseCardRow(id, treeID, nodeID, appID, cardType, dataJSON, actionsJSON, status,
		contextHash, revision, createdAt, updatedAt, dismissedAt, archivedAt)
}

func scanEventFromRows(rows *sql.Rows) (*CardEvent, error) {
	var (
		seq                                     int64
		eventID, cardID, eventType, actorKind, actorID, payloadJSON, createdAt string
	)

	err := rows.Scan(&seq, &eventID, &cardID, &eventType, &actorKind, &actorID, &payloadJSON, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("card: scan event: %w", err)
	}

	return parseEventRow(seq, eventID, cardID, eventType, actorKind, actorID, payloadJSON, createdAt)
}

func parseCardRow(
	id, treeIDStr, nodeIDStr, appID, cardType, dataJSON, actionsJSON, status string,
	contextHash string, revision int64,
	createdAtStr, updatedAtStr string,
	dismissedAt, archivedAt sql.NullString,
) (*Card, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("card: parse id: %w", err)
	}

	treeUID := uuid.Nil
	if treeIDStr != "" {
		treeUID, _ = uuid.Parse(treeIDStr)
	}
	nodeUID := uuid.Nil
	if nodeIDStr != "" {
		nodeUID, _ = uuid.Parse(nodeIDStr)
	}

	var actions []CardAction
	if actionsJSON != "" && actionsJSON != "[]" {
		if err := json.Unmarshal([]byte(actionsJSON), &actions); err != nil {
			actions = []CardAction{}
		}
	}

	createdAt := parseTime(createdAtStr)
	updatedAt := parseTime(updatedAtStr)

	var dismissedAtPtr *time.Time
	if dismissedAt.Valid {
		t := parseTime(dismissedAt.String)
		dismissedAtPtr = &t
	}
	var archivedAtPtr *time.Time
	if archivedAt.Valid {
		t := parseTime(archivedAt.String)
		archivedAtPtr = &t
	}

	return &Card{
		ID:          uid,
		TreeID:      treeUID,
		NodeID:      nodeUID,
		AppID:       appID,
		CardType:    CardType(cardType),
		Data:        json.RawMessage(dataJSON),
		Actions:     actions,
		Status:      CardStatus(status),
		ContextHash: contextHash,
		Revision:    revision,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
		DismissedAt: dismissedAtPtr,
		ArchivedAt:  archivedAtPtr,
	}, nil
}

func parseEventRow(seq int64, eventIDStr, cardIDStr, eventType, actorKind, actorID, payloadJSON, createdAtStr string) (*CardEvent, error) {
	eid, err := uuid.Parse(eventIDStr)
	if err != nil {
		return nil, fmt.Errorf("card: parse event id: %w", err)
	}
	cid, err := uuid.Parse(cardIDStr)
	if err != nil {
		return nil, fmt.Errorf("card: parse card id: %w", err)
	}

	return &CardEvent{
		Sequence:  seq,
		EventID:   eid,
		CardID:    cid,
		EventType: CardEventType(eventType),
		ActorKind: CardActorKind(actorKind),
		ActorID:   actorID,
		Payload:   json.RawMessage(payloadJSON),
		CreatedAt: parseTime(createdAtStr),
	}, nil
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		// fallback to RFC3339
		t, _ = time.Parse(time.RFC3339, s)
	}
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC()
}
