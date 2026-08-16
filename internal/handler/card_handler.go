// Package handler provides HTTP handlers for Canopy REST endpoints.
// CardHandler implements BE-15 (Cards Endpoints) with real CRUD against
// the CardService interface.
// Spec: SPEC-PL-03.
package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// CardHandler wires the card CRUD HTTP routes to the CardService interface.
type CardHandler struct {
	svc service.CardService
}

// NewCardHandler returns a handler wired to the given CardService.
func NewCardHandler(svc service.CardService) *CardHandler {
	return &CardHandler{svc: svc}
}

// Routes mounts the card endpoints under /cards.
//
//	GET    /             — list cards
//	POST   /             — create card
//	GET    /{card_id}    — get card by ID
//	PATCH  /{card_id}    — update card data
//	DELETE /{card_id}    — dismiss/archive card
func (h *CardHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.ListCards)
	r.Post("/", h.CreateCard)
	r.Get("/{card_id}", h.GetCard)
	r.Patch("/{card_id}", h.UpdateCard)
	r.Delete("/{card_id}", h.ArchiveCard)
	return r
}

// ── Request DTOs ──────────────────────────────────────────────────────

// cardCreateRequest is the JSON body for creating a card.
type cardCreateRequest struct {
	TreeID   uuid.UUID        `json:"treeId"`
	NodeID   uuid.UUID        `json:"nodeId"`
	AppID    string           `json:"appId"`
	CardType service.CardType `json:"cardType"`
	Data     any              `json:"data"`
}

// cardUpdateRequest is the JSON body for updating a card.
type cardUpdateRequest struct {
	Data any `json:"data"`
}

// cardsListResponse wraps a list of card summaries.
type cardsListResponse struct {
	Cards []service.CardSummary `json:"cards"`
}

// ── Handlers ──────────────────────────────────────────────────────────

// ListCards returns a paginated list of cards filtered by tree/node.
func (h *CardHandler) ListCards(w http.ResponseWriter, r *http.Request) {
	var treeID, nodeID *uuid.UUID

	if raw := r.URL.Query().Get("tree_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_TREE_ID", "tree_id must be a valid UUID")
			return
		}
		treeID = &id
	}
	if raw := r.URL.Query().Get("node_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_NODE_ID", "node_id must be a valid UUID")
			return
		}
		nodeID = &id
	}

	var cardType *service.CardType
	if raw := r.URL.Query().Get("card_type"); raw != "" {
		ct := service.CardType(raw)
		cardType = &ct
	}

	limit := parseIntParam(r.URL.Query().Get("limit"), 50)
	offset := parseIntParam(r.URL.Query().Get("offset"), 0)

	cards, err := h.svc.ListCards(r.Context(), treeID, nodeID, cardType, limit, offset)
	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("card list failed")
		writeError(w, http.StatusInternalServerError, "CARD_LIST_ERROR", "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, cardsListResponse{Cards: cards})
}

// CreateCard creates a new card attached to a node.
func (h *CardHandler) CreateCard(w http.ResponseWriter, r *http.Request) {
	var req cardCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if req.TreeID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_TREE_ID", "treeId is required")
		return
	}
	if req.NodeID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "MISSING_NODE_ID", "nodeId is required")
		return
	}
	if req.AppID == "" {
		writeError(w, http.StatusBadRequest, "MISSING_APP_ID", "appId is required")
		return
	}
	if req.CardType == "" {
		writeError(w, http.StatusBadRequest, "MISSING_CARD_TYPE", "cardType is required")
		return
	}

	card, err := h.svc.CreateCard(r.Context(), req.TreeID, req.NodeID, req.AppID, req.CardType, req.Data)
	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("card create failed")
		writeError(w, http.StatusInternalServerError, "CARD_CREATE_ERROR", "internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, card)
}

// GetCard retrieves a single card by ID.
func (h *CardHandler) GetCard(w http.ResponseWriter, r *http.Request) {
	cardID, ok := parseCardID(w, r)
	if !ok {
		return
	}

	card, err := h.svc.GetCard(r.Context(), cardID)
	if err != nil {
		writeError(w, http.StatusNotFound, "CARD_NOT_FOUND", "card not found")
		return
	}

	writeJSON(w, http.StatusOK, card)
}

// UpdateCard updates a card's data payload.
func (h *CardHandler) UpdateCard(w http.ResponseWriter, r *http.Request) {
	cardID, ok := parseCardID(w, r)
	if !ok {
		return
	}

	var req cardUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if req.Data == nil {
		writeError(w, http.StatusBadRequest, "MISSING_DATA", "data field is required")
		return
	}

	card, err := h.svc.UpdateCardData(r.Context(), cardID, req.Data)
	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("card update failed")
		writeError(w, http.StatusInternalServerError, "CARD_UPDATE_ERROR", "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, card)
}

// ArchiveCard dismisses or archives a card.
func (h *CardHandler) ArchiveCard(w http.ResponseWriter, r *http.Request) {
	cardID, ok := parseCardID(w, r)
	if !ok {
		return
	}

	if err := h.svc.ArchiveCard(r.Context(), cardID); err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("card archive failed")
		writeError(w, http.StatusInternalServerError, "CARD_ARCHIVE_ERROR", "internal server error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── Helpers ───────────────────────────────────────────────────────────

// parseCardID reads and validates the {card_id} chi URL parameter.
func parseCardID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "card_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CARD_ID", "card_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}
