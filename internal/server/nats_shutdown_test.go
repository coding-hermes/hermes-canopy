package server

import (
	"context"
	"net/http"
	"testing"
)

func TestShutdownDrainsOptionalTransport(t *testing.T) {
	calls := 0
	s := &Server{httpServer: &http.Server{}}
	s.SetTransportDrain(func() error { calls++; return nil })
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("drain calls = %d, want 1", calls)
	}
}
