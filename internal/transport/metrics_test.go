package transport

import "testing"

func TestTransportMetricsIncrementAndSnapshotCopy(t *testing.T) {
	m := &TransportMetrics{}
	m.IncConnectAttempt(TransportNATS)
	m.IncConnectSuccess(TransportNATS)
	m.IncMessageSent(TransportNATS)
	m.IncMessageReceived(TransportNATS)
	m.IncReconnect(TransportNATS)
	m.IncRelayEnqueue()
	m.IncRelayPoll()
	m.IncRelayDrop()
	s := m.Snapshot()
	if s.ConnectAttempts[TransportNATS] != 1 || s.ConnectSuccesses[TransportNATS] != 1 || s.MessagesSent[TransportNATS] != 1 || s.MessagesReceived[TransportNATS] != 1 || s.Reconnects[TransportNATS] != 1 {
		t.Fatalf("unexpected snapshot: %+v", s)
	}
	if s.RelayEnqueues != 1 || s.RelayPolls != 1 || s.RelayDrops != 1 || s.LastTransition[TransportNATS].IsZero() {
		t.Fatalf("unexpected relay/timestamp snapshot: %+v", s)
	}
	s.MessagesSent[TransportNATS] = 99
	if m.Snapshot().MessagesSent[TransportNATS] != 1 {
		t.Fatal("snapshot map aliases registry state")
	}
}
