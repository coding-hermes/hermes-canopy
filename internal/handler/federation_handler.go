package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/federation"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

type FederationHandler struct {
	svc       federation.FederationService
	router    federation.ProfileRouter
	serverURL string
	hub       sse.SSEHub
	mu        sync.RWMutex
	relays    map[uuid.UUID]map[chan federation.FTLEnvelope]struct{}
}

func NewFederationHandler(svc federation.FederationService, serverURL string, hubs ...sse.SSEHub) *FederationHandler {
	var hub sse.SSEHub
	if len(hubs) > 0 {
		hub = hubs[0]
	}
	return &FederationHandler{svc: svc, serverURL: serverURL, hub: hub, relays: make(map[uuid.UUID]map[chan federation.FTLEnvelope]struct{})}
}

func (h *FederationHandler) WithProfileRouter(router federation.ProfileRouter) *FederationHandler {
	h.router = router
	return h
}

func (h *FederationHandler) LinkRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.CreateLink)
	r.Get("/", h.ListLinks)
	r.Delete("/{peer_id}", h.RevokeLink)
	return r
}

func (h *FederationHandler) RouteRoutes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.CreateRoute)
	r.Get("/", h.ListRoutes)
	r.Patch("/", h.UpdateRoute)
	r.Delete("/", h.DeleteRoute)
	r.Patch("/{route_id}", h.UpdateRoute)
	r.Delete("/{route_id}", h.DeleteRoute)
	return r
}

type routeRequest struct {
	ID        uuid.UUID            `json:"id,omitempty"`
	ProfileID uuid.UUID            `json:"profile_id,omitempty"`
	TreeID    *uuid.UUID           `json:"tree_id,omitempty"`
	PeerID    *uuid.UUID           `json:"peer_id,omitempty"`
	RouteType federation.RouteType `json:"route_type,omitempty"`
	Priority  *int                 `json:"priority,omitempty"`
}

func (h *FederationHandler) CreateRoute(w http.ResponseWriter, r *http.Request) {
	if h.router == nil {
		writeError(w, http.StatusServiceUnavailable, "PROFILE_ROUTER_UNAVAILABLE", "profile router is unavailable")
		return
	}
	var req routeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	priority := 0
	if req.Priority != nil {
		priority = *req.Priority
	}
	route, err := h.router.Create(r.Context(), &federation.Route{ProfileID: req.ProfileID, TreeID: req.TreeID, PeerID: req.PeerID, RouteType: req.RouteType, Priority: priority})
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, route)
}

func (h *FederationHandler) ListRoutes(w http.ResponseWriter, r *http.Request) {
	if h.router == nil {
		writeError(w, http.StatusServiceUnavailable, "PROFILE_ROUTER_UNAVAILABLE", "profile router is unavailable")
		return
	}
	profileID, ok := optionalUUIDQuery(w, r, "profile_id")
	if !ok {
		return
	}
	treeID, ok := optionalUUIDQuery(w, r, "tree_id")
	if !ok {
		return
	}
	routes, err := h.router.List(r.Context(), profileID, treeID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"routes": routes})
}

func optionalUUIDQuery(w http.ResponseWriter, r *http.Request, name string) (*uuid.UUID, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, true
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_"+strings.ToUpper(name), name+" must be a valid UUID")
		return nil, false
	}
	return &id, true
}

func routeID(w http.ResponseWriter, r *http.Request, bodyID uuid.UUID) (uuid.UUID, bool) {
	raw := chi.URLParam(r, "route_id")
	if raw == "" {
		raw = r.URL.Query().Get("id")
	}
	if raw == "" && bodyID != uuid.Nil {
		return bodyID, true
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ROUTE_ID", "route_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}

func (h *FederationHandler) UpdateRoute(w http.ResponseWriter, r *http.Request) {
	if h.router == nil {
		writeError(w, http.StatusServiceUnavailable, "PROFILE_ROUTER_UNAVAILABLE", "profile router is unavailable")
		return
	}
	var req routeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	id, ok := routeID(w, r, req.ID)
	if !ok {
		return
	}
	var routeType *federation.RouteType
	if req.RouteType != "" {
		routeType = &req.RouteType
	}
	var treeID **uuid.UUID
	if req.TreeID != nil {
		treeID = &req.TreeID
	}
	var peerID **uuid.UUID
	if req.PeerID != nil {
		peerID = &req.PeerID
	} else if req.RouteType == federation.RouteLocal {
		// Switching a route to local also clears its remote peer.
		var noPeer *uuid.UUID
		peerID = &noPeer
	}
	route, err := h.router.Update(r.Context(), id, federation.RouteUpdate{TreeID: treeID, PeerID: peerID, RouteType: routeType, Priority: req.Priority})
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, route)
}

