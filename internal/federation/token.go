package federation

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

type tokenSigner struct {
	legacyKey  []byte
	publicKey  ed25519.PublicKey
	privateKey ed25519.PrivateKey
	serverID   uuid.UUID
	serverURL  string
	now        func() time.Time
}

func newTokenSigner(legacyKey []byte, serverID uuid.UUID, serverURL string) *tokenSigner {
	seed := sha256.Sum256(append([]byte("hermes-federation-ed25519:"), legacyKey...))
	return newTokenSignerWithIdentity(legacyKey, ed25519.NewKeyFromSeed(seed[:]), serverID, serverURL)
}

func newTokenSignerWithIdentity(legacyKey []byte, privateKey ed25519.PrivateKey, serverID uuid.UUID, serverURL string) *tokenSigner {
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	return &tokenSigner{append([]byte(nil), legacyKey...), publicKey, append(ed25519.PrivateKey(nil), privateKey...), serverID, serverURL, func() time.Time { return time.Now().UTC() }}
}

func keyFingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *tokenSigner) fingerprint() string { return keyFingerprint(s.publicKey) }

func (s *tokenSigner) generate(profileID, treeID uuid.UUID) (string, error) {
	now := s.now()
	claims := TokenClaims{1, s.serverID, s.serverURL, profileID, treeID, now, now.Add(24 * time.Hour), s.fingerprint()}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.privateKey, []byte(encoded))), nil
}

func (s *tokenSigner) verify(token string) (*TokenClaims, error) {
	return s.verifyWithKey(token, s.publicKey)
}

func (s *tokenSigner) verifyWithKey(token string, publicKey ed25519.PublicKey) (*TokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, ErrTokenInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrTokenInvalid
	}
	edValid := len(publicKey) == ed25519.PublicKeySize && ed25519.Verify(publicKey, []byte(parts[0]), sig)
	legacyValid := false
	// P2 transition: accept P1 HMAC tokens until their 24-hour expiry.
	if !edValid && len(s.legacyKey) > 0 {
		mac := hmac.New(sha256.New, s.legacyKey)
		_, _ = mac.Write([]byte(parts[0]))
		legacyValid = hmac.Equal(sig, mac.Sum(nil))
	}
	if !edValid && !legacyValid {
		return nil, ErrTokenInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrTokenInvalid
	}
	var claims TokenClaims
	if json.Unmarshal(payload, &claims) != nil || claims.TokenVersion != 1 || claims.ServerID == uuid.Nil || claims.ProfileID == uuid.Nil || claims.TreeID == uuid.Nil || claims.ServerURL == "" {
		return nil, ErrTokenInvalid
	}
	if edValid && claims.SigningKeyFP != keyFingerprint(publicKey) {
		return nil, ErrTokenInvalid
	}
	if !claims.ExpiresAt.After(s.now()) {
		return nil, ErrTokenExpired
	}
	return &claims, nil
}
