package card

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// createActionCard seeds a card through the service (so the card_created event
// is recorded exactly as production writes it), declares actions on it, and
// returns the resulting card plus the repository holding it.
func createActionCard(t *testing.T, mgr *CardDBManager, ctype CardType, actions ...CardAction) (*Card, CardRepository) {
	t.Helper()
	ctx := context.Background()

	repo, err := mgr.Repository(ctype)
	if err != nil {
		t.Fatalf("Repository(%s): %v", ctype, err)
	}

	summary, err := NewCardServiceImpl(mgr).CreateCard(
		ctx, uuid.New(), uuid.New(), "action-test", service.CardType(ctype),
		map[string]any{"title": "action card"})
	if err != nil {
		t.Fatalf("CreateCard: %v", err)
	}

	declared := actions
	created, err := repo.Patch(ctx, summary.ID, 1, PatchCardInput{Actions: &declared})
	if err != nil {
		t.Fatalf("Patch(actions): %v", err)
	}
	return created, repo
}

// cardEvents returns every event recorded for a card in sequence order.
func cardEvents(t *testing.T, repo CardRepository, cardID uuid.UUID) []CardEvent {
	t.Helper()
	events, err := repo.ListEvents(context.Background(), cardID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	return events
}

// eventTypes reduces an event slice to its event types, in sequence order.
func eventTypes(events []CardEvent) []CardEventType {
	out := make([]CardEventType, 0, len(events))
	for _, e := range events {
		out = append(out, e.EventType)
	}
	return out
}

// requireEventTypes fails when the card's event log does not match want exactly
// (same types, same order).
func requireEventTypes(t *testing.T, events []CardEvent, want ...CardEventType) {
	t.Helper()
	got := eventTypes(events)
	if len(got) != len(want) {
		t.Fatalf("event log = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event log = %v, want %v", got, want)
		}
	}
}

// requireMonotonicSequences pins the event sequence contract: strictly
// increasing, starting at 1.
func requireMonotonicSequences(t *testing.T, events []CardEvent) {
	t.Helper()
	for i, e := range events {
		if e.Sequence != int64(i+1) {
			t.Fatalf("event %d has sequence %d, want %d (sequence must be monotonic and gap-free)",
				i, e.Sequence, i+1)
		}
	}
}

// decodingPayload decodes an event payload into a generic map.
func decodingPayload(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode payload %s: %v", string(raw), err)
	}
	return m
}

// failingActionExecutor is a deterministic execution failure: it stands in for
// an app adapter (SPEC-PL-03 §4.3 AppCardAdapter.HandleAction) that errored.
type failingActionExecutor struct{ err error }

func (f failingActionExecutor) ExecuteCardAction(context.Context, *Card, CardAction, json.RawMessage) (json.RawMessage, error) {
	return nil, f.err
}

// recordingActionExecutor captures how the service invoked the boundary and
// returns a fixed result.
type recordingActionExecutor struct {
	gotCardID  uuid.UUID
	gotAction  CardAction
	gotPayload json.RawMessage
	result     json.RawMessage
}

func (r *recordingActionExecutor) ExecuteCardAction(_ context.Context, card *Card, action CardAction, payload json.RawMessage) (json.RawMessage, error) {
	r.gotCardID, r.gotAction, r.gotPayload = card.ID, action, payload
	return r.result, nil
}

// ---------------------------------------------------------------------------
// Declared action success (AC1, AC3)
// ---------------------------------------------------------------------------

