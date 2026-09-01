package transport

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeNATSBus struct {
	mu            sync.Mutex
	connected     bool
	connectURL    string
	connectBearer string
	lastSubject   string
	subs          map[int]fakeNATSSubscriber
	nextID        int
	pingErr       error
}

type fakeNATSSubscriber struct {
	filter  string
	handler func([]byte)
}

type fakeNATSSubscription struct {
	bus  *fakeNATSBus
	id   int
	once sync.Once
}

func newFakeNATSBus() *fakeNATSBus {
	return &fakeNATSBus{subs: make(map[int]fakeNATSSubscriber)}
}

func (b *fakeNATSBus) Connect(_ context.Context, serverURL, bearer string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.connected = true
	b.connectURL = serverURL
	b.connectBearer = bearer
	return nil
}

func (b *fakeNATSBus) Publish(_ context.Context, subject string, data []byte) error {
	b.mu.Lock()
	if !b.connected {
		b.mu.Unlock()
		return ErrConnectionClosed
	}
	b.lastSubject = subject
	var handlers []func([]byte)
	for _, sub := range b.subs {
		if fakeNATSSubjectMatch(sub.filter, subject) {
			handlers = append(handlers, sub.handler)
		}
	}
	b.mu.Unlock()
	for _, handler := range handlers {
		handler(append([]byte(nil), data...))
	}
	return nil
}

func (b *fakeNATSBus) Subscribe(_ context.Context, subject string, handler func([]byte)) (NATSSubscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	b.subs[b.nextID] = fakeNATSSubscriber{filter: subject, handler: handler}
	return &fakeNATSSubscription{bus: b, id: b.nextID}, nil
}

func (b *fakeNATSBus) Ping(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pingErr != nil {
		return b.pingErr
	}
	if !b.connected {
		return ErrConnectionClosed
	}
	return nil
}

func (b *fakeNATSBus) Close() error {
	b.mu.Lock()
	b.connected = false
	b.mu.Unlock()
	return nil
}

func (s *fakeNATSSubscription) Unsubscribe() error {
	s.once.Do(func() {
		s.bus.mu.Lock()
		delete(s.bus.subs, s.id)
		s.bus.mu.Unlock()
	})
	return nil
}

func fakeNATSSubjectMatch(filter, subject string) bool {
	filterParts := splitSubject(filter)
	subjectParts := splitSubject(subject)
	if len(filterParts) != len(subjectParts) {
		return false
	}
	for i := range filterParts {
		if filterParts[i] != "*" && filterParts[i] != subjectParts[i] {
			return false
		}
	}
	return true
}

func splitSubject(subject string) []string {
	var parts []string
	start := 0
	for i := 0; i <= len(subject); i++ {
		if i == len(subject) || subject[i] == '.' {
			parts = append(parts, subject[start:i])
			start = i + 1
		}
	}
	return parts
}

func TestNATSAdapterLifecycleAndRouting(t *testing.T) {
	bus := newFakeNATSBus()
	adapter := NewNATSAdapter(bus)
	treeID := uuid.NewString()
	conn, err := adapter.Connect(context.Background(), ConnectOptions{
		Target: "nats://127.0.0.1:4222", TransportType: TransportNATS,
		Auth: AuthMaterial{Bearer: "credential"}, Metadata: map[string]string{"tree_id": treeID},
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if conn.State != StateActive {
		t.Fatalf("state = %s, want active", conn.State)
	}
	bus.mu.Lock()
	if bus.connectURL != "nats://127.0.0.1:4222" || bus.connectBearer != "credential" {
		t.Fatalf("connect arguments = (%q, %q)", bus.connectURL, bus.connectBearer)
	}
	bus.mu.Unlock()

	received, err := adapter.Receive(context.Background(), conn)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	msg := &Message{Opcode: OpNodeAdd, TreeID: treeID, Sequence: 42}
	if err := adapter.Send(context.Background(), conn, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	bus.mu.Lock()
	if bus.lastSubject != "canopy."+treeID+".node_add" {
		t.Errorf("subject = %q", bus.lastSubject)
	}
	bus.mu.Unlock()
	select {
	case got := <-received:
		if got.Sequence != 42 || got.TreeID != treeID || got.Opcode != OpNodeAdd {
			t.Fatalf("received = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delivery")
	}
	if conn.SequenceWatermark != 42 {
		t.Fatalf("watermark = %d, want 42", conn.SequenceWatermark)
	}
	if err := adapter.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if err := adapter.Disconnect(context.Background(), conn); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if conn.State != StateClosed {
		t.Fatalf("state = %s, want closed", conn.State)
	}
	if _, open := <-received; open {
		t.Fatal("receive channel remains open")
	}
	bus.mu.Lock()
	remaining := len(bus.subs)
	bus.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("subscriptions = %d, want 0", remaining)
	}
	if err := adapter.Disconnect(context.Background(), conn); err != nil {
		t.Fatalf("second Disconnect: %v", err)
	}
	if err := adapter.Health(context.Background()); !errors.Is(err, ErrTransportUnreachable) {
		t.Fatalf("Health after close = %v, want ErrTransportUnreachable", err)
	}
}

func TestNATSAdapterHealthReportsClientFailure(t *testing.T) {
	bus := newFakeNATSBus()
	adapter := NewNATSAdapter(bus)
	conn, err := adapter.Connect(context.Background(), ConnectOptions{Target: "nats://bus:4222"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = adapter.Disconnect(context.Background(), conn) }()
	bus.mu.Lock()
	bus.pingErr = errors.New("ping failed")
	bus.mu.Unlock()
	if err := adapter.Health(context.Background()); !errors.Is(err, ErrTransportUnreachable) {
		t.Fatalf("Health = %v, want ErrTransportUnreachable", err)
	}
}

func TestNATSSubject(t *testing.T) {
	tests := []struct {
		name    string
		treeID  string
		opcode  Opcode
		want    string
		wantErr error
	}{
		{name: "uuid node add", treeID: "0191a8b2-7fff-7000-9000-000000000201", opcode: OpNodeAdd, want: "canopy.0191a8b2-7fff-7000-9000-000000000201.node_add"},
		{name: "uuid ack", treeID: uuid.NewString(), opcode: OpAck},
		{name: "empty tree", treeID: "", opcode: OpHeartbeat, wantErr: ErrConnectionFailed},
		{name: "dot", treeID: "tree.part", opcode: OpHeartbeat, wantErr: ErrConnectionFailed},
		{name: "wildcard", treeID: "*", opcode: OpHeartbeat, wantErr: ErrConnectionFailed},
		{name: "space", treeID: "tree id", opcode: OpHeartbeat, wantErr: ErrConnectionFailed},
		{name: "bad opcode", treeID: uuid.NewString(), opcode: Opcode(0xff), wantErr: ErrUnsupportedOpcode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := natsSubject(tt.treeID, tt.opcode)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("natsSubject() = %q, nil error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("natsSubject: %v", err)
			}
			want := tt.want
			if want == "" {
				want = "canopy." + tt.treeID + ".ack"
			}
			if got != want {
				t.Fatalf("subject = %q, want %q", got, want)
			}
		})
	}
}

func TestConnectionManagerRegistersNATSAdapter(t *testing.T) {
	manager := NewConnectionManager(nil)
	adapter := NewNATSAdapter(newFakeNATSBus())
	if err := manager.RegisterAdapter(adapter); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}
	manager.mu.RLock()
	got := manager.adapters[TransportNATS]
	manager.mu.RUnlock()
	if got != adapter {
		t.Fatal("NATS adapter was not registered by transport type")
	}
}
