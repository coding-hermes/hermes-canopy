package iteration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
)

type recoveryTestProcess struct {
	id     string
	status atomic.Value
}

func newRecoveryTestProcess(id string) *recoveryTestProcess {
	process := &recoveryTestProcess{id: id}
	process.status.Store(AgentProcessStatusRunning)
	return process
}

func (p *recoveryTestProcess) ID() string { return p.id }
func (p *recoveryTestProcess) Status() AgentProcessStatus {
	return p.status.Load().(AgentProcessStatus)
}
func (p *recoveryTestProcess) setStatus(status AgentProcessStatus) { p.status.Store(status) }
func (p *recoveryTestProcess) SubscribeFeedback(context.Context, uuid.UUID) (<-chan FeedbackEvent, error) {
	return nil, nil
}
func (p *recoveryTestProcess) Cancel(context.Context, uuid.UUID) error { return nil }

func TestCrashRecoveryPreservesHistoryFeedbackAndEmitsSnapshot(t *testing.T) {
	process := newRecoveryTestProcess("agent-test")
	service, created, manager := testService(t, IterationSubtypeSearch, process)
	ctx := context.Background()
	session := cardSession(t, created)

	if err := service.AppendEvent(ctx, created.ID, IterationEvent{
		Subtype: IterationSubtypeSearch, EventType: EventURLDiscovered,
		Data: json.RawMessage(`{"url":"https://example.com/docs"}`), AgentID: process.id, AgentSessionID: session,
	}); err != nil {
		t.Fatalf("history event: %v", err)
	}
	if err := service.SubmitFeedback(ctx, created.ID, FeedbackInput{
		CardID: created.ID, Subtype: IterationSubtypeSearch, FeedbackKind: FeedbackKindRelevance,
		Target: json.RawMessage(`{"url":"https://example.com/docs"}`), ActorID: "user-1", SessionID: session,
	}); err != nil {
		t.Fatalf("feedback: %v", err)
	}
	repo, err := manager.Repository(card.CardTypeIteration)
	if err != nil {
		t.Fatalf("repository: %v", err)
	}
	before, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("card before crash: %v", err)
	}
	beforeEvents, err := repo.ListEvents(ctx, created.ID, 0, 100)
	if err != nil {
		t.Fatalf("events before crash: %v", err)
	}

	events, cleanup, err := service.SubscribeEvents(ctx, created.ID)
	if err != nil {
		t.Fatalf("subscribe events: %v", err)
	}
	defer cleanup()
	process.setStatus(AgentProcessStatusCrashed)
	monitor := NewProcessMonitor(service, time.Hour)
	if err := monitor.Poll(ctx); err != nil {
		t.Fatalf("monitor poll: %v", err)
	}

	var errorEvent, snapshot IterationEvent
	select {
	case errorEvent = <-events:
	case <-time.After(time.Second):
		t.Fatal("agent_error was not published")
	}
	select {
	case snapshot = <-events:
	case <-time.After(time.Second):
		t.Fatal("card snapshot was not published")
	}
	if errorEvent.EventType != EventAgentError || snapshot.EventType != EventCardSnapshot {
		t.Fatalf("crash notifications = %q, %q", errorEvent.EventType, snapshot.EventType)
	}
	var errorData map[string]any
	if err := json.Unmarshal(errorEvent.Data, &errorData); err != nil {
		t.Fatalf("agent_error data: %v", err)
	}
	if errorData["agent_id"] != process.id || errorData["status"] != string(AgentProcessStatusCrashed) {
		t.Fatalf("agent_error context = %#v", errorData)
	}
	var snapshotData map[string]any
	if err := json.Unmarshal(snapshot.Data, &snapshotData); err != nil {
		t.Fatalf("snapshot data: %v", err)
	}
	if snapshotData["state"] != string(IterationStateInterrupted) {
		t.Fatalf("snapshot state = %v", snapshotData["state"])
	}

	after, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("card after crash: %v", err)
	}
	assertOnlyStateChanged(t, before.Data, after.Data, IterationStateInterrupted)
	afterEvents, err := repo.ListEvents(ctx, created.ID, 0, 100)
	if err != nil {
		t.Fatalf("events after crash: %v", err)
	}
	if len(afterEvents) != len(beforeEvents)+1 {
		t.Fatalf("event count = %d, want %d", len(afterEvents), len(beforeEvents)+1)
	}
	if _, ok := service.processes[created.ID]; ok {
		t.Fatal("crashed process remained registered")
	}
	feedback, feedbackCleanup, err := service.SubscribeFeedback(ctx, created.ID)
	if err != nil {
		t.Fatalf("feedback replay: %v", err)
	}
	defer feedbackCleanup()
	select {
	case replayed := <-feedback:
		if replayed.Acknowledged || replayed.FeedbackKind != FeedbackKindRelevance {
			t.Fatalf("replayed feedback = %+v", replayed)
		}
	case <-time.After(time.Second):
		t.Fatal("unacknowledged feedback was lost on crash")
	}

	staleEvent := IterationEvent{
		Subtype: IterationSubtypeSearch, EventType: EventURLDiscovered,
		Data: json.RawMessage(`{"url":"https://example.com/stale"}`), AgentID: process.id, AgentSessionID: session,
	}
	if err := service.AppendEvent(ctx, created.ID, staleEvent); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("stale append error = %v, want ErrRecoveryRequired", err)
	}
	if err := service.ReportProcessStatus(ctx, created.ID, process.id, AgentProcessStatusCrashed); err != nil {
		t.Fatalf("duplicate crash report: %v", err)
	}
	finalEvents, err := repo.ListEvents(ctx, created.ID, 0, 100)
	if err != nil {
		t.Fatalf("events after duplicate crash: %v", err)
	}
	if len(finalEvents) != len(afterEvents) {
		t.Fatalf("duplicate crash appended an event: %d -> %d", len(afterEvents), len(finalEvents))
	}
	active, err := service.ListActiveCards(ctx)
	if err != nil {
		t.Fatalf("active cards: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("interrupted card omitted from active list: %#v", active)
	}
	progress, err := service.GetProgress(ctx)
	if err != nil || len(progress) != 1 {
		t.Fatalf("interrupted progress = %#v, err=%v", progress, err)
	}
}

