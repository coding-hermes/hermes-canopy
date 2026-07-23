// Package sync provides tree snapshot/delta computation and the sync engine.
//
// This file defines the SyncEngine — the central coordinator that ties
// together event logging, snapshot creation, delta computation, and SSE
// broadcast. Services call OnNodeMutation / OnTreeMutation after every DB
// write; the engine handles the rest.

package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
)

// MutationType describes the kind of mutation that occurred.
type MutationType string

const (
	MutNodeAdded   MutationType = "node_added"
	MutNodeUpdated MutationType = "node_updated"
	MutNodeRemoved MutationType = "node_removed"
	MutEdgeAdded   MutationType = "edge_added"
	MutEdgeRemoved MutationType = "edge_removed"
	MutTreeCreated MutationType = "tree_created"
	MutTreeUpdated MutationType = "tree_updated"
	MutTreeDeleted MutationType = "tree_deleted"
)

// NodeMutation carries the details of a node-level mutation.
type NodeMutation struct {
	Type          MutationType
	TreeID        uuid.UUID
	NodeID        uuid.UUID
	ActorID       uuid.UUID
	Content       string
	ContentFormat string
	NodeType      string
	ParentID      *uuid.UUID
	EdgeID        uuid.UUID
	EdgeType      string
	SequenceNum   int64
	Timestamp     time.Time
}

// TreeMutation carries the details of a tree-level mutation.
type TreeMutation struct {
	Type    MutationType
	TreeID  uuid.UUID
	ActorID uuid.UUID
}

// SyncEngine is the central coordinator for tree synchronization.
// It accepts mutation events from services, logs them to the event repo,
// creates snapshots on mutations, and broadcasts events via the SSE hub.
type SyncEngine interface {
	// OnNodeMutation is called by services after a node mutation.
	OnNodeMutation(ctx context.Context, m NodeMutation) error

	// OnTreeMutation is called by services after a tree mutation.
	OnTreeMutation(ctx context.Context, m TreeMutation) error

	// ComputeDeltaForClient computes the delta between a client's last-known
	// snapshot hash and the current tree state.
	ComputeDeltaForClient(ctx context.Context, treeID uuid.UUID, lastKnownHash string) (*TreeDelta, error)

	// GetLatestSnapshot returns the latest snapshot for a tree.
	GetLatestSnapshot(ctx context.Context, treeID uuid.UUID) (*db.TreeSnapshot, error)

	// CreateSnapshot forces a snapshot creation.
	CreateSnapshot(ctx context.Context, treeID uuid.UUID) (*db.TreeSnapshot, error)
}

// EngineConfig tunes the SyncEngine's snapshot and event behaviour.
type EngineConfig struct {
	// SnapshotOnCommit creates a snapshot after every mutation. Default: true.
	SnapshotOnCommit bool
}

// DefaultEngineConfig returns sensible defaults.
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		SnapshotOnCommit: true,
	}
}

type engine struct {
	eventRepo    db.EventRepo
	snapshotRepo db.SnapshotRepo
	sseHub       sse.SSEHub
	cfg          EngineConfig
}

// NewEngine returns a wired SyncEngine.
func NewEngine(eventRepo db.EventRepo, snapshotRepo db.SnapshotRepo, sseHub sse.SSEHub, cfg EngineConfig) SyncEngine {
	return &engine{
		eventRepo:    eventRepo,
		snapshotRepo: snapshotRepo,
		sseHub:       sseHub,
		cfg:          cfg,
	}
}

func (e *engine) OnNodeMutation(ctx context.Context, m NodeMutation) error {
	// Build event payload based on mutation type.
	payload := buildNodeEventPayload(m)
	eventType := string(m.Type)

	// Write to event log
	ev, err := e.eventRepo.AppendEvent(ctx, m.TreeID, eventType, &m.NodeID, edgeIDPtr(m), payload, nil)
	if err != nil {
		return fmt.Errorf("sync: append event: %w", err)
	}

	// Optionally create a snapshot
	if e.cfg.SnapshotOnCommit {
		if _, err := e.snapshotRepo.CreateSnapshot(ctx, m.TreeID); err != nil {
			log.Warn().Err(err).Str("tree_id", m.TreeID.String()).Msg("sync: snapshot creation failed (non-fatal)")
		}
	}

	// Broadcast via SSE hub
	e.broadcastMutationEvent(ctx, m.TreeID, eventType, m.ActorID, ev.SequenceNum, payload)
	return nil
}

