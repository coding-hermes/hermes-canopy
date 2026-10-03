package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/mls"
)

type recordingKeyPackageManager struct {
	profileID  uuid.UUID
	credential mls.MLSCredential
	stored     mls.MLSKeyPackage
}

func (m *recordingKeyPackageManager) GenerateKeyPackage(_ context.Context, profileID uuid.UUID, credential mls.MLSCredential) (mls.MLSKeyPackage, error) {
	m.profileID = profileID
	m.credential = credential
	m.stored = mls.MLSKeyPackage{ID: uuid.New(), ProfileID: profileID, KeyPackageBytes: []byte("package")}
	return m.stored, nil
}

func (m *recordingKeyPackageManager) GetKeyPackage(context.Context, uuid.UUID) (mls.MLSKeyPackage, error) {
	return m.stored, nil
}

func (m *recordingKeyPackageManager) ExpireKeyPackage(context.Context, uuid.UUID) error { return nil }

func TestGenerateKeyPackage_DoesNotAcceptPrivateKey(t *testing.T) {
	workspaceID := uuid.New()
	profileID := uuid.New()
	publicKey := ed25519.PublicKey(bytes.Repeat([]byte{0x42}, ed25519.PublicKeySize))
	manager := &recordingKeyPackageManager{}
	h := NewMLSHandler(nil, manager)

	r := chi.NewRouter()
	r.Route("/api/v1/workspaces/{workspace_id}/mls", func(r chi.Router) {
		r.Post("/key-packages", h.GenerateKeyPackage)
	})

	body := map[string]any{
		"workspace_id": workspaceID,
		"profile_id":   profileID,
		"credential": map[string]any{
			"identity":             []byte("profile-identity"),
			"credential_type":      "basic",
			"signature_public_key": []byte(publicKey),
		},
		"private_key": []byte("client-private-key-that-must-be-ignored"),
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+workspaceID.String()+"/mls/key-packages", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusCreated, resp.Body.String())
	}
	if manager.profileID != profileID {
		t.Fatalf("manager profileID = %v, want %v", manager.profileID, profileID)
	}
	if !bytes.Equal(manager.credential.SignaturePublicKey, publicKey) {
		t.Fatal("manager did not receive the credential signature public key")
	}
	if !bytes.Equal(manager.stored.KeyPackageBytes, []byte("package")) {
		t.Fatal("stored package was not returned by the manager")
	}
	if strings.Contains(resp.Body.String(), "private_key") || strings.Contains(resp.Body.String(), "client-private-key") {
		t.Fatalf("response exposed private-key material: %s", resp.Body.String())
	}
}

func TestMLSHandler_EpochConflictMapsToConflict(t *testing.T) {
	h := &MLSHandler{}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)

	h.writeMLSError(resp, req, mls.ErrEpochConflict, "commit proposals")

	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusConflict)
	}
}

func TestMLSHandler_DecryptionFailedMapsToBadRequest(t *testing.T) {
	h := &MLSHandler{}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)

	h.writeMLSError(resp, req, fmt.Errorf("gcm open: %w", mls.ErrDecryptionFailed), "decrypt mls message")

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	var body apiErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Code != "DECRYPT_FAILED" {
		t.Fatalf("error code = %q, want DECRYPT_FAILED", body.Error.Code)
	}
}

func TestMLSHandler_DecryptRejectsInvalidBase64(t *testing.T) {
	workspaceID := uuid.New()
	r := chi.NewRouter()
	h := &MLSHandler{}
	r.Post("/api/v1/workspaces/{workspace_id}/mls/decrypt", h.Decrypt)

	body := []byte(`{"workspace_id":"` + workspaceID.String() + `","ciphertext":{"ciphertext":"not-base64"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+workspaceID.String()+"/mls/decrypt", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
	var responseBody apiErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&responseBody); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if responseBody.Error.Code != "INVALID_BODY" {
		t.Fatalf("error code = %q, want INVALID_BODY", responseBody.Error.Code)
	}
}