func TestTerminalCrashReportIsIdempotentNoOp(t *testing.T) {
	process := newRecoveryTestProcess("agent-test")
	service, created, manager := testService(t, IterationSubtypeSearch, process)
	ctx := context.Background()
	session := cardSession(t, created)
	if err := service.AppendEvent(ctx, created.ID, IterationEvent{
		Subtype: IterationSubtypeSearch, EventType: EventSearchCompleted,
		Data: json.RawMessage(`{"completed":1,"total":1}`), AgentID: process.id, AgentSessionID: session,
	}); err != nil {
		t.Fatalf("complete card: %v", err)
	}
	repo, err := manager.Repository(card.CardTypeIteration)
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.ListEvents(ctx, created.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ReportProcessStatus(ctx, created.ID, process.id, AgentProcessStatusCrashed); err != nil {
		t.Fatalf("terminal crash report: %v", err)
	}
	after, err := repo.ListEvents(ctx, created.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("terminal crash appended events: %d -> %d", len(before), len(after))
	}
}

func TestRecoveryRequiredBeforeResumedProgressAndNewWork(t *testing.T) {
	process := newRecoveryTestProcess("agent-test")
	service, created, _ := testService(t, IterationSubtypeSearch, process)
	ctx := context.Background()
	session := cardSession(t, created)
	process.setStatus(AgentProcessStatusCrashed)
	if err := service.ReportProcessStatus(ctx, created.ID, process.id, AgentProcessStatusCrashed); err != nil {
		t.Fatalf("crash report: %v", err)
	}

	work := IterationEvent{
		Subtype: IterationSubtypeSearch, EventType: EventURLDiscovered,
		Data: json.RawMessage(`{"url":"https://example.com/blocked"}`), AgentID: process.id, AgentSessionID: session,
	}
	if err := service.AppendEvent(ctx, created.ID, work); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("work before recovery = %v, want ErrRecoveryRequired", err)
	}
	replacement := newRecoveryTestProcess(process.id)
	if err := service.ResumeProcess(ctx, created.ID, replacement.id); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("resume without replay = %v, want ErrRecoveryRequired", err)
	}
	recovery, err := service.BeginRecovery(ctx, created.ID, replacement)
	if err != nil {
		t.Fatalf("begin recovery: %v", err)
	}
	defer recovery.Cleanup()
	if err := service.ResumeProcess(ctx, created.ID, replacement.id); err != nil {
		t.Fatalf("resume after replay with no pending feedback: %v", err)
	}
	resumed, err := service.GetCard(ctx, created.ID)
	if err != nil {
		t.Fatalf("resumed card: %v", err)
	}
	data, err := ValidateCardData(resumed.Data)
	if err != nil || data.State != IterationStateRunning {
		t.Fatalf("resumed state = %q, err=%v", data.State, err)
	}
	if err := service.AppendEvent(ctx, created.ID, work); err != nil {
		t.Fatalf("work after recovery: %v", err)
	}
	events, err := service.ListEvents(ctx, created.ID, 0, 100)
	if err != nil {
		t.Fatalf("list recovery events: %v", err)
	}
	foundResume := false
	for _, event := range events {
		if event.EventType != EventAgentProgress {
			continue
		}
		var payload map[string]string
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["message"] == "resumed" && payload["recovered_from"] == process.id {
			foundResume = true
		}
	}
	if !foundResume {
		t.Fatal("resumed agent_progress event was not durable")
	}
}

