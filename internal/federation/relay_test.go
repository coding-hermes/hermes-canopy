package federation

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

func TestPGRelayQueueOverflowDropsOldest(t *testing.T) {
	t.Setenv("CANOPY_REQUIRE_DB", "1")
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	ownerID, profileID, treeID, peerID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,hermes_user_id,display_name) VALUES($1,$2,'Queue Owner')`, ownerID, "queue-"+ownerID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO profiles(id,owner_id,profile_type,name,display_name) VALUES($1,$2,'hermes-profile',$3,'Queue Profile')`, profileID, ownerID, "queue-"+profileID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trees(id,owner_id,title) VALUES($1,$2,'Queue Tree')`, treeID, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO federation_peers(id,server_url,signing_key_fp,state,tree_id,created_by) VALUES($1,$2,'sha256:test',0,$3,$4)`, peerID, "https://queue-"+peerID.String()+".example", treeID, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO federation_events(tree_id,sender_profile_id,target_peer_id,sequence_no,payload)
		SELECT $1,$2,$3,n,'{}'::jsonb FROM generate_series(1,$4) n`, treeID, profileID, peerID, MaxPeerQueueDepth); err != nil {
		t.Fatal(err)
	}
	repo := NewPGRelayRepository(pool)
	if _, err := repo.Enqueue(ctx, &RelayEvent{TreeID: treeID, SenderProfileID: profileID, TargetPeerID: peerID, SequenceNo: MaxPeerQueueDepth + 1, Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	depth, err := repo.QueueDepth(ctx, peerID)
	if err != nil {
		t.Fatal(err)
	}
	if depth != MaxPeerQueueDepth {
		t.Fatalf("queue depth = %d, want %d", depth, MaxPeerQueueDepth)
	}
	var oldest int64
	if err := pool.QueryRow(ctx, `SELECT min(sequence_no) FROM federation_events WHERE target_peer_id=$1`, peerID).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if oldest != 2 {
		t.Fatalf("oldest sequence = %d, want 2", oldest)
	}
}

type memoryRelayQueue struct {
	mu       sync.Mutex
	events   []RelayEvent
	cursor   map[uuid.UUID]int64
	receipts map[uuid.UUID]bool
}

func newMemoryRelayQueue() *memoryRelayQueue {
	return &memoryRelayQueue{cursor: make(map[uuid.UUID]int64), receipts: make(map[uuid.UUID]bool)}
}

func (q *memoryRelayQueue) Enqueue(_ context.Context, event *RelayEvent) (*RelayEvent, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	event.Status, event.CreatedAt = RelayPending, time.Now().UTC()
	q.events = append(q.events, *event)
	copy := *event
	return &copy, nil
}
func (q *memoryRelayQueue) Replay(_ context.Context, peer uuid.UUID, cursor int64, limit int) ([]RelayEvent, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []RelayEvent{}
	for _, event := range q.events {
		if event.TargetPeerID == peer && event.SequenceNo > cursor && len(out) < limit {
			out = append(out, event)
		}
	}
	return out, nil
}
func (q *memoryRelayQueue) Pending(ctx context.Context, peer uuid.UUID, limit int) ([]RelayEvent, error) {
	events, _ := q.Replay(ctx, peer, -1, limit)
	out := events[:0]
	for _, event := range events {
		if event.Status != RelayDelivered {
			out = append(out, event)
		}
	}
	return out, nil
}
func (q *memoryRelayQueue) MarkAttempt(_ context.Context, id uuid.UUID, deliveryErr error, delivered bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.events {
		if q.events[i].EventID == id {
			q.events[i].DeliveryAttempts++
			if delivered {
				q.events[i].Status = RelayDelivered
			} else {
				q.events[i].Status = RelayFailed
			}
			return nil
		}
	}
	return errors.New("not found")
}
func (q *memoryRelayQueue) Cursor(_ context.Context, peer uuid.UUID) (int64, error) {
	return q.cursor[peer], nil
}
func (q *memoryRelayQueue) AdvanceCursor(_ context.Context, peer uuid.UUID, n int64) error {
	if n > q.cursor[peer] {
		q.cursor[peer] = n
	}
	return nil
}
func (q *memoryRelayQueue) RecordReceipt(_ context.Context, id, _ uuid.UUID, _ int64) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.receipts[id] {
		return false, nil
	}
	q.receipts[id] = true
	return true, nil
}

func TestRelayQueueReplayOrdersAfterCursor(t *testing.T) {
	queue := newMemoryRelayQueue()
	relay := NewRelayService(queue, nil, nil, nil)
	peer, tree, profile := uuid.New(), uuid.New(), uuid.New()
	for sequence := int64(1); sequence <= 3; sequence++ {
		_, err := relay.Enqueue(context.Background(), peer, &FTLEnvelope{FTLVersion: 1, PeerID: peer, TreeID: tree, SenderProfileID: profile, Sequence: sequence})
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := relay.Replay(context.Background(), peer, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].SequenceNo != 2 || events[1].SequenceNo != 3 {
		t.Fatalf("replay = %#v", events)
	}
}

func TestRelayChunkingReassemblesPayload(t *testing.T) {
	relay := NewRelayService(newMemoryRelayQueue(), nil, nil, nil)
	relay.chunkSize = 4
	event := RelayEvent{EventID: uuid.New(), TargetPeerID: uuid.New(), SequenceNo: 7, Payload: []byte("abcdefghij")}
	frames := relay.Frames(event)
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3", len(frames))
	}
	var got []byte
	for i, frame := range frames {
		if frame.ChunkSeq != i+1 || frame.ChunkTotal != 3 {
			t.Fatalf("metadata = %#v", frame)
		}
		got = append(got, frame.ChunkPayload...)
	}
	if string(got) != string(event.Payload) {
		t.Fatalf("payload = %q", got)
	}
}

func TestRelayReceiptDeduplicatesEventID(t *testing.T) {
	relay := NewRelayService(newMemoryRelayQueue(), nil, nil, nil)
	id, peer := uuid.New(), uuid.New()
	first, _ := relay.RecordReceipt(context.Background(), id, peer, 1)
	second, _ := relay.RecordReceipt(context.Background(), id, peer, 1)
	if !first || second {
		t.Fatalf("receipt results = %v, %v", first, second)
	}
}
