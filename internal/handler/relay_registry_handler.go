package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/coding-hermes/hermes-canopy/internal/relay"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type RelayRegistryService interface {
	RegisterInstance(context.Context, *relay.RegisterRequest) (*relay.RegisterResponse, error)
	GetAvailableRelays(context.Context, uuid.UUID) ([]relay.RelayNodeInfo, error)
	DeleteInstance(context.Context, uuid.UUID) error
}

type RelayRegistryHandler struct{ registry RelayRegistryService }

func NewRelayRegistryHandler(registry RelayRegistryService) *RelayRegistryHandler {
	return &RelayRegistryHandler{registry: registry}
}

func (h *RelayRegistryHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.With(requireAdmin).Post("/instances", h.Register)
	r.With(requireAdmin).Delete("/instances/{instance_id}", h.Delete)
	r.Get("/", h.Discover)
	return r
}

func (h *RelayRegistryHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req relay.RegisterRequest
	if err := decodeJSON(r, &req); err != nil || req.TenantID == uuid.Nil || len(req.PublicKey) != 32 || req.ListenAddr == "" || req.Tier == "" || req.ProvisioningToken == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "tenant_id, 32-byte public_key, listen_addr, tier, and provisioning_token are required")
		return
	}
	response, err := h.registry.RegisterInstance(r.Context(), &req)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *RelayRegistryHandler) Delete(w http.ResponseWriter, r *http.Request) {
	instanceID, err := uuid.Parse(chi.URLParam(r, "instance_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INSTANCE_ID", "instance_id must be a UUID")
		return
	}
	if err := h.registry.DeleteInstance(r.Context(), instanceID); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RelayRegistryHandler) Discover(w http.ResponseWriter, r *http.Request) {
	tenantID := TenantIDFromContext(r.Context())
	if tenantID == uuid.Nil {
		writeError(w, http.StatusForbidden, "TENANT_SCOPE_REQUIRED", "authenticated token must include tenant_id")
		return
	}
	relays, err := h.registry.GetAvailableRelays(r.Context(), tenantID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	if len(relays) == 0 {
		writeError(w, http.StatusServiceUnavailable, "no_available_relays", "All relay nodes for this tenant are currently unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant_id": tenantID, "relays": relays})
}

func (h *RelayRegistryHandler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, relay.ErrProvisioningTokenInvalid):
		writeError(w, http.StatusUnauthorized, "PROVISIONING_TOKEN_INVALID", "invalid or expired provisioning token")
	case errors.Is(err, relay.ErrProvisioningTokenScope), errors.Is(err, relay.ErrProvisioningTokenUsed):
		writeError(w, http.StatusForbidden, "PROVISIONING_TOKEN_FORBIDDEN", err.Error())
	case errors.Is(err, relay.ErrInstanceNotFound):
		writeError(w, http.StatusNotFound, "RELAY_INSTANCE_NOT_FOUND", "relay instance not found")
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
	}
}
