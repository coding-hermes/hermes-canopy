package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

// --- SnapshotRepo stubs for sync tests ---------------------------------------

type snapshotRepoStub struct {
	snapshots      []*db.TreeSnapshot
	createFn       func(ctx context.Context, treeID uuid.UUID) (*db.TreeSnapshot, error)
}

func (s *snapshotRepoStub) CreateSnapshot(ctx context.Context, treeID uuid.UUID) (*db.TreeSnapshot, error) {
	var ts *db.TreeSnapshot
	if s.createFn != nil {
		var err error
		ts, err = s.createFn(ctx, treeID)
		if err != nil {
			return nil, err
		}
	}
	if ts == nil {
		ts = &db.TreeSnapshot{
			ID:      uuid.MustParse("10000000-0000-7000-8000-000000000001"),
			TreeID:  treeID,
			Hash:    "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			NodeCount:  5,
			EdgeCount:  4,
			CreatedAt: time.Now(),
		}
	}
	s.snapshots = append(s.snapshots, ts)
	return ts, nil
}

func (s *snapshotRepoStub) GetSnapshot(ctx context.Context, hash string) (*db.TreeSnapshot, error) {
	for _, ts := range s.snapshots {
		if ts.Hash == hash {
			return ts, nil
		}
	}
	return nil, nil
}

func (s *snapshotRepoStub) GetLatestSnapshot(ctx context.Context, treeID uuid.UUID) (*db.TreeSnapshot, error) {
	if len(s.snapshots) == 0 {
		return nil, nil
	}
	return s.snapshots[len(s.snapshots)-1], nil
}

func (s *snapshotRepoStub) List(ctx context.Context, treeID uuid.UUID, limit, offset int) ([]db.TreeSnapshot, error) {
	return nil, nil
}

func (s *snapshotRepoStub) GetByTreeAndHashRange(ctx context.Context, treeID uuid.UUID, fromHash, toHash string) ([]db.TreeSnapshot, error) {
	return nil, nil
}

func (s *snapshotRepoStub) DeleteOlderThan(ctx context.Context, treeID uuid.UUID, before time.Time) error {
	return nil
}

func (s *snapshotRepoStub) GetSnapshotChain(ctx context.Context, treeID uuid.UUID, fromHash string) ([]db.TreeSnapshot, error) {
	return nil, nil
}

func (s *snapshotRepoStub) CompactSnapshots(ctx context.Context, treeID uuid.UUID, before time.Time) (int, error) {
	return 0, nil
}

func (s *snapshotRepoStub) DeleteSnapshotsBefore(ctx context.Context, treeID uuid.UUID, before time.Time) (int, error) {
	return 0, nil
}

// --- Tests -------------------------------------------------------------------

func TestSyncService_ComputeSnapshot(t *testing.T) {
	repo := &snapshotRepoStub{}
	svc := NewSyncService(repo)

	treeID := uuid.MustParse("00000000-0000-7000-8000-000000000001")
	snap, err := svc.ComputeSnapshot(context.Background(), treeID)
	if err != nil {
		t.Fatalf("ComputeSnapshot failed: %v", err)
	}

	if snap.NodeCount != 5 {
		t.Errorf("expected NodeCount=5, got %d", snap.NodeCount)
	}
	if snap.EdgeCount != 4 {
		t.Errorf("expected EdgeCount=4, got %d", snap.EdgeCount)
	}
	if snap.Hash == "" {
		t.Error("expected non-empty hash")
	}
	if snap.SnapshotID != uuid.MustParse("10000000-0000-7000-8000-000000000001") {
		t.Error("expected snapshot ID to match stub")
	}
}

func TestSyncService_GetSyncResponse_FullSnapshot(t *testing.T) {
	repo := &snapshotRepoStub{}
	svc := NewSyncService(repo)
	treeID := uuid.MustParse("00000000-0000-7000-8000-000000000002")

	// Create a snapshot first
	_, err := svc.ComputeSnapshot(context.Background(), treeID)
	if err != nil {
		t.Fatalf("ComputeSnapshot failed: %v", err)
	}

	// No lastKnownHash → full snapshot
	resp, err := svc.GetSyncResponse(context.Background(), treeID, "")
	if err != nil {
		t.Fatalf("GetSyncResponse failed: %v", err)
	}

	if !resp.FullSnapshot {
		t.Error("expected full snapshot when lastKnownHash is empty")
	}
	if resp.Snapshot == nil {
		t.Fatal("expected snapshot in response")
	}
	if resp.CurrentHash == "" {
		t.Error("expected non-empty current hash")
	}
}

