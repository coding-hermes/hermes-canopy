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
	HMACKeyID  uint32 `json:"hmac_key_id"`
	Rotations  uint64 `json:"rotations"`
}

type RelayService struct {
	mu              sync.RWMutex
	rotationMu      sync.Mutex
	config          DeploymentConfig
	transport       RelayTransport
	hub             sse.SSEHub
	clock           clock
	now             func() time.Time
	status          string
	keys            map[uint16]authenticationKey
	rotations       uint64
	rotationStop    chan struct{}
	rotationDone    chan struct{}
	persistRotation func(context.Context, time.Time, uint32) error
}

func NewRelayService(cfg DeploymentConfig, transport RelayTransport, hub sse.SSEHub, registries ...*RelayRegistry) (*RelayService, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if transport == nil {
		if cfg.ListenAddr != "" {
			relayHub := NewRelayHub(cfg)
			if len(registries) > 0 && registries[0] != nil {
				relayHub.SetHeartbeatHook(cfg.InstanceID, registries[0].UpdateInstanceHeartbeat)
				relayHub.SetTenantHooks(registries[0].ResolveInstanceTenant, registries[0].OpenSession)
			}
			transport = relayHub
		} else {
			transport = noopRelayTransport{}
		}
	}
	keys := map[uint16]authenticationKey{uint16(cfg.HMACKeyID): {key: append([]byte(nil), cfg.HMACKey...)}}
	if len(cfg.HMACKeyPrev) > 0 {
		entry := authenticationKey{key: append([]byte(nil), cfg.HMACKeyPrev...)}
		if cfg.HMACKeyRotatedAt != nil {
			entry.expiresAt = cfg.HMACKeyRotatedAt.Add(hmacKeyGracePeriod)
		}
		keys[uint16(cfg.HMACKeyPrevID)] = entry
	}
	return &RelayService{config: cfg, transport: transport, hub: hub, clock: realClock{}, now: time.Now, status: StatusDisabled, keys: keys}, nil
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
	r.startRotationLoopLocked()
	return nil
}

func (r *RelayService) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	if r.status == StatusDisabled {
		r.mu.Unlock()
		return nil
	}
	r.status = StatusDraining
	if r.rotationStop != nil {
		close(r.rotationStop)
		r.rotationStop = nil
	}
	rotationDone := r.rotationDone
	r.publishLocked()
	r.mu.Unlock()
	if rotationDone != nil {
		select {
		case <-rotationDone:
		case <-ctx.Done():
		}
	}

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
	return RelayHealth{Mode: r.config.Mode, ListenAddr: r.config.ListenAddr, Status: r.status, Sessions: sessions, HMACKeyID: uint32(r.config.HMACKeyID), Rotations: r.rotations}
}

func (r *RelayService) publishLocked() {
	if r.hub == nil {
		return
	}
	b, _ := json.Marshal(RelayHealth{Mode: r.config.Mode, ListenAddr: r.config.ListenAddr, Status: r.status, Sessions: r.transport.ActiveSessions(), HMACKeyID: uint32(r.config.HMACKeyID), Rotations: r.rotations})
	r.hub.Broadcast(uuid.Nil, sse.SSEEvent{Type: "relay_status", Data: b})
}
