package relay

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

const (
	StatusDisabled = "disabled"
	StatusRunning  = "running"
	StatusDraining = "draining"
)

// RelayTransport is the Phase 2 protocol boundary.
type RelayTransport interface {
	Start(context.Context, DeploymentConfig) error
	StopAccepting() error
	NotifyShutdown() error
	ActiveSessions() int
	DrainDone() <-chan struct{}
	Close() error
}

type noopRelayTransport struct{}

func (noopRelayTransport) Start(context.Context, DeploymentConfig) error { return nil }
func (noopRelayTransport) StopAccepting() error                          { return nil }
func (noopRelayTransport) NotifyShutdown() error                         { return nil }
func (noopRelayTransport) ActiveSessions() int                           { return 0 }
func (noopRelayTransport) DrainDone() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
func (noopRelayTransport) Close() error { return nil }

type clock interface {
	After(time.Duration) <-chan time.Time
}
type realClock struct{}

func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type RelayHealth struct {
	Mode       string `json:"mode"`
	ListenAddr string `json:"listen_addr"`
	Status     string `json:"status"`
	Sessions   int    `json:"sessions"`
}

type RelayService struct {
	mu        sync.RWMutex
	config    DeploymentConfig
	transport RelayTransport
	hub       sse.SSEHub
	clock     clock
	status    string
}

func NewRelayService(cfg DeploymentConfig, transport RelayTransport, hub sse.SSEHub) (*RelayService, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if transport == nil {
		transport = noopRelayTransport{}
	}
	return &RelayService{config: cfg, transport: transport, hub: hub, clock: realClock{}, status: StatusDisabled}, nil
}

func (r *RelayService) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.config.Mode == ModeAirGapped || !r.config.Enabled {
		r.status = StatusDisabled
		return nil
	}
	if r.status == StatusRunning {
		return nil
	}
	if err := r.transport.Start(ctx, r.config); err != nil {
		return err
	}
	r.status = StatusRunning
	r.publishLocked()
	return nil
}

func (r *RelayService) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	if r.status == StatusDisabled {
		r.mu.Unlock()
		return nil
	}
	r.status = StatusDraining
	r.publishLocked()
	r.mu.Unlock()

	err := errors.Join(r.transport.StopAccepting(), r.transport.NotifyShutdown())
	if r.transport.ActiveSessions() > 0 {
		select {
		case <-ctx.Done():
			err = errors.Join(err, ctx.Err())
		case <-r.transport.DrainDone():
		case <-r.clock.After(time.Duration(r.config.DrainTimeoutSecs) * time.Second):
		}
	}
	err = errors.Join(err, r.transport.Close())
	r.mu.Lock()
	r.status = StatusDisabled
	r.publishLocked()
	r.mu.Unlock()
	return err
}

func (r *RelayService) Health() RelayHealth {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sessions := 0
	if r.status != StatusDisabled {
		sessions = r.transport.ActiveSessions()
	}
	return RelayHealth{Mode: r.config.Mode, ListenAddr: r.config.ListenAddr, Status: r.status, Sessions: sessions}
}

func (r *RelayService) publishLocked() {
	if r.hub == nil {
		return
	}
	b, _ := json.Marshal(RelayHealth{Mode: r.config.Mode, ListenAddr: r.config.ListenAddr, Status: r.status, Sessions: r.transport.ActiveSessions()})
	r.hub.Broadcast(uuid.Nil, sse.SSEEvent{Type: "relay_status", Data: b})
}
