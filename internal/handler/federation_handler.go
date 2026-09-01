package handler

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/federation"
)

type FederationHandler struct {
	svc       federation.FederationService
	serverURL string
}

func NewFederationHandler(svc federation.FederationService, serverURL string) *FederationHandler {
	return &FederationHandler{svc: svc, serverURL: serverURL}
}

func (h *FederationHandler) LinkRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.CreateLink)
	r.Get("/", h.ListLinks)
	r.Delete("/{peer_id}", h.RevokeLink)
	return r
}

type createFederationLinkRequest struct {
	RemoteServerURL string    `json:"remote_server_url"`
	LocalProfileID  uuid.UUID `json:"local_profile_id"`
	TreeID          uuid.UUID `json:"tree_id"`
}

type federationLinkResponse struct {
	PeerID          uuid.UUID  `json:"peer_id"`
	TreeID          uuid.UUID  `json:"tree_id"`
	RemoteServerURL string     `json:"remote_server_url"`
	State           string     `json:"state"`
	ConnectedAt     *time.Time `json:"connected_at"`
}

type federationLinksResponse struct {
	Links []federationLinkResponse `json:"links"`
}

func (h *FederationHandler) CreateLink(w http.ResponseWriter, r *http.Request) {
	var req createFederationLinkRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	peer, _, err := h.svc.CreateFederationLink(r.Context(), req.RemoteServerURL, req.LocalProfileID, req.TreeID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, linkResponse(peer))
}

func (h *FederationHandler) ListLinks(w http.ResponseWriter, r *http.Request) {
	peers, err := h.svc.ListAllPeers(r.Context())
	if err != nil {
		h.writeError(w, err)
		return
	}
	links := make([]federationLinkResponse, 0, len(peers))
	for _, peer := range peers {
		links = append(links, linkResponse(peer))
	}
	writeJSON(w, http.StatusOK, federationLinksResponse{Links: links})
}

func (h *FederationHandler) RevokeLink(w http.ResponseWriter, r *http.Request) {
	peerID, err := uuid.Parse(chi.URLParam(r, "peer_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PEER_ID", "peer_id must be a valid UUID")
		return
	}
	if err := h.svc.RevokeFederationLink(r.Context(), peerID); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type federationHandshakeRequest struct {
	Token          string `json:"token"`
	ServerURL      string `json:"server_url"`
	ECDHEPublicKey string `json:"ecdhe_public_key"`
}

type federationHandshakeResponse struct {
	PeerID         uuid.UUID `json:"peer_id"`
	ServerURL      string    `json:"server_url"`
	ECDHEPublicKey string    `json:"ecdhe_public_key"`
}

// FederationAuthMiddleware implements SPEC-FTR-02 §5's dual authentication
// boundary for the handshake route. Existing user JWTs remain accepted for
// local diagnostics; P2P callers authenticate with the same federation token
// carried in their request body.
func FederationAuthMiddleware(jwtSecret string, svc federation.FederationService) func(http.Handler) http.Handler {
	jwtAuth := AuthMiddleware(jwtSecret)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parts := strings.Fields(r.Header.Get("Authorization"))
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				if _, err := svc.VerifyToken(parts[1]); err == nil {
					next.ServeHTTP(w, r)
					return
				}
			}
			jwtAuth(next).ServeHTTP(w, r)
		})
	}
}

func (h *FederationHandler) Handshake(w http.ResponseWriter, r *http.Request) {
	var req federationHandshakeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	key, err := base64.StdEncoding.DecodeString(req.ECDHEPublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ECDHE_PUBLIC_KEY", "ecdhe_public_key must be base64")
		return
	}
	peer, err := h.svc.AcceptFederationLink(r.Context(), req.Token, req.ServerURL, key)
	if err != nil {
		h.writeError(w, err)
		return
	}
	// P1 persists but does not use the peer key. A local response key is not
	// generated until P2, so the deterministic empty byte string is returned.
	writeJSON(w, http.StatusOK, federationHandshakeResponse{PeerID: peer.ID, ServerURL: h.serverURL, ECDHEPublicKey: base64.StdEncoding.EncodeToString(nil)})
}

func linkResponse(peer *federation.FederationPeer) federationLinkResponse {
	return federationLinkResponse{peer.ID, peer.TreeID, peer.ServerURL, peer.State.String(), peer.ConnectedAt}
}

func (h *FederationHandler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, federation.ErrTokenInvalid), errors.Is(err, federation.ErrTokenExpired):
		writeError(w, http.StatusUnauthorized, "FEDERATION_TOKEN_INVALID", "invalid or expired federation token")
	case errors.Is(err, federation.ErrLinkRevoked):
		writeError(w, http.StatusGone, "FEDERATION_LINK_REVOKED", "federation link has been revoked")
	case errors.Is(err, federation.ErrFederationNotFound):
		writeError(w, http.StatusNotFound, "FEDERATION_PEER_NOT_FOUND", "federation peer not found")
	case errors.Is(err, federation.ErrLinkAlreadyExists):
		writeError(w, http.StatusConflict, "FEDERATION_LINK_EXISTS", "federation link already exists")
	case errors.Is(err, federation.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "federation link fields are invalid")
	default:
		log.Error().Err(err).Msg("federation handler")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "federation operation failed")
	}
}
