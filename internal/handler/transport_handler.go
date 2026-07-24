// Package handler provides HTTP handlers for Canopy transport REST endpoints.
// SPEC-FTR-04 §6 — exact route contracts.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/transport"
)

// TransportHandler wires the transport REST routes to the transport layer.
type TransportHandler struct {
	adapter    transport.TransportAdapter
	connMgr    *transport.ConnectionManager
	configRepo db.TransportConfigRepo
	eventRepo  db.TransportEventRepo
	nodeID     string
}

// NewTransportHandler returns a handler wired to the transport layer.
func NewTransportHandler(
	adapter transport.TransportAdapter,
	connMgr *transport.ConnectionManager,
	configRepo db.TransportConfigRepo,
	eventRepo db.TransportEventRepo,
	nodeID string,
) *TransportHandler {
	return &TransportHandler{
		adapter:    adapter,
		connMgr:    connMgr,
		configRepo: configRepo,
		eventRepo:  eventRepo,
		nodeID:     nodeID,
	}
}

// Routes mounts the transport endpoints under /api/v1/transports.
func (h *TransportHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/status", h.Status)
	r.Get("/{type}", h.GetConfig)
	r.Put("/{type}", h.UpdateConfig)
	r.Delete("/{type}", h.DisableTransport)
	return r
}

// --- GET /api/v1/transports/status -----------------------------------------

func (h *TransportHandler) Status(w http.ResponseWriter, r *http.Request) {
	configs, err := h.configRepo.List(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("transport handler: list configs")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list transport configs")
		return
	}

	items := make([]transportStatusItem, 0, len(configs))
	for _, cfg := range configs {
		state := "closed"
		if cfg.Enabled {
			state = "active"
		}
		item := transportStatusItem{
			Type:        cfg.TransportType,
			Enabled:     cfg.Enabled,
			State:       state,
			Connections: 0,
			HealthOK:    cfg.Enabled,
		}
		items = append(items, item)
	}

	resp := transportStatusResponse{
		NodeID:          h.nodeID,
		DeploymentMode:  "local",
		NetworkTopology: "loopback",
		Transports:      items,
		ActiveFallback:  map[string]string{},
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- GET /api/v1/transports/{type} -----------------------------------------

func (h *TransportHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	tt := chi.URLParam(r, "type")
	cfg, err := h.configRepo.Get(r.Context(), tt)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "transport type not configured: "+tt)
			return
		}
		log.Error().Err(err).Str("type", tt).Msg("transport handler: get config")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get transport config")
		return
	}

	state := "active"
	if !cfg.Enabled {
		state = "closed"
	}

	resp := transportConfigResponse{
		TransportType:  cfg.TransportType,
		Enabled:        cfg.Enabled,
		MaxMessageSize: cfg.MaxMessageSize,
		HeartbeatSecs:  cfg.HeartbeatSecs,
		ConnectTimeout: cfg.ConnectTimeout,
		ConfigJSON:     cfg.ConfigJSON,
		State:          state,
		Connections:    0,
		UpdatedAt:      cfg.UpdatedAt.Format(time.RFC3339),
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- PUT /api/v1/transports/{type} -----------------------------------------

type updateTransportRequest struct {
	Enabled        *bool                  `json:"enabled,omitempty"`
	MaxMessageSize *int64                 `json:"max_message_size,omitempty"`
	HeartbeatSecs  *int                   `json:"heartbeat_secs,omitempty"`
	ConnectTimeout *int                   `json:"connect_timeout,omitempty"`
	ConfigJSON     map[string]interface{} `json:"config_json,omitempty"`
}

func (h *TransportHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	tt := chi.URLParam(r, "type")
	var req updateTransportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "invalid JSON request body")
		return
	}
	existing, err := h.configRepo.Get(r.Context(), tt)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "transport type not configured: "+tt)
			return
		}
		log.Error().Err(err).Str("type", tt).Msg("transport handler: update config get")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get transport config")
		return
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if req.MaxMessageSize != nil {
		existing.MaxMessageSize = *req.MaxMessageSize
	}
	if req.HeartbeatSecs != nil {
		existing.HeartbeatSecs = *req.HeartbeatSecs
	}
	if req.ConnectTimeout != nil {
		existing.ConnectTimeout = *req.ConnectTimeout
	}
	if req.ConfigJSON != nil {
		existing.ConfigJSON = req.ConfigJSON
	}
	updated, err := h.configRepo.Upsert(r.Context(), existing)
	if err != nil {
		log.Error().Err(err).Str("type", tt).Msg("transport handler: update config upsert")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update transport config")
		return
	}
	state := "active"
	if !updated.Enabled {
		state = "closed"
	}
	resp := transportConfigResponse{
		TransportType:  updated.TransportType,
		Enabled:        updated.Enabled,
		MaxMessageSize: updated.MaxMessageSize,
		HeartbeatSecs:  updated.HeartbeatSecs,
		ConnectTimeout: updated.ConnectTimeout,
		ConfigJSON:     updated.ConfigJSON,
		State:          state,
		Connections:    0,
		UpdatedAt:      updated.UpdatedAt.Format(time.RFC3339),
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- DELETE /api/v1/transports/{type} ---------------------------------------

func (h *TransportHandler) DisableTransport(w http.ResponseWriter, r *http.Request) {
	tt := chi.URLParam(r, "type")
	if err := h.configRepo.Disable(r.Context(), tt); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "transport type not configured: "+tt)
			return
		}
		log.Error().Err(err).Str("type", tt).Msg("transport handler: disable")
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to disable transport")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- GET /health/transports/{type} (no auth required) ----------------------

