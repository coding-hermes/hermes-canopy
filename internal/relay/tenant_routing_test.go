package relay

import (
	"errors"
	"net"
	"testing"

	"github.com/google/uuid"
)

func routingSession(instance, tenant uuid.UUID) (*RelaySession, net.Conn) {
	server, client := net.Pipe()
	return &RelaySession{ID: uuid.NewString(), InstanceID: instance, TenantID: tenant, Conn: server, done: make(chan struct{})}, client
}

func TestRelayHubTenantRouting(t *testing.T) {
	h := NewRelayHub(DefaultConfig())
	tenantA, tenantB := uuid.New(), uuid.New()
	source, _ := routingSession(uuid.New(), tenantA)
	same, sameClient := routingSession(uuid.New(), tenantA)
	defer sameClient.Close()
	cross, crossClient := routingSession(uuid.New(), tenantB)
	defer crossClient.Close()
	h.sessions[source.ID] = source
	h.sessions[same.ID] = same
	h.sessions[cross.ID] = cross
	payload := append(same.InstanceID[:], byte(1))
	frame := Frame{Type: FrameData, Payload: payload}
	done := make(chan error, 1)
	go func() { _, err := ReadFrame(sameClient); done <- err }()
	if err := h.RouteToInstance(source, same.InstanceID, frame); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := h.RouteToInstance(source, cross.InstanceID, Frame{Type: FrameData, Payload: append(cross.InstanceID[:], byte(1))}); !errors.Is(err, ErrTenantIsolation) {
		t.Fatalf("cross tenant err=%v", err)
	}
}