func (e *engine) OnTreeMutation(ctx context.Context, m TreeMutation) error {
	if e.cfg.SnapshotOnCommit && m.Type != MutTreeDeleted {
		if _, err := e.snapshotRepo.CreateSnapshot(ctx, m.TreeID); err != nil {
			log.Warn().Err(err).Str("tree_id", m.TreeID.String()).Msg("sync: tree snapshot creation failed (non-fatal)")
		}
	}
	return nil
}

func (e *engine) ComputeDeltaForClient(ctx context.Context, treeID uuid.UUID, lastKnownHash string) (*TreeDelta, error) {
	latestSnap, err := e.snapshotRepo.GetLatestSnapshot(ctx, treeID)
	if err != nil {
		return nil, fmt.Errorf("sync: get latest snapshot: %w", err)
	}
	if latestSnap == nil {
		return nil, errors.New("sync: no snapshots exist for tree")
	}

	// If hashes match, return empty delta (no changes).
	if lastKnownHash == latestSnap.Hash {
		return &TreeDelta{
			FromHash:  lastKnownHash,
			ToHash:    latestSnap.Hash,
			NodeCount: latestSnap.NodeCount,
			EdgeCount: latestSnap.EdgeCount,
		}, nil
	}

	// Look up the client's snapshot.
	fromSnap, err := e.snapshotRepo.GetSnapshot(ctx, lastKnownHash)
	if err != nil {
		return nil, fmt.Errorf("sync: get client snapshot: %w", err)
	}

	if fromSnap == nil {
		// Hash not found (compacted away) — send full tree as delta.
		return e.buildFullDelta(ctx, treeID)
	}

	// Compute delta between the two snapshots.
	return e.computeDeltaFromSnapshots(ctx, fromSnap, latestSnap)
}

func (e *engine) GetLatestSnapshot(ctx context.Context, treeID uuid.UUID) (*db.TreeSnapshot, error) {
	return e.snapshotRepo.GetLatestSnapshot(ctx, treeID)
}

func (e *engine) CreateSnapshot(ctx context.Context, treeID uuid.UUID) (*db.TreeSnapshot, error) {
	return e.snapshotRepo.CreateSnapshot(ctx, treeID)
}

// broadcastMutationEvent sends an SSE event through the hub.
func (e *engine) broadcastMutationEvent(ctx context.Context, treeID uuid.UUID, eventType string, actorID uuid.UUID, seqNum int64, payload json.RawMessage) {
	if e.sseHub == nil {
		return
	}
	ev := sse.SSEEvent{
		ID:          sse.EventID(treeID, seqNum),
		Type:        eventType,
		Data:        payload,
		Timestamp:   time.Now().UTC(),
		TreeID:      treeID,
		SequenceNum: seqNum,
		ActorID:     actorID,
	}
	e.sseHub.Broadcast(treeID, ev)
}

// buildNodeEventPayload constructs the JSON event payload for a node mutation.
func buildNodeEventPayload(m NodeMutation) json.RawMessage {
	data := map[string]any{
		"mutation_type": string(m.Type),
		"node_id":       m.NodeID.String(),
		"actor_id":      m.ActorID.String(),
		"timestamp":     m.Timestamp.UTC().Format(time.RFC3339),
	}
	if m.Content != "" {
		data["content"] = m.Content
		data["content_format"] = m.ContentFormat
	}
	if m.NodeType != "" {
		data["node_type"] = m.NodeType
	}
	if m.ParentID != nil {
		data["parent_id"] = m.ParentID.String()
	}
	if m.EdgeID != uuid.Nil {
		data["edge_id"] = m.EdgeID.String()
		data["edge_type"] = m.EdgeType
	}
	raw, _ := json.Marshal(data)
	return raw
}

func edgeIDPtr(m NodeMutation) *uuid.UUID {
	if m.EdgeID == uuid.Nil {
		return nil
	}
	return &m.EdgeID
}

