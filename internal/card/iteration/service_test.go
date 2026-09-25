package iteration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
)

func testService(t *testing.T, subtype IterationSubtype, process AgentProcess) (*IterationCardServiceImpl, *card.Card, *card.CardDBManager) {
	t.Helper()
	manager := card.NewCardDBManager(t.TempDir())
	t.Cleanup(func() { _ = manager.Close() })
	service := NewIterationCardService(manager)
	input := CreateIterationCardInput{
		Subtype: subtype,
		AgentID: "agent-test",
		Process: process,
	}
	created, err := service.CreateCardWithInput(context.Background(), input)
	if err != nil {
		t.Fatalf("CreateCardWithInput: %v", err)
	}
	return service, created, manager
}

func cardSession(t *testing.T, value *card.Card) uuid.UUID {
	t.Helper()
	var data struct {
		SessionID uuid.UUID `json:"sessionId"`
	}
	if err := json.Unmarshal(value.Data, &data); err != nil {
		t.Fatalf("decode card data: %v", err)
	}
	return data.SessionID
}

func TestValidateEventPairsAndEnvelopeBounds(t *testing.T) {
	cases := []struct {
		name    string
		subtype IterationSubtype
		event   string
		valid   string
		invalid string
	}{
		{
			name: "search", subtype: IterationSubtypeSearch, event: EventSearchStarted,
			valid: `{"query":"docs","batch_total":2}`, invalid: `{"query":"docs"}`,
		},
		{
			name: "code exec", subtype: IterationSubtypeCodeExec, event: EventExecStart,
			valid: `{"command":"go test","start_time":"2026-01-01T00:00:00Z"}`, invalid: `{"command":"go test"}`,
		},
		{
			name: "file read", subtype: IterationSubtypeFileRead, event: EventFileReadOpened,
			valid: `{"path":"main.go","absolute_path":"/tmp/main.go","size":10,"mime_type":"text/plain","language":"go"}`, invalid: `{"path":"main.go"}`,
		},
		{
			name: "thinking", subtype: IterationSubtypeThinking, event: EventThoughtStarted,
			valid: `{"step_id":"step-1","title":"Inspect"}`, invalid: `{"step_id":"step-1"}`,
		},
		{
			name: "tool call", subtype: IterationSubtypeToolCall, event: EventToolCallStarted,
			valid: `{"tool_name":"read","params":{},"start_time":"2026-01-01T00:00:00Z","gated":false}`, invalid: `{"tool_name":"read"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateEventPair(tc.subtype, tc.event, json.RawMessage(tc.valid)); err != nil {
				t.Fatalf("valid pair rejected: %v", err)
			}
			if err := ValidateEventPair(tc.subtype, tc.event, json.RawMessage(tc.invalid)); err == nil {
				t.Fatal("invalid pair accepted")
			}
		})
	}

	data, err := defaultData(uuid.MustParse("0191a9c3-0000-7000-8000-000000000001"), IterationSubtypeSearch, "agent-test")
	if err != nil {
		t.Fatalf("defaultData: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("decode default data: %v", err)
	}
	object["title"] = strings.Repeat("x", 161)
	tooLong, _ := json.Marshal(object)
	if _, err := ValidateCardData(tooLong); err == nil {
		t.Fatal("title over 160 code points accepted")
	}
	object["title"] = "valid"
	object["agentId"] = strings.Repeat("a", 129)
	tooLongAgent, _ := json.Marshal(object)
	if _, err := ValidateCardData(tooLongAgent); err == nil {
		t.Fatal("agent ID over 128 characters accepted")
	}
}

func TestAppendEventRejectsMismatchedAgentAndSubtype(t *testing.T) {
	service, created, _ := testService(t, IterationSubtypeSearch, nil)
	ctx := context.Background()
	session := cardSession(t, created)
	valid := json.RawMessage(`{"url":"https://example.com/docs"}`)

	err := service.AppendEvent(ctx, created.ID, IterationEvent{
		Subtype: IterationSubtypeThinking, EventType: EventThoughtStarted,
		Data: json.RawMessage(`{"step_id":"step-1","title":"wrong"}`), AgentID: "agent-test", AgentSessionID: session,
	})
	if err == nil {
		t.Fatal("mismatched subtype accepted")
	}
	err = service.AppendEvent(ctx, created.ID, IterationEvent{
		Subtype: IterationSubtypeSearch, EventType: EventURLDiscovered,
		Data: valid, AgentID: "other-agent", AgentSessionID: session,
	})
	if !errors.Is(err, ErrAgentNotRegistered) {
		t.Fatalf("wrong agent error = %v, want ErrAgentNotRegistered", err)
	}
}

func TestAppendEventMaterializationRollsBackWithEvent(t *testing.T) {
	service, created, manager := testService(t, IterationSubtypeSearch, nil)
	ctx := context.Background()
	session := cardSession(t, created)
	repo, err := manager.Repository(card.CardTypeIteration)
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	before, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get before: %v", err)
	}
	beforeEvents, err := repo.ListEvents(ctx, created.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents before: %v", err)
	}

	service.materializeHook = func(*sql.Tx) error { return errors.New("forced materialization failure") }
	err = service.AppendEvent(ctx, created.ID, IterationEvent{
		Subtype: IterationSubtypeSearch, EventType: EventURLDiscovered,
		Data: json.RawMessage(`{"url":"https://example.com/docs"}`), AgentID: "agent-test", AgentSessionID: session,
	})
	if err == nil || !strings.Contains(err.Error(), "forced materialization failure") {
		t.Fatalf("AppendEvent error = %v, want forced failure", err)
	}
	service.materializeHook = nil

	after, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after: %v", err)
	}
	afterEvents, err := repo.ListEvents(ctx, created.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents after: %v", err)
	}
	if after.Revision != before.Revision || string(after.Data) != string(before.Data) {
		t.Fatalf("card changed after rollback: revision %d -> %d", before.Revision, after.Revision)
	}
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("event count after rollback = %d, before = %d", len(afterEvents), len(beforeEvents))
	}

	if err := service.AppendEvent(ctx, created.ID, IterationEvent{
		Subtype: IterationSubtypeSearch, EventType: EventURLDiscovered,
		Data: json.RawMessage(`{"url":"https://example.com/docs"}`), AgentID: "agent-test", AgentSessionID: session,
	}); err != nil {
		t.Fatalf("successful AppendEvent: %v", err)
	}
	materialized, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get materialized card: %v", err)
	}
	var materializedData struct {
		URLs []string `json:"urlsSearched"`
	}
	if err := json.Unmarshal(materialized.Data, &materializedData); err != nil {
		t.Fatalf("decode materialized data: %v", err)
	}
	if len(materializedData.URLs) != 1 || materializedData.URLs[0] != "https://example.com/docs" {
		t.Fatalf("materialized URLs = %#v", materializedData.URLs)
	}
}

func TestFeedbackPersistPublishAcknowledgeAndReplay(t *testing.T) {
	service, created, manager := testService(t, IterationSubtypeSearch, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := cardSession(t, created)
	feedbackCh, cleanup, err := service.SubscribeFeedback(ctx, created.ID)
	if err != nil {
		t.Fatalf("SubscribeFeedback: %v", err)
	}
	defer cleanup()

	err = service.SubmitFeedback(context.Background(), created.ID, FeedbackInput{
		CardID: created.ID, Subtype: IterationSubtypeSearch, FeedbackKind: FeedbackKindRelevance,
		Target: json.RawMessage(`{"url":"https://example.com/docs","snippetId":"s-1"}`),
		Note:   "use the official source", ActorID: "user-1", SessionID: session,
		Timestamp: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}
	var feedback FeedbackEvent
	select {
	case feedback = <-feedbackCh:
	case <-time.After(time.Second):
		t.Fatal("feedback was not published")
	}
	if feedback.ID == uuid.Nil || feedback.Acknowledged {
		t.Fatalf("published feedback = %+v", feedback)
	}

	repo, err := manager.Repository(card.CardTypeIteration)
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	events, err := repo.ListEvents(context.Background(), created.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	foundUserFeedback := false
	for _, event := range events {
		if event.EventType == card.CardEventType(EventUserFeedback) {
			foundUserFeedback = true
		}
	}
	if !foundUserFeedback {
		t.Fatal("user_feedback was not durable")
	}

	if err := service.Acknowledge(context.Background(), created.ID, feedback.ID); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if err := service.Acknowledge(context.Background(), created.ID, feedback.ID); err != nil {
		t.Fatalf("idempotent Acknowledge: %v", err)
	}
	replayCtx, replayCancel := context.WithCancel(context.Background())
	defer replayCancel()
	replay, replayCleanup, err := service.SubscribeFeedback(replayCtx, created.ID)
	if err != nil {
		t.Fatalf("SubscribeFeedback replay: %v", err)
	}
	defer replayCleanup()
	select {
	case value := <-replay:
		t.Fatalf("acknowledged feedback replayed: %+v", value)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestListActiveCardsAndGetProgressUseMaterializedState(t *testing.T) {
	service, created, _ := testService(t, IterationSubtypeSearch, nil)
	ctx := context.Background()
	session := cardSession(t, created)
	active, err := service.ListActiveCards(ctx)
	if err != nil {
		t.Fatalf("ListActiveCards: %v", err)
	}
	if len(active) != 1 || active[0].ID != created.ID {
		t.Fatalf("active cards = %#v", active)
	}
	progress, err := service.GetProgress(ctx)
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if len(progress) != 1 || progress[0].Status != ProgressStatusRunning {
		t.Fatalf("progress = %#v", progress)
	}

	if err := service.AppendEvent(ctx, created.ID, IterationEvent{
		Subtype: IterationSubtypeSearch, EventType: EventSearchCompleted,
		Data: json.RawMessage(`{"completed":1,"total":1}`), AgentID: "agent-test", AgentSessionID: session,
	}); err != nil {
		t.Fatalf("complete event: %v", err)
	}
	active, err = service.ListActiveCards(ctx)
	if err != nil {
		t.Fatalf("ListActiveCards after completion: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("terminal card remained active: %#v", active)
	}
}

type cancelProcess struct {
	id      string
	cancels atomic.Int32
}

func (p *cancelProcess) ID() string                 { return p.id }
func (p *cancelProcess) Status() AgentProcessStatus { return AgentProcessStatusRunning }
func (p *cancelProcess) SubscribeFeedback(context.Context, uuid.UUID) (<-chan FeedbackEvent, error) {
	return nil, nil
}
func (p *cancelProcess) Cancel(context.Context, uuid.UUID) error {
	p.cancels.Add(1)
	return nil
}

func TestCancelCardIsIdempotentAfterTerminalState(t *testing.T) {
	process := &cancelProcess{id: "agent-test"}
	service, created, manager := testService(t, IterationSubtypeCodeExec, process)
	if err := service.CancelCard(context.Background(), created.ID); err != nil {
		t.Fatalf("CancelCard: %v", err)
	}
	if got := process.cancels.Load(); got != 1 {
		t.Fatalf("cancel calls = %d, want 1", got)
	}

	repo, err := manager.Repository(card.CardTypeIteration)
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	stored, err := repo.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get cancelled card: %v", err)
	}
	data, err := ValidateCardData(stored.Data)
	if err != nil {
		t.Fatalf("Validate cancelled data: %v", err)
	}
	if data.State != IterationStateCancelled {
		t.Fatalf("state = %q, want cancelled", data.State)
	}
	if err := service.CancelCard(context.Background(), created.ID); err != nil {
		t.Fatalf("second CancelCard: %v", err)
	}
	if got := process.cancels.Load(); got != 1 {
		t.Fatalf("cancel calls after second request = %d, want 1", got)
	}
}
