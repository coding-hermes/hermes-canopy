package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

type cardListContractService struct {
	service.CardService
	cards []service.CardSummary
	err   error
	calls int
}

func (s *cardListContractService) ListCards(_ context.Context, _ *uuid.UUID, _ *uuid.UUID, cardType *service.CardType, _, _ int) ([]service.CardSummary, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if cardType == nil {
		return s.cards, nil
	}
	return s.cards, nil
}

func cardListContractRouter(svc service.CardService) http.Handler {
	r := chi.NewRouter()
	r.Mount("/cards", NewCardHandler(svc).Routes())
	return r
}

func cardListContractRequest(t *testing.T, h http.Handler, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/cards/?"+query, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListCardsInvalidCardTypeIsClientError(t *testing.T) {
	svc := &cardListContractService{}
	rec := cardListContractRequest(t, cardListContractRouter(svc), "card_type=nosuch")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body apiErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if body.Error.Code != "INVALID_CARD_TYPE" {
		t.Fatalf("error code = %q, want INVALID_CARD_TYPE", body.Error.Code)
	}
	for _, value := range []string{"card_type", "compact", "expanded", "iteration"} {
		if !strings.Contains(body.Error.Message, value) {
			t.Errorf("error message %q does not name %q", body.Error.Message, value)
		}
	}
	if svc.calls != 0 {
		t.Fatalf("service calls = %d, want 0 for invalid card_type", svc.calls)
	}
}

func TestListCardsValidTypeKeepsRESTTypeWireKey(t *testing.T) {
	svc := &cardListContractService{cards: []service.CardSummary{{
		ID: uuid.New(), Type: service.CardTypeCompact,
	}}}
	rec := cardListContractRequest(t, cardListContractRouter(svc), "card_type=compact")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Cards []map[string]json.RawMessage `json:"cards"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(body.Cards) != 1 {
		t.Fatalf("cards length = %d, want 1", len(body.Cards))
	}
	if _, ok := body.Cards[0]["type"]; !ok {
		t.Fatalf("REST card summary is missing wire key type: %s", rec.Body.String())
	}
	if _, ok := body.Cards[0]["card_type"]; ok {
		t.Fatalf("REST card summary unexpectedly contains card_type: %s", rec.Body.String())
	}
	if svc.calls != 1 {
		t.Fatalf("service calls = %d, want 1", svc.calls)
	}
}

func TestListCardsServiceFailureRemainsInternalError(t *testing.T) {
	svc := &cardListContractService{err: errors.New("card store unavailable")}
	rec := cardListContractRequest(t, cardListContractRouter(svc), "card_type=compact")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	var body apiErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if body.Error.Code != "CARD_LIST_ERROR" {
		t.Fatalf("error code = %q, want CARD_LIST_ERROR", body.Error.Code)
	}
	if svc.calls != 1 {
		t.Fatalf("service calls = %d, want 1", svc.calls)
	}
}
