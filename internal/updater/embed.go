package updater

import (
	"crypto/ed25519"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// releasePublicKeyHex is the Ed25519 public key embedded into the binary at
// build time via go:embed. It is the key that release manifests must be
// signed with for signature verification to succeed (SPEC-FTR-06 §49).
//
// NOTE: the key committed here is a placeholder generated for development.
// The matching private key is deliberately NOT in the repository. A
// production deployment MUST generate its own keypair and either replace this
// file before rebuilding or override the embedded key at runtime via
// --update-public-key-file / CANOPY_UPDATE_PUBLIC_KEY_FILE. See README
// "Signed updates (self-update)".
//
//go:embed release_public_key.txt
var releasePublicKeyHex string

// DefaultPublicKey returns the embedded release-signing public key. It fails
// only if the embedded value is malformed, which TestEmbeddedPublicKey pins as
// a build-time invariant.
func DefaultPublicKey() (ed25519.PublicKey, error) {
	return ParsePublicKeyHex(releasePublicKeyHex)
}

// ParsePublicKeyHex decodes a hex-encoded Ed25519 public key (32 bytes, 64
// hex characters). Surrounding whitespace is ignored so the value may come
// from a file with a trailing newline.
func ParsePublicKeyHex(value string) (ed25519.PublicKey, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("updater: decode public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("updater: public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// ResolvePublicKey returns the Ed25519 public key read from the hex-encoded
// file at path, or the embedded default when path is empty. This is the single
// seam cmd/canopyd uses to honour --update-public-key-file /
// CANOPY_UPDATE_PUBLIC_KEY_FILE over the embedded release key.
func ResolvePublicKey(path string) (ed25519.PublicKey, error) {
	if strings.TrimSpace(path) == "" {
		return DefaultPublicKey()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("updater: read public key file: %w", err)
	}
	return ParsePublicKeyHex(string(raw))
}
