package relay

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

const hmacKeyGracePeriod = 48 * time.Hour

var errHMACKeyIDExhausted = errors.New("relay: HMAC key ID exhausted")

// SetRotationPersister wires durable relay_config updates without coupling the
// relay lifecycle to a concrete database implementation.
func (r *RelayService) SetRotationPersister(persist func(context.Context, time.Time, uint32) error) {
	r.mu.Lock()
	r.persistRotation = persist
	r.mu.Unlock()
}

func (r *RelayService) startRotationLoopLocked() {
	if r.rotationStop != nil {
		return
	}
	interval := r.config.HMACKeyRotateInterval
	if interval <= 0 {
		// interval <= 0 disables auto-rotation (operator knob; rotation still
		// available via RotateNow). Not defaulted here: lifecycle tests and
		// operators that only want manual rotation must not spawn a ticker.
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	r.rotationStop, r.rotationDone = stop, done
	go func() {
		defer close(done)
		for {
			r.mu.RLock()
			rotatedAt := r.config.HMACKeyRotatedAt
			r.mu.RUnlock()
			wait := interval
			if rotatedAt != nil {
				wait = rotatedAt.Add(interval).Sub(r.now())
				if wait < 0 {
					wait = 0
				}
			}
			select {
			case <-stop:
				return
			case <-r.clock.After(wait):
				if err := r.RotateNow(context.Background()); err != nil {
					log.Error().Err(err).Msg("relay HMAC key rotation failed")
				}
			}
		}
	}()
}

// RotateNow immediately generates and installs a fresh 256-bit HMAC key.
func (r *RelayService) RotateNow(ctx context.Context) error { return r.rotateHMACKey(ctx) }

func (r *RelayService) rotateHMACKey(ctx context.Context) error {
	r.rotationMu.Lock()
	defer r.rotationMu.Unlock()
	r.publishRotation("hmac_rotation_started", 0)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}

	r.mu.Lock()
	if r.config.HMACKeyID >= math.MaxUint16 {
		r.mu.Unlock()
		return errHMACKeyIDExhausted
	}
	now := r.now()
	newID := uint32(r.config.HMACKeyID + 1)
	persist := r.persistRotation
	r.mu.Unlock()
	if persist != nil {
		if err := persist(ctx, now, newID); err != nil {
			return err
		}
	}

	r.mu.Lock()
	for id, entry := range r.keys {
		if !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt) {
			delete(r.keys, id)
		}
	}
	oldID := uint16(r.config.HMACKeyID)
	if entry, ok := r.keys[oldID]; ok {
		entry.expiresAt = now.Add(hmacKeyGracePeriod)
		r.keys[oldID] = entry
	}
	r.keys[uint16(newID)] = authenticationKey{key: key}
	r.config.HMACKey, r.config.HMACKeyID, r.config.HMACKeyRotatedAt = key, int(newID), &now
	r.rotations++
	keys := make(map[uint16]authenticationKey, len(r.keys))
	for id, entry := range r.keys {
		keys[id] = entry
	}
	if h, ok := r.transport.(interface {
		updateHMACKeys(uint16, map[uint16]authenticationKey)
	}); ok {
		h.updateHMACKeys(uint16(newID), keys)
	}
	r.mu.Unlock()
	log.Info().Uint32("hmac_key_id", newID).Time("rotated_at", now).Msg("relay HMAC key rotated")
	r.publishRotation("hmac_rotation_completed", newID)
	return nil
}

func (r *RelayService) publishRotation(eventType string, keyID uint32) {
	r.mu.RLock()
	hub, instanceID := r.hub, r.config.InstanceID
	r.mu.RUnlock()
	if hub == nil {
		return
	}
	b, _ := json.Marshal(map[string]any{"event_type": eventType, "instance_id": instanceID, "hmac_key_id": keyID})
	hub.Broadcast(uuid.Nil, sse.SSEEvent{Type: "relay_tenant_event", Data: b})
}
