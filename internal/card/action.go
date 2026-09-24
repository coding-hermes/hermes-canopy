package card

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// CardActionExecutor is the execution boundary for a declared card action.
//
// SPEC-PL-03 §4.3 defines AppCardAdapter.HandleAction as the place an app
// implements the behaviour behind a declared action. Canopy does not run
// arbitrary server-side code for a card action: the durable event trail —
// action_requested followed by action_completed or agent_error — is the record
// of what happened, and an executor is the only hook through which an app can
// contribute behaviour. NewCardServiceImpl installs the default (echo)
// executor; WithActionExecutor replaces it, which is also how tests drive the
// agent_error path deterministically.
type CardActionExecutor interface {
	// ExecuteCardAction runs action on card and returns the result payload
	// recorded in the action_completed event. A non-nil error is recorded as an
	// agent_error event instead, and an invalid-JSON result is treated as a
	// failure so a broken adapter cannot poison the event log.
	ExecuteCardAction(ctx context.Context, card *Card, action CardAction, payload json.RawMessage) (json.RawMessage, error)
}

// echoActionExecutor is the default execution boundary: the requested action
// completes with the payload the caller supplied. It is deliberately
// side-effect free — the action path exists to record the request and its
// outcome against the card, not to execute arbitrary code.
type echoActionExecutor struct{}

func (echoActionExecutor) ExecuteCardAction(_ context.Context, _ *Card, _ CardAction, payload json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return json.RawMessage("{}"), nil
	}
	return payload, nil
}

// WithActionExecutor installs the execution boundary used by SubmitCardAction.
// A nil executor restores the default echo executor.
func (s *CardServiceImpl) WithActionExecutor(exec CardActionExecutor) *CardServiceImpl {
	if exec == nil {
		exec = echoActionExecutor{}
	}
	s.executor = exec
	return s
}

// actionRequestedEvent is the payload recorded with an action_requested event.
type actionRequestedEvent struct {
	Handler string          `json:"handler"`
	Payload json.RawMessage `json:"payload"`
}

// actionCompletedEvent is the payload recorded with an action_completed event.
type actionCompletedEvent struct {
	Handler string          `json:"handler"`
	Result  json.RawMessage `json:"result"`
}

// actionErrorEvent is the payload recorded with an agent_error event.
type actionErrorEvent struct {
	Handler string `json:"handler"`
	Error   string `json:"error"`
}

