package card

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// CardServiceImpl implements service.CardService backed by the card package.
type CardServiceImpl struct {
	dbMgr    *CardDBManager
	executor CardActionExecutor
	// hub carries freshly appended events to live SSE subscribers
	// (SPEC-PL-03 §9). It is nil until WithEventHub installs one, and a nil
	// hub makes publishing a no-op — so a service built by
	// NewCardServiceImpl alone keeps its previous behaviour exactly.
	hub *CardEventHub
}

// NewCardServiceImpl creates a CardServiceImpl that uses the given CardDBManager
// to obtain per-type repositories. The action execution boundary defaults to
// the side-effect-free echo executor (see CardActionExecutor); install a real
// adapter with WithActionExecutor.
func NewCardServiceImpl(dbMgr *CardDBManager) *CardServiceImpl {
	return &CardServiceImpl{dbMgr: dbMgr, executor: echoActionExecutor{}}
}

// CreateCard creates a new card with an initial card_created event.
func (s *CardServiceImpl) CreateCard(
	ctx context.Context,
	treeID, nodeID uuid.UUID,
	appID string,
	cardType service.CardType,
	data any,
) (*service.CardSummary, error) {
	ct := CardType(cardType)
	if !IsValidCardType(ct) {
		return nil, fmt.Errorf("card: invalid card type %q", ct)
	}

	repo, err := s.dbMgr.Repository(ct)
	if err != nil {
		return nil, fmt.Errorf("card: get repo: %w", err)
	}

	dataJSON, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("card: marshal data: %w", err)
	}

	input := CreateCardInput{
		ID:       uuid.New(),
		TreeID:   treeID,
		NodeID:   nodeID,
		AppID:    appID,
		CardType: ct,
		Data:     dataJSON,
		Actions:  []CardAction{},
		// ContextHash is empty on creation; it gets set later when context is compiled.
		ContextHash: "",
	}

	card, err := repo.Create(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("card: create: %w", err)
	}

	// Append card_created event.
	created, err := repo.AppendEvent(ctx, card.ID, AppendEventInput{
		EventID:   uuid.New(),
		EventType: EventCardCreated,
		ActorKind: ActorUser,
		ActorID:   "user",
		Payload:   dataJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("card: append create event: %w", err)
	}
	s.publishEvent(created)

	return CardToSummary(card), nil
}

// GetCard retrieves a card by ID.
func (s *CardServiceImpl) GetCard(ctx context.Context, cardID uuid.UUID) (*service.CardSummary, error) {
	// Try each card type repo until we find the card.
	for _, ct := range []CardType{CardTypeCompact, CardTypeExpanded, CardTypeIteration} {
		repo, err := s.dbMgr.Repository(ct)
		if err != nil {
			continue
		}
		card, err := repo.Get(ctx, cardID)
		if err != nil {
			continue
		}
		summary := CardToSummary(card)

		// Populate last event sequence.
		seq, _ := repo.MaxSequence(ctx, cardID)
		summary.LastEventSeq = seq

		return summary, nil
	}

	return nil, fmt.Errorf("card: card %s not found", cardID)
}

// ListCards lists cards with optional filters.
func (s *CardServiceImpl) ListCards(
	ctx context.Context,
	treeID, nodeID *uuid.UUID,
	cardType *service.CardType,
	limit, offset int,
) ([]service.CardSummary, error) {
	var ctypes []CardType
	if cardType != nil {
		ct := CardType(*cardType)
		if !IsValidCardType(ct) {
			return nil, fmt.Errorf("card: invalid card type %q", ct)
		}
		ctypes = []CardType{ct}
	} else {
		ctypes = []CardType{CardTypeCompact, CardTypeExpanded, CardTypeIteration}
	}

	var allSummaries []service.CardSummary
	for _, ct := range ctypes {
		repo, err := s.dbMgr.Repository(ct)
		if err != nil {
			continue
		}

		opts := ListCardsOptions{
			TreeID:   treeID,
			NodeID:   nodeID,
			CardType: &ct,
			Limit:    limit,
			Offset:   offset,
		}

		cards, err := repo.List(ctx, opts)
		if err != nil {
			continue
		}

		for _, c := range cards {
			summary := CardToSummary(&c)
			seq, _ := repo.MaxSequence(ctx, c.ID)
			summary.LastEventSeq = seq
			allSummaries = append(allSummaries, *summary)
		}
	}

	if allSummaries == nil {
		allSummaries = []service.CardSummary{}
	}
	return allSummaries, nil
}

