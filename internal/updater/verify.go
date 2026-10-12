package updater

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// VerifySignature verifies a release signature over the raw SHA-256 digest.
// For compatibility with early release tooling it also accepts a signature
// over the lowercase hexadecimal digest.
func VerifySignature(publicKey ed25519.PublicKey, digest, signature []byte) bool {
	if len(publicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize || len(digest) != sha256.Size {
		return false
	}
	if ed25519.Verify(publicKey, digest, signature) {
		return true
	}
	return ed25519.Verify(publicKey, []byte(hex.EncodeToString(digest)), signature)
}

// VerifySHA256Signature verifies a signature against a hexadecimal SHA-256
// value as found in release metadata.
func VerifySHA256Signature(publicKey ed25519.PublicKey, sha256Hex string, signature []byte) bool {
	if len(sha256Hex) != sha256.Size*2 {
		return false
	}
	digest, err := hex.DecodeString(sha256Hex)
	return err == nil && VerifySignature(publicKey, digest, signature)
}

// VerifyManifestSignature verifies the optional signature over the SHA-256 of
// the canonical JSON manifest with manifest_signature omitted. Empty
// signatures are allowed so local/test manifests can be used for checking;
// when present, a public key and a valid signature are mandatory.
func VerifyManifestSignature(publicKey ed25519.PublicKey, manifest ReleaseManifest) error {
	if len(manifest.ManifestSignature) == 0 {
		return nil
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("updater: manifest signature present but no Ed25519 public key configured")
	}
	signature := append([]byte(nil), manifest.ManifestSignature...)
	manifest.ManifestSignature = nil
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("updater: canonicalize manifest: %w", err)
	}
	digest := sha256.Sum256(canonical)
	if !VerifySignature(publicKey, digest[:], signature) {
		return ErrInvalidSignature
	}
	return nil
}
