package transport

import (
	"sync/atomic"
	"time"
)

const transportTypeCount = 5

// TransportMetrics is the process-wide, lock-free transport metrics registry.
// Endpoints and credentials are intentionally not retained.
type TransportMetrics struct {
	connectAttempts  [transportTypeCount]atomic.Int64
	connectSuccesses [transportTypeCount]atomic.Int64
	connectFailures  [transportTypeCount]atomic.Int64
	messagesSent     [transportTypeCount]atomic.Int64
	messagesReceived [transportTypeCount]atomic.Int64
	reconnects       [transportTypeCount]atomic.Int64
	lastTransition   [transportTypeCount]atomic.Int64
	relayEnqueues    atomic.Int64
	relayPolls       atomic.Int64
	relayDrops       atomic.Int64
}

type TransportMetricSnapshot struct {
	ConnectAttempts  map[TransportType]int64     `json:"connect_attempts"`
	ConnectSuccesses map[TransportType]int64     `json:"connect_successes"`
	ConnectFailures  map[TransportType]int64     `json:"connect_failures"`
	MessagesSent     map[TransportType]int64     `json:"messages_sent"`
	MessagesReceived map[TransportType]int64     `json:"messages_received"`
	Reconnects       map[TransportType]int64     `json:"reconnects"`
	LastTransition   map[TransportType]time.Time `json:"last_transition"`
	RelayEnqueues    int64                       `json:"relay_enqueues"`
	RelayPolls       int64                       `json:"relay_polls"`
	RelayDrops       int64                       `json:"relay_drops"`
}

var transportMetrics = &TransportMetrics{}

func Metrics() *TransportMetrics { return transportMetrics }

func transportIndex(tt TransportType) int {
	switch tt {
	case TransportSSE:
		return 0
	case TransportWebRTC:
		return 1
	case TransportNATS:
		return 2
	case TransportRedis:
		return 3
	case TransportRelay:
		return 4
	default:
		return -1
	}
}

func (m *TransportMetrics) transition(tt TransportType) {
	if i := transportIndex(tt); i >= 0 {
		m.lastTransition[i].Store(time.Now().UTC().UnixNano())
	}
}
func (m *TransportMetrics) IncConnectAttempt(tt TransportType) {
	if i := transportIndex(tt); i >= 0 {
		m.connectAttempts[i].Add(1)
		m.transition(tt)
	}
}
func (m *TransportMetrics) IncConnectSuccess(tt TransportType) {
	if i := transportIndex(tt); i >= 0 {
		m.connectSuccesses[i].Add(1)
		m.transition(tt)
	}
}
func (m *TransportMetrics) IncConnectFailure(tt TransportType) {
	if i := transportIndex(tt); i >= 0 {
		m.connectFailures[i].Add(1)
		m.transition(tt)
	}
}
func (m *TransportMetrics) IncMessageSent(tt TransportType) {
	if i := transportIndex(tt); i >= 0 {
		m.messagesSent[i].Add(1)
	}
}
func (m *TransportMetrics) IncMessageReceived(tt TransportType) {
	if i := transportIndex(tt); i >= 0 {
		m.messagesReceived[i].Add(1)
	}
}
func (m *TransportMetrics) IncReconnect(tt TransportType) {
	if i := transportIndex(tt); i >= 0 {
		m.reconnects[i].Add(1)
		m.transition(tt)
	}
}
func (m *TransportMetrics) RecordTransition(tt TransportType) { m.transition(tt) }
func (m *TransportMetrics) IncRelayEnqueue()                  { m.relayEnqueues.Add(1) }
func (m *TransportMetrics) IncRelayPoll()                     { m.relayPolls.Add(1) }
func (m *TransportMetrics) IncRelayDrop()                     { m.relayDrops.Add(1) }

func (m *TransportMetrics) Snapshot() TransportMetricSnapshot {
	s := TransportMetricSnapshot{
		ConnectAttempts: map[TransportType]int64{}, ConnectSuccesses: map[TransportType]int64{},
		ConnectFailures: map[TransportType]int64{}, MessagesSent: map[TransportType]int64{},
		MessagesReceived: map[TransportType]int64{}, Reconnects: map[TransportType]int64{},
		LastTransition: map[TransportType]time.Time{}, RelayEnqueues: m.relayEnqueues.Load(),
		RelayPolls: m.relayPolls.Load(), RelayDrops: m.relayDrops.Load(),
	}
	for _, tt := range AllTransportTypes() {
		i := transportIndex(tt)
		s.ConnectAttempts[tt] = m.connectAttempts[i].Load()
		s.ConnectSuccesses[tt] = m.connectSuccesses[i].Load()
		s.ConnectFailures[tt] = m.connectFailures[i].Load()
		s.MessagesSent[tt] = m.messagesSent[i].Load()
		s.MessagesReceived[tt] = m.messagesReceived[i].Load()
		s.Reconnects[tt] = m.reconnects[i].Load()
		if ns := m.lastTransition[i].Load(); ns != 0 {
			s.LastTransition[tt] = time.Unix(0, ns).UTC()
		}
	}
	return s
}
