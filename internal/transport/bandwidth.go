package transport

import (
	"sync"
	"time"
)

// BandwidthProfile captures measured throughput for a connection.
type BandwidthProfile struct {
	BytesPerSecond int64
	LatencyMs      int64
	PacketLoss     float64
	mu             sync.Mutex
}

// Record updates the profile with a new throughput and latency sample. A
// sample's byte count is interpreted as bytes per second by this API.
func (bp *BandwidthProfile) Record(bytes int, latency time.Duration) {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	if bytes < 0 {
		bytes = 0
	}
	bp.BytesPerSecond = int64(bytes)
	if latency < 0 {
		latency = 0
	}
	bp.LatencyMs = latency.Milliseconds()
}

// Snapshot returns the current profile values atomically.
func (bp *BandwidthProfile) Snapshot() (bps int64, latencyMs int64, loss float64) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	return bp.BytesPerSecond, bp.LatencyMs, bp.PacketLoss
}

// Tier returns the bandwidth tier based on BytesPerSecond.
func (bp *BandwidthProfile) Tier() string {
	bps, _, _ := bp.Snapshot()
	switch {
	case bps > 1_000_000:
		return "full"
	case bps >= 100_000:
		return "reduced"
	default:
		return "minimal"
	}
}

// BandwidthTier is retained as a compatibility alias for older event helpers.
func (bp *BandwidthProfile) BandwidthTier() string {
	return bp.Tier()
}