func TestRecoveryRequiresFeedbackAcknowledgement(t *testing.T) {
	process := newRecoveryTestProcess("agent-test")
	service, created, _ := testService(t, IterationSubtypeSearch, process)
	ctx := context.Background()
	session := cardSession(t, created)
	if err := service.SubmitFeedback(ctx, created.ID, FeedbackInput{
		CardID: created.ID, Subtype: IterationSubtypeSearch, FeedbackKind: FeedbackKindSteer,
		Note: "use primary sources", ActorID: "user-1", SessionID: session,
	}); err != nil {
		t.Fatalf("feedback: %v", err)
	}
	if err := service.ReportProcessStatus(ctx, created.ID, process.id, AgentProcessStatusCrashed); err != nil {
		t.Fatalf("crash report: %v", err)
	}
	replacement := newRecoveryTestProcess(process.id)
	recovery, err := service.BeginRecovery(ctx, created.ID, replacement)
	if err != nil {
		t.Fatalf("begin recovery: %v", err)
	}
	defer recovery.Cleanup()
	var replayed FeedbackEvent
	select {
	case replayed = <-recovery.Feedback:
	case <-time.After(time.Second):
		t.Fatal("durable feedback was not replayed")
	}
	if err := service.ResumeProcess(ctx, created.ID, replacement.id); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("resume before acknowledgement = %v, want ErrRecoveryRequired", err)
	}
	if err := service.Acknowledge(ctx, created.ID, replayed.ID); err != nil {
		t.Fatalf("acknowledge replayed feedback: %v", err)
	}
	if err := service.ResumeProcess(ctx, created.ID, replacement.id); err != nil {
		t.Fatalf("resume after acknowledgement: %v", err)
	}
}

func TestPendingApprovalToolStateSurvivesCrashAndResume(t *testing.T) {
	sessionID, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"subtype": "iteration_tool_call", "title": "Write migration", "state": "running", "agentId": "agent-test",
		"sessionId": sessionID.String(), "progress": map[string]any{
			"cardId": uuid.MustParse("0191a9c3-0000-7000-8000-000000000001").String(), "type": "tool_call", "title": "Write migration", "current": 0, "total": 0, "status": "running", "updatedAt": time.Now().UTC().Format(time.RFC3339Nano),
		},
		"toolName": "write_file", "params": map[string]any{"path": "migration.sql"}, "result": nil,
		"status": "pending_approval", "startTime": nil, "endTime": nil, "durationMs": nil, "error": nil, "gated": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	process := newRecoveryTestProcess("agent-test")
	manager := card.NewCardDBManager(t.TempDir())
	t.Cleanup(func() { _ = manager.Close() })
	service := NewIterationCardService(manager)
	created, err := service.CreateCardWithInput(context.Background(), CreateIterationCardInput{Subtype: IterationSubtypeToolCall, AgentID: process.id, Data: data, Process: process})
	if err != nil {
		t.Fatalf("create tool card: %v", err)
	}
	if err := service.ReportProcessStatus(context.Background(), created.ID, process.id, AgentProcessStatusCrashed); err != nil {
		t.Fatalf("crash report: %v", err)
	}
	before, err := service.GetCard(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var beforeData map[string]any
	if err := json.Unmarshal(before.Data, &beforeData); err != nil {
		t.Fatal(err)
	}
	if beforeData["state"] != string(IterationStateInterrupted) || beforeData["status"] != "pending_approval" {
		t.Fatalf("pending approval after crash = %#v", beforeData)
	}
	replacement := newRecoveryTestProcess(process.id)
	recovery, err := service.BeginRecovery(context.Background(), created.ID, replacement)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Cleanup()
	if err := service.ResumeProcess(context.Background(), created.ID, replacement.id); err != nil {
		t.Fatalf("resume tool card: %v", err)
	}
	after, err := service.GetCard(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var afterData map[string]any
	if err := json.Unmarshal(after.Data, &afterData); err != nil {
		t.Fatal(err)
	}
	if afterData["state"] != string(IterationStateRunning) || afterData["status"] != "pending_approval" || afterData["result"] != nil {
		t.Fatalf("approval state changed during resume = %#v", afterData)
	}
}

func assertOnlyStateChanged(t *testing.T, before, after json.RawMessage, state IterationState) {
	t.Helper()
	var beforeObject, afterObject map[string]any
	if err := json.Unmarshal(before, &beforeObject); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &afterObject); err != nil {
		t.Fatal(err)
	}
	if afterObject["state"] != string(state) {
		t.Fatalf("state = %v, want %q", afterObject["state"], state)
	}
	delete(beforeObject, "state")
	delete(afterObject, "state")
	if !reflect.DeepEqual(beforeObject, afterObject) {
		t.Fatalf("data changed beyond state: before=%#v after=%#v", beforeObject, afterObject)
	}
}
