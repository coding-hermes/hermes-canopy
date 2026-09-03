package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

const (
	StatusDisabled = "disabled"
	StatusRunning  = "running"
	StatusDraining = "draining"
)

var errCGNATProbeSucceeded = errors.New("relay: outbound probe succeeded")

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
	Mode              string `json:"mode"`
	ListenAddr        string `json:"listen_addr"`
	Status            string `json:"status"`
	Sessions          int    `json:"sessions"`
	HMACKeyID         uint32 `json:"hmac_key_id"`
	Rotations         uint64 `json:"rotations"`
	ClientState       string `json:"client_state,omitempty"`
	LastError         string `json:"last_error,omitempty"`
	Degraded          bool   `json:"degraded"`
	DegradationReason string `json:"degradation_reason,omitempty"`
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
		if cfg.Mode == ModeSelfHosted && cfg.ConnectAddr != "" && cfg.ListenAddr == "" {
			transport = NewRelayClient(cfg)
		} else if cfg.ListenAddr != "" {
			relayHub := NewRelayHub(cfg)
			if len(registries) > 0 && registries[0] != nil {
				relayHub.SetHeartbeatHook(cfg.InstanceID, registries[0].UpdateInstanceHeartbeat)
				relayHub.SetTenantHooks(registries[0].ResolveInstanceTenant, registries[0].OpenSession)
			}
			relayHub.SetSessionEventHub(hub)
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
		fallback := ShouldFallbackToRelayClient(err, r.config)
		if !fallback && r.publicListenConfigured() && r.probeConnectAddr(ctx) {
			fallback = ShouldFallbackToRelayClient(errors.Join(err, errCGNATProbeSucceeded), r.config)
		}
		if !fallback {
			return err
		}
		client := NewRelayClient(r.config)
		r.transport = client
		logCGNATFallback(r.config, err)
		if err := client.Start(ctx, r.config); err != nil {
			return err
		}
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
	health := RelayHealth{Mode: r.config.Mode, ListenAddr: r.config.ListenAddr, Status: r.status, Sessions: sessions, HMACKeyID: uint32(r.config.HMACKeyID), Rotations: r.rotations}
	if client, ok := r.transport.(interface{ ClientHealth() (string, string) }); ok {
		health.ClientState, health.LastError = client.ClientHealth()
	}
	health.Degraded, health.DegradationReason = tlsCertificateHealth(r.config, r.now())
	return health
}

func (r *RelayService) publishLocked() {
	if r.hub == nil {
		return
	}
	health := RelayHealth{Mode: r.config.Mode, ListenAddr: r.config.ListenAddr, Status: r.status, Sessions: r.transport.ActiveSessions(), HMACKeyID: uint32(r.config.HMACKeyID), Rotations: r.rotations}
	if client, ok := r.transport.(interface{ ClientHealth() (string, string) }); ok {
		health.ClientState, health.LastError = client.ClientHealth()
	}
	health.Degraded, health.DegradationReason = tlsCertificateHealth(r.config, r.now())
	b, _ := json.Marshal(health)
	r.hub.Broadcast(uuid.Nil, sse.SSEEvent{Type: "relay_status", Data: b})
}

// ShouldFallbackToRelayClient is the deterministic CGNAT policy seam. Runtime
// probing is represented by errCGNATProbeSucceeded so tests need no sockets.
func ShouldFallbackToRelayClient(listenErr error, cfg DeploymentConfig) bool {
	return cfg.Mode == ModeSelfHosted && cfg.ConnectAddr != "" && cfg.ListenAddr != "" &&
		(errors.Is(listenErr, syscall.EADDRNOTAVAIL) || errors.Is(listenErr, errCGNATProbeSucceeded))
}

func (r *RelayService) probeConnectAddr(ctx context.Context) bool {
	u, err := url.Parse(r.config.ConnectAddr)
	if err != nil || u.Scheme != "tcp" {
		return false
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", u.Host)
	if err == nil {
		_ = conn.Close()
	}
	return err == nil
}

func (r *RelayService) publicListenConfigured() bool {
	u, err := url.Parse(r.config.ListenAddr)
	if err != nil {
		return false
	}
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return host == "" || host == "0.0.0.0" || host == "::" || (ip != nil && !ip.IsLoopback() && !ip.IsPrivate())
}

func logCGNATFallback(cfg DeploymentConfig, err error) {
	log.Info().Str("event", "relay_cgnat_fallback").Str("listen_addr", cfg.ListenAddr).
		Str("connect_addr", cfg.ConnectAddr).Err(err).Msg("relay falling back to outbound client mode")
}
