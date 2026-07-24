package handler

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/mls"
)

// MLSHandler wires workspace MLS (Messaging Layer Security, RFC 9420)
// routes to the MLSService and KeyPackageManager interfaces per SPEC-FTR-03.
type MLSHandler struct {
	svc   mls.MLSService
	kpMgr mls.KeyPackageManager
}

// NewMLSHandler returns a handler wired to the given MLSService and KeyPackageManager.
func NewMLSHandler(svc mls.MLSService, kpMgr mls.KeyPackageManager) *MLSHandler {
	return &MLSHandler{svc: svc, kpMgr: kpMgr}
}

// Routes mounts MLS endpoints under a workspace MLS path.
func (h *MLSHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/groups", h.GetGroup)
	r.Post("/groups", h.CreateGroup)
	r.Post("/groups/join", h.JoinGroup)
	r.Post("/groups/leave", h.LeaveGroup)
	r.Post("/encrypt", h.Encrypt)
	r.Post("/decrypt", h.Decrypt)
	r.Get("/state", h.GetState)
	r.Post("/key-packages", h.GenerateKeyPackage)
	r.Get("/key-packages", h.GetKeyPackage)
	r.Post("/commit-proposals", h.CommitProposals)
	return r
}

// --- Handlers ---------------------------------------------------------------

// GetGroup returns the workspace MLS group summary.
func (h *MLSHandler) GetGroup(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	state, err := h.svc.GetGroupState(r.Context(), workspaceID)
	if err != nil {
		h.writeMLSError(w, r, err, "get mls group")
		return
	}
	writeJSON(w, http.StatusOK, state.Group)
}

