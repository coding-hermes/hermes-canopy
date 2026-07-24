package transport

import (
	"encoding/json"
	"time"
)

// --- SSE event helpers (SPEC-FTR-04 §5) -------------------------------------
//
// These functions build the JSON payloads for the three transport-layer SSE
// event types: transport_status, transport_error, transport_degradation.
// The event "type" string and the JSON "data" object are returned separately
// so callers can pass them directly to sse.ComposeEvent or sse.SSEEvent.

// --- transport_status (SPEC-FTR-04 §5.1) ------------------------------------

// TransportStatusData is the data payload for the transport_status SSE event.
type TransportStatusData struct {
	TransportType     TransportType `json:"transport_type"`
	State             string        `json:"state"`
	PeerID            string        `json:"peer_id"`
	Target            string        `json:"target"`
	EstablishedAt     *time.Time    `json:"established_at,omitempty"`
	Degraded          bool          `json:"degraded"`
	DegradationReason *string       `json:"degradation_reason"`
}

// BuildTransportStatusEvent builds a transport_status SSE event payload.
// degradationReason should be set only when state is "degraded" or after a
// degraded→closed transition. Pass nil for no reason.
func BuildTransportStatusEvent(
	tt TransportType,
	state ConnectionState,
	peerID string,
	target string,
	establishedAt *time.Time,
	degradationReason *string,
) (eventType string, data json.RawMessage) {
	d := TransportStatusData{
		TransportType:     tt,
		State:             state.String(),
		PeerID:            peerID,
		Target:            target,
		EstablishedAt:     establishedAt,
		Degraded:          state == StateDegraded,
		DegradationReason: degradationReason,
	}
	raw, _ := json.Marshal(d)
	return "transport_status", raw
}

// --- transport_error (SPEC-FTR-04 §5.2) -------------------------------------

// TransportErrorData is the data payload for the transport_error SSE event.
type TransportErrorData struct {
	TransportType    TransportType `json:"transport_type"`
	ErrorCode        string        `json:"error_code"`
	ErrorMessage     string        `json:"error_message"`
	PeerID           string        `json:"peer_id"`
	FallbackAvailable bool         `json:"fallback_available"`
	NextTransport    *string       `json:"next_transport,omitempty"`
}

// BuildTransportErrorEvent builds a transport_error SSE event payload.
func BuildTransportErrorEvent(
	tt TransportType,
	errorCode string,
	errorMessage string,
	peerID string,
	fallbackAvailable bool,
	nextTransport *string,
) (eventType string, data json.RawMessage) {
	d := TransportErrorData{
		TransportType:     tt,
		ErrorCode:         errorCode,
		ErrorMessage:      errorMessage,
		PeerID:            peerID,
		FallbackAvailable: fallbackAvailable,
		NextTransport:     nextTransport,
	}
	raw, _ := json.Marshal(d)
	return "transport_error", raw
}

// --- transport_degradation (SPEC-FTR-04 §5.3) -------------------------------

// TransportDegradationData is the data payload for the
// transport_degradation SSE event.
type TransportDegradationData struct {
	TransportType   TransportType `json:"transport_type"`
	ConnectionID    string        `json:"connection_id"`
	PeerID          string        `json:"peer_id"`
	Degraded        bool          `json:"degraded"`
	Reason          string        `json:"reason"`
	BandwidthTier   string        `json:"bandwidth_tier,omitempty"`
	BytesPerSecond  int64         `json:"bytes_per_second,omitempty"`
	LatencyMs       int64         `json:"latency_ms,omitempty"`
	PacketLoss      float64       `json:"packet_loss,omitempty"`
	FallbackChain   []string      `json:"fallback_chain"`
}

// BuildTransportDegradationEvent builds a transport_degradation SSE event payload.
func BuildTransportDegradationEvent(
	tt TransportType,
	connectionID string,
	peerID string,
	degraded bool,
	reason string,
	bp *BandwidthProfile,
	fallbackChain []TransportType,
) (eventType string, data json.RawMessage) {
	d := TransportDegradationData{
		TransportType: tt,
		ConnectionID:  connectionID,
		PeerID:        peerID,
		Degraded:      degraded,
		Reason:        reason,
		FallbackChain: transportTypesToStrings(fallbackChain),
	}
	if bp != nil {
		d.BandwidthTier = bp.BandwidthTier()
		d.BytesPerSecond = bp.BytesPerSecond
		d.LatencyMs = bp.LatencyMs
		d.PacketLoss = bp.PacketLoss
	}
	raw, _ := json.Marshal(d)
	return "transport_degradation", raw
}

// transportTypesToStrings converts a slice of TransportType to strings.
func transportTypesToStrings(tts []TransportType) []string {
	out := make([]string, len(tts))
	for i, tt := range tts {
		out[i] = string(tt)
	}
	return out
}
