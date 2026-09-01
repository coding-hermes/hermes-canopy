package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

func TestTransportHealthHandlerAuthenticatedShape(t *testing.T) {
	const secret = "transport-health-secret"
	selector := transport.NewTransportSelector(transport.ModeAirGapped, transport.TopologyAirGapped)
	relay := transport.NewRelayService()
	manager := transport.NewConnectionManager(selector, relay)
	h := NewTransportHandler(nil, manager, nil, nil, "node-test")
	r := chi.NewRouter()
	r.With(AuthMiddleware(secret)).Get("/api/v1/transports/health", h.Health)

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": uuid.NewString(), "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/transports/health", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		CurrentTransport string `json:"current_transport"`
		Transports       []struct {
			Type             string `json:"type"`
			State            string `json:"state"`
			MessagesSent     int64  `json:"messages_sent"`
			MessagesReceived int64  `json:"messages_received"`
			QueueDepth       int    `json:"queue_depth"`
		} `json:"transports"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CurrentTransport != "relay" || len(body.Transports) != len(transport.AllTransportTypes()) {
		t.Fatalf("unexpected body: %+v", body)
	}
	for _, item := range body.Transports {
		if item.Type == "" || item.State == "" {
			t.Fatalf("invalid item: %+v", item)
		}
	}
}