// UpdateCardData patches a card's data payload and appends a card_updated event.
// It is retained for internal callers that do not have an HTTP If-Match header;
// the HTTP path uses PatchCard with the caller's expected revision.
func (s *CardServiceImpl) UpdateCardData(ctx context.Context, cardID uuid.UUID, data any) (*service.CardSummary, error) {
	card, _, err := s.findCardAndRepo(ctx, cardID)
	if err != nil {
		return nil, err
	}

	dataJSON, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("card: marshal data: %w", err)
	}
	raw := json.RawMessage(dataJSON)
	return s.PatchCard(ctx, cardID, card.Revision, service.CardPatchInput{Data: &raw})
}

// PatchCard applies a compare-and-swap patch and records either the lifecycle
// event for a status transition or card_updated for ordinary data changes.
func (s *CardServiceImpl) PatchCard(ctx context.Context, cardID uuid.UUID, expectedRevision int64, input service.CardPatchInput) (*service.CardSummary, error) {
	card, repo, err := s.findCardAndRepo(ctx, cardID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", service.ErrCardNotFound, cardID)
	}

	if card.Revision != expectedRevision {
		return nil, fmt.Errorf("card: patch %s at revision %d (current %d): %w", cardID, expectedRevision, card.Revision, ErrRevisionConflict)
	}
	hasDataMutation := input.Data != nil || input.Actions != nil || input.ContextHash != nil
	if card.Status == CardStatusArchived {
		return nil, fmt.Errorf("%w: %s", ErrStatusArchived, cardID)
	}
	if card.Status == CardStatusDismissed && hasDataMutation {
		return nil, fmt.Errorf("%w: %s", ErrStatusDismissed, cardID)
	}
	if input.Status == nil && !hasDataMutation {
		return nil, fmt.Errorf("%w: patch has no mutable fields", ErrInvalidTransition)
	}

	var (
		repoInput = PatchCardInput{Data: input.Data, ContextHash: input.ContextHash}
		lifecycle CardEventType
		status    *CardStatus
	)
	if input.Actions != nil {
		actions := make([]CardAction, len(*input.Actions))
		for i, action := range *input.Actions {
			actions[i] = CardAction{Label: action.Label, Handler: action.Handler}
		}
		repoInput.Actions = &actions
	}
	if input.Status != nil {
		target := CardStatus(*input.Status)
		status = &target
		repoInput.Status = status
		var valid bool
		switch {
		case card.Status == CardStatusActive && target == CardStatusDismissed:
			lifecycle, valid = EventCardDismissed, true
		case card.Status == CardStatusDismissed && target == CardStatusActive:
			lifecycle, valid = EventCardRestored, true
		case (card.Status == CardStatusActive || card.Status == CardStatusDismissed) && target == CardStatusArchived:
			lifecycle, valid = EventCardArchived, true
		}
		if !valid {
			if card.Status == CardStatusArchived {
				return nil, fmt.Errorf("%w: %s", ErrStatusArchived, cardID)
			}
			return nil, fmt.Errorf("%w: %s to %s", ErrInvalidTransition, card.Status, target)
		}
		if target == CardStatusArchived && hasDataMutation {
			return nil, fmt.Errorf("%w: %s", ErrStatusArchived, cardID)
		}
	}

	updated, err := repo.Patch(ctx, cardID, expectedRevision, repoInput)
	if err != nil {
		return nil, fmt.Errorf("card: patch: %w", err)
	}

	if lifecycle != "" {
		payload, _ := json.Marshal(map[string]string{"status": string(*status)})
		event, appendErr := repo.AppendEvent(ctx, cardID, AppendEventInput{
			EventID: uuid.New(), EventType: lifecycle, ActorKind: ActorUser, ActorID: "user", Payload: payload,
		})
		if appendErr != nil {
			return nil, fmt.Errorf("card: append lifecycle event: %w", appendErr)
		}
		s.publishEvent(event)
	}
	if hasDataMutation {
		payload := json.RawMessage(`{}`)
		if input.Data != nil {
			payload = *input.Data
		}
		event, appendErr := repo.AppendEvent(ctx, cardID, AppendEventInput{
			EventID: uuid.New(), EventType: EventCardUpdated, ActorKind: ActorUser, ActorID: "user", Payload: payload,
		})
		if appendErr != nil {
			return nil, fmt.Errorf("card: append update event: %w", appendErr)
		}
		s.publishEvent(event)
	}

	summary := CardToSummary(updated)
	seq, _ := repo.MaxSequence(ctx, cardID)
	summary.LastEventSeq = seq
	return summary, nil
}

