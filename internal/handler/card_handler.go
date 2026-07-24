// Package handler provides HTTP handlers for Canopy REST endpoints.
// CardHandler implements BE-15 (Cards Endpoints) as stub 501 responses
// until the full implementation is wired in a dedicated worker tick.
package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

// CardHandler wires the card CRUD HTTP routes to the CardService interface.
// Spec: SPEC-PL-03.
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

// ListCards returns a paginated list of cards.
func (h *CardHandler) ListCards(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, "ListCards")
}

// CreateCard creates a new card attached to a node.
func (h *CardHandler) CreateCard(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, "CreateCard")
}

// GetCard retrieves a single card by ID.
func (h *CardHandler) GetCard(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, "GetCard")
}

// UpdateCard updates a card's data payload.
func (h *CardHandler) UpdateCard(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, "UpdateCard")
}

// ArchiveCard dismisses or archives a card.
func (h *CardHandler) ArchiveCard(w http.ResponseWriter, r *http.Request) {
	writeNotImplemented(w, "ArchiveCard")
}
