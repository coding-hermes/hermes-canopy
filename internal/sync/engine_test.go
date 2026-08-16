package sync_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	stdsync "sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
	enginesync "github.com/coding-hermes/hermes-canopy/internal/sync"
)

// fakeEventRepo records AppendEvent calls and returns incrementing sequence numbers.
type fakeEventRepo struct {
	mu     stdsync.Mutex
	seq    int64
	events []*db.TreeEvent
}

func (r *fakeEventRepo) AppendEvent(
	ctx context.Context,
	treeID uuid.UUID,
	eventType string,
	nodeID, edgeID *uuid.UUID,
	payload []byte,
	snapshotID *uuid.UUID,
) (*db.TreeEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	ev := &db.TreeEvent{
		TreeID:      treeID,
		EventType:   eventType,
		NodeID:      nodeID,
		EdgeID:      edgeID,
		Payload:     payload,
		SequenceNum: r.seq,
	}
	r.events = append(r.events, ev)
	return ev, nil
}

func (r *fakeEventRepo) GetEventsSince(ctx context.Context, treeID uuid.UUID, sinceSeq int64, limit int) ([]db.TreeEvent, error) {
	return nil, nil
}

func (r *fakeEventRepo) GetEventsBetweenSnapshots(ctx context.Context, fromHash, toHash string) ([]db.TreeEvent, error) {
	return nil, nil
}

func (r *fakeEventRepo) GetLatestSequenceNum(ctx context.Context, treeID uuid.UUID) (int64, error) {
	return 0, nil
}

// fakeSnapshotRepo is a no-op SnapshotRepo for engine construction.
type fakeSnapshotRepo struct{}

func (f *fakeSnapshotRepo) CreateSnapshot(ctx context.Context, treeID uuid.UUID) (*db.TreeSnapshot, error) {
	return nil, nil
}
func (f *fakeSnapshotRepo) GetSnapshot(ctx context.Context, hash string) (*db.TreeSnapshot, error) {
	return nil, nil
}
func (f *fakeSnapshotRepo) GetLatestSnapshot(ctx context.Context, treeID uuid.UUID) (*db.TreeSnapshot, error) {
	return nil, nil
}
func (f *fakeSnapshotRepo) GetSnapshotChain(ctx context.Context, treeID uuid.UUID, fromHash string) ([]db.TreeSnapshot, error) {
	return nil, nil
}
func (f *fakeSnapshotRepo) CompactSnapshots(ctx context.Context, treeID uuid.UUID, before time.Time) (int, error) {
	return 0, nil
}
func (f *fakeSnapshotRepo) DeleteSnapshotsBefore(ctx context.Context, treeID uuid.UUID, before time.Time) (int, error) {
	return 0, nil
}

// fakeSSEHub records every broadcast event so tests can assert on the wire shape.
type fakeSSEHub struct {
	mu     stdsync.Mutex
	events []sse.SSEEvent
}

func (h *fakeSSEHub) Broadcast(treeID uuid.UUID, ev sse.SSEEvent) sse.SSEEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, ev)
	return ev
}

func (h *fakeSSEHub) Subscribe(ctx context.Context, treeID uuid.UUID, client sse.SSEClient) error {
	return nil
}
func (h *fakeSSEHub) Unsubscribe(treeID uuid.UUID, clientID string) {}
func (h *fakeSSEHub) ReplaySince(ctx context.Context, treeID uuid.UUID, clientID string, sinceEventID string) error {
	return nil
}
func (h *fakeSSEHub) SubscriberCount(treeID uuid.UUID) int { return 0 }
func (h *fakeSSEHub) TotalConnections() int                 { return 0 }
func (h *fakeSSEHub) Shutdown(ctx context.Context) error    { return nil }

func TestOnNodeMutation_BroadcastsNodeAdded(t *testing.T) {
	treeID := uuid.MustParse("00000000-0000-0000-0000-000000000010")
	nodeID := uuid.MustParse("00000000-0000-0000-0000-000000000011")
	actorID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	repo := &fakeEventRepo{}
	hub := &fakeSSEHub{}
	engine := enginesync.NewEngine(repo, &fakeSnapshotRepo{}, hub, enginesync.DefaultEngineConfig())

	err := engine.OnNodeMutation(context.Background(), enginesync.NodeMutation{
		Type:          enginesync.MutNodeAdded,
		TreeID:        treeID,
		NodeID:        nodeID,
		ActorID:       actorID,
		Content:       "hello world",
		ContentFormat: "markdown",
		NodeType:      "message",
	})
	require.NoError(t, err)

	require.Len(t, repo.events, 1)
	assert.Equal(t, "node_added", repo.events[0].EventType)
	assert.Equal(t, treeID, repo.events[0].TreeID)

	require.Len(t, hub.events, 1)
	broadcast := hub.events[0]
	assert.Equal(t, "node_added", broadcast.Type)
	assert.Equal(t, treeID, broadcast.TreeID)
	assert.Equal(t, actorID, broadcast.ActorID)
	assert.Equal(t, int64(1), broadcast.SequenceNum)
	assert.Contains(t, string(broadcast.Data), "node_id")
	assert.Contains(t, string(broadcast.Data), "hello world")
}

func TestApplyYjsUpdate_BroadcastsBase64Update(t *testing.T) {
	treeID := uuid.MustParse("00000000-0000-0000-0000-000000000020")
	actorID := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	repo := &fakeEventRepo{}
	hub := &fakeSSEHub{}
	engine := enginesync.NewEngine(repo, &fakeSnapshotRepo{}, hub, enginesync.DefaultEngineConfig())

	update := []byte("fake-yjs-update")
	err := engine.ApplyYjsUpdate(context.Background(), treeID, actorID, update)
	require.NoError(t, err)

	require.Len(t, repo.events, 1)
	assert.Equal(t, "yjs_update", repo.events[0].EventType)
	assert.Equal(t, treeID, repo.events[0].TreeID)

	require.Len(t, hub.events, 1)
	broadcast := hub.events[0]
	assert.Equal(t, "yjs_update", broadcast.Type)
	assert.Equal(t, treeID, broadcast.TreeID)
	assert.Equal(t, actorID, broadcast.ActorID)
	assert.Equal(t, int64(1), broadcast.SequenceNum)

	// Data must be a JSON string containing the base64-encoded update.
	var decoded string
	require.NoError(t, json.Unmarshal(broadcast.Data, &decoded))
	got, err := base64.StdEncoding.DecodeString(decoded)
	require.NoError(t, err)
	assert.Equal(t, update, got)
}
