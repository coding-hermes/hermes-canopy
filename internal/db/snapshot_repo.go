// Package db provides the PostgreSQL data layer for Canopy.
//
// This file defines the SnapshotRepo interface and its pgx implementation.
// Spec: SPEC-DM-02 §4.3.

package db

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/google/uuid"
)

// SnapshotRepo manages tree snapshots. Per SPEC-DM-02 §4.3.
type SnapshotRepo interface {
	// CreateSnapshot computes a new snapshot for the given tree and stores it.
	CreateSnapshot(ctx context.Context, treeID uuid.UUID) (*TreeSnapshot, error)

	// GetSnapshot returns a snapshot by its hash. Returns nil, nil if not found.
	GetSnapshot(ctx context.Context, hash string) (*TreeSnapshot, error)

	// GetLatestSnapshot returns the most recent snapshot for a tree.
	// Returns nil, nil if no snapshots exist yet.
	GetLatestSnapshot(ctx context.Context, treeID uuid.UUID) (*TreeSnapshot, error)

	// GetSnapshotChain returns snapshots from fromHash (exclusive) to latest.
	// Returns snapshots in chronological order (oldest first).
	GetSnapshotChain(ctx context.Context, treeID uuid.UUID, fromHash string) ([]TreeSnapshot, error)

	// CompactSnapshots merges snapshots older than before into a single snapshot.
	// Returns the number of snapshots compacted.
	CompactSnapshots(ctx context.Context, treeID uuid.UUID, before time.Time) (int, error)

	// DeleteSnapshotsBefore removes all snapshots older than before.
	// Returns count deleted.
	DeleteSnapshotsBefore(ctx context.Context, treeID uuid.UUID, before time.Time) (int, error)
}

// PGSnapshotRepo is the pgx-backed SnapshotRepo.
type PGSnapshotRepo struct {
	pool *pgxpool.Pool
}

// NewSnapshotRepo returns a pgx-backed SnapshotRepo.
func NewSnapshotRepo(pool *pgxpool.Pool) SnapshotRepo {
	return &PGSnapshotRepo{pool: pool}
}

// snapshotNode is the query result for a single active node.
type snapshotNode struct {
	ID            uuid.UUID
	SeqNum        int64
	ContentHash   string
	ContentFormat string
	NodeType      string
	CreatedAt     time.Time
	ParentID      *uuid.UUID
}

// snapshotEdge is the query result for a single active edge.
type snapshotEdge struct {
	ID       uuid.UUID
	SourceID uuid.UUID
	TargetID uuid.UUID
	EdgeType string
}

func (r *PGSnapshotRepo) CreateSnapshot(ctx context.Context, treeID uuid.UUID) (*TreeSnapshot, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, sequence_num, content_hash, content_format, node_type, created_at, parent_id
		 FROM nodes WHERE tree_id = $1 AND deleted_at IS NULL
		 ORDER BY sequence_num, id`, treeID)
	if err != nil {
		return nil, fmt.Errorf("snapshot: query nodes: %w", err)
	}
	ns, err := pgx.CollectRows(rows, pgx.RowToStructByPos[snapshotNode])
	if err != nil {
		return nil, fmt.Errorf("snapshot: collect nodes: %w", err)
	}

	erows, err := r.pool.Query(ctx,
		`SELECT id, source_id, target_id, edge_type
		 FROM edges WHERE tree_id = $1 AND deleted_at IS NULL
		 ORDER BY source_id, target_id, edge_type, id`, treeID)
	if err != nil {
		return nil, fmt.Errorf("snapshot: query edges: %w", err)
	}
	es, err := pgx.CollectRows(erows, pgx.RowToStructByPos[snapshotEdge])
	if err != nil {
		return nil, fmt.Errorf("snapshot: collect edges: %w", err)
	}

	// Build compact snapshot_data JSON.
	compactNodes := make(map[string][]any, len(ns))
	for _, n := range ns {
		pid := "nil"
		if n.ParentID != nil {
			pid = n.ParentID.String()
		}
		compactNodes[n.ID.String()] = []any{
			n.SeqNum,
			n.CreatedAt.UTC().Format(time.RFC3339Nano),
			pid,
			n.ContentHash,
			n.ContentFormat,
			n.NodeType,
		}
	}
	compactEdges := make(map[string][]any, len(es))
	for _, e := range es {
		compactEdges[e.ID.String()] = []any{
			e.SourceID.String(),
			e.TargetID.String(),
			e.EdgeType,
		}
	}
	sd := map[string]any{"nodes": compactNodes, "edges": compactEdges}
	dataJSON, err := json.Marshal(sd)
	if err != nil {
		return nil, fmt.Errorf("snapshot: marshal: %w", err)
	}

	// Get parent hash (latest snapshot before this one).
	var parentHash *string
	if err := r.pool.QueryRow(ctx,
		`SELECT hash FROM tree_snapshots WHERE tree_id = $1 ORDER BY created_at DESC LIMIT 1`,
		treeID).Scan(&parentHash); err != nil && err != pgx.ErrNoRows {
		return nil, fmt.Errorf("snapshot: parent hash: %w", err)
	}

	hash := computeSnapshotHash(ns, es)

	var snap TreeSnapshot
	err = r.pool.QueryRow(ctx,
		`INSERT INTO tree_snapshots (tree_id, parent_hash, hash, node_count, edge_count, snapshot_data)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, tree_id, parent_hash, hash, node_count, edge_count, snapshot_data, created_at`,
		treeID, parentHash, hash, len(ns), len(es), dataJSON,
	).Scan(&snap.ID, &snap.TreeID, &snap.ParentHash, &snap.Hash,
		&snap.NodeCount, &snap.EdgeCount, &snap.SnapshotData, &snap.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("snapshot: insert: %w", err)
	}
	return &snap, nil
}

func (r *PGSnapshotRepo) GetSnapshot(ctx context.Context, hash string) (*TreeSnapshot, error) {
	return scanSnapshot(r.pool.QueryRow(ctx,
		`SELECT id, tree_id, parent_hash, hash, node_count, edge_count, snapshot_data, created_at
		 FROM tree_snapshots WHERE hash = $1`, hash))
}

func (r *PGSnapshotRepo) GetLatestSnapshot(ctx context.Context, treeID uuid.UUID) (*TreeSnapshot, error) {
	return scanSnapshot(r.pool.QueryRow(ctx,
		`SELECT id, tree_id, parent_hash, hash, node_count, edge_count, snapshot_data, created_at
		 FROM tree_snapshots WHERE tree_id = $1 ORDER BY created_at DESC LIMIT 1`, treeID))
}

func (r *PGSnapshotRepo) GetSnapshotChain(ctx context.Context, treeID uuid.UUID, fromHash string) ([]TreeSnapshot, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT ts.id, ts.tree_id, ts.parent_hash, ts.hash, ts.node_count, ts.edge_count, ts.snapshot_data, ts.created_at
		 FROM tree_snapshots ts
		 WHERE ts.tree_id = $1 AND ts.created_at > (
		     SELECT created_at FROM tree_snapshots WHERE hash = $2
		 )
		 ORDER BY ts.created_at ASC`, treeID, fromHash)
	if err != nil {
		return nil, fmt.Errorf("snapshot: chain: %w", err)
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByPos[TreeSnapshot])
}

