package federation

import (
	"sync"
	"time"
)

// FederationMetrics holds in-process counters for the P7 observability
// deliverable. Deliberately stdlib-only (no Prometheus dependency): state is
// exposed via GET /api/v1/federation/health and structured logs, matching the
// repo's zero-new-dependency convention.
type FederationMetrics struct {
	mu sync.Mutex

	// Counters (monotonic).
	handshakesTotal     int64
	eventsAcceptedTotal int64
	eventsRejectedTotal int64 // rate-limited or invalid inbound events
	eventsRelayedTotal  int64 // outbound frames successfully sent
	replaysTotal        int64
	conflictsTotal      int64
	conflictsResolved   int64
	rateLimitedTotal    int64

	// Last-event timestamps (zero = never).
	lastInboundEvent  time.Time
	lastOutboundEvent time.Time
	lastError         time.Time
}

var metrics = &FederationMetrics{}

// MetricsSnapshot is a copy-safe point-in-time view of federation metrics.
type MetricsSnapshot struct {
	HandshakesTotal     int64     `json:"handshakes_total"`
	EventsAcceptedTotal int64     `json:"events_accepted_total"`
	EventsRejectedTotal int64     `json:"events_rejected_total"`
	EventsRelayedTotal  int64     `json:"events_relayed_total"`
	ReplaysTotal        int64     `json:"replays_total"`
	ConflictsTotal      int64     `json:"conflicts_total"`
	ConflictsResolved   int64     `json:"conflicts_resolved_total"`
	RateLimitedTotal    int64     `json:"rate_limited_total"`
	LastInboundEvent    time.Time `json:"last_inbound_event,omitempty"`
	LastOutboundEvent   time.Time `json:"last_outbound_event,omitempty"`
	LastError           time.Time `json:"last_error,omitempty"`
}

// Metrics returns the process-wide federation metrics registry.
func Metrics() *FederationMetrics { return metrics }

// IncHandshake records an inbound handshake attempt.
func (m *FederationMetrics) IncHandshake() {
	m.mu.Lock()
	m.handshakesTotal++
	m.lastInboundEvent = time.Now()
	m.mu.Unlock()
}

// IncEventAccepted records an accepted inbound federation event.
func (m *FederationMetrics) IncEventAccepted() {
	m.mu.Lock()
	m.eventsAcceptedTotal++
	m.lastInboundEvent = time.Now()
	m.mu.Unlock()
}

// IncEventRejected records a rejected inbound event (invalid, bad signature).
func (m *FederationMetrics) IncEventRejected() {
	m.mu.Lock()
	m.eventsRejectedTotal++
	m.mu.Unlock()
}

// IncRelayed records a successfully sent outbound event frame.
func (m *FederationMetrics) IncRelayed() {
	m.mu.Lock()
	m.eventsRelayedTotal++
	m.lastOutboundEvent = time.Now()
	m.mu.Unlock()
}

// IncReplay records a replay batch served to a reconnecting peer.
func (m *FederationMetrics) IncReplay() {
	m.mu.Lock()
	m.replaysTotal++
	m.mu.Unlock()
}

// IncConflict records a newly detected concurrent-write conflict.
func (m *FederationMetrics) IncConflict() {
	m.mu.Lock()
	m.conflictsTotal++
	m.mu.Unlock()
}

// IncConflictResolved records a conflict resolution (manual or default).
func (m *FederationMetrics) IncConflictResolved() {
	m.mu.Lock()
	m.conflictsResolved++
	m.mu.Unlock()
}

// IncRateLimited records a request rejected by the per-peer rate limiter.
func (m *FederationMetrics) IncRateLimited() {
	m.mu.Lock()
	m.rateLimitedTotal++
	m.mu.Unlock()
}

// RecordError stamps the time of the most recent federation error.
func (m *FederationMetrics) RecordError() {
	m.mu.Lock()
	m.lastError = time.Now()
	m.mu.Unlock()
}

// Snapshot returns a consistent copy-safe view of all counters.
func (m *FederationMetrics) Snapshot() MetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return MetricsSnapshot{
		HandshakesTotal:     m.handshakesTotal,
		EventsAcceptedTotal: m.eventsAcceptedTotal,
		EventsRejectedTotal: m.eventsRejectedTotal,
		EventsRelayedTotal:  m.eventsRelayedTotal,
		ReplaysTotal:        m.replaysTotal,
		ConflictsTotal:      m.conflictsTotal,
		ConflictsResolved:   m.conflictsResolved,
		RateLimitedTotal:    m.rateLimitedTotal,
		LastInboundEvent:    m.lastInboundEvent,
		LastOutboundEvent:   m.lastOutboundEvent,
		LastError:           m.lastError,
	}
}
