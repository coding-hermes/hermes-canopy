package card

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

// CardServiceImpl implements service.CardService backed by the card package.
type CardServiceImpl struct {
	dbMgr *CardDBManager
}

// NewCardServiceImpl creates a CardServiceImpl that uses the given CardDBManager
// to obtain per-type repositories.
func NewCardServiceImpl(dbMgr *CardDBManager) *CardServiceImpl {
	return &CardServiceImpl{dbMgr: dbMgr}
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
	_, err = repo.AppendEvent(ctx, card.ID, AppendEventInput{
		EventID:   uuid.New(),
		EventType: EventCardCreated,
		ActorKind: ActorUser,
		ActorID:   "user",
		Payload:   dataJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("card: append create event: %w", err)
	}

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
func (s *CardServiceImpl) UpdateCardData(ctx context.Context, cardID uuid.UUID, data any) (*service.CardSummary, error) {
	card, repo, err := s.findCardAndRepo(ctx, cardID)
	if err != nil {
		return nil, err
	}

	dataJSON, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("card: marshal data: %w", err)
	}

	raw := json.RawMessage(dataJSON)
	updated, err := repo.Patch(ctx, cardID, card.Revision, PatchCardInput{
		Data: &raw,
	})
	if err != nil {
		return nil, fmt.Errorf("card: patch: %w", err)
	}

	// Append card_updated event.
	_, err = repo.AppendEvent(ctx, cardID, AppendEventInput{
		EventID:   uuid.New(),
		EventType: EventCardUpdated,
		ActorKind: ActorUser,
		ActorID:   "user",
		Payload:   json.RawMessage(dataJSON),
	})
	if err != nil {
		return nil, fmt.Errorf("card: append update event: %w", err)
	}

	summary := CardToSummary(updated)
	seq, _ := repo.MaxSequence(ctx, cardID)
	summary.LastEventSeq = seq
	return summary, nil
}

// ArchiveCard sets the card status to archived and appends a card_archived event.
func (s *CardServiceImpl) ArchiveCard(ctx context.Context, cardID uuid.UUID) error {
	card, repo, err := s.findCardAndRepo(ctx, cardID)
	if err != nil {
		return err
	}

	status := CardStatusArchived
	_, err = repo.Patch(ctx, cardID, card.Revision, PatchCardInput{
		Status: &status,
	})
	if err != nil {
		return fmt.Errorf("card: archive: %w", err)
	}

	// Append card_archived event.
	_, err = repo.AppendEvent(ctx, cardID, AppendEventInput{
		EventID:   uuid.New(),
		EventType: EventCardArchived,
		ActorKind: ActorUser,
		ActorID:   "user",
		Payload:   json.RawMessage("{}"),
	})
	if err != nil {
		return fmt.Errorf("card: append archive event: %w", err)
	}

	return nil
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
