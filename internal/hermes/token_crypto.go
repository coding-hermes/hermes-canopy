package hermes

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

const aes256KeySize = 32

// EncryptToken encrypts a plaintext token with AES-256-GCM using the provided key.
// The returned byte slice is nonce || ciphertext.
func EncryptToken(key []byte, plaintext string) ([]byte, error) {
	gcm, err := newTokenGCM(key)
	if err != nil {
		return nil, fmt.Errorf("hermes: encrypt token: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("hermes: encrypt token: generate nonce: %w", err)
	}

	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// DecryptToken decrypts ciphertext encoded as nonce || ciphertext with AES-256-GCM.
func DecryptToken(key []byte, ciphertext []byte) (string, error) {
	gcm, err := newTokenGCM(key)
	if err != nil {
		return "", fmt.Errorf("hermes: decrypt token: %w", err)
	}
	if len(ciphertext) < gcm.NonceSize()+gcm.Overhead() {
		return "", fmt.Errorf("hermes: decrypt token: ciphertext too short")
	}

	nonce := ciphertext[:gcm.NonceSize()]
	sealed := ciphertext[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("hermes: decrypt token: authenticate ciphertext: %w", err)
	}
	return string(plaintext), nil
}

func newTokenGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != aes256KeySize {
		return nil, fmt.Errorf("AES-256 key must be %d bytes, got %d", aes256KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	return gcm, nil
}