// buildFullDelta creates a delta with all nodes/edges as "added" (full sync).
func (e *engine) buildFullDelta(ctx context.Context, treeID uuid.UUID) (*TreeDelta, error) {
	latestSnap, err := e.snapshotRepo.GetLatestSnapshot(ctx, treeID)
	if err != nil {
		return nil, err
	}
	if latestSnap == nil {
		return nil, errors.New("sync: no snapshots for full delta")
	}

	// Parse snapshot_data to extract current nodes/edges.
	var sd struct {
		Nodes map[string][]any `json:"nodes"`
		Edges map[string][]any `json:"edges"`
	}
	if err := json.Unmarshal(latestSnap.SnapshotData, &sd); err != nil {
		return nil, fmt.Errorf("sync: unmarshal snapshot data: %w", err)
	}

	delta := &TreeDelta{
		FromHash:   "",
		ToHash:     latestSnap.Hash,
		AddedNodes: make(map[uuid.UUID]CompactNode, len(sd.Nodes)),
		AddedEdges: make(map[uuid.UUID]CompactEdge, len(sd.Edges)),
		NodeCount:  latestSnap.NodeCount,
		EdgeCount:  latestSnap.EdgeCount,
	}

	for idStr, vals := range sd.Nodes {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		if len(vals) >= 6 {
			seqNum, _ := toInt64(vals[0])
			parentID, _ := vals[2].(string)
			contentHash, _ := vals[3].(string)
			contentFmt, _ := vals[4].(string)
			nodeType, _ := vals[5].(string)
			createdStr, _ := vals[1].(string)
			delta.AddedNodes[id] = CompactNode{
				SeqNum:        seqNum,
				CreatedAt:     createdStr,
				ParentID:      parentID,
				ContentHash:   contentHash,
				ContentFormat: contentFmt,
				NodeType:      nodeType,
			}
		}
	}

	for idStr, vals := range sd.Edges {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		if len(vals) >= 3 {
			src, _ := vals[0].(string)
			tgt, _ := vals[1].(string)
			typ, _ := vals[2].(string)
			delta.AddedEdges[id] = CompactEdge{
				SourceID: src,
				TargetID: tgt,
				EdgeType: typ,
			}
		}
	}

	return delta, nil
}

// snapshotData is the parsed JSONB structure from tree_snapshots.snapshot_data.
type snapshotData struct {
	Nodes map[string][]any `json:"nodes"`
	Edges map[string][]any `json:"edges"`
}

// computeDeltaFromSnapshots computes the delta between two existing snapshots.
func (e *engine) computeDeltaFromSnapshots(ctx context.Context, from, to *db.TreeSnapshot) (*TreeDelta, error) {
	var fromData, toData snapshotData
	if err := json.Unmarshal(from.SnapshotData, &fromData); err != nil {
		return nil, fmt.Errorf("sync: unmarshal from snapshot: %w", err)
	}
	if err := json.Unmarshal(to.SnapshotData, &toData); err != nil {
		return nil, fmt.Errorf("sync: unmarshal to snapshot: %w", err)
	}

	// Build digests from compact data.
	fromNodes := snapshotDataToNodes(fromData)
	toNodes := snapshotDataToNodes(toData)
	fromEdges := snapshotDataToEdges(fromData)
	toEdges := snapshotDataToEdges(toData)

	return ComputeDelta(from, to, fromNodes, toNodes, fromEdges, toEdges)
}

func snapshotDataToNodes(sd snapshotData) []NodeDigest {
	var nodes []NodeDigest
	for idStr, vals := range sd.Nodes {
		id, err := uuid.Parse(idStr)
		if err != nil || len(vals) < 6 {
			continue
		}
		seqNum, _ := toInt64(vals[0])
		parentID, _ := vals[2].(string)
		contentHash, _ := vals[3].(string)
		contentFmt, _ := vals[4].(string)
		nodeType, _ := vals[5].(string)
		nodes = append(nodes, NodeDigest{
			ID:            id,
			SeqNum:        seqNum,
			ContentHash:   contentHash,
			ContentFormat: contentFmt,
			NodeType:      nodeType,
			ParentID:      parentID,
		})
	}
	return nodes
}

func snapshotDataToEdges(sd snapshotData) []EdgeDigest {
	var edges []EdgeDigest
	for idStr, vals := range sd.Edges {
		id, err := uuid.Parse(idStr)
		if err != nil || len(vals) < 3 {
			continue
		}
		src, _ := vals[0].(string)
		srcID, _ := uuid.Parse(src)
		tgt, _ := vals[1].(string)
		tgtID, _ := uuid.Parse(tgt)
		typ, _ := vals[2].(string)
		edges = append(edges, EdgeDigest{
			ID:       id,
			SourceID: srcID,
			TargetID: tgtID,
			EdgeType: typ,
		})
	}
	return edges
}

func toInt64(v any) (int64, bool) {
	switch val := v.(type) {
	case float64:
		return int64(val), true
	case int64:
		return val, true
	case int:
		return int64(val), true
	}
	return 0, false
}
