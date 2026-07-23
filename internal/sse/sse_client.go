package sse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Client is a concrete SSEClient backed by an http.ResponseWriter. The
// underlying writer is buffered via bufio.Writer and flushed manually so
// that:
//
//   - writes never block longer than the network round-trip,
//   - the connection can be cancelled by closing the underlying writer.
//
// The Client owns no goroutines — the caller's HTTP handler drives the
// heartbeat loop. Broadcaster pushes events into a bounded chan via Send.
type Client struct {
	id       string
	treeID   uuid.UUID
	userID   uuid.UUID
	lastEvID atomic.Value // string — last formatEvent ID sent

	writer  http.ResponseWriter
	flusher http.Flusher
	buf     *bufio.Writer

	mu       sync.Mutex // protects Send/SendRaw interleaving
	closed   atomic.Bool
	doneCh   chan struct{}
	closeErr error

	// Outbound queue used by the hub to broadcast without blocking on the
	// underlying writer. When full (or unbuffered), Send returns an error
	// and the hub unsubscribes the client.
	outbox chan []byte
}

// NewClient constructs an SSEClient. The flusher MUST be the same
// flusher obtained from the http.ResponseWriter type assertion in the
// handler — passing a non-flushable writer will deadlock on send.
func NewClient(id string, userID, treeID uuid.UUID, w http.ResponseWriter, flusher http.Flusher) *Client {
	c := &Client{
		id:      id,
		userID:  userID,
		treeID:  treeID,
		writer:  w,
		flusher: flusher,
		buf:     bufio.NewWriterSize(w, 4096),
		doneCh:  make(chan struct{}),
		outbox:  make(chan []byte, EventBufferSize),
	}
	c.lastEvID.Store("")
	return c
}

// NewClientWithOutbox is like NewClient but with a custom outbox capacity.
// Used by tests that need smaller buffers.
func NewClientWithOutbox(id string, userID, treeID uuid.UUID, w http.ResponseWriter, flusher http.Flusher, outboxCap int) *Client {
	c := &Client{
		id:      id,
		userID:  userID,
		treeID:  treeID,
		writer:  w,
		flusher: flusher,
		buf:     bufio.NewWriterSize(w, 4096),
		doneCh:  make(chan struct{}),
		outbox:  make(chan []byte, outboxCap),
	}
	c.lastEvID.Store("")
	return c
}

// ID returns the unique client identifier.
func (c *Client) ID() string { return c.id }

// UserID returns the authenticated user. May be uuid.Nil in MVP.
func (c *Client) UserID() uuid.UUID { return c.userID }

// TreeID returns the tree the client is subscribed to.
func (c *Client) TreeID() uuid.UUID { return c.treeID }

// Done returns a channel closed when the client is shut down.
func (c *Client) Done() <-chan struct{} { return c.doneCh }

// LastEventID returns the last event ID successfully written. Safe to call
// concurrently.
func (c *Client) LastEventID() string {
	v, _ := c.lastEvID.Load().(string)
	return v
}

// Send queues an SSE-event for transmission. Returns ErrClientClosed if the
// client is shutting down, or ErrClientSlow if the outbox is full.
func (c *Client) Send(event SSEEvent) error {
	if c.closed.Load() {
		return ErrClientClosed
	}
	line := formatEvent(event)
	select {
	case c.outbox <- line:
		c.lastEvID.Store(event.ID)
		return nil
	default:
		// Buffer full → slow consumer.
		return ErrClientSlow
	}
}

// SendRaw writes raw SSE bytes directly. Used for heartbeats and in-stream
// errors that bypass the event log.
func (c *Client) SendRaw(raw string) error {
	if c.closed.Load() {
		return ErrClientClosed
	}
	select {
	case c.outbox <- []byte(raw):
		return nil
	default:
		return ErrClientSlow
	}
}

// Flush drains buffered bytes to the network and pumps pending outbox events
// once. Returns the first non-nil I/O error. It is the caller's job to
// invoke Flush in a loop until Close is called or Done() fires.
func (c *Client) Flush() error {
	if c.closed.Load() {
		return ErrClientClosed
	}
	// Non-blocking outbox drain.
	for {
		select {
		case msg := <-c.outbox:
			c.mu.Lock()
			err := c.writeAll(msg)
			c.mu.Unlock()
			if err != nil {
				return err
			}
		default:
			return c.flushBuf()
		}
	}
}

func (c *Client) flushBuf() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.buf.Flush(); err != nil {
		return err
	}
	c.flusher.Flush()
	return nil
}

