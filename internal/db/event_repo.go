// Package db provides the PostgreSQL data layer for Canopy.
//
// This file defines the EventRepo interface and its pgx implementation.
// Spec: SPEC-DM-02 §4.4.

package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/google/uuid"
)

// EventRepo manages the append-only tree event log. Per SPEC-DM-02 §4.4.
type EventRepo interface {
	// AppendEvent writes a single event to the log, auto-incrementing sequence_num.
	AppendEvent(ctx context.Context, treeID uuid.UUID, eventType string, nodeID, edgeID *uuid.UUID, payload []byte, snapshotID *uuid.UUID) (*TreeEvent, error)

	// GetEventsSince returns all events with sequence_num > sinceSeq for a tree.
	GetEventsSince(ctx context.Context, treeID uuid.UUID, sinceSeq int64, limit int) ([]TreeEvent, error)

	// GetEventsBetweenSnapshots returns events between two snapshots.
	GetEventsBetweenSnapshots(ctx context.Context, fromHash, toHash string) ([]TreeEvent, error)

	// GetLatestSequenceNum returns the highest sequence_num for a tree. Returns 0 if no events.
	GetLatestSequenceNum(ctx context.Context, treeID uuid.UUID) (int64, error)
}

// PGEventRepo is the pgx-backed EventRepo.
type PGEventRepo struct {
	pool *pgxpool.Pool
}

// NewEventRepo returns a pgx-backed EventRepo.
func NewEventRepo(pool *pgxpool.Pool) EventRepo {
	return &PGEventRepo{pool: pool}
}

func (r *PGEventRepo) AppendEvent(ctx context.Context, treeID uuid.UUID, eventType string, nodeID, edgeID *uuid.UUID, payload []byte, snapshotID *uuid.UUID) (*TreeEvent, error) {
	// Atomically advance the sequence counter and insert the event.
	var ev TreeEvent
	err := r.pool.QueryRow(ctx,
		`WITH next_seq AS (
		     INSERT INTO tree_event_seq (tree_id, next_seq)
		     VALUES ($1, 1)
		     ON CONFLICT (tree_id) DO UPDATE SET next_seq = tree_event_seq.next_seq + 1
		     RETURNING next_seq - 1 AS seq
		 )
		 INSERT INTO tree_events (tree_id, snapshot_id, event_type, node_id, edge_id, payload, sequence_num)
		 VALUES ($1, $2, $3, $4, $5, $6, (SELECT seq FROM next_seq))
		 RETURNING id, tree_id, snapshot_id, event_type, node_id, edge_id, payload, sequence_num, created_at`,
		treeID, snapshotID, eventType, nodeID, edgeID, payload,
	).Scan(&ev.ID, &ev.TreeID, &ev.SnapshotID, &ev.EventType,
		&ev.NodeID, &ev.EdgeID, &ev.Payload, &ev.SequenceNum, &ev.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("event: append: %w", err)
	}
	return &ev, nil
}

func (r *PGEventRepo) GetEventsSince(ctx context.Context, treeID uuid.UUID, sinceSeq int64, limit int) ([]TreeEvent, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, tree_id, snapshot_id, event_type, node_id, edge_id, payload, sequence_num, created_at
		 FROM tree_events
		 WHERE tree_id = $1 AND sequence_num > $2
		 ORDER BY sequence_num ASC
		 LIMIT $3`, treeID, sinceSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("event: get since: %w", err)
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByPos[TreeEvent])
}

func (r *PGEventRepo) GetEventsBetweenSnapshots(ctx context.Context, fromHash, toHash string) ([]TreeEvent, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT te.id, te.tree_id, te.snapshot_id, te.event_type, te.node_id, te.edge_id, te.payload, te.sequence_num, te.created_at
		 FROM tree_events te
		 JOIN tree_snapshots sfrom ON sfrom.hash = $1
		 JOIN tree_snapshots sto   ON sto.hash = $2
		 WHERE te.tree_id = sfrom.tree_id
		   AND te.sequence_num > COALESCE(
		       (SELECT te2.sequence_num FROM tree_events te2 WHERE te2.snapshot_id = sfrom.id ORDER BY te2.sequence_num DESC LIMIT 1),
		       0)
		   AND te.sequence_num <= COALESCE(
		       (SELECT te3.sequence_num FROM tree_events te3 WHERE te3.snapshot_id = sto.id ORDER BY te3.sequence_num DESC LIMIT 1),
		       (SELECT te4.sequence_num FROM tree_events te4 WHERE te4.snapshot_id = sto.id ORDER BY te4.sequence_num DESC LIMIT 1))
		 ORDER BY te.sequence_num ASC`, fromHash, toHash)
	if err != nil {
		return nil, fmt.Errorf("event: between snapshots: %w", err)
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByPos[TreeEvent])
}

func (r *PGEventRepo) GetLatestSequenceNum(ctx context.Context, treeID uuid.UUID) (int64, error) {
	var seq int64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(sequence_num), 0) FROM tree_events WHERE tree_id = $1`, treeID).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("event: latest seq: %w", err)
	}
	return seq, nil
}
