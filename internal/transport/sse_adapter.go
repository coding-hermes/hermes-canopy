package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

const defaultSSEMaxMessageSize int64 = 1 << 20

// SSEAdapter implements the Phase 1 transport over an in-process hub (UUID
// target) or a remote HTTP event stream (URL target).
// connStateMu guards Connection.State/SequenceWatermark/LastActivity for
// connections that have no sseConnection (pre-connect / post-disconnect).
var connStateMu sync.Map // connID uuid.UUID -> *sync.RWMutex

func stateMuFor(conn *Connection) *sync.RWMutex {
	mu, _ := connStateMu.LoadOrStore(conn.ID, &sync.RWMutex{})
	return mu.(*sync.RWMutex)
}

type SSEAdapter struct {
	hub    sse.SSEHub
	client *http.Client
	mu     sync.RWMutex
	conns  map[string]*sseConnection
}

type sseConnection struct {
	conn      *Connection
	treeID    uuid.UUID
	recv      chan *Message
	client    *adapterSSEClient
	cancel    context.CancelFunc
	maxSize   int64
	token     string
	lastID    string
	lastMu    sync.RWMutex
	stateMu   sync.RWMutex
	closeOnce sync.Once
}

// NewSSEAdapter wraps a hub. A nil hub is valid for URL-based SSE clients.
func NewSSEAdapter(hub sse.SSEHub) *SSEAdapter {
	return &SSEAdapter{hub: hub, client: &http.Client{}, conns: make(map[string]*sseConnection)}
}

func (a *SSEAdapter) TransportType() TransportType { return TransportSSE }

func (a *SSEAdapter) Connect(ctx context.Context, opts ConnectOptions) (*Connection, error) {
	if opts.TransportType != "" && opts.TransportType != TransportSSE {
		return nil, ErrTransportMismatch
	}
	if err := contextError(ctx); err != nil {
		return nil, errors.Join(ErrConnectionFailed, err)
	}
	now := time.Now().UTC()
	conn := &Connection{ID: uuid.NewString(), TransportType: TransportSSE, Peer: opts.Target,
		TenantID: opts.TenantID, Metadata: cloneMetadata(opts.Metadata), State: StateConnecting,
		EstablishedAt: now, LastActivity: now}
	maxSize := opts.MaxMessageSize
	if maxSize <= 0 || maxSize > defaultSSEMaxMessageSize {
		maxSize = defaultSSEMaxMessageSize
	}
	runCtx, cancel := context.WithCancel(context.Background())
	sc := &sseConnection{conn: conn, recv: make(chan *Message, 256), cancel: cancel, maxSize: maxSize, token: opts.Auth.Token}

	if target, err := url.ParseRequestURI(opts.Target); err == nil && (target.Scheme == "http" || target.Scheme == "https") {
		a.mu.Lock()
		a.conns[conn.ID] = sc
		a.mu.Unlock()
		conn.State = StateActive
		go a.consumeHTTP(runCtx, sc, opts.Timeout)
		return conn, nil
	}
	if a.hub == nil {
		cancel()
		return nil, ErrConnectionFailed
	}
	treeID, err := uuid.Parse(opts.Target)
	if err != nil {
		cancel()
		return nil, errors.Join(ErrConnectionFailed, err)
	}
	sc.treeID = treeID
	sc.client = newAdapterSSEClient(conn.ID, treeID, sc.recv)
	if err := a.hub.Subscribe(ctx, treeID, sc.client); err != nil {
		cancel()
		return nil, errors.Join(ErrConnectionFailed, err)
	}
	a.mu.Lock()
	a.conns[conn.ID] = sc
	a.mu.Unlock()
	conn.State = StateActive
	return conn, nil
}

func (a *SSEAdapter) Send(ctx context.Context, conn *Connection, msg *Message) error {
	if conn == nil || conn.State != StateActive {
		return ErrConnectionClosed
	}
	if msg == nil || msg.Opcode < OpTreeCreate || msg.Opcode > OpAck {
		return ErrUnsupportedOpcode
	}
	if err := contextError(ctx); err != nil {
		return errors.Join(ErrSendTimeout, err)
	}
	a.mu.RLock()
	sc := a.conns[conn.ID]
	a.mu.RUnlock()
	if sc == nil {
		return ErrNotConnected
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("transport: encode message: %w", err)
	}
	if int64(len(data)) > sc.maxSize {
		return ErrPayloadTooLarge
	}
	if a.hub == nil || sc.treeID == uuid.Nil {
		return ErrNotConnected
	}
	a.hub.Broadcast(sc.treeID, sse.SSEEvent{Type: msg.Opcode.String(), Data: data, TreeID: sc.treeID, Timestamp: time.Now().UTC()})
	conn.LastActivity = time.Now().UTC()
	return nil
}

func (a *SSEAdapter) Receive(ctx context.Context, conn *Connection) (<-chan *Message, error) {
	if conn == nil {
		return nil, ErrConnectionClosed
	}
	a.mu.RLock()
	sc := a.conns[conn.ID]
	a.mu.RUnlock()
	if sc == nil {
		return nil, ErrNotConnected
	}
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = a.Disconnect(context.Background(), conn)
			case <-sc.clientDone():
			}
		}()
	}
	return sc.recv, nil
}