// SubmitCardAction implements service.CardService. A rejected request (no
// handler, invalid payload, unknown card, undeclared handler, archived card, or
// dismissed card) appends nothing; an accepted request appends action_requested
// and then exactly one terminal event, action_completed or agent_error.
func (s *CardServiceImpl) SubmitCardAction(
	ctx context.Context,
	cardID uuid.UUID,
	handler string,
	payload json.RawMessage,
) (*service.CardActionOutcome, error) {
	handler = strings.TrimSpace(handler)
	if handler == "" {
		return nil, service.ErrCardActionHandlerRequired
	}

	payload, err := normalizeActionPayload(payload)
	if err != nil {
		return nil, err
	}

	card, repo, err := s.findCardAndRepo(ctx, cardID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", service.ErrCardNotFound, cardID)
	}
	if card.Status == CardStatusArchived {
		return nil, fmt.Errorf("%w: %s", ErrStatusArchived, cardID)
	}
	if card.Status == CardStatusDismissed {
		return nil, fmt.Errorf("%w: %s", ErrStatusDismissed, cardID)
	}

	action, ok := findDeclaredAction(card.Actions, handler)
	if !ok {
		// Rejected before any write: an undeclared handler leaves no event
		// trail at all, so it can never leave a dangling action_requested.
		return nil, fmt.Errorf("%w: %q", service.ErrCardActionNotDeclared, handler)
	}

	requestedPayload, err := marshalEventPayload(actionRequestedEvent{Handler: handler, Payload: payload})
	if err != nil {
		return nil, err
	}

	requested, err := repo.AppendEvent(ctx, cardID, AppendEventInput{
		EventID:   uuid.New(),
		EventType: EventActionRequested,
		ActorKind: ActorUser,
		ActorID:   "user",
		Payload:   requestedPayload,
	})
	if err != nil {
		return nil, fmt.Errorf("card: append action_requested: %w", err)
	}
	s.publishEvent(requested)

	outcome := &service.CardActionOutcome{
		CardID:       cardID,
		Handler:      handler,
		RequestedSeq: requested.Sequence,
		Payload:      payload,
	}

	result, execErr := s.executor.ExecuteCardAction(ctx, card, action, payload)
	if execErr == nil {
		result = normalizeActionResult(result)
		if !json.Valid(result) {
			execErr = fmt.Errorf("card: action %q returned an invalid JSON result", handler)
		}
	}

	if execErr != nil {
		failurePayload, err := marshalEventPayload(actionErrorEvent{Handler: handler, Error: execErr.Error()})
		if err != nil {
			return nil, err
		}
		failure, err := repo.AppendEvent(ctx, cardID, AppendEventInput{
			EventID:   uuid.New(),
			EventType: EventAgentError,
			ActorKind: ActorAgent,
			ActorID:   handler,
			Payload:   failurePayload,
		})
		if err != nil {
			return nil, fmt.Errorf("card: append agent_error: %w", err)
		}
		s.publishEvent(failure)

		outcome.Status = service.CardActionStatusError
		outcome.ResultSeq = failure.Sequence
		outcome.ResultEventID = failure.EventID
		outcome.ResultEventType = string(EventAgentError)
		outcome.Error = execErr.Error()
		return outcome, fmt.Errorf("%w: %s", service.ErrCardActionFailed, execErr)
	}

	completedPayload, err := marshalEventPayload(actionCompletedEvent{Handler: handler, Result: result})
	if err != nil {
		return nil, err
	}

	completed, err := repo.AppendEvent(ctx, cardID, AppendEventInput{
		EventID:   uuid.New(),
		EventType: EventActionCompleted,
		ActorKind: ActorAgent,
		ActorID:   handler,
		Payload:   completedPayload,
	})
	if err != nil {
		return nil, fmt.Errorf("card: append action_completed: %w", err)
	}
	s.publishEvent(completed)

	outcome.Status = service.CardActionStatusCompleted
	outcome.ResultSeq = completed.Sequence
	outcome.ResultEventID = completed.EventID
	outcome.ResultEventType = string(EventActionCompleted)
	outcome.Result = result
	return outcome, nil
}

// findDeclaredAction returns the declared action whose handler matches handler.
// The match is exact: a card action is only ever the handler its card declared.
func findDeclaredAction(actions []CardAction, handler string) (CardAction, bool) {
	for _, a := range actions {
		if a.Handler == handler {
			return a, true
		}
	}
	return CardAction{}, false
}

// normalizeActionPayload canonicalizes an action payload before it is recorded:
// an absent or JSON-null payload becomes {}, and any other value must be a JSON
// object. The service is a public seam, so it re-checks rather than trusting the
// caller — a scalar or array payload is rejected instead of being recorded.
func normalizeActionPayload(payload json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return json.RawMessage("{}"), nil
	}
	if !json.Valid(trimmed) || trimmed[0] != '{' {
		return nil, service.ErrCardActionPayloadInvalid
	}
	return trimmed, nil
}

// normalizeActionResult maps an empty executor result onto {} so the
// action_completed payload is always valid JSON.
func normalizeActionResult(result json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(result)) == 0 {
		return json.RawMessage("{}")
	}
	return result
}

// marshalEventPayload serializes an event payload, failing loudly instead of
// appending a half-written event.
func marshalEventPayload(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("card: marshal event payload: %w", err)
	}
	return b, nil
}
