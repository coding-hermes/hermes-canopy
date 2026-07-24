package transport

import (
	"sort"
	"sync"
)

// DeploymentMode enumerates the seven Canopy deployment modes.
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

// String returns the stable deployment mode identifier.
func (m DeploymentMode) String() string {
	switch m {
	case ModeLocal:
		return "local"
	case ModeLAN:
		return "lan"
	case ModeSelfHosted:
		return "self_hosted"
	case ModeSaaS:
		return "saas"
	case ModeP2P:
		return "p2p"
	case ModeFederated:
		return "federated"
	case ModeAirGapped:
		return "air_gapped"
	default:
		return "unknown"
	}
}

// NetworkTopology describes the node's network position.
type NetworkTopology int

const (
	TopologyLoopback NetworkTopology = iota
	TopologyLAN
	TopologyNAT
	TopologyPublic
	TopologyAirGapped
)

// String returns the stable topology identifier.
func (t NetworkTopology) String() string {
	switch t {
	case TopologyLoopback:
		return "loopback"
	case TopologyLAN:
		return "lan"
	case TopologyNAT:
		return "nat"
	case TopologyPublic:
		return "public"
	case TopologyAirGapped:
		return "air_gapped"
	default:
		return "unknown"
	}
}

// TransportSelector picks the best transport for a deployment mode.
type TransportSelector struct {
	mode      DeploymentMode
	topology  NetworkTopology
	available []TransportType
	fallbacks map[TransportType]TransportType
	mu        sync.RWMutex
}

// NewTransportSelector creates a selector based on the deployment mode and
// network topology. It pre-populates the ordered fallback chain.
func NewTransportSelector(mode DeploymentMode, topology NetworkTopology) *TransportSelector {
	selector := &TransportSelector{
		mode:      mode,
		topology:  topology,
		fallbacks: make(map[TransportType]TransportType),
	}
	selector.applyPriorityMatrix()
	return selector
}

func (ts *TransportSelector) applyPriorityMatrix() {
	switch ts.mode {
	case ModeLocal, ModeLAN, ModeSelfHosted:
		ts.available = []TransportType{TransportSSE}
	case ModeSaaS:
		ts.available = []TransportType{TransportSSE, TransportNATS, TransportRelay}
	case ModeP2P:
		ts.available = []TransportType{TransportWebRTC, TransportSSE, TransportRelay}
	case ModeFederated:
		ts.available = []TransportType{TransportNATS, TransportRedis, TransportWebRTC}
	case ModeAirGapped:
		ts.available = []TransportType{TransportRelay}
	default:
		ts.available = []TransportType{TransportSSE}
	}
	for i := 0; i+1 < len(ts.available); i++ {
		ts.fallbacks[ts.available[i]] = ts.available[i+1]
	}
}

// SelectPrimary returns the best transport for a given peer. For MVP, SSE is
// always selected because it is the only implemented transport adapter.
func (ts *TransportSelector) SelectPrimary(peerID string) TransportType {
	_ = peerID
	return TransportSSE
}

// SelectFallback returns the next transport in the fallback chain.
func (ts *TransportSelector) SelectFallback(current TransportType) (TransportType, error) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	next, ok := ts.fallbacks[current]
	if !ok {
		return "", ErrNoTransportAvailable
	}
	return next, nil
}

// DetectTopology probes the network. MVP returns TopologyLoopback.
func (ts *TransportSelector) DetectTopology() NetworkTopology {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.topology = TopologyLoopback
	return ts.topology
}

// Mode returns the configured deployment mode.
func (ts *TransportSelector) Mode() DeploymentMode {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.mode
}

// Topology returns the current topology.
func (ts *TransportSelector) Topology() NetworkTopology {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.topology
}

// Available returns the ordered transport priority list.
func (ts *TransportSelector) Available() []TransportType {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	result := make([]TransportType, len(ts.available))
	copy(result, ts.available)
	return result
}

// SetTopology overrides the detected topology.
func (ts *TransportSelector) SetTopology(topology NetworkTopology) {
	ts.mu.Lock()
	ts.topology = topology
	ts.mu.Unlock()
}

// negotiateCapabilities computes the intersection of local and remote
// capabilities. The order is deterministic and sorted lexicographically.
func negotiateCapabilities(local, remote []string) []string {
	if len(local) == 0 || len(remote) == 0 {
		return nil
	}
	remoteSet := make(map[string]struct{}, len(remote))
	for _, capability := range remote {
		remoteSet[capability] = struct{}{}
	}

	seen := make(map[string]struct{}, len(local))
	result := make([]string, 0, len(local))
	for _, capability := range local {
		if _, ok := seen[capability]; ok {
			continue
		}
		if _, ok := remoteSet[capability]; ok {
			seen[capability] = struct{}{}
			result = append(result, capability)
		}
	}
	sort.Strings(result)
	return result
}
