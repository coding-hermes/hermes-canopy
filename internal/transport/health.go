package transport

import (
	"context"
	"sync"
	"time"
)

type TransportEvent struct {
	Type          TransportType   `json:"transport_type"`
	Event         string          `json:"event"`
	Degraded      bool            `json:"degraded"`
	Reason        string          `json:"reason"`
	FallbackChain []TransportType `json:"fallback_chain,omitempty"`
}
type healthCounters struct{ successes, failures int }
type HealthMonitor struct {
	selector *TransportSelector
	emit     func(TransportEvent)
	cancel   context.CancelFunc
	done     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	counters map[TransportType]healthCounters
}

func (ts *TransportSelector) StartHealthMonitor(parent context.Context, emit func(TransportEvent)) *HealthMonitor {
	ctx, cancel := context.WithCancel(parent)
	h := &HealthMonitor{selector: ts, emit: emit, cancel: cancel, done: make(chan struct{}), counters: map[TransportType]healthCounters{}}
	go h.run(ctx)
	return h
}
func (h *HealthMonitor) run(ctx context.Context) {
	defer close(h.done)
	h.selector.mu.RLock()
	interval := h.selector.healthInterval
	h.selector.mu.RUnlock()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.Check(ctx)
		}
	}
}
func (h *HealthMonitor) Check(ctx context.Context) {
	h.selector.mu.RLock()
	adapters := map[TransportType]TransportAdapter{}
	for k, v := range h.selector.adapters {
		adapters[k] = v
	}
	up, down := h.selector.upThreshold, h.selector.downThreshold
	chain := append([]TransportType(nil), h.selector.available...)
	h.selector.mu.RUnlock()
	for tt, a := range adapters {
		err := a.Health(ctx)
		h.mu.Lock()
		c := h.counters[tt]
		if err == nil {
			c.successes++
			c.failures = 0
		} else {
			c.failures++
			c.successes = 0
		}
		h.counters[tt] = c
		h.mu.Unlock()
		was := h.selector.IsHealthy(tt)
		now := was
		if was && c.failures >= down {
			now = false
		}
		if !was && c.successes >= up {
			now = true
		}
		if now != was {
			h.selector.setHealthy(tt, now)
			if h.emit != nil {
				reason := "health_check_failed"
				if now {
					reason = "health_recovery"
				}
				h.emit(TransportEvent{Type: tt, Event: "transport_degradation", Degraded: !now, Reason: reason, FallbackChain: chain})
			}
		}
	}
}
func (h *HealthMonitor) Stop() { h.once.Do(func() { h.cancel(); <-h.done }) }
