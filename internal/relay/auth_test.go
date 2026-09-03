package relay

import (
	"errors"
	"testing"
)

func TestFrameAuthentication(t *testing.T) {
	a := NewFrameAuthenticator(7, []byte("current"), 6, []byte("previous"))
	f, err := a.Sign(Frame{Type: FrameHello, KeyID: 7, Payload: []byte{0xa1, 0x00, 0x01}})
	if err != nil || a.Verify(f) != nil {
		t.Fatalf("valid frame: sign=%v verify=%v", err, a.Verify(f))
	}
	tampered := f
	tampered.Payload = append([]byte(nil), f.Payload...)
	tampered.Payload[0] ^= 1
	if !errors.Is(a.Verify(tampered), ErrAuthFailed) {
		t.Fatal("tampered payload accepted")
	}
	tampered = f
	tampered.HMAC = append([]byte(nil), f.HMAC...)
	tampered.HMAC[0] ^= 1
	if !errors.Is(a.Verify(tampered), ErrAuthFailed) {
		t.Fatal("invalid MAC accepted")
	}
	unknown := f
	unknown.KeyID = 99
	if !errors.Is(a.Verify(unknown), ErrAuthFailed) {
		t.Fatal("unknown key accepted")
	}
	previous, err := a.Sign(Frame{Type: FramePing, KeyID: 6})
	if err != nil || a.Verify(previous) != nil {
		t.Fatalf("previous key rejected: %v", err)
	}
}
