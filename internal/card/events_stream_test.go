package card

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// failingCardActionExecutor is the agent_error path's driver: a declared
// action whose execution boundary fails records agent_error.
type failingCardActionExecutor struct{ err error }

func (f failingCardActionExecutor) ExecuteCardAction(
	_ context.Context, _ *Card, _ CardAction, _ json.RawMessage,
) (json.RawMessage, error) {
	return nil, f.err
}

// receiveEvent reads one event from a live subscription, failing the test
// rather than hanging when the stream is silent.
func receiveEvent(t *testing.T, ch <-chan service.CardEvent) service.CardEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("card event subscription closed unexpectedly")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a live card event")
		return service.CardEvent{}
	}
}

// ── hub ───────────────────────────────────────────────────────────────

func TestCardEventHubDeliversToSubscriber(t *testing.T) {
	hub := NewCardEventHub()
	cardID := uuid.New()

	ch, unsubscribe := hub.Subscribe(cardID)
	defer unsubscribe()

	if got := hub.SubscriberCount(cardID); got != 1 {
		t.Fatalf("SubscriberCount = %d, want 1", got)
	}
	if got := hub.TotalSubscribers(); got != 1 {
		t.Fatalf("TotalSubscribers = %d, want 1", got)
	}

	want := CardEvent{Sequence: 7, CardID: cardID, EventType: EventCardUpdated, ActorKind: ActorUser, ActorID: "user"}
	hub.Publish(cardID, want)

	select {
	case got := <-ch:
		if got.Sequence != want.Sequence || got.EventType != want.EventType || got.CardID != want.CardID {
			t.Fatalf("delivered event = %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("published event never arrived")
	}

	if got := hub.PublishedCount(); got != 1 {
		t.Fatalf("PublishedCount = %d, want 1", got)
	}
}

func TestCardEventHubIsolatesCards(t *testing.T) {
	hub := NewCardEventHub()
	subscribed, other := uuid.New(), uuid.New()

	ch, unsubscribe := hub.Subscribe(subscribed)
	defer unsubscribe()

	hub.Publish(other, CardEvent{Sequence: 1, CardID: other, EventType: EventCardUpdated})

	select {
	case ev := <-ch:
		t.Fatalf("subscriber for %s received another card's event: %+v", subscribed, ev)
	case <-time.After(100 * time.Millisecond):
	}

	hub.Publish(subscribed, CardEvent{Sequence: 2, CardID: subscribed, EventType: EventCardUpdated})
	select {
	case ev := <-ch:
		if ev.Sequence != 2 {
			t.Fatalf("delivered sequence = %d, want 2", ev.Sequence)
		}
	case <-time.After(time.Second):
		t.Fatal("own-card event never arrived")
	}
}

// TestCardEventHubDropsRatherThanBlocks pins the append-path guarantee: a
// subscriber that never reads must not stall (or panic) the publisher. It
// receives cardEventBuffer events and loses the rest — recoverable by cursor
// replay, which is exactly why dropping is allowed here.
func TestCardEventHubDropsRatherThanBlocks(t *testing.T) {
	hub := NewCardEventHub()
	cardID := uuid.New()

	ch, unsubscribe := hub.Subscribe(cardID)
	defer unsubscribe()

	const publishes = cardEventBuffer + 36

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < publishes; i++ {
			hub.Publish(cardID, CardEvent{Sequence: int64(i + 1), CardID: cardID, EventType: EventAgentProgress})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a full subscriber — an append path would stall behind a viewer")
	}

	received := 0
drain:
	for {
		select {
		case <-ch:
			received++
			continue
		default:
			break drain
		}
	}

	if received != cardEventBuffer {
		t.Fatalf("buffered events = %d, want %d (the buffer); drops are the recovery path", received, cardEventBuffer)
	}
	if got := hub.PublishedCount(); got != publishes {
		t.Fatalf("PublishedCount = %d, want %d (every publish is counted, delivered or dropped)", got, publishes)
	}
}

func TestCardEventHubUnsubscribeRemovesAndCloses(t *testing.T) {
	hub := NewCardEventHub()
	cardID := uuid.New()

	ch, unsubscribe := hub.Subscribe(cardID)
	if got := hub.SubscriberCount(cardID); got != 1 {
		t.Fatalf("SubscriberCount = %d, want 1", got)
	}

	unsubscribe()
	unsubscribe() // idempotent: a second call must not panic or double-close

	if got := hub.SubscriberCount(cardID); got != 0 {
		t.Fatalf("SubscriberCount after unsubscribe = %d, want 0", got)
	}
	if got := hub.TotalSubscribers(); got != 0 {
		t.Fatalf("TotalSubscribers after unsubscribe = %d, want 0", got)
	}

	if _, ok := <-ch; ok {
		t.Fatal("subscriber channel is still open after unsubscribe — a stream loop could not end on close")
	}

	// Publishing after the last subscriber left must be a no-op.
	hub.Publish(cardID, CardEvent{Sequence: 1, CardID: cardID})
}

func TestCardEventHubNilIsInert(t *testing.T) {
	var hub *CardEventHub

	ch, unsubscribe := hub.Subscribe(uuid.New())
	if ch != nil {
		t.Fatalf("nil hub Subscribe channel = %v, want nil", ch)
	}
	unsubscribe() // must not panic

	hub.Publish(uuid.New(), CardEvent{Sequence: 1}) // must not panic

	if got := hub.SubscriberCount(uuid.New()); got != 0 {
		t.Fatalf("nil hub SubscriberCount = %d, want 0", got)
	}
	if got := hub.TotalSubscribers(); got != 0 {
		t.Fatalf("nil hub TotalSubscribers = %d, want 0", got)
	}
	if got := hub.PublishedCount(); got != 0 {
		t.Fatalf("nil hub PublishedCount = %d, want 0", got)
	}
}

// TestCardEventHubConcurrentUse is the race surface: publishers, subscribers
// and unsubscribes running at once. It is meaningful under `go test -race`
// (AC7) — a send racing a channel close is the failure it exists to catch.
func TestCardEventHubConcurrentUse(t *testing.T) {
	hub := NewCardEventHub()
	cardID := uuid.New()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(2)
	for p := 0; p < 2; p++ {
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
					hub.Publish(cardID, CardEvent{Sequence: int64(i), CardID: cardID, EventType: EventAgentProgress})
				}
			}
		}()
	}

	// Subscribers come and go, each draining its own channel to completion so
	// the hub's close is exercised against a live reader.
	for s := 0; s < 8; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				ch, unsubscribe := hub.Subscribe(cardID)
				read := make(chan struct{})
				go func() {
					defer close(read)
					for range ch {
					}
				}()
				unsubscribe()
				unsubscribe()
				<-read
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	if got := hub.TotalSubscribers(); got != 0 {
		t.Fatalf("TotalSubscribers after all subscribers left = %d, want 0", got)
	}
}