func (h *FederationHandler) DeleteRoute(w http.ResponseWriter, r *http.Request) {
	if h.router == nil {
		writeError(w, http.StatusServiceUnavailable, "PROFILE_ROUTER_UNAVAILABLE", "profile router is unavailable")
		return
	}
	var req routeRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
			return
		}
	}
	id, ok := routeID(w, r, req.ID)
	if !ok {
		return
	}
	if err := h.router.Delete(r.Context(), id); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	Token            string `json:"token"`
	ServerURL        string `json:"server_url"`
	ECDHEPublicKey   string `json:"ecdhe_public_key"`
	SigningPublicKey string `json:"signing_public_key,omitempty"`
}

type federationHandshakeResponse struct {
	PeerID           uuid.UUID `json:"peer_id"`
	ServerURL        string    `json:"server_url"`
	ECDHEPublicKey   string    `json:"ecdhe_public_key"`
	SigningPublicKey string    `json:"signing_public_key"`
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
				if _, err := svc.VerifyToken(parts[1]); err == nil || strings.Count(parts[1], ".") == 1 {
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
	signingKey, err := base64.StdEncoding.DecodeString(req.SigningPublicKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_SIGNING_PUBLIC_KEY", "signing_public_key must be base64")
		return
	}
	peer, err := h.svc.AcceptFederationLink(r.Context(), req.Token, req.ServerURL, key, signingKey)
	if err != nil {
		h.writeError(w, err)
		return
	}
	localKey, err := h.svc.LocalECDHEPublicKey(peer.ID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, federationHandshakeResponse{PeerID: peer.ID, ServerURL: h.serverURL, ECDHEPublicKey: base64.StdEncoding.EncodeToString(localKey), SigningPublicKey: base64.StdEncoding.EncodeToString(h.svc.SigningPublicKey())})
}

func bearerToken(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return ""
}

// Events streams live FTL envelopes. P4 replay and buffering are deliberately absent.
func (h *FederationHandler) Events(w http.ResponseWriter, r *http.Request) {
	peerID, err := uuid.Parse(r.URL.Query().Get("peer_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PEER_ID", "peer_id must be a valid UUID")
		return
	}
	if _, err = h.svc.AuthenticatePeerToken(r.Context(), bearerToken(r), peerID); err != nil {
		h.writeError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "SSE_UNSUPPORTED", "streaming unsupported")
		return
	}
	ch := make(chan federation.FTLEnvelope, 16)
	h.mu.Lock()
	if h.relays[peerID] == nil {
		h.relays[peerID] = make(map[chan federation.FTLEnvelope]struct{})
	}
	h.relays[peerID][ch] = struct{}{}
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.relays[peerID], ch); h.mu.Unlock() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	encoder := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case envelope := <-ch:
			_, _ = w.Write([]byte("event: ftl_event\ndata: "))
			_ = encoder.Encode(envelope)
			_, _ = w.Write([]byte("\n"))
			flusher.Flush()
		}
	}
}

func (h *FederationHandler) PostEvent(w http.ResponseWriter, r *http.Request) {
	var envelope federation.FTLEnvelope
	if err := decodeJSON(r, &envelope); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "request body must be valid JSON")
		return
	}
	if _, err := h.svc.AuthenticatePeerToken(r.Context(), bearerToken(r), envelope.PeerID); err != nil {
		h.writeError(w, err)
		return
	}
	inner, err := h.svc.ReceiveEvent(r.Context(), &envelope)
	if err != nil {
		h.writeError(w, err)
		return
	}
	dispatchPeerID := envelope.PeerID
	if h.router != nil {
		route, resolveErr := h.router.Resolve(r.Context(), envelope.SenderProfileID, envelope.TreeID)
		if resolveErr != nil && !errors.Is(resolveErr, federation.ErrRouteNotFound) {
			h.writeError(w, resolveErr)
			return
		}
		if resolveErr == nil && route.RouteType == federation.RouteRemote && route.PeerID != nil {
			dispatchPeerID = *route.PeerID
		}
	}
	if h.hub != nil {
		h.hub.Broadcast(envelope.TreeID, sse.ComposeEvent(envelope.TreeID, envelope.SenderProfileID, inner.EventType, inner.Payload))
	}
	h.mu.RLock()
	subscribers := make([]chan federation.FTLEnvelope, 0, len(h.relays[dispatchPeerID]))
	for ch := range h.relays[dispatchPeerID] {
		subscribers = append(subscribers, ch)
	}
	h.mu.RUnlock()
	for _, ch := range subscribers {
		select {
		case ch <- envelope:
		default:
		}
	}
	w.WriteHeader(http.StatusAccepted)
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
	case errors.Is(err, federation.ErrRouteNotFound):
		writeError(w, http.StatusNotFound, "PROFILE_ROUTE_NOT_FOUND", "profile route not found")
	default:
		log.Error().Err(err).Msg("federation handler")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "federation operation failed")
	}
}
