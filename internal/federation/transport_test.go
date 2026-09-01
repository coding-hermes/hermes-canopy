package federation

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTokenEd25519AndLegacyHMAC(t *testing.T) {
	s := newTokenSigner([]byte("legacy-secret"), uuid.New(), "https://one.example")
	profileID, treeID := uuid.New(), uuid.New()
	token, err := s.generate(profileID, treeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.verify(token); err != nil {
		t.Fatalf("verify Ed25519 token: %v", err)
	}

	now := time.Now().UTC()
	claims := TokenClaims{1, s.serverID, s.serverURL, profileID, treeID, now, now.Add(time.Hour), keyFingerprint(s.legacyKey)}
	payload, _ := json.Marshal(claims)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.legacyKey)
	_, _ = mac.Write([]byte(encoded))
	legacy := encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if _, err := s.verify(legacy); err != nil {
		t.Fatalf("verify legacy HMAC token: %v", err)
	}
}

func TestECDHRoundTrip(t *testing.T) {
	aPub, aPriv, err := GenerateECDHKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	bPub, bPriv, err := GenerateECDHKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	aShared, err := DeriveSharedSecret(aPriv, bPub)
	if err != nil {
		t.Fatal(err)
	}
	bShared, err := DeriveSharedSecret(bPriv, aPub)
	if err != nil {
		t.Fatal(err)
	}
	if !hmac.Equal(aShared, bShared) {
		t.Fatal("ECDH shared secrets differ")
	}
}

func TestAEADRoundTripAndTamper(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	peerID := uuid.New()
	ciphertext, nonce, err := EncryptPayload(key, peerID, 7, []byte("hello FTL"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := DecryptPayload(key, peerID, 7, ciphertext, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != "hello FTL" {
		t.Fatalf("plaintext = %q", plaintext)
	}
	ciphertext[0] ^= 1
	if _, err := DecryptPayload(key, peerID, 7, ciphertext, nonce); !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("tamper error = %v", err)
	}
}
