package relay

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
)

var ErrAuthFailed = errors.New("relay: authentication failed")

type FrameAuthenticator struct{ keys map[uint16][]byte }

func NewFrameAuthenticator(currentID uint16, current []byte, previousID uint16, previous []byte) *FrameAuthenticator {
	keys := make(map[uint16][]byte, 2)
	keys[currentID] = append([]byte(nil), current...)
	if len(previous) != 0 {
		keys[previousID] = append([]byte(nil), previous...)
	}
	return &FrameAuthenticator{keys: keys}
}

func (a *FrameAuthenticator) Sign(f Frame) (Frame, error) {
	key, ok := a.keys[f.KeyID]
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
	key, ok := a.keys[f.KeyID]
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