func TestSyncService_GetSyncResponse_NoChanges(t *testing.T) {
	repo := &snapshotRepoStub{}
	svc := NewSyncService(repo)
	treeID := uuid.MustParse("00000000-0000-7000-8000-000000000003")

	snap, err := svc.ComputeSnapshot(context.Background(), treeID)
	if err != nil {
		t.Fatalf("ComputeSnapshot failed: %v", err)
	}

	// lastKnownHash matches latest → empty (204)
	resp, err := svc.GetSyncResponse(context.Background(), treeID, snap.Hash)
	if err != nil {
		t.Fatalf("GetSyncResponse failed: %v", err)
	}

	if resp.FullSnapshot {
		t.Error("expected non-full-snapshot response")
	}
	if resp.Delta != nil {
		t.Error("expected nil delta when hashes match")
	}
	if resp.Snapshot != nil {
		t.Error("expected nil snapshot when hashes match")
	}
}

func TestSyncService_GetSyncResponse_UnknownHash(t *testing.T) {
	repo := &snapshotRepoStub{}
	svc := NewSyncService(repo)
	treeID := uuid.MustParse("00000000-0000-7000-8000-000000000004")

	_, err := svc.ComputeSnapshot(context.Background(), treeID)
	if err != nil {
		t.Fatalf("ComputeSnapshot failed: %v", err)
	}

	// Unknown hash → full snapshot fallback
	resp, err := svc.GetSyncResponse(context.Background(), treeID, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	if err != nil {
		t.Fatalf("GetSyncResponse failed: %v", err)
	}

	if !resp.FullSnapshot {
		t.Error("expected full snapshot when hash not found")
	}
	if resp.Snapshot == nil {
		t.Fatal("expected snapshot in response for unknown hash fallback")
	}
}

func TestSyncService_GetSyncResponse_NoSnapshots(t *testing.T) {
	repo := &snapshotRepoStub{}
	svc := NewSyncService(repo)
	treeID := uuid.MustParse("00000000-0000-7000-8000-000000000005")

	// No snapshots created → error
	_, err := svc.GetSyncResponse(context.Background(), treeID, "")
	if err == nil {
		t.Fatal("expected error when no snapshots exist")
	}
}

func TestSyncService_GetSyncResponse_Delta(t *testing.T) {
	repo := &snapshotRepoStub{}
	svc := NewSyncService(repo)
	treeID := uuid.MustParse("00000000-0000-7000-8000-000000000006")

	// Create first snapshot (old state — 5 nodes, 4 edges)
	snap1, err := svc.ComputeSnapshot(context.Background(), treeID)
	if err != nil {
		t.Fatalf("first ComputeSnapshot failed: %v", err)
	}

	// Create second snapshot with different node/edge counts
	// by overriding the stub's createFn
	repo.createFn = func(ctx context.Context, treeID uuid.UUID) (*db.TreeSnapshot, error) {
		ts := &db.TreeSnapshot{
			ID:         uuid.MustParse("20000000-0000-7000-8000-000000000002"),
			TreeID:     treeID,
			Hash:       "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			NodeCount:  8,
			EdgeCount:  7,
			CreatedAt:  time.Now(),
		}
		repo.snapshots = append(repo.snapshots, ts)
		return ts, nil
	}

	snap2, err := svc.ComputeSnapshot(context.Background(), treeID)
	if err != nil {
		t.Fatalf("second ComputeSnapshot failed: %v", err)
	}

	// Request delta from snap1 to snap2
	resp, err := svc.GetSyncResponse(context.Background(), treeID, snap1.Hash)
	if err != nil {
		t.Fatalf("GetSyncResponse failed: %v", err)
	}

	if resp.FullSnapshot {
		t.Error("expected delta response, not full snapshot")
	}
	if resp.CurrentHash != snap2.Hash {
		t.Errorf("expected currentHash=%s, got %s", snap2.Hash, resp.CurrentHash)
	}
	if resp.Delta == nil {
		t.Fatal("expected non-nil delta")
	}
	if resp.Delta.AddedNodes != 3 {
		t.Errorf("expected AddedNodes=3, got %d", resp.Delta.AddedNodes)
	}
	if resp.Delta.AddedEdges != 3 {
		t.Errorf("expected AddedEdges=3, got %d", resp.Delta.AddedEdges)
	}
}
