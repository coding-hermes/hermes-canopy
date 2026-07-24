package hermes

import (
	"bytes"
	"strings"
	"testing"
)

func TestTokenEncryptionRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plaintext := "hprof_test-token-秘密"

	ciphertext, err := EncryptToken(key, plaintext)
	if err != nil {
		t.Fatalf("EncryptToken() error = %v", err)
	}
	if bytes.Contains(ciphertext, []byte(plaintext)) {
		t.Fatal("EncryptToken() ciphertext contains plaintext")
	}

	got, err := DecryptToken(key, ciphertext)
	if err != nil {
		t.Fatalf("DecryptToken() error = %v", err)
	}
	if got != plaintext {
		t.Fatalf("DecryptToken() = %q, want %q", got, plaintext)
	}
}

func TestTokenEncryptionUsesUniqueNonces(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")

	first, err := EncryptToken(key, "same token")
	if err != nil {
		t.Fatalf("first EncryptToken() error = %v", err)
	}
	second, err := EncryptToken(key, "same token")
	if err != nil {
		t.Fatalf("second EncryptToken() error = %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("EncryptToken() produced identical ciphertext for repeated plaintext")
	}
}

func TestTokenCryptoRejectsInvalidKeyLength(t *testing.T) {
	if _, err := EncryptToken([]byte("too-short"), "token"); err == nil {
		t.Fatal("EncryptToken() error = nil, want invalid key length error")
	}
	if _, err := DecryptToken([]byte("too-short"), make([]byte, 32)); err == nil {
		t.Fatal("DecryptToken() error = nil, want invalid key length error")
	}
}

func TestDecryptTokenRejectsMalformedCiphertext(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")

	_, err := DecryptToken(key, []byte("short"))
	if err == nil {
		t.Fatal("DecryptToken() error = nil, want malformed ciphertext error")
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Fatalf("DecryptToken() error = %q, want too short context", err)
	}
}

func TestDecryptTokenRejectsTampering(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	ciphertext, err := EncryptToken(key, "token")
	if err != nil {
		t.Fatalf("EncryptToken() error = %v", err)
	}
	ciphertext[len(ciphertext)-1] ^= 0xff

	if _, err := DecryptToken(key, ciphertext); err == nil {
		t.Fatal("DecryptToken() error = nil, want authentication error")
	}
}