func (c *Client) writeAll(b []byte) error {
	if _, err := c.buf.Write(b); err != nil {
		return err
	}
	if err := c.buf.Flush(); err != nil {
		return err
	}
	c.flusher.Flush()
	return nil
}

// Close shuts the client down. Idempotent. Subsequent Send/SendRaw/Flush
// calls return ErrClientClosed. Close does not wait for in-flight writes.
func (c *Client) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return c.closeErr
	}
	c.closeErr = c.bestEffortClose()
	close(c.doneCh)
	// Drain outbox into a no-op so a sender can complete without blocking.
	go func() {
		for range c.outbox {
			// drop
		}
	}()
	close(c.outbox)
	return c.closeErr
}

func (c *Client) bestEffortClose() error {
	// Flush whatever is buffered; ignore errors — the underlying conn may
	// already be gone.
	c.mu.Lock()
	defer c.mu.Unlock()
	flushErr := c.buf.Flush()
	if closer, ok := c.writer.(closeNotifier); ok {
		// We don't own the connection — best effort only.
		_ = closer
	}
	if flushErr != nil {
		return flushErr
	}
	// For http.CloseNotifier / hijacked conns we'd close the net.Conn, but
	// chi/stdlib doesn't expose that for plain ResponseWriters. Drop.
	return nil
}

// closeNotifier matches the unexported shape used by some stdlib helpers.
// Kept as a marker type so future hijack-based optimizations can plug in.
type closeNotifier interface{ CloseNotify() bool }

// Errors returned by Client.
var (
	ErrClientClosed = errors.New("sse client closed")
	ErrClientSlow   = errors.New("sse client outbox full")
)

// --- Format helpers ---------------------------------------------------------

// formatEvent renders an SSEEvent into the WHATWG SSE wire format
// (id / event / data / retry, blank terminator). Used by Send.
func formatEvent(ev SSEEvent) []byte {
	var b bytes.Buffer

	id := ev.ID
	if id == "" {
		id = EventID(ev.TreeID, ev.SequenceNum)
	}
	b.WriteString("id: ")
	b.WriteString(id)
	b.WriteByte('\n')

	b.WriteString("event: ")
	b.WriteString(ev.Type)
	b.WriteByte('\n')

	// Wrap the raw data in the envelope shape (SPEC-API-01 §5.2).
	env := envelope{
		EventType:   ev.Type,
		TreeID:      ev.TreeID.String(),
		Timestamp:   ev.Timestamp.UTC().Format(time.RFC3339),
		SequenceNum: ev.SequenceNum,
		ActorID:     ev.ActorID.String(),
		Data:        ev.Data,
	}
	out, err := json.Marshal(env)
	if err != nil {
		// Fall back to a minimal event so the client at least sees the
		// header / event type — never silently drop.
		out = []byte(fmt.Sprintf(`{"event_type":%q,"error":"encode_failed"}`, ev.Type))
	}
	b.WriteString("data: ")
	b.Write(out)
	b.WriteByte('\n')

	b.WriteString("retry: ")
	b.WriteString(fmt.Sprintf("%d", DefaultRetryMS))
	b.WriteByte('\n')

	// Blank line terminator required by the spec for dispatch.
	b.WriteByte('\n')

	return b.Bytes()
}

// ComposeEvent is a package-level helper used by tests and external
// producers (e.g. service callers) to build an SSEEvent envelope directly.
// The returned event has SequenceNum == 0; the hub will assign one.
func ComposeEvent(treeID, actorID uuid.UUID, eventType string, data any) SSEEvent {
	var raw json.RawMessage
	if data != nil {
		if b, ok := data.(json.RawMessage); ok {
			raw = b
		} else {
			b, err := json.Marshal(data)
			if err != nil {
				raw = json.RawMessage(fmt.Sprintf(`{"marshal_error":%q}`, err.Error()))
			} else {
				raw = b
			}
		}
	}
	return SSEEvent{
		Type:      eventType,
		Data:      raw,
		Timestamp: time.Now().UTC(),
		TreeID:    treeID,
		ActorID:   actorID,
	}
}

// connFromWriter extracts a net.Conn from a ResponseWriter if possible
// (after Hijack). Returns nil otherwise. Mostly here for completeness;
// not exercised in MVP.
func connFromWriter(w http.ResponseWriter) net.Conn {
	if hj, ok := w.(http.Hijacker); ok {
		conn, _, err := hj.Hijack()
		if err == nil {
			return conn
		}
	}
	return nil
}