// ── service publish path ──────────────────────────────────────────────

// TestCardServicePublishesEveryAppend drives each service path that writes an
// event and asserts the hub delivered the SAME value the store recorded — the
// assigned sequence included, which is what the SSE `id:` line carries.
func TestCardServicePublishesEveryAppend(t *testing.T) {
	mgr := NewCardDBManager(t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	hub := NewCardEventHub()
	svc := NewCardServiceImpl(mgr).WithEventHub(hub)
	ctx := context.Background()

	// Create assigns the card id internally, so no client can be subscribed
	// yet (the SSE route 404s a card that does not exist). The publish counter
	// is the observable trace of that append path publishing.
	before := hub.PublishedCount()
	card, err := svc.CreateCard(ctx, uuid.New(), uuid.New(), "sse-publish-test",
		service.CardTypeCompact, map[string]any{"title": "publish me"})
	if err != nil {
		t.Fatalf("CreateCard: %v", err)
	}
	if got := hub.PublishedCount() - before; got != 1 {
		t.Fatalf("CreateCard published %d events, want 1", got)
	}

	repo, err := mgr.Repository(CardTypeCompact)
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}

	// Declare an action so the action paths can run (a patch records no event).
	declared := []CardAction{{Label: "Run", Handler: "run"}}
	if _, err := repo.Patch(ctx, card.ID, 1, PatchCardInput{Actions: &declared}); err != nil {
		t.Fatalf("Patch(actions): %v", err)
	}

	events, unsubscribe := svc.SubscribeCardEvents(card.ID)
	defer unsubscribe()
	if events == nil {
		t.Fatal("SubscribeCardEvents returned a nil channel for a hub-backed service")
	}

	if _, err := svc.UpdateCardData(ctx, card.ID, map[string]any{"title": "updated"}); err != nil {
		t.Fatalf("UpdateCardData: %v", err)
	}
	if err := svc.ArchiveCard(ctx, card.ID); err != nil {
		t.Fatalf("ArchiveCard: %v", err)
	}

	// Action paths on a fresh card (the previous one is archived): completed
	// then agent_error.
	actionCard, err := svc.CreateCard(ctx, uuid.New(), uuid.New(), "sse-publish-test",
		service.CardTypeCompact, map[string]any{"title": "action card"})
	if err != nil {
		t.Fatalf("CreateCard (action): %v", err)
	}
	actionRepo, err := mgr.Repository(CardTypeCompact)
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	if _, err := actionRepo.Patch(ctx, actionCard.ID, 1, PatchCardInput{Actions: &declared}); err != nil {
		t.Fatalf("Patch(actions, action card): %v", err)
	}
	// Sequence numbers are global to the events table (AUTOINCREMENT), so the
	// cursor is this card's own create sequence rather than a literal 1.
	createSeq, err := actionRepo.MaxSequence(ctx, actionCard.ID)
	if err != nil {
		t.Fatalf("MaxSequence(action card): %v", err)
	}
	actionEvents, actionUnsub := svc.SubscribeCardEvents(actionCard.ID)
	defer actionUnsub()
	if _, err := svc.SubmitCardAction(ctx, actionCard.ID, "run", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("SubmitCardAction: %v", err)
	}
	failing := NewCardServiceImpl(mgr).WithEventHub(hub).WithActionExecutor(
		failingCardActionExecutor{err: errors.New("adapter exploded")})
	if _, err := failing.SubmitCardAction(ctx, actionCard.ID, "run", json.RawMessage(`{}`)); !errors.Is(err, service.ErrCardActionFailed) {
		t.Fatalf("SubmitCardAction (failing executor) error = %v, want ErrCardActionFailed", err)
	}

	cases := []struct {
		name   string
		cardID uuid.UUID
		events <-chan service.CardEvent
		// after is the sequence already in the log when the subscription was
		// registered: the subscription only ever sees later appends, so the
		// expectation is the stored log after that cursor.
		after int64
		want  int
	}{
		{"update + archive", card.ID, events, 1, 2},
		{"action completed + agent_error", actionCard.ID, actionEvents, createSeq, 4},
	}
	for _, tc := range cases {
		stored, err := repo.ListEvents(ctx, tc.cardID, tc.after, 100)
		if err != nil {
			t.Fatalf("%s: ListEvents: %v", tc.name, err)
		}
		if len(stored) != tc.want {
			t.Fatalf("%s: stored events = %d, want %d", tc.name, len(stored), tc.want)
		}

		for i, want := range stored {
			got := receiveEvent(t, tc.events)
			if got.Sequence != want.Sequence || got.EventID != want.EventID || got.EventType != string(want.EventType) {
				t.Fatalf("%s: published event %d = {seq:%d id:%s type:%s}, want {seq:%d id:%s type:%s}",
					tc.name, i, got.Sequence, got.EventID, got.EventType,
					want.Sequence, want.EventID, want.EventType)
			}
		}
	}
}

