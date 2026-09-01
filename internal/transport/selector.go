package transport

import (
	"sync"
	"time"
)

type DeploymentMode int

const (
	ModeLocal DeploymentMode = iota
	ModeLAN
	ModeSelfHosted
	ModeSaaS
	ModeP2P
	ModeFederated
	ModeAirGapped
)

func (m DeploymentMode) String() string {
	if int(m) < 0 || int(m) > int(ModeAirGapped) {
		return "unknown"
	}
	return [...]string{"local", "lan", "self_hosted", "saas", "p2p", "federated", "air_gapped"}[m]
}

type NetworkTopology int

const (
	TopologyLoopback NetworkTopology = iota
	TopologyLAN
	TopologyNAT
	TopologyPublic
	TopologyAirGapped
)

func (t NetworkTopology) String() string {
	if int(t) < 0 || int(t) > int(TopologyAirGapped) {
		return "unknown"
	}
	return [...]string{"loopback", "lan", "nat", "public", "air_gapped"}[t]
}

type TransportConfig struct {
	TransportType TransportType
	Enabled       bool
	Priority      int
}

type TransportSelector struct {
	mode                       DeploymentMode
	topology                   NetworkTopology
	adapters                   map[TransportType]TransportAdapter
	configs                    map[TransportType]TransportConfig
	available                  []TransportType
	fallbacks                  map[TransportType]TransportType
	healthInterval             time.Duration
	upThreshold, downThreshold int
	health                     map[TransportType]bool
	emit                       func(TransportEvent)
	mu                         sync.RWMutex
}

func NewTransportSelector(mode DeploymentMode, topology NetworkTopology) *TransportSelector {
	ts := &TransportSelector{mode: mode, topology: topology, adapters: map[TransportType]TransportAdapter{}, configs: map[TransportType]TransportConfig{}, fallbacks: map[TransportType]TransportType{}, health: map[TransportType]bool{}, healthInterval: 20 * time.Second, upThreshold: 3, downThreshold: 3}
	ts.applyPriorityMatrix()
	return ts
}
func (ts *TransportSelector) applyPriorityMatrix() {
	ts.available = map[DeploymentMode][]TransportType{
		ModeLocal: {TransportSSE}, ModeLAN: {TransportSSE, TransportWebRTC}, ModeSelfHosted: {TransportSSE, TransportRedis, TransportRelay},
		ModeSaaS: {TransportSSE, TransportNATS, TransportRelay}, ModeP2P: {TransportWebRTC, TransportSSE, TransportRelay},
		ModeFederated: {TransportNATS, TransportRedis, TransportWebRTC, TransportRelay}, ModeAirGapped: {TransportRelay},
	}[ts.mode]
	if len(ts.available) == 0 {
		ts.available = []TransportType{TransportSSE}
	}
	for i := 0; i+1 < len(ts.available); i++ {
		ts.fallbacks[ts.available[i]] = ts.available[i+1]
	}
}
func (ts *TransportSelector) RegisterAdapter(a TransportAdapter) error {
	typed, ok := a.(interface{ TransportType() TransportType })
	if !ok || a == nil {
		return ErrTransportMismatch
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	tt := typed.TransportType()
	ts.adapters[tt] = a
	ts.health[tt] = true
	return nil
}
func (ts *TransportSelector) SetConfigs(configs []TransportConfig) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.configs = map[TransportType]TransportConfig{}
	for _, c := range configs {
		ts.configs[c.TransportType] = c
	}
}
func (ts *TransportSelector) Select(treeID string) (TransportType, error) {
	_ = treeID
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	for _, tt := range ts.available {
		cfg, ok := ts.configs[tt]
		if ok && !cfg.Enabled {
			continue
		}
		if ts.adapters[tt] != nil && ts.health[tt] {
			return tt, nil
		}
	}
	return "", ErrNoTransport
}

// SelectPrimary is retained for Phase 1 callers that default to SSE. New code
// should use Select, which applies registration and health state.
func (ts *TransportSelector) SelectPrimary(peerID string) TransportType {
	_ = peerID
	return TransportSSE
}
func (ts *TransportSelector) SelectFallback(current TransportType) (TransportType, error) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	seen := false
	for _, tt := range ts.available {
		if !seen {
			seen = tt == current
			continue
		}
		return tt, nil
	}
	return "", ErrNoTransport
}
func (ts *TransportSelector) setHealthy(tt TransportType, healthy bool) {
	ts.mu.Lock()
	ts.health[tt] = healthy
	ts.mu.Unlock()
}
func (ts *TransportSelector) IsHealthy(tt TransportType) bool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.health[tt]
}
func (ts *TransportSelector) HealthSettings(interval time.Duration, up, down int) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if interval > 0 {
		ts.healthInterval = interval
	}
	if up > 0 {
		ts.upThreshold = up
	}
	if down > 0 {
		ts.downThreshold = down
	}
}
func (ts *TransportSelector) DetectTopology() NetworkTopology {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.topology = TopologyLoopback
	return ts.topology
}
func (ts *TransportSelector) Mode() DeploymentMode { return ts.mode }
func (ts *TransportSelector) Topology() NetworkTopology {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.topology
}
func (ts *TransportSelector) Available() []TransportType {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return append([]TransportType(nil), ts.available...)
}
func (ts *TransportSelector) SetTopology(t NetworkTopology) {
	ts.mu.Lock()
	ts.topology = t
	ts.mu.Unlock()
}
func (ts *TransportSelector) SetEventHandler(emit func(TransportEvent)) {
	ts.mu.Lock()
	ts.emit = emit
	ts.mu.Unlock()
}
func (ts *TransportSelector) MarkDegraded(tt TransportType, reason string) {
	ts.mu.Lock()
	changed := ts.health[tt]
	ts.health[tt] = false
	emit := ts.emit
	chain := append([]TransportType(nil), ts.available...)
	ts.mu.Unlock()
	if changed && emit != nil {
		emit(TransportEvent{Type: tt, Event: "transport_degradation", Degraded: true, Reason: reason, FallbackChain: chain})
	}
}