func TestSubmitCardActionCompletedRecordsEventSequence(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()

	svc := NewCardServiceImpl(mgr)
	ctx := context.Background()

	c, repo := createActionCard(t, mgr, CardTypeCompact,
		CardAction{Label: "Open", Handler: "open"},
		CardAction{Label: "Dismiss", Handler: "dismiss"},
	)

	payload := json.RawMessage(`{"nodeId":"n1"}`)
	outcome, err := svc.SubmitCardAction(ctx, c.ID, "open", payload)
	if err != nil {
		t.Fatalf("SubmitCardAction: %v", err)
	}
	if outcome == nil {
		t.Fatal("SubmitCardAction returned a nil outcome on success")
	}
	if outcome.Status != service.CardActionStatusCompleted {
		t.Errorf("status = %q, want %q", outcome.Status, service.CardActionStatusCompleted)
	}
	if outcome.ResultEventType != string(EventActionCompleted) {
		t.Errorf("result event type = %q, want %q", outcome.ResultEventType, EventActionCompleted)
	}
	if outcome.Handler != "open" || outcome.CardID != c.ID {
		t.Errorf("outcome identity = (%s, %s), want (%s, open)", outcome.CardID, outcome.Handler, c.ID)
	}
	if outcome.ResultEventID == uuid.Nil {
		t.Error("result_event_id is nil")
	}
	if string(outcome.Payload) != string(payload) {
		t.Errorf("outcome payload = %s, want %s", outcome.Payload, payload)
	}

	events := cardEvents(t, repo, c.ID)
	requireEventTypes(t, events, EventCardCreated, EventActionRequested, EventActionCompleted)
	requireMonotonicSequences(t, events)

	// The outcome's sequences are the DURABLE event sequences, and the terminal
	// event is the requested event's immediate successor (AC3).
	requested, completed := events[1], events[2]
	if outcome.RequestedSeq != requested.Sequence {
		t.Errorf("requested_seq = %d, want %d (the action_requested event)", outcome.RequestedSeq, requested.Sequence)
	}
	if outcome.ResultSeq != completed.Sequence {
		t.Errorf("result_seq = %d, want %d (the action_completed event)", outcome.ResultSeq, completed.Sequence)
	}
	if outcome.ResultSeq != outcome.RequestedSeq+1 {
		t.Errorf("result_seq = %d, want requested_seq+1 = %d", outcome.ResultSeq, outcome.RequestedSeq+1)
	}
	if outcome.ResultSeq != 3 {
		t.Errorf("result_seq = %d, want 3 (card_created occupies sequence 1)", outcome.ResultSeq)
	}

	// action_requested carries the handler and the submitted payload.
	if requested.ActorKind != ActorUser {
		t.Errorf("action_requested actor kind = %q, want %q", requested.ActorKind, ActorUser)
	}
	reqPayload := decodingPayload(t, requested.Payload)
	if reqPayload["handler"] != "open" {
		t.Errorf("action_requested payload handler = %v, want open", reqPayload["handler"])
	}
	if got, ok := reqPayload["payload"].(map[string]any); !ok || got["nodeId"] != "n1" {
		t.Errorf("action_requested payload.payload = %v, want {\"nodeId\":\"n1\"}", reqPayload["payload"])
	}

	// action_completed carries the default (echo) executor's result: the
	// submitted payload, because no adapter is registered for this handler.
	if completed.ActorID != "open" {
		t.Errorf("action_completed actor id = %q, want the handler name open", completed.ActorID)
	}
	donePayload := decodingPayload(t, completed.Payload)
	if got, ok := donePayload["result"].(map[string]any); !ok || got["nodeId"] != "n1" {
		t.Errorf("action_completed payload.result = %v, want the submitted payload", donePayload["result"])
	}
	if string(outcome.Result) != string(payload) {
		t.Errorf("outcome result = %s, want %s", outcome.Result, payload)
	}

	maxSeq, err := repo.MaxSequence(ctx, c.ID)
	if err != nil {
		t.Fatalf("MaxSequence: %v", err)
	}
	if maxSeq != outcome.ResultSeq {
		t.Errorf("MaxSequence = %d, want the terminal event sequence %d", maxSeq, outcome.ResultSeq)
	}
}

// ---------------------------------------------------------------------------
// Undeclared handler (AC2)
// ---------------------------------------------------------------------------

