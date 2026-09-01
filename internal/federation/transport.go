package federation

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const CurrentFTLVersion = 1

type FTLEnvelope struct {
	FTLVersion      int              `json:"ftl_version"`
	SenderServerID  uuid.UUID        `json:"sender_server_id"`
	SenderProfileID uuid.UUID        `json:"sender_profile_id"`
	Sequence        int64            `json:"seq"`
	Clock           map[string]int64 `json:"clock"`
	Timestamp       time.Time        `json:"timestamp"`
	TreeID          uuid.UUID        `json:"tree_id"`
	Ciphertext      []byte           `json:"ciphertext"`
	Nonce           []byte           `json:"nonce"`
	Signature       []byte           `json:"signature"`
	SigningKeyFP    string           `json:"signing_key_fp"`
	PeerID          uuid.UUID        `json:"peer_id"`
}

type FTLInnerPayload struct {
	EventType  string          `json:"event_type"`
	Payload    json.RawMessage `json:"payload"`
	RefEventID string          `json:"ref_event_id,omitempty"`
}

func GenerateECDHKeyPair() (publicKey, privateKey []byte, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return key.PublicKey().Bytes(), key.Bytes(), nil
}

func DeriveSharedSecret(privateKey, publicKey []byte) ([]byte, error) {
	curve := ecdh.X25519()
	priv, err := curve.NewPrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	pub, err := curve.NewPublicKey(publicKey)
	if err != nil {
		return nil, err
	}
	return priv.ECDH(pub)
}

func associatedData(peerID uuid.UUID, sequence int64) []byte {
	data := make([]byte, 24)
	copy(data, peerID[:])
	binary.BigEndian.PutUint64(data[16:], uint64(sequence))
	return data
}

func EncryptPayload(key []byte, peerID uuid.UUID, sequence int64, plaintext []byte) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return aead.Seal(nil, nonce, plaintext, associatedData(peerID, sequence)), nonce, nil
}

func DecryptPayload(key []byte, peerID uuid.UUID, sequence int64, ciphertext, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrDecryptionFailed
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrDecryptionFailed
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, associatedData(peerID, sequence))
	if err != nil {
		return nil, ErrDecryptionFailed
	}
	return plaintext, nil
}

func envelopeSigningBytes(envelope *FTLEnvelope) ([]byte, error) {
	copy := *envelope
	copy.Signature = nil
	return json.Marshal(copy)
}

func (s *Service) EncryptEnvelope(ctx context.Context, peerID, profileID uuid.UUID, sequence int64, eventType string, payload []byte) (*FTLEnvelope, error) {
	peer, err := s.repo.Get(ctx, peerID)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	key := append([]byte(nil), s.sessions[peerID]...)
	s.mu.RUnlock()
	if len(key) == 0 {
		return nil, ErrNoSharedSecret
	}
	inner, err := json.Marshal(FTLInnerPayload{EventType: eventType, Payload: payload})
	if err != nil {
		return nil, err
	}
	ciphertext, nonce, err := EncryptPayload(key, peerID, sequence, inner)
	if err != nil {
		return nil, err
	}
	envelope := &FTLEnvelope{CurrentFTLVersion, s.signer.serverID, profileID, sequence, map[string]int64{s.signer.serverID.String(): sequence}, time.Now().UTC(), peer.TreeID, ciphertext, nonce, nil, s.signer.fingerprint(), peerID}
	message, err := envelopeSigningBytes(envelope)
	if err != nil {
		return nil, err
	}
	envelope.Signature = ed25519.Sign(s.signer.privateKey, message)
	return envelope, nil
}

func (s *Service) ReceiveEvent(ctx context.Context, envelope *FTLEnvelope) (*FTLInnerPayload, error) {
	if envelope == nil || envelope.FTLVersion != CurrentFTLVersion {
		return nil, ErrInvalidInput
	}
	peer, err := s.repo.Get(ctx, envelope.PeerID)
	if err != nil {
		return nil, err
	}
	if peer.TreeID != envelope.TreeID || envelope.SigningKeyFP != peer.SigningKeyFP {
		return nil, ErrSignatureMismatch
	}
	message, err := envelopeSigningBytes(envelope)
	if err != nil || len(peer.SigningPublicKey) != ed25519.PublicKeySize || !ed25519.Verify(peer.SigningPublicKey, message, envelope.Signature) {
		return nil, ErrSignatureMismatch
	}
	s.mu.RLock()
	key := append([]byte(nil), s.sessions[peer.ID]...)
	s.mu.RUnlock()
	if len(key) == 0 {
		return nil, ErrNoSharedSecret
	}
	plaintext, err := DecryptPayload(key, peer.ID, envelope.Sequence, envelope.Ciphertext, envelope.Nonce)
	if err != nil {
		return nil, err
	}
	var inner FTLInnerPayload
	if json.Unmarshal(plaintext, &inner) != nil {
		return nil, ErrDecryptionFailed
	}
	if mutation, ok := decodeMutation(&inner, envelope); ok && s.conflicts != nil {
		if err := s.conflicts.apply(ctx, envelope.TreeID, mutation); err != nil {
			return nil, err
		}
	}
	return &inner, nil
}