func (sc *sseConnection) clientDone() <-chan struct{} {
	if sc.client != nil {
		return sc.client.Done()
	}
	return make(chan struct{})
}

func (a *SSEAdapter) Disconnect(_ context.Context, conn *Connection) error {
	if conn == nil {
		return nil
	}
	a.mu.Lock()
	sc := a.conns[conn.ID]
	delete(a.conns, conn.ID)
	a.mu.Unlock()
	mu := stateMuFor(conn)
	mu.Lock()
	conn.State = StateClosed
	mu.Unlock()
	if sc == nil {
		return nil
	}
	mu.Lock()
	conn.State = StateDisconnecting
	mu.Unlock()
	if sc.client != nil {
		a.hub.Unsubscribe(sc.treeID, conn.ID)
		_ = sc.client.Close()
	}
	sc.cancel()
	sc.closeOnce.Do(func() { close(sc.recv) })
	conn.State = StateClosed
	return nil
}

func (a *SSEAdapter) Health(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if a.hub == nil {
		a.mu.RLock()
		connected := len(a.conns) > 0
		a.mu.RUnlock()
		if !connected {
			return ErrTransportUnreachable
		}
	}
	return nil
}

func (a *SSEAdapter) consumeHTTP(ctx context.Context, sc *sseConnection, timeout time.Duration) {
	defer sc.closeOnce.Do(func() { close(sc.recv) })
	backoff := time.Second
	for ctx.Err() == nil {
		mu := stateMuFor(sc.conn)
		mu.Lock()
		sc.conn.State = StateConnecting
		mu.Unlock()
		_ = a.consumeOnce(ctx, sc, timeout)
		if ctx.Err() != nil {
			return
		}
		if sc.conn.State == StateActive {
			backoff = time.Second
		}
		sc.conn.State = StateDegraded
		jitter := time.Duration(float64(backoff) * (0.75 + rand.Float64()*0.5))
		timer := time.NewTimer(jitter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > 32*time.Second {
			backoff = 32 * time.Second
		}
	}
}

func (a *SSEAdapter) consumeOnce(ctx context.Context, sc *sseConnection, timeout time.Duration) error {
	reqCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, sc.conn.Peer, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if sc.token != "" {
		req.Header.Set("Authorization", "Bearer "+sc.token)
	}
	sc.lastMu.RLock()
	lastID := sc.lastID
	sc.lastMu.RUnlock()
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("transport: SSE status %s", resp.Status)
	}
	mu := stateMuFor(sc.conn)
	mu.Lock()
	sc.conn.State = StateActive
	mu.Unlock()
	return readSSE(resp.Body, func(id string, data []byte) error {
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil // non-transport SSE events share the stream
		}
		if msg.Opcode < OpTreeCreate || msg.Opcode > OpAck {
			return ErrUnsupportedOpcode
		}
		select {
		case sc.recv <- &msg:
			mu := stateMuFor(sc.conn)
			mu.Lock()
			sc.conn.LastActivity = time.Now().UTC()
			sc.conn.SequenceWatermark = msg.Sequence
			mu.Unlock()
			sc.lastMu.Lock()
			if id != "" {
				sc.lastID = id
			}
			sc.lastMu.Unlock()
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

func readSSE(r io.Reader, deliver func(string, []byte) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), int(defaultSSEMaxMessageSize)+4096)
	var id string
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 {
				if err := deliver(id, []byte(strings.Join(data, "\n"))); err != nil {
					return err
				}
			}
			data = data[:0]
			continue
		}
		if strings.HasPrefix(line, "id:") {
			id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}

func cloneMetadata(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type adapterSSEClient struct {
	id     string
	treeID uuid.UUID
	events chan<- *Message
	done   chan struct{}
	once   sync.Once
}

func newAdapterSSEClient(id string, treeID uuid.UUID, events chan<- *Message) *adapterSSEClient {
	return &adapterSSEClient{id: id, treeID: treeID, events: events, done: make(chan struct{})}
}
func (c *adapterSSEClient) ID() string            { return c.id }
func (c *adapterSSEClient) TreeID() uuid.UUID     { return c.treeID }
func (c *adapterSSEClient) UserID() uuid.UUID     { return uuid.Nil }
func (c *adapterSSEClient) LastEventID() string   { return "" }
func (c *adapterSSEClient) Done() <-chan struct{} { return c.done }
func (c *adapterSSEClient) SendRaw(string) error  { return nil }
func (c *adapterSSEClient) Close() error          { c.once.Do(func() { close(c.done) }); return nil }
func (c *adapterSSEClient) Send(ev sse.SSEEvent) error {
	var msg Message
	if err := json.Unmarshal(ev.Data, &msg); err != nil {
		return err
	}
	select {
	case c.events <- &msg:
		return nil
	case <-c.done:
		return ErrConnectionClosed
	}
}

var _ TransportAdapter = (*SSEAdapter)(nil)
