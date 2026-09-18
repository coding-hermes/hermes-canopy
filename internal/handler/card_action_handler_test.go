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

	"github.com/coding-hermes/hermes-canopy/internal/card"
	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// newCardActionRouter mounts the real card routes the way production mounts
// them (/cards), so the {card_id} path parameter and the /{card_id}/actions
// sub-route are exercised through chi's routing table rather than by calling the
// handler method directly. The card surface needs no database: cards live in
// per-type SQLite stores under a temp directory.
func newCardActionRouter(svc service.CardService) http.Handler {
	r := chi.NewRouter()
	r.Mount("/cards", NewCardHandler(svc).Routes())
	return r
}

// cardActionStore returns a manager-backed service plus the repository holding
// the seeded card, so a single test can drive HTTP and read the event log.
func cardActionStore(t *testing.T, actions ...card.CardAction) (*card.CardServiceImpl, card.CardRepository, uuid.UUID) {
	t.Helper()
	mgr := card.NewCardDBManager(t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	repo, err := mgr.Repository(card.CardTypeCompact)
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	created, err := repo.Create(context.Background(), card.CreateCardInput{
		ID:          uuid.New(),
		TreeID:      uuid.New(),
		NodeID:      uuid.New(),
		AppID:       "action-http-test",
		CardType:    card.CardTypeCompact,
		Data:        json.RawMessage(`{"title":"HTTP action card"}`),
		Actions:     actions,
		ContextHash: "abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123abc1",
	})
	if err != nil {
		t.Fatalf("Create card: %v", err)
	}
	return card.NewCardServiceImpl(mgr), repo, created.ID
}

// postCardAction submits body to /cards/{cardID}/actions.
func postCardAction(t *testing.T, h http.Handler, cardID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/cards/"+cardID+"/actions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// decodeCardActionError decodes the JSON error envelope of a failed response.
func decodeCardActionError(t *testing.T, rec *httptest.ResponseRecorder) apiError {
	t.Helper()
	var body apiErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error envelope %q: %v", rec.Body.String(), err)
	}
	return body.Error
}

// cardEventTypes lists the card's events in sequence order.
func cardEventTypes(t *testing.T, repo card.CardRepository, cardID uuid.UUID) []card.CardEventType {
	t.Helper()
	events, err := repo.ListEvents(context.Background(), cardID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	out := make([]card.CardEventType, 0, len(events))
	for _, e := range events {
		out = append(out, e.EventType)
	}
	return out
}

// requireNoActionEvents fails when the card acquired any action event.
func requireNoActionEvents(t *testing.T, repo card.CardRepository, cardID uuid.UUID) {
	t.Helper()
	for _, e := range cardEventTypes(t, repo, cardID) {
		if e == card.EventActionRequested || e == card.EventActionCompleted || e == card.EventAgentError {
			t.Fatalf("rejected request appended a %s event", e)
		}
	}
}

// failingCardActionExecutor is a declared action whose execution boundary fails
// — the agent_error arm of SPEC-PL-03 §4.4 invariant 6.
type failingCardActionExecutor struct{}

func (failingCardActionExecutor) ExecuteCardAction(context.Context, *card.Card, card.CardAction, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("adapter exploded")
}

// ---------------------------------------------------------------------------
// AC1: a declared action succeeds over HTTP
// ---------------------------------------------------------------------------

func TestCardActionRouteDeclaredActionSucceeds(t *testing.T) {
	svc, repo, cardID := cardActionStore(t,
		card.CardAction{Label: "Open", Handler: "open"},
		card.CardAction{Label: "Dismiss", Handler: "dismiss"},
	)

	rec := postCardAction(t, newCardActionRouter(svc), cardID.String(), `{"handler":"open","payload":{"nodeId":"n1"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var outcome service.CardActionOutcome
	if err := json.Unmarshal(rec.Body.Bytes(), &outcome); err != nil {
		t.Fatalf("decode outcome %q: %v", rec.Body.String(), err)
	}
	if outcome.CardID != cardID {
		t.Errorf("card_id = %s, want %s", outcome.CardID, cardID)
	}
	if outcome.Handler != "open" {
		t.Errorf("handler = %q, want open", outcome.Handler)
	}
	if outcome.Status != service.CardActionStatusCompleted {
		t.Errorf("status = %q, want %q", outcome.Status, service.CardActionStatusCompleted)
	}
	if outcome.ResultEventType != string(card.EventActionCompleted) {
		t.Errorf("result_event_type = %q, want %q", outcome.ResultEventType, card.EventActionCompleted)
	}
	if outcome.ResultEventID == uuid.Nil {
		t.Error("result_event_id is nil")
	}
	if string(outcome.Payload) != `{"nodeId":"n1"}` {
		t.Errorf("payload = %s, want the submitted payload", outcome.Payload)
	}

	// The response's sequences are the durable event sequences, not invented
	// ones: the card really holds action_requested then action_completed.
	events, err := repo.ListEvents(context.Background(), cardID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("event log = %v, want [action_requested action_completed]", cardEventTypes(t, repo, cardID))
	}
	if events[0].EventType != card.EventActionRequested || events[1].EventType != card.EventActionCompleted {
		t.Fatalf("event log = %v, want [action_requested action_completed]", cardEventTypes(t, repo, cardID))
	}
	if outcome.RequestedSeq != events[0].Sequence {
		t.Errorf("requested_seq = %d, want the action_requested sequence %d", outcome.RequestedSeq, events[0].Sequence)
	}
	if outcome.ResultSeq != events[1].Sequence {
		t.Errorf("result_seq = %d, want the action_completed sequence %d", outcome.ResultSeq, events[1].Sequence)
	}
	if outcome.ResultSeq != outcome.RequestedSeq+1 {
		t.Errorf("result_seq = %d, want requested_seq+1 = %d", outcome.ResultSeq, outcome.RequestedSeq+1)
	}
	if events[0].CardID != cardID || events[1].CardID != cardID {
		t.Error("events were appended against a different card")
	}
}

// ---------------------------------------------------------------------------
// AC2: an undeclared handler is rejected and appends no event
// ---------------------------------------------------------------------------

func TestCardActionRouteUndeclaredHandlerIsRejected(t *testing.T) {
	svc, repo, cardID := cardActionStore(t,
		card.CardAction{Label: "Open", Handler: "open"},
		card.CardAction{Label: "Dismiss", Handler: "dismiss"},
	)

	rec := postCardAction(t, newCardActionRouter(svc), cardID.String(), `{"handler":"delete","payload":{}}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	body := decodeCardActionError(t, rec)
	if body.Code != "CARD_ACTION_NOT_DECLARED" {
		t.Errorf("error code = %q, want CARD_ACTION_NOT_DECLARED", body.Code)
	}
	if !strings.Contains(body.Message, "delete") {
		t.Errorf("error message = %q, want it to name the rejected handler", body.Message)
	}
	if got := cardEventTypes(t, repo, cardID); len(got) != 0 {
		t.Fatalf("event log = %v, want empty — an undeclared handler must append nothing", got)
	}

	// A declared handler on the same card still works, and no action_completed
	// exists for the rejected handler.
	rec = postCardAction(t, newCardActionRouter(svc), cardID.String(), `{"handler":"open"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("declared action after rejection: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got := cardEventTypes(t, repo, cardID)
	if len(got) != 2 || got[0] != card.EventActionRequested || got[1] != card.EventActionCompleted {
		t.Fatalf("event log = %v, want [action_requested action_completed]", got)
	}
}

// ---------------------------------------------------------------------------
// AC4: invalid requests and unknown cards are deliberate client errors
// ---------------------------------------------------------------------------

func TestCardActionRouteInvalidRequests(t *testing.T) {
	svc, repo, cardID := cardActionStore(t, card.CardAction{Label: "Open", Handler: "open"})
	h := newCardActionRouter(svc)

	cases := []struct {
		name     string
		cardID   string
		body     string
		wantCode int
		wantErr  string
	}{
		{"malformed JSON", cardID.String(), `{"handler":`, http.StatusBadRequest, "INVALID_JSON"},
		{"empty body", cardID.String(), ``, http.StatusBadRequest, "INVALID_JSON"},
		{"handler wrong type", cardID.String(), `{"handler":42}`, http.StatusBadRequest, "INVALID_JSON"},
		{"unknown field", cardID.String(), `{"handler":"open","bogus":true}`, http.StatusBadRequest, "INVALID_JSON"},
		{"payload malformed", cardID.String(), `{"handler":"open","payload":{oops}}`, http.StatusBadRequest, "INVALID_JSON"},
		{"payload is a string", cardID.String(), `{"handler":"open","payload":"{oops"}`, http.StatusBadRequest, "INVALID_PAYLOAD"},
		{"payload is an array", cardID.String(), `{"handler":"open","payload":[1,2]}`, http.StatusBadRequest, "INVALID_PAYLOAD"},
		{"payload is a number", cardID.String(), `{"handler":"open","payload":5}`, http.StatusBadRequest, "INVALID_PAYLOAD"},
		{"empty handler", cardID.String(), `{"handler":""}`, http.StatusBadRequest, "MISSING_HANDLER"},
		{"whitespace handler", cardID.String(), `{"handler":"   "}`, http.StatusBadRequest, "MISSING_HANDLER"},
		{"missing handler field", cardID.String(), `{"payload":{}}`, http.StatusBadRequest, "MISSING_HANDLER"},
		{"card id not a uuid", "not-a-uuid", `{"handler":"open"}`, http.StatusBadRequest, "INVALID_CARD_ID"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postCardAction(t, h, tc.cardID, tc.body)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if got := decodeCardActionError(t, rec).Code; got != tc.wantErr {
				t.Errorf("error code = %q, want %q; body=%s", got, tc.wantErr, rec.Body.String())
			}
		})
	}

	requireNoActionEvents(t, repo, cardID)
}

func TestCardActionRouteUnknownCardReturns404(t *testing.T) {
	svc, repo, knownCardID := cardActionStore(t, card.CardAction{Label: "Open", Handler: "open"})

	unknown := uuid.New()
	rec := postCardAction(t, newCardActionRouter(svc), unknown.String(), `{"handler":"open","payload":{}}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeCardActionError(t, rec).Code; got != "CARD_NOT_FOUND" {
		t.Errorf("error code = %q, want CARD_NOT_FOUND", got)
	}
	requireNoActionEvents(t, repo, knownCardID)
}

// ---------------------------------------------------------------------------
// AC3: execution failure over HTTP records agent_error
// ---------------------------------------------------------------------------

func TestCardActionRouteExecutionFailureReturns500AndRecordsAgentError(t *testing.T) {
	svc, repo, cardID := cardActionStore(t, card.CardAction{Label: "Open", Handler: "open"})
	svc.WithActionExecutor(failingCardActionExecutor{})

	rec := postCardAction(t, newCardActionRouter(svc), cardID.String(), `{"handler":"open","payload":{"nodeId":"n1"}}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeCardActionError(t, rec).Code; got != "CARD_ACTION_FAILED" {
		t.Errorf("error code = %q, want CARD_ACTION_FAILED", got)
	}

	got := cardEventTypes(t, repo, cardID)
	if len(got) != 2 || got[0] != card.EventActionRequested || got[1] != card.EventAgentError {
		t.Fatalf("event log = %v, want [action_requested agent_error]", got)
	}
	for _, e := range got {
		if e == card.EventActionCompleted {
			t.Fatal("a failed action recorded action_completed")
		}
	}

	events, err := repo.ListEvents(context.Background(), cardID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if events[1].Sequence != events[0].Sequence+1 {
		t.Errorf("agent_error sequence = %d, want %d", events[1].Sequence, events[0].Sequence+1)
	}
	var failurePayload struct {
		Handler string `json:"handler"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(events[1].Payload, &failurePayload); err != nil {
		t.Fatalf("decode agent_error payload %s: %v", events[1].Payload, err)
	}
	if failurePayload.Handler != "open" || failurePayload.Error == "" {
		t.Errorf("agent_error payload = %+v, want the handler and a failure reason", failurePayload)
	}
}

func TestCardActionRouteNullPayloadCompletes(t *testing.T) {
	svc, repo, cardID := cardActionStore(t, card.CardAction{Label: "Open", Handler: "open"})

	rec := postCardAction(t, newCardActionRouter(svc), cardID.String(), `{"handler":"open","payload":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var outcome service.CardActionOutcome
	if err := json.Unmarshal(rec.Body.Bytes(), &outcome); err != nil {
		t.Fatalf("decode outcome: %v", err)
	}
	if string(outcome.Payload) != `{}` {
		t.Errorf("payload = %s, want {}", outcome.Payload)
	}
	got := cardEventTypes(t, repo, cardID)
	if len(got) != 2 || got[1] != card.EventActionCompleted {
		t.Fatalf("event log = %v, want [action_requested action_completed]", got)
	}
}

// ---------------------------------------------------------------------------
// Sentinel → status mapping (the handler's contract, independent of the store)
// ---------------------------------------------------------------------------

// stubCardActionService overrides only SubmitCardAction; the embedded interface
// keeps every other CardService method satisfied.
type stubCardActionService struct {
	service.CardService

	outcome    *service.CardActionOutcome
	err        error
	calls      int
	gotHandler string
	gotPayload json.RawMessage
}

func (s *stubCardActionService) SubmitCardAction(_ context.Context, _ uuid.UUID, handler string, payload json.RawMessage) (*service.CardActionOutcome, error) {
	s.calls++
	s.gotHandler = handler
	s.gotPayload = payload
	return s.outcome, s.err
}

func TestCardActionErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantErr  string
	}{
		{"handler required", service.ErrCardActionHandlerRequired, http.StatusBadRequest, "MISSING_HANDLER"},
		{"payload invalid", service.ErrCardActionPayloadInvalid, http.StatusBadRequest, "INVALID_PAYLOAD"},
		{"card not found", service.ErrCardNotFound, http.StatusNotFound, "CARD_NOT_FOUND"},
		{"not declared", service.ErrCardActionNotDeclared, http.StatusUnprocessableEntity, "CARD_ACTION_NOT_DECLARED"},
		{"execution failed", service.ErrCardActionFailed, http.StatusInternalServerError, "CARD_ACTION_FAILED"},
		{"unexpected failure", errors.New("boom"), http.StatusInternalServerError, "CARD_ACTION_ERROR"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubCardActionService{err: tc.err}
			rec := postCardAction(t, newCardActionRouter(svc), uuid.New().String(), `{"handler":"open"}`)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if got := decodeCardActionError(t, rec).Code; got != tc.wantErr {
				t.Errorf("error code = %q, want %q", got, tc.wantErr)
			}
			if svc.calls != 1 {
				t.Errorf("service calls = %d, want 1", svc.calls)
			}
		})
	}
}

func TestCardActionRouteTrimsHandlerBeforeSubmit(t *testing.T) {
	outcome := &service.CardActionOutcome{
		CardID:          uuid.New(),
		Handler:         "open",
		Status:          service.CardActionStatusCompleted,
		RequestedSeq:    1,
		ResultSeq:       2,
		ResultEventType: string(card.EventActionCompleted),
		Payload:         json.RawMessage(`{}`),
	}
	svc := &stubCardActionService{outcome: outcome}

	rec := postCardAction(t, newCardActionRouter(svc), uuid.New().String(), `{"handler":"  open  ","payload":{"a":1}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if svc.gotHandler != "open" {
		t.Errorf("service handler = %q, want the trimmed handler open", svc.gotHandler)
	}
	if string(svc.gotPayload) != `{"a":1}` {
		t.Errorf("service payload = %s, want the submitted payload", svc.gotPayload)
	}
}
