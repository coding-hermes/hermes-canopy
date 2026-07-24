package transport

import (
	"context"
	"sort"
	"sync"
	"time"
)

// --- DeploymentMode (SPEC-FTR-04 §3.5 / T1.8 §6.2) -------------------------

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

// String returns the deployment mode identifier used in API responses.
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

// --- NetworkTopology (SPEC-FTR-04 §3.5 / T1.8 §6.2) ------------------------

// NetworkTopology describes the node's network position.
type NetworkTopology int

const (
	TopologyLoopback NetworkTopology = iota
	TopologyLAN
	TopologyNAT
	TopologyPublic
	TopologyAirGapped
)

// String returns the topology identifier used in API responses.
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

// --- TransportSelector (SPEC-FTR-04 §3.5) ----------------------------------

// TransportSelector picks the best transport for a deployment mode based on
// the priority matrix defined in T1.8 §6.2 and the spec's §2 Decision 3.
type TransportSelector struct {
	mode      DeploymentMode
	topology  NetworkTopology
	available []TransportType
	fallbacks map[TransportType]TransportType
	mu        sync.RWMutex
}

// NewTransportSelector constructs a selector for the given deployment mode.
// The priority matrix and fallback chains are compiled at construction time;
// DetectTopology can refine the ordering at startup.
func NewTransportSelector(mode DeploymentMode) *TransportSelector {
	ts := &TransportSelector{
		mode:      mode,
		fallbacks: make(map[TransportType]TransportType),
	}
	ts.applyPriorityMatrix()
	return ts
}

// applyPriorityMatrix populates available + fallbacks based on the
// deployment mode (SPEC-FTR-04 §2 Decision 3, T1.8 §6.2).
//
//	Local        → SSE
//	LAN          → SSE
//	SelfHosted   → SSE
//	SaaS         → SSE, NATS
//	P2P          → WebRTC, SSE
//	Federated    → NATS, SSE
//	AirGapped    → Relay
func (ts *TransportSelector) applyPriorityMatrix() {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	ts.available = ts.available[:0]
	// Clear existing fallbacks.
	ts.fallbacks = make(map[TransportType]TransportType)

	switch ts.mode {
	case ModeLocal:
		ts.available = []TransportType{TransportSSE}
	case ModeLAN:
		ts.available = []TransportType{TransportSSE}
	case ModeSelfHosted:
		ts.available = []TransportType{TransportSSE}
	case ModeSaaS:
		ts.available = []TransportType{TransportSSE, TransportNATS}
		ts.fallbacks[TransportSSE] = TransportNATS
	case ModeP2P:
		ts.available = []TransportType{TransportWebRTC, TransportSSE}
		ts.fallbacks[TransportWebRTC] = TransportSSE
	case ModeFederated:
		ts.available = []TransportType{TransportNATS, TransportSSE}
		ts.fallbacks[TransportNATS] = TransportSSE
	case ModeAirGapped:
		ts.available = []TransportType{TransportRelay}
	}
}

// Mode returns the deployment mode.
func (ts *TransportSelector) Mode() DeploymentMode {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.mode
}

// Topology returns the detected network topology.
func (ts *TransportSelector) Topology() NetworkTopology {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.topology
}

// Available returns the ordered list of transports available for this
// deployment mode (primary first).
func (ts *TransportSelector) Available() []TransportType {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := make([]TransportType, len(ts.available))
	copy(out, ts.available)
	return out
}

// SelectPrimary returns the primary transport for a peer. In MVP, the
// primary is always the first entry in the priority matrix for the
// deployment mode. peerID is accepted for future per-peer selection logic.
func (ts *TransportSelector) SelectPrimary(peerID string) TransportType {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	if len(ts.available) == 0 {
		return TransportSSE // safe default
	}
	return ts.available[0]
}

// SelectFallback returns the next transport to try after current fails.
// Returns ErrTransportUnreachable if there is no further fallback.
func (ts *TransportSelector) SelectFallback(current TransportType) (TransportType, error) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	// Walk the fallback chain.
	next, ok := ts.fallbacks[current]
	if !ok {
		return "", ErrTransportUnreachable
	}
	return next, nil
}

// DetectTopology performs a one-shot network probe to determine the
// node's topology. For MVP, this is a heuristic: if the target is a
// loopback address, assume loopback; otherwise assume NAT (the safest
// general assumption behind a home/office router).
//
// This method does not change the available transports list — only the
// topology metadata used for reporting. The priority matrix is fixed at
// construction time per Decision 3.
func (ts *TransportSelector) DetectTopology() NetworkTopology {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// MVP heuristic: local mode → loopback, everything else → NAT.
	topology := TopologyNAT
	if ts.mode == ModeLocal {
		topology = TopologyLoopback
	}
	ts.topology = topology
	return topology
}

// SetTopology overrides the detected topology (e.g. from config).
func (ts *TransportSelector) SetTopology(t NetworkTopology) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.topology = t
}

// --- negotiateCapabilities (SPEC-FTR-04 §3.5, §7 Edge Case 12) --------------

// negotiateCapabilities computes the intersection of local and remote
// transport capability flags. If the intersection is empty, the caller
// should reject the connection with ErrTransportMismatch.
func negotiateCapabilities(local, remote []string) []string {
	if len(local) == 0 || len(remote) == 0 {
		return nil
	}
	remoteSet := make(map[string]struct{}, len(remote))
	for _, c := range remote {
		remoteSet[c] = struct{}{}
	}
	var out []string
	for _, c := range local {
		if _, ok := remoteSet[c]; ok {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// --- context guard (keeps "context" import used even if future ---------------
// changes remove the only call site) -----------------------------------------
var _ context.Context = (*cancelCtx)(nil)

type cancelCtx struct{}

func (*cancelCtx) Deadline() (time.Time, bool)   { return time.Time{}, false }
func (*cancelCtx) Done() <-chan struct{}          { return nil }
func (*cancelCtx) Err() error                     { return context.Canceled }
func (*cancelCtx) Value(key any) any              { return nil }