// ArchiveCard keeps DELETE semantics while accepting an optional revision for
// legacy internal callers. HTTP callers always supply If-Match via this value.
func (s *CardServiceImpl) ArchiveCard(ctx context.Context, cardID uuid.UUID, expectedRevision ...int64) error {
	card, _, err := s.findCardAndRepo(ctx, cardID)
	if err != nil {
		return err
	}
	expected := card.Revision
	if len(expectedRevision) > 0 {
		expected = expectedRevision[0]
	}
	status := string(CardStatusArchived)
	if _, err := s.PatchCard(ctx, cardID, expected, service.CardPatchInput{Status: &status}); err != nil {
		return fmt.Errorf("card: archive: %w", err)
	}
	return nil
}

// ListCardEvents implements service.CardService: the card's stored events after
// a sequence cursor, in sequence order. Replay for the SSE stream reads the
// event log directly — there is no second event store (SPEC-PL-03 §9.2).
func (s *CardServiceImpl) ListCardEvents(
	ctx context.Context,
	cardID uuid.UUID,
	afterSequence int64,
	limit int,
) ([]service.CardEvent, error) {
	_, repo, err := s.findCardAndRepo(ctx, cardID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", service.ErrCardNotFound, cardID)
	}

	events, err := repo.ListEvents(ctx, cardID, afterSequence, limit)
	if err != nil {
		return nil, err
	}

	out := make([]service.CardEvent, 0, len(events))
	for _, ev := range events {
		out = append(out, toServiceCardEvent(ev))
	}
	return out, nil
}

// MaxCardEventSequence implements service.CardService: the highest stored event
// sequence for a card (0 when the card has no events). The SSE handler uses it
// to refuse a replay larger than the backlog limit BEFORE any SSE header is
// written.
func (s *CardServiceImpl) MaxCardEventSequence(ctx context.Context, cardID uuid.UUID) (int64, error) {
	_, repo, err := s.findCardAndRepo(ctx, cardID)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", service.ErrCardNotFound, cardID)
	}
	return repo.MaxSequence(ctx, cardID)
}

// findCardAndRepo looks up a card across all card type databases.
func (s *CardServiceImpl) findCardAndRepo(ctx context.Context, cardID uuid.UUID) (*Card, CardRepository, error) {
	for _, ct := range []CardType{CardTypeCompact, CardTypeExpanded, CardTypeIteration} {
		repo, err := s.dbMgr.Repository(ct)
		if err != nil {
			continue
		}
		card, err := repo.Get(ctx, cardID)
		if err != nil {
			continue
		}
		return card, repo, nil
	}
	return nil, nil, fmt.Errorf("card: card %s not found", cardID)
}
