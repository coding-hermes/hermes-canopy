package federation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

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
