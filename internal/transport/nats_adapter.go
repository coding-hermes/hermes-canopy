package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
)

const defaultNATSMaxMessageSize int64 = 1 << 20

// NATSClient is the dependency boundary for a future nats.go/JetStream client.
// Phase 2 keeps that dependency out of the binary and permits an in-process bus
// in tests.
type NATSClient interface {
	Connect(ctx context.Context, serverURL, bearer string) error
	Publish(ctx context.Context, subject string, data []byte) error
	Subscribe(ctx context.Context, subject string, handler func([]byte)) (NATSSubscription, error)
	Ping(ctx context.Context) error
	Close() error
}

// NATSSubscription is the cleanup surface needed by the adapter.
type NATSSubscription interface {
	Unsubscribe() error
}

type unavailableNATSClient struct{}

func (unavailableNATSClient) Connect(context.Context, string, string) error {
	return ErrTransportUnreachable
}
func (unavailableNATSClient) Publish(context.Context, string, []byte) error {
	return ErrTransportUnreachable
}
func (unavailableNATSClient) Subscribe(context.Context, string, func([]byte)) (NATSSubscription, error) {
	return nil, ErrTransportUnreachable
}
func (unavailableNATSClient) Ping(context.Context) error { return ErrTransportUnreachable }
func (unavailableNATSClient) Close() error               { return nil }

// NATSAdapter maps tree messages to NATS subjects. A real nats.go client is
// intentionally deferred; callers may inject a client implementing NATSClient.
type NATSAdapter struct {
	client NATSClient
	mu     sync.RWMutex
	conns  map[string]*natsConnection
}

// NATSTransportAdapter is the spec name for NATSAdapter. The alias preserves
// the Phase 1 constructor/type name used elsewhere in the package.
type NATSTransportAdapter = NATSAdapter

type natsConnection struct {
	conn      *Connection
	recv      chan *Message
	done      chan struct{}
	sub       NATSSubscription
	maxSize   int64
	delivery  sync.RWMutex
	closed    bool
	closeOnce sync.Once
}

// NewNATSAdapter creates an adapter. With no client it remains a compilable,
// unavailable production stub until the real client is wired in Phase 3.
func NewNATSAdapter(clients ...NATSClient) *NATSAdapter {
	var client NATSClient = unavailableNATSClient{}
	if len(clients) > 0 && clients[0] != nil {
		client = clients[0]
	}
	return &NATSAdapter{client: client, conns: make(map[string]*natsConnection)}
}

// NewNATSTransportAdapter creates the spec-named NATS transport adapter.
func NewNATSTransportAdapter(clients ...NATSClient) *NATSTransportAdapter {
	return NewNATSAdapter(clients...)
}

func (a *NATSAdapter) TransportType() TransportType { return TransportNATS }

func (a *NATSAdapter) Connect(ctx context.Context, opts ConnectOptions) (*Connection, error) {
	if opts.TransportType != "" && opts.TransportType != TransportNATS {
		return nil, ErrTransportMismatch
	}
	if err := validateNATSURL(opts.Target); err != nil {
		return nil, errors.Join(ErrConnectionFailed, err)
	}
	if err := contextError(ctx); err != nil {
		return nil, errors.Join(ErrConnectionFailed, err)
	}

	now := time.Now().UTC()
	conn := &Connection{
		ID: uuid.NewString(), TransportType: TransportNATS, Peer: opts.Target,
		TenantID: opts.TenantID, Metadata: cloneMetadata(opts.Metadata),
		State: StateConnecting, EstablishedAt: now, LastActivity: now,
	}
	bearer := opts.Auth.Bearer
	if bearer == "" {
		bearer = opts.Auth.Token
	}
	if err := connectNATSWithBackoff(ctx, opts.Timeout, func(attemptCtx context.Context) error {
		return a.client.Connect(attemptCtx, opts.Target, bearer)
	}); err != nil {
		mu := stateMuFor(conn)
		mu.Lock()
		conn.State = StateClosed
		mu.Unlock()
		return nil, errors.Join(ErrConnectionFailed, err)
	}

	maxSize := opts.MaxMessageSize
	if maxSize <= 0 || maxSize > defaultNATSMaxMessageSize {
		maxSize = defaultNATSMaxMessageSize
	}
	nc := &natsConnection{conn: conn, recv: make(chan *Message, 256), done: make(chan struct{}), maxSize: maxSize}
	filter := "canopy.*.*"
	if treeID := opts.Metadata["tree_id"]; treeID != "" {
		if !validNATSSubjectToken(treeID) {
			_ = a.client.Close()
			return nil, errors.Join(ErrConnectionFailed, fmt.Errorf("transport: invalid NATS tree ID %q", treeID))
		}
		filter = "canopy." + treeID + ".*"
	}
	sub, err := a.client.Subscribe(ctx, filter, func(data []byte) { nc.deliver(data) })
	if err != nil {
		_ = a.client.Close()
		return nil, errors.Join(ErrConnectionFailed, err)
	}
	nc.sub = sub
	a.mu.Lock()
	a.conns[conn.ID] = nc
	a.mu.Unlock()
	mu := stateMuFor(conn)
	mu.Lock()
	conn.State = StateActive
	mu.Unlock()
	return conn, nil
}

