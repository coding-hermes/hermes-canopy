package dist

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// ParsePrivateKey parses a release key supplied as PKCS#8 PEM, base64 raw
// Ed25519 bytes, or hexadecimal raw Ed25519 bytes. It never supplies a
// fallback key: release signing must fail closed when the secret is absent or
// malformed.
func ParsePrivateKey(value string) (ed25519.PrivateKey, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("dist: signing key is empty")
	}
	if block, _ := pem.Decode([]byte(value)); block != nil {
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("dist: parse PKCS#8 signing key: %w", err)
		}
		privateKey, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("dist: signing key is not Ed25519")
		}
		return append(ed25519.PrivateKey(nil), privateKey...), nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(decoded), nil
	}
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(decoded), nil
	}
	return nil, errors.New("dist: signing key must be PKCS#8 PEM, base64, or 64-byte hex Ed25519 material")
}
