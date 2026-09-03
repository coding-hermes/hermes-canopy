package relay

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"sync"
	"time"
)

var ErrAuthFailed = errors.New("relay: authentication failed")

type authenticationKey struct {
	key       []byte
	expiresAt time.Time
}

type FrameAuthenticator struct {
	mu   sync.RWMutex
	keys map[uint16]authenticationKey
	now  func() time.Time
}

func NewFrameAuthenticator(currentID uint16, current []byte, previousID uint16, previous []byte) *FrameAuthenticator {
	keys := make(map[uint16]authenticationKey, 2)
	keys[currentID] = authenticationKey{key: append([]byte(nil), current...)}
	if len(previous) != 0 {
		keys[previousID] = authenticationKey{key: append([]byte(nil), previous...)}
	}
	return &FrameAuthenticator{keys: keys, now: time.Now}
}

// SetKeys atomically installs the complete validation key ring. A zero expiry
// denotes the current key; expired entries are rejected during lookup.
func (a *FrameAuthenticator) SetKeys(keys map[uint16]authenticationKey) {
	copyKeys := make(map[uint16]authenticationKey, len(keys))
	for id, entry := range keys {
		entry.key = append([]byte(nil), entry.key...)
		copyKeys[id] = entry
	}
	a.mu.Lock()
	a.keys = copyKeys
	a.mu.Unlock()
}

func (a *FrameAuthenticator) key(id uint16) ([]byte, bool) {
	a.mu.RLock()
	entry, ok := a.keys[id]
	now := a.now
	a.mu.RUnlock()
	if !ok || (!entry.expiresAt.IsZero() && !now().Before(entry.expiresAt)) {
		return nil, false
	}
	return entry.key, true
}

func (a *FrameAuthenticator) Sign(f Frame) (Frame, error) {
	key, ok := a.key(f.KeyID)
	if !ok {
		return Frame{}, ErrAuthFailed
	}
	f.HMAC = nil
	b, err := EncodeFrame(f)
	if err != nil {
		return Frame{}, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(b)
	f.HMAC = mac.Sum(nil)
	return f, nil
}

func (a *FrameAuthenticator) Verify(f Frame) error {
	key, ok := a.key(f.KeyID)
	if !ok || len(f.HMAC) != HMACSize {
		return ErrAuthFailed
	}
	want := append([]byte(nil), f.HMAC...)
	f.HMAC = nil
	b, err := EncodeFrame(f)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(b)
	if !hmac.Equal(want, mac.Sum(nil)) {
		return ErrAuthFailed
	}
	return nil
}
