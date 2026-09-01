package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrConflictNotFound = errors.New("federation: conflict not found")

type Conflict struct {
	ID              uuid.UUID       `json:"id"`
	TreeID          uuid.UUID       `json:"tree_id"`
	NodeID          uuid.UUID       `json:"node_id"`
	LeftPayload     json.RawMessage `json:"left_payload"`
	RightPayload    json.RawMessage `json:"right_payload"`
	DetectedAt      time.Time       `json:"detected_at"`
	ResolutionState string          `json:"resolution_state"`
	Resolution      *string         `json:"resolution,omitempty"`
	ResolvedAt      *time.Time      `json:"resolved_at,omitempty"`
}

type conflictStore struct{ pool *pgxpool.Pool }

func newConflictStore(pool *pgxpool.Pool) *conflictStore { return &conflictStore{pool: pool} }

type mutationSnapshot struct {
	NodeID  uuid.UUID
	Payload json.RawMessage
	Clock   VectorClock
	Lamport int64
	PeerID  uuid.UUID
}

func decodeMutation(inner *FTLInnerPayload, envelope *FTLEnvelope) (*mutationSnapshot, bool) {
	if inner.EventType != "node_updated" && inner.EventType != "remote_node_updated" {
		return nil, false
	}
	var header struct {
		NodeID  uuid.UUID `json:"node_id"`
		Lamport int64     `json:"lamport"`
	}
	if json.Unmarshal(inner.Payload, &header) != nil || header.NodeID == uuid.Nil {
		return nil, false
	}
	lamport := header.Lamport
	if lamport == 0 {
		lamport = envelope.Sequence
	}
	peerID := envelope.SenderServerID
	if peerID == uuid.Nil {
		peerID = envelope.PeerID
	}
	return &mutationSnapshot{header.NodeID, append(json.RawMessage(nil), inner.Payload...), VectorClock(envelope.Clock), lamport, peerID}, true
}

func (s *conflictStore) apply(ctx context.Context, treeID uuid.UUID, incoming *mutationSnapshot) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var clockJSON, currentPayload []byte
	var currentLamport int64
	var currentPeer uuid.UUID
	err = tx.QueryRow(ctx, `SELECT clock,winner_payload,winner_lamport,winner_peer_id FROM federation_node_clocks
		WHERE tree_id=$1 AND node_id=$2 FOR UPDATE`, treeID, incoming.NodeID).Scan(&clockJSON, &currentPayload, &currentLamport, &currentPeer)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = applyNodePayload(ctx, tx, treeID, incoming.NodeID, incoming.Payload); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO federation_node_clocks(tree_id,node_id,clock,winner_payload,winner_lamport,winner_peer_id)
			VALUES($1,$2,$3,$4,$5,$6)`, treeID, incoming.NodeID, incoming.Clock, incoming.Payload, incoming.Lamport, incoming.PeerID)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	var currentClock VectorClock
	if err = json.Unmarshal(clockJSON, &currentClock); err != nil {
		return fmt.Errorf("federation: decode stored vector clock: %w", err)
	}
	relation := currentClock.Compare(incoming.Clock)
	if relation == Equal || relation == After {
		return tx.Commit(ctx)
	}
	merged := currentClock.Merge(incoming.Clock)
	winnerPayload, winnerLamport, winnerPeer := incoming.Payload, incoming.Lamport, incoming.PeerID
	if relation == Concurrent {
		if _, err = tx.Exec(ctx, `INSERT INTO federation_conflicts(tree_id,node_id,left_payload,right_payload) VALUES($1,$2,$3,$4)`,
			treeID, incoming.NodeID, currentPayload, incoming.Payload); err != nil {
			return err
		}
		if currentLamport > incoming.Lamport || (currentLamport == incoming.Lamport && currentPeer.String() > incoming.PeerID.String()) {
			winnerPayload, winnerLamport, winnerPeer = currentPayload, currentLamport, currentPeer
		}
	}
	if err = applyNodePayload(ctx, tx, treeID, incoming.NodeID, winnerPayload); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE federation_node_clocks SET clock=$3,winner_payload=$4,winner_lamport=$5,winner_peer_id=$6,updated_at=clock_timestamp()
		WHERE tree_id=$1 AND node_id=$2`, treeID, incoming.NodeID, merged, winnerPayload, winnerLamport, winnerPeer)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func applyNodePayload(ctx context.Context, tx pgx.Tx, treeID, nodeID uuid.UUID, payload json.RawMessage) error {
	var fields struct {
		Content       *string         `json:"content"`
		ContentFormat *string         `json:"content_format"`
		Metadata      json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(payload, &fields); err != nil {
		return ErrInvalidInput
	}
	var metadata any
	if len(fields.Metadata) != 0 && string(fields.Metadata) != "null" {
		metadata = fields.Metadata
	}
	tag, err := tx.Exec(ctx, `UPDATE nodes SET
		content=COALESCE($3,content), content_format=COALESCE($4,content_format), metadata=COALESCE($5,metadata)
		WHERE tree_id=$1 AND id=$2`, treeID, nodeID, fields.Content, fields.ContentFormat, metadata)
	if err != nil {
		return fmt.Errorf("federation: apply node payload: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidInput
	}
	return nil
}

func scanConflict(row pgx.Row) (*Conflict, error) {
	var conflict Conflict
	if err := row.Scan(&conflict.ID, &conflict.TreeID, &conflict.NodeID, &conflict.LeftPayload,
		&conflict.RightPayload, &conflict.DetectedAt, &conflict.ResolutionState, &conflict.Resolution, &conflict.ResolvedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConflictNotFound
		}
		return nil, err
	}
	return &conflict, nil
}

const conflictColumns = `id,tree_id,node_id,left_payload,right_payload,detected_at,resolution_state,resolution,resolved_at`

func (s *conflictStore) list(ctx context.Context, treeID *uuid.UUID, unresolved bool) ([]Conflict, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+conflictColumns+` FROM federation_conflicts
		WHERE ($1::uuid IS NULL OR tree_id=$1) AND (NOT $2 OR resolution_state='unresolved')
		ORDER BY detected_at DESC,id`, treeID, unresolved)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	conflicts := make([]Conflict, 0)
	for rows.Next() {
		conflict, err := scanConflict(rows)
		if err != nil {
			return nil, err
		}
		conflicts = append(conflicts, *conflict)
	}
	return conflicts, rows.Err()
}

func (s *conflictStore) resolve(ctx context.Context, id uuid.UUID, resolution string) (*Conflict, error) {
	if resolution != "left" && resolution != "right" {
		return nil, ErrInvalidInput
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	conflict, err := scanConflict(tx.QueryRow(ctx, `SELECT `+conflictColumns+` FROM federation_conflicts WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	if conflict.ResolvedAt != nil {
		return conflict, tx.Commit(ctx)
	}
	payload := conflict.LeftPayload
	if resolution == "right" {
		payload = conflict.RightPayload
	}
	if err := applyNodePayload(ctx, tx, conflict.TreeID, conflict.NodeID, payload); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE federation_node_clocks SET winner_payload=$3,updated_at=clock_timestamp()
		WHERE tree_id=$1 AND node_id=$2`, conflict.TreeID, conflict.NodeID, payload); err != nil {
		return nil, err
	}
	conflict, err = scanConflict(tx.QueryRow(ctx, `UPDATE federation_conflicts SET resolution_state='resolved',resolution=$2,resolved_at=clock_timestamp()
		WHERE id=$1 RETURNING `+conflictColumns, id, resolution))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return conflict, nil
}
