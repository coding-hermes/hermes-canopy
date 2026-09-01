package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

func TestSSEAdapterSingleTransportLifecycle(t *testing.T) {
	hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1})
	defer func() { _ = hub.Shutdown(context.Background()) }()
	adapter := NewSSEAdapter(hub)
	treeID := uuid.NewString()
	conn, err := adapter.Connect(context.Background(), ConnectOptions{Target: treeID, TransportType: TransportSSE})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if conn.State != StateActive {
		t.Fatalf("state = %s, want active", conn.State)
	}
	received, err := adapter.Receive(context.Background(), conn)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	for seq := uint64(1); seq <= 100; seq++ {
		if err := adapter.Send(context.Background(), conn, &Message{Opcode: OpNodeAdd, TreeID: treeID, Sequence: seq}); err != nil {
			t.Fatalf("Send(%d): %v", seq, err)
		}
	}
	for want := uint64(1); want <= 100; want++ {
		select {
		case msg := <-received:
			if msg.Sequence != want {
				t.Fatalf("sequence = %d, want %d", msg.Sequence, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for sequence %d", want)
		}
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
}

func TestSSEAdapterReconnectsWithLastEventID(t *testing.T) {
	var requests atomic.Int32
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		request := requests.Add(1)
		start, end := 1, 25
		if request > 1 {
			if got := r.Header.Get("Last-Event-ID"); got != "25" {
				t.Errorf("Last-Event-ID = %q, want 25", got)
			}
			start, end = 26, 50
		}
		for seq := start; seq <= end; seq++ {
			data, _ := json.Marshal(Message{Opcode: OpNodeAdd, TreeID: uuid.Nil.String(), Sequence: uint64(seq)})
			_, _ = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", strconv.Itoa(seq), data)
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	adapter := NewSSEAdapter(nil)
	conn, err := adapter.Connect(context.Background(), ConnectOptions{Target: server.URL, TransportType: TransportSSE})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	received, err := adapter.Receive(context.Background(), conn)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	for want := uint64(1); want <= 50; want++ {
		select {
		case msg := <-received:
			if msg.Sequence != want {
				t.Fatalf("sequence = %d, want %d", msg.Sequence, want)
			}
		case <-time.After(4 * time.Second):
			t.Fatalf("timed out waiting for sequence %d", want)
		}
	}
	if requests.Load() < 2 {
		t.Fatalf("requests = %d, want at least 2", requests.Load())
	}
	_ = adapter.Disconnect(context.Background(), conn)
}

func TestConnectionManagerSSERoutingAndOfflineBuffer(t *testing.T) {
	hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1})
	defer func() { _ = hub.Shutdown(context.Background()) }()
	adapter := NewSSEAdapter(hub)
	manager := NewConnectionManager(nil, adapter)
	if err := manager.RouteMessage(context.Background(), "offline", &Message{Opcode: OpHeartbeat, Sequence: 1}); err != nil {
		t.Fatalf("offline RouteMessage: %v", err)
	}
	if got := manager.DrainQueue("offline"); len(got) != 1 {
		t.Fatalf("offline queue length = %d, want 1", len(got))
	}

	treeID := uuid.NewString()
	conn, err := adapter.Connect(context.Background(), ConnectOptions{Target: treeID})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = adapter.Disconnect(context.Background(), conn) }()
	if err := manager.OnConnect(conn); err != nil {
		t.Fatalf("OnConnect: %v", err)
	}
	received, _ := adapter.Receive(context.Background(), conn)
	msg := &Message{Opcode: OpHeartbeat, TreeID: treeID, Sequence: 2}
	if err := manager.RouteMessage(context.Background(), treeID, msg); err != nil {
		t.Fatalf("online RouteMessage: %v", err)
	}
	select {
	case got := <-received:
		if got.Sequence != 2 {
			t.Fatalf("sequence = %d, want 2", got.Sequence)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for routed message")
	}
}