func (r *PGSnapshotRepo) CompactSnapshots(ctx context.Context, treeID uuid.UUID, before time.Time) (int, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM tree_snapshots
		 WHERE tree_id = $1 AND created_at < $2
		   AND id NOT IN (
		       SELECT id FROM tree_snapshots
		       WHERE tree_id = $1 AND created_at < $2
		       ORDER BY created_at DESC LIMIT 1
		   )`, treeID, before)
	if err != nil {
		return 0, fmt.Errorf("snapshot: compact: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *PGSnapshotRepo) DeleteSnapshotsBefore(ctx context.Context, treeID uuid.UUID, before time.Time) (int, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM tree_snapshots WHERE tree_id = $1 AND created_at < $2`, treeID, before)
	if err != nil {
		return 0, fmt.Errorf("snapshot: delete before: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func scanSnapshot(row pgx.Row) (*TreeSnapshot, error) {
	var s TreeSnapshot
	err := row.Scan(&s.ID, &s.TreeID, &s.ParentHash, &s.Hash,
		&s.NodeCount, &s.EdgeCount, &s.SnapshotData, &s.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("snapshot: scan: %w", err)
	}
	return &s, nil
}

// computeSnapshotHash produces a deterministic SHA256 hash of the tree state.
// Algorithm per SPEC-DM-02 §6: canonical sort → byte buffer → SHA256.
func computeSnapshotHash(nodes []snapshotNode, edges []snapshotEdge) string {
	type nDigest struct {
		id            string
		seqNum        int64
		createdAtMs   int64
		parentID      string
		contentHash   string
		contentFormat string
		nodeType      string
	}
	type eDigest struct {
		id       string
		sourceID string
		targetID string
		edgeType string
	}

	nd := make([]nDigest, len(nodes))
	for i, n := range nodes {
		pid := "nil"
		if n.ParentID != nil {
			pid = n.ParentID.String()
		}
		nd[i] = nDigest{
			id:            n.ID.String(),
			seqNum:        n.SeqNum,
			createdAtMs:   n.CreatedAt.UnixMilli(),
			parentID:      pid,
			contentHash:   n.ContentHash,
			contentFormat: n.ContentFormat,
			nodeType:      n.NodeType,
		}
	}

	ed := make([]eDigest, len(edges))
	for i, e := range edges {
		ed[i] = eDigest{
			id:       e.ID.String(),
			sourceID: e.SourceID.String(),
			targetID: e.TargetID.String(),
			edgeType: e.EdgeType,
		}
	}

	// Sort nodes by (seqNum, id)
	sort.Slice(nd, func(i, j int) bool {
		if nd[i].seqNum != nd[j].seqNum {
			return nd[i].seqNum < nd[j].seqNum
		}
		return nd[i].id < nd[j].id
	})

	// Sort edges by (source, target, type, id)
	sort.Slice(ed, func(i, j int) bool {
		if ed[i].sourceID != ed[j].sourceID {
			return ed[i].sourceID < ed[j].sourceID
		}
		if ed[i].targetID != ed[j].targetID {
			return ed[i].targetID < ed[j].targetID
		}
		if ed[i].edgeType != ed[j].edgeType {
			return ed[i].edgeType < ed[j].edgeType
		}
		return ed[i].id < ed[j].id
	})

	// Build canonical byte buffer per SPEC-DM-02 §6.1
	var buf []byte
	for _, n := range nd {
		buf = append(buf, fmt.Sprintf("%s:%d:%d:%s:%s:%s:%s\n",
			n.id, n.seqNum, n.createdAtMs,
			n.parentID, n.contentHash, n.contentFormat, n.nodeType)...)
	}
	for _, e := range ed {
		buf = append(buf, fmt.Sprintf("%s:%s:%s:%s\n",
			e.id, e.sourceID, e.targetID, e.edgeType)...)
	}

	h := sha256.Sum256(buf)
	return fmt.Sprintf("%x", h)
}