func TestSubmitCardActionUndeclaredHandlerAppendsNothing(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()

	svc := NewCardServiceImpl(mgr)
	ctx := context.Background()

	c, repo := createActionCard(t, mgr, CardTypeCompact,
		CardAction{Label: "Open", Handler: "open"},
		CardAction{Label: "Dismiss", Handler: "dismiss"},
	)

	outcome, err := svc.SubmitCardAction(ctx, c.ID, "delete", json.RawMessage(`{}`))
	if !errors.Is(err, service.ErrCardActionNotDeclared) {
		t.Fatalf("error = %v, want ErrCardActionNotDeclared", err)
	}
	if outcome != nil {
		t.Errorf("outcome = %+v, want nil for a rejected handler", outcome)
	}

	events := cardEvents(t, repo, c.ID)
	requireEventTypes(t, events, EventCardCreated)
	for _, e := range events {
		if e.EventType == EventActionCompleted {
			t.Fatal("an undeclared handler appended action_completed")
		}
		if e.EventType == EventActionRequested {
			t.Fatal("an undeclared handler appended action_requested")
		}
	}

	// The rejection must not poison the card: a declared handler still works and
	// continues the same monotonic sequence.
	outcome, err = svc.SubmitCardAction(ctx, c.ID, "dismiss", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("SubmitCardAction(dismiss): %v", err)
	}
	if outcome.RequestedSeq != 2 || outcome.ResultSeq != 3 {
		t.Errorf("post-rejection sequences = (%d, %d), want (2, 3)", outcome.RequestedSeq, outcome.ResultSeq)
	}
	requireEventTypes(t, cardEvents(t, repo, c.ID),
		EventCardCreated, EventActionRequested, EventActionCompleted)
}

// ---------------------------------------------------------------------------
// Request validation (AC4)
// ---------------------------------------------------------------------------

func TestSubmitCardActionRejectsEmptyHandlerBeforeLookup(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()

	svc := NewCardServiceImpl(mgr)
	ctx := context.Background()

	for _, handler := range []string{"", "   ", "\t"} {
		// The card does not exist: validation must fire before the lookup, so
		// the caller is told the request is malformed, not that the card is
		// missing.
		outcome, err := svc.SubmitCardAction(ctx, uuid.New(), handler, json.RawMessage(`{}`))
		if !errors.Is(err, service.ErrCardActionHandlerRequired) {
			t.Errorf("handler %q: error = %v, want ErrCardActionHandlerRequired", handler, err)
		}
		if outcome != nil {
			t.Errorf("handler %q: outcome = %+v, want nil", handler, outcome)
		}
	}
}

func TestSubmitCardActionUnknownCard(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()

	svc := NewCardServiceImpl(mgr)
	cardID := uuid.New()

	outcome, err := svc.SubmitCardAction(context.Background(), cardID, "open", json.RawMessage(`{}`))
	if !errors.Is(err, service.ErrCardNotFound) {
		t.Fatalf("error = %v, want ErrCardNotFound", err)
	}
	if outcome != nil {
		t.Errorf("outcome = %+v, want nil for an unknown card", outcome)
	}
}

func TestSubmitCardActionInvalidPayloadAppendsNothing(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()

	svc := NewCardServiceImpl(mgr)
	ctx := context.Background()

	c, repo := createActionCard(t, mgr, CardTypeCompact, CardAction{Label: "Open", Handler: "open"})

	// Malformed JSON and non-object JSON are both rejected: the payload is
	// recorded verbatim in the event log, so a scalar or array would be a
	// silently different contract.
	for _, raw := range []string{
		`{"nodeId":`,
		`"nodeId"`,
		`[1,2]`,
		`5`,
		`true`,
	} {
		payload := json.RawMessage(raw)
		outcome, err := svc.SubmitCardAction(ctx, c.ID, "open", payload)
		if !errors.Is(err, service.ErrCardActionPayloadInvalid) {
			t.Errorf("payload %s: error = %v, want ErrCardActionPayloadInvalid", raw, err)
		}
		if outcome != nil {
			t.Errorf("payload %s: outcome = %+v, want nil for an invalid payload", raw, outcome)
		}
	}
	requireEventTypes(t, cardEvents(t, repo, c.ID), EventCardCreated)
}

