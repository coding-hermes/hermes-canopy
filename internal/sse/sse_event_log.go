package sse

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// eventLog is the concrete SSEEventLog. One log exists per server process;
// each tree gets its own monotonic counter and bounded ring buffer.
//
// Concurrency model:
//   - countersMu guards per-tree sequence counters
//   - shardsMu guards the map of tree → ring
//   - each ring has its own sync.RWMutex for Since / SinceTime / Prune
type eventLog struct {
	cap int // per-tree ring capacity

	countersMu sync.Mutex
	counters   map[uuid.UUID]*int64

	shardsMu sync.RWMutex
	shards   map[uuid.UUID]*ringBuf
}

func newEventLog(capacity int) *eventLog {
	return &eventLog{
		cap:      capacity,
		counters: make(map[uuid.UUID]*int64),
		shards:   make(map[uuid.UUID]*ringBuf),
	}
}

// Append assigns the next monotonic sequence number for the tree and stores
// the event in the ring. Returns the populated SSEEvent.
func (l *eventLog) Append(treeID uuid.UUID, eventType string, data json.RawMessage, actorID uuid.UUID) SSEEvent {
	seq := l.nextSeq(treeID)

	l.shardsMu.RLock()
	ring, ok := l.shards[treeID]
	l.shardsMu.RUnlock()
	if !ok {
		l.shardsMu.Lock()
		// Re-check after upgrade.
		ring, ok = l.shards[treeID]
		if !ok {
			ring = newRingBuf(l.cap)
			l.shards[treeID] = ring
		}
		l.shardsMu.Unlock()
	}

	ev := SSEEvent{
		ID:          EventID(treeID, seq),
		Type:        eventType,
		Data:        data,
		Timestamp:   time.Now().UTC(),
		TreeID:      treeID,
		SequenceNum: seq,
		ActorID:     actorID,
	}
	ring.append(ev)
	return ev
}

// nextSeq returns the next sequence number for a tree, allocating the
// counter on first use. Sequence numbers are 1-indexed.
func (l *eventLog) nextSeq(treeID uuid.UUID) int64 {
	l.countersMu.Lock()
	defer l.countersMu.Unlock()
	ctr, ok := l.counters[treeID]
	if !ok {
		var v int64
		ctr = &v
		l.counters[treeID] = ctr
	}
	next := *ctr + 1
	*ctr = next
	return next
}

// Since returns events with SequenceNum > sinceSeqNum for the given tree,
// in ascending order, capped at maxEvents. If the cap is hit, truncated=true
// and the caller is expected to send a tree_snapshot and reconnect.
//
// Empty results are returned as a zero-length slice, not nil — callers can
// always `for _, ev := range events`.
func (l *eventLog) Since(treeID uuid.UUID, sinceSeqNum int64, maxEvents int) ([]SSEEvent, bool, error) {
	l.shardsMu.RLock()
	ring, ok := l.shards[treeID]
	l.shardsMu.RUnlock()
	if !ok {
		return []SSEEvent{}, false, nil
	}
	return ring.since(sinceSeqNum, maxEvents)
}

// SinceTime is like Since but compares against Timestamp instead of SequenceNum.
// Useful when a client provides a wall-clock Last-Event-ID fallback.
func (l *eventLog) SinceTime(treeID uuid.UUID, since time.Time, maxEvents int) ([]SSEEvent, bool, error) {
	l.shardsMu.RLock()
	ring, ok := l.shards[treeID]
	l.shardsMu.RUnlock()
	if !ok {
		return []SSEEvent{}, false, nil
	}
	return ring.sinceTime(since, maxEvents)
}

// Prune removes events older than `retention` across every tree. Returns the
// number of events removed. Safe to call concurrently.
func (l *eventLog) Prune(retention time.Duration) int {
	l.shardsMu.RLock()
	rings := make([]*ringBuf, 0, len(l.shards))
	for _, r := range l.shards {
		rings = append(rings, r)
	}
	l.shardsMu.RUnlock()

	removed := 0
	for _, r := range rings {
		removed += r.prune(retention)
	}
	return removed
}

// --- ringBuf (per-tree bounded ring) ---------------------------------------

// ringBuf is a simple append-and-prune ring buffer kept in insertion order.
//
// The buffer is append-only until capacity is reached; thereafter, the
// oldest entry is evicted when a new one is appended.
//
// All operations are safe under the ring's own RWMutex; callers must not
// hold additional locks when invoking ring methods.
type ringBuf struct {
	mu      sync.RWMutex
	entries []SSEEvent
	cap     int

	// Cursor for fast O(cap) prune when retention elapses.
	head int // index of oldest entry; == len(entries) means empty/full
}

func newRingBuf(capacity int) *ringBuf {
	if capacity <= 0 {
		capacity = DefaultLogSize
	}
	return &ringBuf{
		entries: make([]SSEEvent, 0, capacity),
		cap:     capacity,
	}
}

// append inserts ev and evicts the oldest entry if at capacity.
func (r *ringBuf) append(ev SSEEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.entries) < r.cap {
		r.entries = append(r.entries, ev)
		return
	}
	// At capacity — shift in-place. For a 1000-entry ring this is fast
	// enough on the broadcast path; if we ever need bigger we can use a
	// circular index instead.
	r.entries = append(r.entries[1:], ev)
}

// since returns events with sequence > sinceSeq, capped at max, in order.
func (r *ringBuf) since(sinceSeq int64, max int) ([]SSEEvent, bool, error) {
	r.mu.RLock()
	out := make([]SSEEvent, 0, max)
	truncated := false
	for _, e := range r.entries {
		if e.SequenceNum <= sinceSeq {
			continue
		}
		if len(out) >= max {
			truncated = true
			break
		}
		out = append(out, e)
	}
	r.mu.RUnlock()

	// Defensive: ensure ascending order even if a future implementation
	// reverses the in-memory order.
	sort.SliceStable(out, func(i, j int) bool { return out[i].SequenceNum < out[j].SequenceNum })
	return out, truncated, nil
}

// sinceTime mirrors since with a timestamp predicate.
func (r *ringBuf) sinceTime(since time.Time, max int) ([]SSEEvent, bool, error) {
	r.mu.RLock()
	out := make([]SSEEvent, 0, max)
	truncated := false
	for _, e := range r.entries {
		if !e.Timestamp.After(since) {
			continue
		}
		if len(out) >= max {
			truncated = true
			break
		}
		out = append(out, e)
	}
	r.mu.RUnlock()

	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	return out, truncated, nil
}

// prune removes entries older than retention. Returns count removed.
func (r *ringBuf) prune(retention time.Duration) int {
	cutoff := time.Now().UTC().Add(-retention)
	r.mu.Lock()
	defer r.mu.Unlock()

	kept := r.entries[:0]
	removed := 0
	for _, e := range r.entries {
		if e.Timestamp.Before(cutoff) {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	// Copy to avoid leaking cap of the underlying array.
	if removed > 0 {
		fresh := make([]SSEEvent, len(kept))
		copy(fresh, kept)
		r.entries = fresh
		r.head = 0
	}
	return removed
}