// TestCardServiceWithoutHubIsUnchanged pins the nil-hub contract: publishing is
// a no-op, the subscription is a nil channel, and every existing constructor
// keeps working (harness, scripts and any deployment that never wires SSE).
func TestCardServiceWithoutHubIsUnchanged(t *testing.T) {
	mgr := NewCardDBManager(t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	svc := NewCardServiceImpl(mgr)
	ctx := context.Background()

	ch, unsubscribe := svc.SubscribeCardEvents(uuid.New())
	if ch != nil {
		t.Fatalf("SubscribeCardEvents without a hub = %v, want nil", ch)
	}
	unsubscribe() // must not panic

	card, err := svc.CreateCard(ctx, uuid.New(), uuid.New(), "no-hub",
		service.CardTypeCompact, map[string]any{"title": "no hub"})
	if err != nil {
		t.Fatalf("CreateCard without a hub: %v", err)
	}
	if _, err := svc.UpdateCardData(ctx, card.ID, map[string]any{"title": "still fine"}); err != nil {
		t.Fatalf("UpdateCardData without a hub: %v", err)
	}

	events, err := svc.ListCardEvents(ctx, card.ID, 0, 10)
	if err != nil {
		t.Fatalf("ListCardEvents: %v", err)
	}
	if len(events) != 2 || events[0].EventType != string(EventCardCreated) || events[1].EventType != string(EventCardUpdated) {
		t.Fatalf("ListCardEvents = %+v, want [card_created card_updated] in sequence order", events)
	}
	if events[0].Sequence >= events[1].Sequence {
		t.Fatalf("event sequences are not ascending: %d then %d", events[0].Sequence, events[1].Sequence)
	}

	max, err := svc.MaxCardEventSequence(ctx, card.ID)
	if err != nil {
		t.Fatalf("MaxCardEventSequence: %v", err)
	}
	if max != events[1].Sequence {
		t.Fatalf("MaxCardEventSequence = %d, want %d", max, events[1].Sequence)
	}

	// Unknown card: the read paths report "not found" so the HTTP layer can
	// answer 404 rather than 500.
	if _, err := svc.ListCardEvents(ctx, uuid.New(), 0, 10); !errors.Is(err, service.ErrCardNotFound) {
		t.Fatalf("ListCardEvents(unknown card) error = %v, want ErrCardNotFound", err)
	}
	if _, err := svc.MaxCardEventSequence(ctx, uuid.New()); !errors.Is(err, service.ErrCardNotFound) {
		t.Fatalf("MaxCardEventSequence(unknown card) error = %v, want ErrCardNotFound", err)
	}
}

// TestCardServiceSubscriptionAdaptsAndCloses pins the adapter around the hub: a
// published card.CardEvent arrives as a service.CardEvent with its payload
// intact, and unsubscribing closes the caller's channel and drops the hub
// subscriber (no goroutine left behind).
func TestCardServiceSubscriptionAdaptsAndCloses(t *testing.T) {
	mgr := NewCardDBManager(t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	hub := NewCardEventHub()
	svc := NewCardServiceImpl(mgr).WithEventHub(hub)
	cardID := uuid.New()

	ch, unsubscribe := svc.SubscribeCardEvents(cardID)
	if got := hub.SubscriberCount(cardID); got != 1 {
		t.Fatalf("hub SubscriberCount = %d, want 1", got)
	}

	payload := json.RawMessage(`{"message":"tests running"}`)
	hub.Publish(cardID, CardEvent{
		Sequence:  42,
		EventID:   uuid.New(),
		CardID:    cardID,
		EventType: EventAgentProgress,
		ActorKind: ActorAgent,
		ActorID:   "coding",
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	})

	got := receiveEvent(t, ch)
	if got.Sequence != 42 || got.EventType != string(EventAgentProgress) || got.ActorKind != string(ActorAgent) {
		t.Fatalf("adapted event = %+v, want sequence 42 / agent_progress / agent", got)
	}
	if string(got.Payload) != string(payload) {
		t.Fatalf("adapted payload = %s, want %s", got.Payload, payload)
	}

	unsubscribe()
	unsubscribe() // idempotent

	if _, ok := <-ch; ok {
		t.Fatal("service subscription channel still open after unsubscribe")
	}

	deadline := time.Now().Add(5 * time.Second)
	for hub.SubscriberCount(cardID) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("hub SubscriberCount = %d after unsubscribe, want 0 (leaked subscriber)", hub.SubscriberCount(cardID))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestToServiceCardEventNormalizesEmptyPayload keeps the wire contract honest: a
// frame body must always be valid JSON, so an empty payload becomes {}.
func TestToServiceCardEventNormalizesEmptyPayload(t *testing.T) {
	got := toServiceCardEvent(CardEvent{Sequence: 1, EventType: EventAgentProgress})
	if string(got.Payload) != "{}" {
		t.Fatalf("payload = %q, want {}", got.Payload)
	}

	raw := json.RawMessage(`{"a":1}`)
	kept := toServiceCardEvent(CardEvent{Sequence: 2, Payload: raw})
	if string(kept.Payload) != string(raw) {
		t.Fatalf("payload = %q, want %q", kept.Payload, raw)
	}
}
