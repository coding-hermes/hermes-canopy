package transport_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/coding-hermes/hermes-canopy/internal/handler"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

func relayEnvelope(id string, seq uint64) *transport.RelayEnvelope {
	return &transport.RelayEnvelope{EventID: id, PeerID: "peer-b", Message: &transport.Message{Opcode: transport.OpNodeAdd, TreeID: "tree", Sequence: seq, Payload: json.RawMessage(`{}`)}}
}

func TestRelayEnqueuePollIdempotent(t *testing.T) {
	s := transport.NewRelayService()
	if err := s.Enqueue(relayEnvelope("event-1", 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(relayEnvelope("event-1", 1)); err != nil {
		t.Fatal(err)
	}
	got := s.Poll("peer-b")
	if len(got) != 1 || got[0].Message.Sequence != 1 {
		t.Fatalf("poll = %#v", got)
	}
	if got := s.Poll("peer-b"); len(got) != 0 {
		t.Fatalf("second poll len = %d", len(got))
	}
}

func TestRelayBoundedDropsOldest(t *testing.T) {
	s := transport.NewRelayService(2)
	for i := uint64(1); i <= 3; i++ {
		if err := s.Enqueue(relayEnvelope(string(rune('a'+i)), i)); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Poll("peer-b")
	if len(got) != 2 || got[0].Message.Sequence != 2 || got[1].Message.Sequence != 3 {
		t.Fatalf("poll = %#v", got)
	}
}

func TestRelayHTTPRequiresJWT(t *testing.T) {
	s := transport.NewRelayService()
	r := chi.NewRouter()
	r.Use(handler.AuthMiddleware("secret"))
	r.Post("/api/v1/transport/relay", s.Post)
	body, _ := json.Marshal(relayEnvelope("event", 1))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/transport/relay", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}