func (a *NATSAdapter) Send(ctx context.Context, conn *Connection, msg *Message) error {
	if conn == nil {
		return ErrConnectionClosed
	}
	mu := stateMuFor(conn)
	mu.RLock()
	active := conn.State == StateActive
	mu.RUnlock()
	if !active {
		return ErrConnectionClosed
	}
	if msg == nil || msg.Opcode < OpTreeCreate || msg.Opcode > OpAck {
		return ErrUnsupportedOpcode
	}
	if err := contextError(ctx); err != nil {
		return errors.Join(ErrSendTimeout, err)
	}
	a.mu.RLock()
	nc := a.conns[conn.ID]
	a.mu.RUnlock()
	if nc == nil {
		return ErrNotConnected
	}
	subject, err := natsSubject(msg.TreeID, msg.Opcode)
	if err != nil {
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("transport: encode message: %w", err)
	}
	if int64(len(data)) > nc.maxSize {
		return ErrPayloadTooLarge
	}
	if err := a.client.Publish(ctx, subject, data); err != nil {
		if contextError(ctx) != nil {
			return errors.Join(ErrSendTimeout, err)
		}
		return err
	}
	mu.Lock()
	conn.LastActivity = time.Now().UTC()
	mu.Unlock()
	return nil
}

func (a *NATSAdapter) Receive(ctx context.Context, conn *Connection) (<-chan *Message, error) {
	if conn == nil {
		return nil, ErrConnectionClosed
	}
	a.mu.RLock()
	nc := a.conns[conn.ID]
	a.mu.RUnlock()
	if nc == nil {
		return nil, ErrNotConnected
	}
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = a.Disconnect(context.Background(), conn)
			case <-nc.done:
			}
		}()
	}
	return nc.recv, nil
}

func (a *NATSAdapter) Disconnect(_ context.Context, conn *Connection) error {
	if conn == nil {
		return nil
	}
	a.mu.Lock()
	nc := a.conns[conn.ID]
	delete(a.conns, conn.ID)
	remaining := len(a.conns)
	a.mu.Unlock()
	mu := stateMuFor(conn)
	mu.Lock()
	if conn.State == StateClosed {
		mu.Unlock()
		return nil
	}
	conn.State = StateDisconnecting
	mu.Unlock()
	if nc != nil {
		if nc.sub != nil {
			_ = nc.sub.Unsubscribe()
		}
		nc.delivery.Lock()
		nc.closed = true
		nc.closeOnce.Do(func() {
			close(nc.done)
			close(nc.recv)
		})
		nc.delivery.Unlock()
	}
	if remaining == 0 {
		_ = a.client.Close()
	}
	mu.Lock()
	conn.State = StateClosed
	mu.Unlock()
	return nil
}

func (a *NATSAdapter) Health(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := a.client.Ping(ctx); err != nil {
		return errors.Join(ErrTransportUnreachable, err)
	}
	return nil
}

func (nc *natsConnection) deliver(data []byte) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil || msg.Opcode < OpTreeCreate || msg.Opcode > OpAck {
		return
	}
	nc.delivery.RLock()
	defer nc.delivery.RUnlock()
	if nc.closed {
		return
	}
	select {
	case nc.recv <- &msg:
		mu := stateMuFor(nc.conn)
		mu.Lock()
		nc.conn.LastActivity = time.Now().UTC()
		nc.conn.SequenceWatermark = msg.Sequence
		mu.Unlock()
	default:
		// A slow consumer must not block the shared NATS delivery callback.
	}
}

func natsSubject(treeID string, opcode Opcode) (string, error) {
	if !validNATSSubjectToken(treeID) {
		return "", fmt.Errorf("transport: invalid NATS tree ID %q", treeID)
	}
	if opcode < OpTreeCreate || opcode > OpAck {
		return "", ErrUnsupportedOpcode
	}
	return "canopy." + treeID + "." + opcode.String(), nil
}

func validNATSSubjectToken(token string) bool {
	if token == "" || strings.ContainsAny(token, ".*>") {
		return false
	}
	for _, r := range token {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validateNATSURL(target string) error {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "nats" || u.Host == "" {
		return fmt.Errorf("transport: invalid NATS URL %q", target)
	}
	return nil
}

func connectNATSWithBackoff(ctx context.Context, timeout time.Duration, connect func(context.Context) error) error {
	attemptCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	backoff := time.Second
	for {
		err := connect(attemptCtx)
		if err == nil {
			return nil
		}
		if timeout <= 0 {
			return err
		}
		jitter := time.Duration(float64(backoff) * (0.75 + rand.Float64()*0.5))
		timer := time.NewTimer(jitter)
		select {
		case <-attemptCtx.Done():
			timer.Stop()
			return attemptCtx.Err()
		case <-timer.C:
		}
		backoff *= 2
		if backoff > 32*time.Second {
			backoff = 32 * time.Second
		}
	}
}

var _ TransportAdapter = (*NATSAdapter)(nil)