func TestSubmitCardActionNullPayloadDefaultsToEmptyObject(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()

	svc := NewCardServiceImpl(mgr)
	ctx := context.Background()

	c, repo := createActionCard(t, mgr, CardTypeCompact, CardAction{Label: "Open", Handler: "open"})

	for _, payload := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`null`)} {
		outcome, err := svc.SubmitCardAction(ctx, c.ID, "open", payload)
		if err != nil {
			t.Fatalf("payload %q: SubmitCardAction: %v", string(payload), err)
		}
		if string(outcome.Payload) != `{}` {
			t.Errorf("payload %q: outcome payload = %s, want {}", string(payload), outcome.Payload)
		}
		if string(outcome.Result) != `{}` {
			t.Errorf("payload %q: outcome result = %s, want {}", string(payload), outcome.Result)
		}
	}

	events := cardEvents(t, repo, c.ID)
	requireEventTypes(t, events,
		EventCardCreated,
		EventActionRequested, EventActionCompleted,
		EventActionRequested, EventActionCompleted,
		EventActionRequested, EventActionCompleted)
	requireMonotonicSequences(t, events)
	for _, e := range events {
		if e.EventType == EventActionRequested {
			if p := decodingPayload(t, e.Payload); p["payload"] == nil {
				t.Errorf("action_requested payload recorded %v, want an empty object", p["payload"])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Execution failure (AC3)
// ---------------------------------------------------------------------------

func TestSubmitCardActionExecutionFailureRecordsAgentError(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()

	svc := NewCardServiceImpl(mgr).WithActionExecutor(failingActionExecutor{err: errors.New("adapter exploded")})
	ctx := context.Background()

	c, repo := createActionCard(t, mgr, CardTypeCompact, CardAction{Label: "Open", Handler: "open"})

	outcome, err := svc.SubmitCardAction(ctx, c.ID, "open", json.RawMessage(`{"nodeId":"n1"}`))
	if !errors.Is(err, service.ErrCardActionFailed) {
		t.Fatalf("error = %v, want ErrCardActionFailed", err)
	}
	// The failure is durable, so the caller still gets the event trail.
	if outcome == nil {
		t.Fatal("outcome is nil on an execution failure; callers cannot read the agent_error event")
	}
	if outcome.Status != service.CardActionStatusError {
		t.Errorf("status = %q, want %q", outcome.Status, service.CardActionStatusError)
	}
	if outcome.ResultEventType != string(EventAgentError) {
		t.Errorf("result event type = %q, want %q", outcome.ResultEventType, EventAgentError)
	}
	if outcome.Error == "" {
		t.Error("outcome.Error is empty; the failure reason must be exposed")
	}

	events := cardEvents(t, repo, c.ID)
	requireEventTypes(t, events, EventCardCreated, EventActionRequested, EventAgentError)
	requireMonotonicSequences(t, events)
	if outcome.RequestedSeq != events[1].Sequence || outcome.ResultSeq != events[2].Sequence {
		t.Errorf("outcome sequences = (%d, %d), want (%d, %d)",
			outcome.RequestedSeq, outcome.ResultSeq, events[1].Sequence, events[2].Sequence)
	}
	if outcome.ResultSeq != outcome.RequestedSeq+1 {
		t.Errorf("result_seq = %d, want requested_seq+1 = %d", outcome.ResultSeq, outcome.RequestedSeq+1)
	}
	for _, e := range events {
		if e.EventType == EventActionCompleted {
			t.Fatal("a failed action appended action_completed")
		}
	}
	if got := decodingPayload(t, events[2].Payload)["error"]; got != "adapter exploded" {
		t.Errorf("agent_error payload error = %v, want %q", got, "adapter exploded")
	}
}

func TestSubmitCardActionInvalidExecutorResultIsAFailure(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()

	// An adapter that returns unusable JSON must not be able to write a
	// half-valid action_completed event.
	svc := NewCardServiceImpl(mgr).WithActionExecutor(&recordingActionExecutor{result: json.RawMessage(`{"broken":`)})
	ctx := context.Background()

	c, repo := createActionCard(t, mgr, CardTypeCompact, CardAction{Label: "Open", Handler: "open"})

	outcome, err := svc.SubmitCardAction(ctx, c.ID, "open", json.RawMessage(`{}`))
	if !errors.Is(err, service.ErrCardActionFailed) {
		t.Fatalf("error = %v, want ErrCardActionFailed", err)
	}
	if outcome == nil || outcome.Status != service.CardActionStatusError {
		t.Fatalf("outcome = %+v, want a non-nil error outcome", outcome)
	}
	requireEventTypes(t, cardEvents(t, repo, c.ID), EventCardCreated, EventActionRequested, EventAgentError)
}

// ---------------------------------------------------------------------------
// Execution boundary wiring
// ---------------------------------------------------------------------------

func TestSubmitCardActionInvokesExecutorWithDeclaredAction(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()

	exec := &recordingActionExecutor{result: json.RawMessage(`{"opened":true}`)}
	svc := NewCardServiceImpl(mgr).WithActionExecutor(exec)
	ctx := context.Background()

	declared := CardAction{Label: "Open in viewer", Handler: "viewer.open"}
	c, repo := createActionCard(t, mgr, CardTypeExpanded, declared, CardAction{Label: "Noop", Handler: "noop"})

	payload := json.RawMessage(`{"nodeId":"n7"}`)
	outcome, err := svc.SubmitCardAction(ctx, c.ID, "viewer.open", payload)
	if err != nil {
		t.Fatalf("SubmitCardAction: %v", err)
	}

	if exec.gotCardID != c.ID {
		t.Errorf("executor card = %s, want %s", exec.gotCardID, c.ID)
	}
	if exec.gotAction != declared {
		t.Errorf("executor action = %+v, want the declared action %+v", exec.gotAction, declared)
	}
	if string(exec.gotPayload) != string(payload) {
		t.Errorf("executor payload = %s, want %s", exec.gotPayload, payload)
	}
	if string(outcome.Result) != `{"opened":true}` {
		t.Errorf("outcome result = %s, want the executor result", outcome.Result)
	}
	events := cardEvents(t, repo, c.ID)
	requireEventTypes(t, events, EventCardCreated, EventActionRequested, EventActionCompleted)
	if got := decodingPayload(t, events[2].Payload)["result"]; got == nil {
		t.Error("action_completed recorded no result")
	}
	// The card lives in the expanded store and resolution must reach it.
	if c.CardType != CardTypeExpanded {
		t.Fatalf("test card type = %s, want expanded", c.CardType)
	}
}

func TestSubmitCardActionSequenceMonotonicAcrossSubmissions(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()

	svc := NewCardServiceImpl(mgr)
	ctx := context.Background()

	c, repo := createActionCard(t, mgr, CardTypeIteration,
		CardAction{Label: "Open", Handler: "open"},
		CardAction{Label: "Dismiss", Handler: "dismiss"},
	)

	wantSeqs := [][2]int64{{2, 3}, {4, 5}, {6, 7}}
	handlers := []string{"open", "dismiss", "open"}
	for i, handler := range handlers {
		outcome, err := svc.SubmitCardAction(ctx, c.ID, handler, json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("submission %d (%s): %v", i, handler, err)
		}
		if outcome.RequestedSeq != wantSeqs[i][0] || outcome.ResultSeq != wantSeqs[i][1] {
			t.Fatalf("submission %d sequences = (%d, %d), want (%d, %d)",
				i, outcome.RequestedSeq, outcome.ResultSeq, wantSeqs[i][0], wantSeqs[i][1])
		}
	}

	events := cardEvents(t, repo, c.ID)
	requireEventTypes(t, events,
		EventCardCreated,
		EventActionRequested, EventActionCompleted,
		EventActionRequested, EventActionCompleted,
		EventActionRequested, EventActionCompleted)
	requireMonotonicSequences(t, events)
}