func (h *TransportHandler) HealthProbe(tt string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		healthErr := h.adapter.Health(r.Context())
		latency := time.Since(start).Milliseconds()
		if healthErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, healthProbeResponse{
				TransportType: tt, Healthy: false, CheckedAt: time.Now().UTC().Format(time.RFC3339),
				LatencyMs: latency, Error: healthErr.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, healthProbeResponse{
			TransportType: tt, Healthy: true, CheckedAt: time.Now().UTC().Format(time.RFC3339),
			LatencyMs: latency,
		})
	}
}

// --- Response shapes (SPEC-FTR-04 §6.2) ------------------------------------

type transportStatusResponse struct {
	NodeID          string                `json:"node_id"`
	DeploymentMode  string                `json:"deployment_mode"`
	NetworkTopology string                `json:"network_topology"`
	Transports      []transportStatusItem `json:"transports"`
	ActiveFallback  map[string]string     `json:"active_fallback_chains"`
}

type transportStatusItem struct {
	Type            string  `json:"type"`
	Enabled         bool    `json:"enabled"`
	State           string  `json:"state"`
	Connections     int     `json:"connections"`
	HealthOK        bool    `json:"health_ok"`
	LastHealthCheck *string `json:"last_health_check"`
}

type transportConfigResponse struct {
	TransportType  string                 `json:"transport_type"`
	Enabled        bool                   `json:"enabled"`
	MaxMessageSize int64                  `json:"max_message_size"`
	HeartbeatSecs  int                    `json:"heartbeat_secs"`
	ConnectTimeout int                    `json:"connect_timeout"`
	ConfigJSON     map[string]interface{} `json:"config_json"`
	State          string                 `json:"state"`
	Connections    int                    `json:"connections"`
	UpdatedAt      string                 `json:"updated_at"`
}

type healthProbeResponse struct {
	TransportType string `json:"transport_type"`
	Healthy       bool   `json:"healthy"`
	CheckedAt     string `json:"checked_at"`
	LatencyMs     int64  `json:"latency_ms,omitempty"`
	Error         string `json:"error,omitempty"`
}
