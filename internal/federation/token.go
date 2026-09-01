package federation

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type TokenClaims struct {
	TokenVersion int       `json:"token_version"`
	ServerID     uuid.UUID `json:"server_id"`
	ServerURL    string    `json:"server_url"`
	ProfileID    uuid.UUID `json:"profile_id"`
	TreeID       uuid.UUID `json:"tree_id"`
	IssuedAt     time.Time `json:"issued_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	SigningKeyFP string    `json:"signing_key_fp"`
}

// tokenSigner implements the signed payload in SPEC-FTR-02 §4.2 with HMAC-
// SHA256 for P1. The spec's design decision already calls federation tokens
// HMAC-verified; Ed25519 key exchange/management is explicitly deferred to P2.
type tokenSigner struct {
	key       []byte
	serverID  uuid.UUID
	serverURL string
	now       func() time.Time
}

func newTokenSigner(key []byte, serverID uuid.UUID, serverURL string) *tokenSigner {
	return &tokenSigner{key: key, serverID: serverID, serverURL: serverURL, now: func() time.Time { return time.Now().UTC() }}
}

func (s *tokenSigner) fingerprint() string {
	sum := sha256.Sum256(s.key)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *tokenSigner) generate(profileID, treeID uuid.UUID) (string, error) {
	now := s.now()
	claims := TokenClaims{1, s.serverID, s.serverURL, profileID, treeID, now, now.Add(24 * time.Hour), s.fingerprint()}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("federation: marshal token: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *tokenSigner) verify(token string) (*TokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, ErrTokenInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrTokenInvalid
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, ErrTokenInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrTokenInvalid
	}
	var claims TokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil || claims.TokenVersion != 1 || claims.ServerID == uuid.Nil || claims.ProfileID == uuid.Nil || claims.TreeID == uuid.Nil || claims.ServerURL == "" || claims.SigningKeyFP != s.fingerprint() {
		return nil, ErrTokenInvalid
	}
	if !claims.ExpiresAt.After(s.now()) {
		return nil, ErrTokenExpired
	}
	return &claims, nil
}
