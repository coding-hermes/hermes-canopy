package card

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

func testDBManager(t *testing.T) *CardDBManager {
	t.Helper()
	dir := t.TempDir()
	return NewCardDBManager(dir)
}

func TestRepoCRUD(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()

	ctx := context.Background()
	repo, err := mgr.Repository(CardTypeCompact)
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}

	// 1. Create a card.
	treeID := uuid.New()
	nodeID := uuid.New()
	cardID := uuid.New()

	input := CreateCardInput{
		ID:          cardID,
		TreeID:      treeID,
		NodeID:      nodeID,
		AppID:       "test-app",
		CardType:    CardTypeCompact,
		Data:        json.RawMessage(`{"key":"value"}`),
		Actions:     []CardAction{{Label: "Test", Handler: "test.handler"}},
		ContextHash: "abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123abc1",
	}

	card, err := repo.Create(ctx, input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if card.ID != cardID {
		t.Errorf("expected card ID %s, got %s", cardID, card.ID)
	}
	if card.Status != CardStatusActive {
		t.Errorf("expected status active, got %s", card.Status)
	}

	// 2. Get the card.
	got, err := repo.Get(ctx, cardID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AppID != "test-app" {
		t.Errorf("expected app_id test-app, got %s", got.AppID)
	}
	if string(got.Data) != `{"key":"value"}` {
		t.Errorf("expected data {\"key\":\"value\"}, got %s", string(got.Data))
	}

	// 3. List cards.
	cards, err := repo.List(ctx, ListCardsOptions{
		TreeID:   &treeID,
		CardType: ptr(CardTypeCompact),
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(cards) != 1 {
		t.Errorf("expected 1 card in list, got %d", len(cards))
	}

	// 4. Update (patch) the card.
	newData := json.RawMessage(`{"updated":true}`)
	updated, err := repo.Patch(ctx, cardID, 1, PatchCardInput{
		Data: &newData,
	})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if updated.Revision != 2 {
		t.Errorf("expected revision 2, got %d", updated.Revision)
	}
	if string(updated.Data) != `{"updated":true}` {
		t.Errorf("expected updated data, got %s", string(updated.Data))
	}

	// 5. Archive the card.
	status := CardStatusArchived
	archived, err := repo.Patch(ctx, cardID, 2, PatchCardInput{
		Status: &status,
	})
	if err != nil {
		t.Fatalf("Archive (patch): %v", err)
	}
	if archived.Status != CardStatusArchived {
		t.Errorf("expected status archived, got %s", archived.Status)
	}
	if archived.ArchivedAt == nil {
		t.Error("expected ArchivedAt to be set")
	}
}

func TestRepoEvents(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()

	ctx := context.Background()
	repo, err := mgr.Repository(CardTypeExpanded)
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}

	// Create a card to attach events to.
	cardID := uuid.New()
	input := CreateCardInput{
		ID: cardID, TreeID: uuid.New(), NodeID: uuid.New(),
		AppID: "event-test", CardType: CardTypeExpanded,
		Data: json.RawMessage(`{}`), ContextHash: "abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123abc1",
	}
	_, err = repo.Create(ctx, input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Append events.
	eventID1 := uuid.New()
	e1, err := repo.AppendEvent(ctx, cardID, AppendEventInput{
		EventID:   eventID1,
		EventType: EventAgentProgress,
		ActorKind: ActorAgent,
		ActorID:   "agent-1",
		Payload:   json.RawMessage(`{"step":1}`),
	})
	if err != nil {
		t.Fatalf("AppendEvent 1: %v", err)
	}
	if e1.Sequence != 1 {
		t.Errorf("expected sequence 1, got %d", e1.Sequence)
	}

	eventID2 := uuid.New()
	_, err = repo.AppendEvent(ctx, cardID, AppendEventInput{
		EventID:   eventID2,
		EventType: EventAgentOutput,
		ActorKind: ActorAgent,
		ActorID:   "agent-1",
		Payload:   json.RawMessage(`{"output":"done"}`),
	})
	if err != nil {
		t.Fatalf("AppendEvent 2: %v", err)
	}

	// MaxSequence.
	maxSeq, err := repo.MaxSequence(ctx, cardID)
	if err != nil {
		t.Fatalf("MaxSequence: %v", err)
	}
	if maxSeq != 2 {
		t.Errorf("expected max sequence 2, got %d", maxSeq)
	}

	// ListEvents from start.
	events, err := repo.ListEvents(ctx, cardID, 0, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}

	// ListEvents after sequence 1 (should get only event 2).
	events, err = repo.ListEvents(ctx, cardID, 1, 10)
	if err != nil {
		t.Fatalf("ListEvents after seq 1: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event after seq 1, got %d", len(events))
	}
}

func TestServiceCreateCard(t *testing.T) {
	// This tests the service layer using the card DB.
	mgr := testDBManager(t)
	defer mgr.Close()

	svc := NewCardServiceImpl(mgr)
	ctx := context.Background()

	treeID := uuid.New()
	nodeID := uuid.New()

	summary, err := svc.CreateCard(ctx, treeID, nodeID, "test-app", "compact", map[string]any{"hello": "world"})
	if err != nil {
		t.Fatalf("CreateCard: %v", err)
	}
	if summary.TreeID != treeID {
		t.Errorf("expected treeID %s, got %s", treeID, summary.TreeID)
	}
	if summary.AppID != "test-app" {
		t.Errorf("expected appID test-app, got %s", summary.AppID)
	}

	// Get the card.
	got, err := svc.GetCard(ctx, summary.ID)
	if err != nil {
		t.Fatalf("GetCard: %v", err)
	}
	if got.ID != summary.ID {
		t.Errorf("expected ID %s, got %s", summary.ID, got.ID)
	}

	// List cards.
	list, err := svc.ListCards(ctx, &treeID, nil, nil, 10, 0)
	if err != nil {
		t.Fatalf("ListCards: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 card, got %d", len(list))
	}

	// Update card data.
	updated, err := svc.UpdateCardData(ctx, summary.ID, map[string]any{"updated": true})
	if err != nil {
		t.Fatalf("UpdateCardData: %v", err)
	}
	if updated.LastEventSeq != 2 {
		t.Errorf("expected LastEventSeq 2 (create + update events), got %d", updated.LastEventSeq)
	}

	// Archive card.
	if err := svc.ArchiveCard(ctx, summary.ID); err != nil {
		t.Fatalf("ArchiveCard: %v", err)
	}

	// Verify archived.
	got, err = svc.GetCard(ctx, summary.ID)
	if err != nil {
		t.Fatalf("GetCard after archive: %v", err)
	}
	if got.Status != "archived" {
		t.Errorf("expected status archived, got %s", got.Status)
	}
}

func ptr[T any](v T) *T {
	return &v
}

func TestRepoPatchDistinguishesRevisionConflictFromNotFound(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()
	repo, err := mgr.Repository(CardTypeCompact)
	if err != nil {
		t.Fatal(err)
	}
	created, err := repo.Create(context.Background(), CreateCardInput{
		ID: uuid.New(), TreeID: uuid.New(), NodeID: uuid.New(), AppID: "revision-test", CardType: CardTypeCompact,
		Data: json.RawMessage(`{"title":"before"}`), ContextHash: "abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123abc1",
	})
	if err != nil {
		t.Fatal(err)
	}

	data := json.RawMessage(`{"title":"after"}`)
	_, err = repo.Patch(context.Background(), created.ID, created.Revision+1, PatchCardInput{Data: &data})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale patch error = %v, want ErrRevisionConflict", err)
	}

	_, err = repo.Patch(context.Background(), uuid.New(), 1, PatchCardInput{Data: &data})
	if errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("missing card error = %v, must not be ErrRevisionConflict", err)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing card error = %v, want sql.ErrNoRows", err)
	}
}

func TestRepoPatchRestoreClearsDismissedAt(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()
	repo, err := mgr.Repository(CardTypeCompact)
	if err != nil {
		t.Fatal(err)
	}
	created, err := repo.Create(context.Background(), CreateCardInput{
		ID: uuid.New(), TreeID: uuid.New(), NodeID: uuid.New(), AppID: "lifecycle-test", CardType: CardTypeCompact,
		Data: json.RawMessage(`{}`), ContextHash: "abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123abc1",
	})
	if err != nil {
		t.Fatal(err)
	}

	dismissed := CardStatusDismissed
	card, err := repo.Patch(context.Background(), created.ID, 1, PatchCardInput{Status: &dismissed})
	if err != nil {
		t.Fatal(err)
	}
	if card.DismissedAt == nil {
		t.Fatal("dismissed patch did not set dismissed_at")
	}

	active := CardStatusActive
	card, err = repo.Patch(context.Background(), created.ID, 2, PatchCardInput{Status: &active})
	if err != nil {
		t.Fatal(err)
	}
	if card.DismissedAt != nil {
		t.Fatalf("restore dismissed_at = %v, want nil", card.DismissedAt)
	}
}

func TestServiceCardLifecycleTransitionsAndGuards(t *testing.T) {
	mgr := testDBManager(t)
	defer mgr.Close()
	svc := NewCardServiceImpl(mgr)
	ctx := context.Background()

	created, err := svc.CreateCard(ctx, uuid.New(), uuid.New(), "lifecycle-service", service.CardTypeCompact, map[string]any{"title": "one"})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := mgr.Repository(CardTypeCompact)
	if err != nil {
		t.Fatal(err)
	}

	dismissed := string(CardStatusDismissed)
	got, err := svc.PatchCard(ctx, created.ID, 1, service.CardPatchInput{Status: &dismissed})
	if err != nil || got.Status != string(CardStatusDismissed) || got.Revision != 2 {
		t.Fatalf("active->dismissed = (%+v, %v)", got, err)
	}
	stored, _ := repo.Get(ctx, created.ID)
	if stored.DismissedAt == nil {
		t.Fatal("service dismiss did not set dismissed_at")
	}
	if _, err := svc.UpdateCardData(ctx, created.ID, map[string]any{"blocked": true}); !errors.Is(err, ErrStatusDismissed) {
		t.Fatalf("dismissed data update error = %v, want ErrStatusDismissed", err)
	}

	active := string(CardStatusActive)
	got, err = svc.PatchCard(ctx, created.ID, 2, service.CardPatchInput{Status: &active})
	if err != nil || got.Status != string(CardStatusActive) || got.Revision != 3 {
		t.Fatalf("dismissed->active = (%+v, %v)", got, err)
	}
	stored, _ = repo.Get(ctx, created.ID)
	if stored.DismissedAt != nil {
		t.Fatal("service restore did not clear dismissed_at")
	}
	events, err := repo.ListEvents(ctx, created.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(events); len(got) != 3 || got[0] != EventCardCreated || got[1] != EventCardDismissed || got[2] != EventCardRestored {
		t.Fatalf("restore lifecycle events = %v, want [created dismissed restored]", got)
	}

	if _, err := svc.PatchCard(ctx, created.ID, 3, service.CardPatchInput{Status: &active}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("active->active error = %v, want ErrInvalidTransition", err)
	}

	archived := string(CardStatusArchived)
	got, err = svc.PatchCard(ctx, created.ID, 3, service.CardPatchInput{Status: &archived})
	if err != nil || got.Status != string(CardStatusArchived) {
		t.Fatalf("active->archived = (%+v, %v)", got, err)
	}
	if _, err := svc.PatchCard(ctx, created.ID, got.Revision, service.CardPatchInput{Status: &active}); !errors.Is(err, ErrStatusArchived) {
		t.Fatalf("archived->active error = %v, want ErrStatusArchived", err)
	}
	if _, err := svc.UpdateCardData(ctx, created.ID, map[string]any{"blocked": true}); !errors.Is(err, ErrStatusArchived) {
		t.Fatalf("archived data update error = %v, want ErrStatusArchived", err)
	}

	second, err := svc.CreateCard(ctx, uuid.New(), uuid.New(), "lifecycle-service", service.CardTypeCompact, map[string]any{"title": "two"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PatchCard(ctx, second.ID, 1, service.CardPatchInput{Status: &dismissed}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PatchCard(ctx, second.ID, 2, service.CardPatchInput{Status: &archived}); err != nil {
		t.Fatalf("dismissed->archived: %v", err)
	}
	secondEvents, err := repo.ListEvents(ctx, second.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(secondEvents); len(got) != 3 || got[1] != EventCardDismissed || got[2] != EventCardArchived {
		t.Fatalf("dismissed archive events = %v", got)
	}
}
