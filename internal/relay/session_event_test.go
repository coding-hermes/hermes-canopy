package relay

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

type captureSSEHub struct {
	mu     sync.Mutex
	events []sse.SSEEvent
}

func (*captureSSEHub) Subscribe(context.Context, uuid.UUID, sse.SSEClient) error { return nil }
func (*captureSSEHub) Unsubscribe(uuid.UUID, string)                             {}
func (h *captureSSEHub) Broadcast(_ uuid.UUID, event sse.SSEEvent) sse.SSEEvent {
	h.mu.Lock()
	h.events = append(h.events, event)
	h.mu.Unlock()
	return event
}
func (*captureSSEHub) ReplaySince(context.Context, uuid.UUID, string, string) error { return nil }
func (*captureSSEHub) SubscriberCount(uuid.UUID) int                                { return 0 }
func (*captureSSEHub) TotalConnections() int                                        { return 0 }
func (*captureSSEHub) Shutdown(context.Context) error                               { return nil }

func TestRelaySessionEventsOnOpenAndClose(t *testing.T) {
	h, auth, addr := startTestHub(t, 1)
	events := &captureSSEHub{}
	h.SetSessionEventHub(events)
	conn, _ := dialHello(t, addr, auth)
	_ = conn.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		events.mu.Lock()
		count := len(events.events)
		events.mu.Unlock()
		if count == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	events.mu.Lock()
	defer events.mu.Unlock()
	if len(events.events) != 2 {
		t.Fatalf("session events=%d, want 2", len(events.events))
	}
	for i, want := range []string{"connected", "disconnected"} {
		if events.events[i].Type != "relay_session_event" {
			t.Fatalf("event type=%q", events.events[i].Type)
		}
		var payload struct {
			EventType string `json:"event_type"`
			SessionID string `json:"session_id"`
			Protocol  string `json:"protocol"`
		}
		if err := json.Unmarshal(events.events[i].Data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.EventType != want || payload.SessionID == "" || payload.Protocol != "tcp" {
			t.Fatalf("payload=%+v", payload)
		}
	}
}