// CreateGroup creates a new MLS group for the workspace.
func (h *MLSHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	var req struct {
		WorkspaceID      uuid.UUID `json:"workspace_id"`
		CreatorProfileID uuid.UUID `json:"creator_profile_id"`
		AdminPublicKey   []byte    `json:"admin_public_key"`
		AdminPrivateKey  []byte    `json:"admin_private_key"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	if !matchesMLSWorkspace(workspaceID, req.WorkspaceID) {
		writeError(w, http.StatusBadRequest, "WORKSPACE_ID_MISMATCH", "workspace_id must match the route workspace")
		return
	}

	if len(req.AdminPublicKey) != ed25519.PublicKeySize || len(req.AdminPrivateKey) != ed25519.PrivateKeySize {
		writeError(w, http.StatusBadRequest, "INVALID_KEY_PAIR", "admin_public_key and admin_private_key must be valid Ed25519 keys")
		return
	}
	keyPair := mls.Ed25519KeyPair{
		PublicKey:  ed25519.PublicKey(req.AdminPublicKey),
		PrivateKey: ed25519.PrivateKey(req.AdminPrivateKey),
	}

	group, err := h.svc.CreateGroup(r.Context(), workspaceID, req.CreatorProfileID, keyPair)
	if err != nil {
		h.writeMLSError(w, r, err, "create mls group")
		return
	}
	writeJSON(w, http.StatusCreated, group)
}

// JoinGroup adds a profile to the workspace MLS group.
func (h *MLSHandler) JoinGroup(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	var req struct {
		WorkspaceID  uuid.UUID         `json:"workspace_id"`
		ProfileID    uuid.UUID         `json:"profile_id"`
		KeyPackage   mls.MLSKeyPackage `json:"key_package"`
		WelcomeBytes []byte            `json:"welcome_bytes"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	if !matchesMLSWorkspace(workspaceID, req.WorkspaceID) {
		writeError(w, http.StatusBadRequest, "WORKSPACE_ID_MISMATCH", "workspace_id must match the route workspace")
		return
	}

	if err := h.svc.JoinGroup(r.Context(), workspaceID, req.ProfileID, req.KeyPackage, req.WelcomeBytes); err != nil {
		h.writeMLSError(w, r, err, "join mls group")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// LeaveGroup removes a profile from the workspace MLS group.
func (h *MLSHandler) LeaveGroup(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	var req struct {
		WorkspaceID uuid.UUID `json:"workspace_id"`
		ProfileID   uuid.UUID `json:"profile_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	if !matchesMLSWorkspace(workspaceID, req.WorkspaceID) {
		writeError(w, http.StatusBadRequest, "WORKSPACE_ID_MISMATCH", "workspace_id must match the route workspace")
		return
	}

	if err := h.svc.LeaveGroup(r.Context(), workspaceID, req.ProfileID); err != nil {
		h.writeMLSError(w, r, err, "leave mls group")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Encrypt encrypts a plaintext message for the workspace MLS group.
func (h *MLSHandler) Encrypt(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	var req struct {
		WorkspaceID uuid.UUID `json:"workspace_id"`
		ProfileID   uuid.UUID `json:"profile_id"`
		Plaintext   []byte    `json:"plaintext_base64"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	if !matchesMLSWorkspace(workspaceID, req.WorkspaceID) {
		writeError(w, http.StatusBadRequest, "WORKSPACE_ID_MISMATCH", "workspace_id must match the route workspace")
		return
	}

	ciphertext, err := h.svc.Encrypt(r.Context(), workspaceID, req.ProfileID, req.Plaintext)
	if err != nil {
		h.writeMLSError(w, r, err, "encrypt mls message")
		return
	}
	writeJSON(w, http.StatusOK, ciphertext)
}

// Decrypt decrypts an MLS ciphertext for the workspace MLS group.
func (h *MLSHandler) Decrypt(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	var req struct {
		WorkspaceID uuid.UUID         `json:"workspace_id"`
		ProfileID   uuid.UUID         `json:"profile_id"`
		Ciphertext  mls.MLSCiphertext `json:"ciphertext"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	if !matchesMLSWorkspace(workspaceID, req.WorkspaceID) {
		writeError(w, http.StatusBadRequest, "WORKSPACE_ID_MISMATCH", "workspace_id must match the route workspace")
		return
	}

	plaintext, err := h.svc.Decrypt(r.Context(), workspaceID, req.ProfileID, req.Ciphertext)
	if err != nil {
		h.writeMLSError(w, r, err, "decrypt mls message")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plaintext_base64": plaintext})
}

// GetState returns the full MLS group state including members.
func (h *MLSHandler) GetState(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	state, err := h.svc.GetGroupState(r.Context(), workspaceID)
	if err != nil {
		h.writeMLSError(w, r, err, "get mls group state")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// GenerateKeyPackage generates a new MLS key package for a profile.
func (h *MLSHandler) GenerateKeyPackage(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	var req struct {
		WorkspaceID uuid.UUID         `json:"workspace_id"`
		ProfileID   uuid.UUID         `json:"profile_id"`
		Credential  mls.MLSCredential `json:"credential"`
		PublicKey   []byte            `json:"public_key"`
		PrivateKey  []byte            `json:"private_key"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	if !matchesMLSWorkspace(workspaceID, req.WorkspaceID) {
		writeError(w, http.StatusBadRequest, "WORKSPACE_ID_MISMATCH", "workspace_id must match the route workspace")
		return
	}
	if len(req.PublicKey) != ed25519.PublicKeySize || len(req.PrivateKey) != ed25519.PrivateKeySize {
		writeError(w, http.StatusBadRequest, "INVALID_KEY_PAIR", "public_key and private_key must be valid Ed25519 keys")
		return
	}
	if req.Credential.ProfileID == uuid.Nil {
		req.Credential.ProfileID = req.ProfileID
	}
	keyPair := mls.Ed25519KeyPair{
		PublicKey:  ed25519.PublicKey(req.PublicKey),
		PrivateKey: ed25519.PrivateKey(req.PrivateKey),
	}

	kp, err := h.kpMgr.GenerateKeyPackage(r.Context(), req.ProfileID, req.Credential, keyPair)
	if err != nil {
		h.writeMLSError(w, r, err, "generate key package")
		return
	}
	writeJSON(w, http.StatusCreated, kp)
}

// GetKeyPackage returns the current key package for a profile.
func (h *MLSHandler) GetKeyPackage(w http.ResponseWriter, r *http.Request) {
	if _, ok := parseWorkspaceID(w, r); !ok {
		return
	}

	profileIDStr := r.URL.Query().Get("profile_id")
	if profileIDStr == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PROFILE_ID", "profile_id query parameter is required")
		return
	}
	profileID, err := uuid.Parse(profileIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PROFILE_ID", "profile_id must be a valid UUID")
		return
	}

	kp, err := h.kpMgr.GetKeyPackage(r.Context(), profileID)
	if err != nil {
		h.writeMLSError(w, r, err, "get key package")
		return
	}
	writeJSON(w, http.StatusOK, kp)
}

// CommitProposals commits pending MLS proposals for the workspace group.
func (h *MLSHandler) CommitProposals(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseWorkspaceID(w, r)
	if !ok {
		return
	}

	var req struct {
		WorkspaceID uuid.UUID `json:"workspace_id"`
		ProfileID   uuid.UUID `json:"profile_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	if !matchesMLSWorkspace(workspaceID, req.WorkspaceID) {
		writeError(w, http.StatusBadRequest, "WORKSPACE_ID_MISMATCH", "workspace_id must match the route workspace")
		return
	}

	commitBytes, err := h.svc.CommitProposals(r.Context(), workspaceID, req.ProfileID)
	if err != nil {
		h.writeMLSError(w, r, err, "commit proposals")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commit_bytes": commitBytes})
}

func matchesMLSWorkspace(routeID, bodyID uuid.UUID) bool {
	return bodyID == uuid.Nil || bodyID == routeID
}

// --- Error helpers ----------------------------------------------------------

func (h *MLSHandler) writeMLSError(w http.ResponseWriter, r *http.Request, err error, operation string) {
	switch {
	case errors.Is(err, mls.ErrMLSGroupNotFound),
		errors.Is(err, mls.ErrNotGroupMember),
		errors.Is(err, mls.ErrMemberNotInGroup),
		errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, mls.ErrKeyPackageExpired),
		errors.Is(err, mls.ErrEpochMismatch),
		errors.Is(err, mls.ErrInvalidCredential):
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
	case errors.Is(err, mls.ErrMemberAlreadyInGroup),
		errors.Is(err, mls.ErrUnauthorizedCommit):
		writeError(w, http.StatusConflict, "CONFLICT", err.Error())
	default:
		log.Ctx(r.Context()).Error().Err(err).Str("operation", strings.TrimSpace(operation)).Msg("mls request failed")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
	}
}
