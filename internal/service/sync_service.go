// Package service contains the business logic layer for Canopy.
// SyncService implements the snapshot/delta sync protocol defined in
// SPEC-DM-02 §7. It wraps SnapshotRepo and provides ComputeSnapshot for
// server-side snapshot creation and GetSyncResponse for client sync requests.
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

// --- Error sentinels ---------------------------------------------------------

var (
	ErrSyncTreeNotFound = errors.New("sync service: tree not found")
	ErrSnapshotNotFound = errors.New("sync service: snapshot not found")
	ErrInvalidHash      = errors.New("sync service: invalid hash format (must be 64 hex chars)")
)

// --- Types -------------------------------------------------------------------

// SyncResponse is returned by GetSyncResponse. When FullSnapshot is true,
// all nodes/edges are served as a full snapshot (hash not found or first sync).
// When false, Delta contains the changes from lastKnownHash to current.
type SyncResponse struct {
	TreeID       uuid.UUID `json:"treeId"`
	CurrentHash  string    `json:"currentHash"`
	FullSnapshot bool      `json:"fullSnapshot"`
	Snapshot     *Snapshot `json:"snapshot,omitempty"`
	Delta        *Delta    `json:"delta,omitempty"`
}

// Snapshot is a point-in-time tree state, returned to clients.
type Snapshot struct {
	Hash       string    `json:"hash"`
	NodeCount  int       `json:"nodeCount"`
	EdgeCount  int       `json:"edgeCount"`
	SnapshotID uuid.UUID `json:"snapshotId"`
	TreeID     uuid.UUID `json:"treeId"`
	CreatedAt  string    `json:"createdAt"`
}

// Delta contains the set of changes between two snapshots.
type Delta struct {
	AddedNodes int `json:"addedNodes"`
	AddedEdges int `json:"addedEdges"`
	TotalNodes int `json:"totalNodes"`
	TotalEdges int `json:"totalEdges"`
}

// SyncService defines the sync engine contract.
type SyncService interface {
	// ComputeSnapshot captures the current state of a tree and stores
	// it as a new snapshot. Returns the created snapshot metadata.
	ComputeSnapshot(ctx context.Context, treeID uuid.UUID) (*Snapshot, error)

	// GetSyncResponse returns either a full snapshot or a delta,
	// depending on whether lastKnownHash matches the latest snapshot.
	// If lastKnownHash is empty, returns full snapshot.
	// If lastKnownHash matches latest, returns empty (204).
	// If lastKnownHash is not found (compacted), returns full snapshot.
	// Otherwise returns a delta.
	GetSyncResponse(ctx context.Context, treeID uuid.UUID, lastKnownHash string) (*SyncResponse, error)
}

// syncService is the concrete SyncService implementation.
type syncService struct {
	snapshots db.SnapshotRepo
}

// NewSyncService creates a new SyncService.
func NewSyncService(snapshots db.SnapshotRepo) SyncService {
	return &syncService{snapshots: snapshots}
}

func (s *syncService) ComputeSnapshot(ctx context.Context, treeID uuid.UUID) (*Snapshot, error) {
	ts, err := s.snapshots.CreateSnapshot(ctx, treeID)
	if err != nil {
		return nil, fmt.Errorf("sync: compute snapshot: %w", err)
	}
	return snapshotFromDB(ts), nil
}

func (s *syncService) GetSyncResponse(ctx context.Context, treeID uuid.UUID, lastKnownHash string) (*SyncResponse, error) {
	// Get latest snapshot
	latest, err := s.snapshots.GetLatestSnapshot(ctx, treeID)
	if err != nil {
		return nil, fmt.Errorf("sync: get latest: %w", err)
	}

	if latest == nil {
		// Tree has no snapshots yet — nothing to sync.
		log.Ctx(ctx).Warn().Str("tree_id", treeID.String()).Msg("sync: no snapshots exist for tree")
		return nil, ErrTreeNotFound
	}

	resp := &SyncResponse{
		TreeID:      latest.TreeID,
		CurrentHash: latest.Hash,
	}

	// Case 1: No lastKnownHash → full snapshot
	if lastKnownHash == "" {
		resp.FullSnapshot = true
		resp.Snapshot = snapshotFromDB(latest)
		return resp, nil
	}

	// Case 2: lastKnownHash matches latest → no changes (204)
	if lastKnownHash == latest.Hash {
		resp.FullSnapshot = false
		return resp, nil
	}

	// Case 3: Try to find the requested hash
	known, err := s.snapshots.GetSnapshot(ctx, lastKnownHash)
	if err != nil {
		return nil, fmt.Errorf("sync: lookup hash: %w", err)
	}

	if known == nil {
		// Hash not found (compacted) → full snapshot
		resp.FullSnapshot = true
		resp.Snapshot = snapshotFromDB(latest)
		return resp, nil
	}

	// Case 4: Delta between known and latest
	resp.FullSnapshot = false
	resp.Delta = &Delta{
		AddedNodes: latest.NodeCount - known.NodeCount,
		AddedEdges: latest.EdgeCount - known.EdgeCount,
		TotalNodes: latest.NodeCount,
		TotalEdges: latest.EdgeCount,
	}
	return resp, nil
}

// snapshotFromDB converts a db.TreeSnapshot to the service-level Snapshot type.
func snapshotFromDB(ts *db.TreeSnapshot) *Snapshot {
	if ts == nil {
		return nil
	}
	return &Snapshot{
		Hash:       ts.Hash,
		NodeCount:  ts.NodeCount,
		EdgeCount:  ts.EdgeCount,
		SnapshotID: ts.ID,
		TreeID:     ts.TreeID,
		CreatedAt:  ts.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}
