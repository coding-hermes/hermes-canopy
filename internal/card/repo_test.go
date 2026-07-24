package card

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
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
