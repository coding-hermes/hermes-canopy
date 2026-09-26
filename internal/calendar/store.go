package calendar

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
)

const calendarCardType = card.CardTypeExpanded

type eventEnvelope struct {
	Schema string        `json:"schema"`
	Event  CalendarEvent `json:"event"`
}

// CalendarStore is the persistence boundary used by the calendar service.
// Implementations must preserve event revisions and must not expose provider
// transport concerns.
type CalendarStore interface {
	Create(ctx context.Context, event CalendarEvent) (*CalendarEvent, error)
	Get(ctx context.Context, id string) (*CalendarEvent, error)
	Update(ctx context.Context, id string, expectedRevision int64, event CalendarEvent) (*CalendarEvent, error)
	Cancel(ctx context.Context, id string, expectedRevision int64) (*CalendarEvent, error)
	Delete(ctx context.Context, id string, expectedRevision int64) (*CalendarEvent, error)
	List(ctx context.Context, query RangeQuery) ([]CalendarEvent, error)
}

// CardCalendarStore stores events as expanded cards using the existing
// CardRepository/CardDBManager seam. It deliberately does not create a
// calendar-specific database or file format.
type CardCalendarStore struct {
	repo card.CardRepository
}

var _ CalendarStore = (*CardCalendarStore)(nil)

// NewCardCalendarStore opens the existing expanded-card repository from
// manager. The manager owns the database lifetime and must be closed by the
// caller.
func NewCardCalendarStore(manager *card.CardDBManager) (*CardCalendarStore, error) {
	if manager == nil {
		return nil, errors.New("calendar: card database manager is nil")
	}
	repo, err := manager.Repository(calendarCardType)
	if err != nil {
		return nil, fmt.Errorf("calendar: open card repository: %w", err)
	}
	return NewCardCalendarStoreFromRepository(repo), nil
}

// NewCardCalendarStoreFromRepository is useful when the application already
// resolved the expanded-card repository. The repository remains owned by the
// caller.
func NewCardCalendarStoreFromRepository(repo card.CardRepository) *CardCalendarStore {
	return &CardCalendarStore{repo: repo}
}

// Create persists an event and appends the standard card_created activity. An
// omitted ID is assigned a UUID; CalendarEvent.Validate still rejects an empty
// ID when called directly, keeping the domain contract strict.
func (s *CardCalendarStore) Create(ctx context.Context, event CalendarEvent) (*CalendarEvent, error) {
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	if event.UpdatedAt.IsZero() {
		event.UpdatedAt = event.CreatedAt
	}
	event.Revision = 1

	cardID, err := uuid.Parse(event.ID)
	if err != nil {
		return nil, invalid("id must be a UUID: %v", err)
	}
	data, err := marshalEnvelope(event)
	if err != nil {
		return nil, err
	}
	created, err := s.repo.Create(ctx, card.CreateCardInput{
		ID:       cardID,
		TreeID:   uuid.Nil,
		NodeID:   uuid.Nil,
		AppID:    AppID,
		CardType: calendarCardType,
		Data:     data,
		Actions:  []card.CardAction{},
	})
	if err != nil {
		return nil, translateCardError(err)
	}
	if event.Status == StatusCancelled {
		status := card.CardStatusDismissed
		created, err = s.repo.Patch(ctx, cardID, created.Revision, card.PatchCardInput{Status: &status})
		if err != nil {
			return nil, translateCardError(err)
		}
	}
	event.Revision = created.Revision
	data, err = marshalEnvelope(event)
	if err != nil {
		return nil, err
	}
	// A cancelled create is materialized as dismissed above; rewrite its data
	// with the resulting revision before returning it.
	if created.Revision != 1 {
		updated, patchErr := s.repo.Patch(ctx, cardID, created.Revision, card.PatchCardInput{Data: &data})
		if patchErr != nil {
			return nil, translateCardError(patchErr)
		}
		created = updated
		event.Revision = created.Revision
		data, _ = marshalEnvelope(event)
	}
	if _, err := s.repo.AppendEvent(ctx, cardID, card.AppendEventInput{
		EventID:   uuid.New(),
		EventType: card.EventCardCreated,
		ActorKind: card.ActorSystem,
		ActorID:   "calendar",
		Payload:   data,
	}); err != nil {
		return nil, translateCardError(err)
	}
	return &event, nil
}

// Get retrieves one event, including cancelled events.
func (s *CardCalendarStore) Get(ctx context.Context, id string) (*CalendarEvent, error) {
	cardID, err := parseID(id)
	if err != nil {
		return nil, err
	}
	stored, err := s.repo.Get(ctx, cardID)
	if err != nil {
		return nil, translateCardError(err)
	}
	return s.decodeCard(stored)
}

// Update replaces the event data using compare-and-swap revision semantics.
// Updating a cancelled event is rejected; cancellation is deliberately
// monotonic in phase 1.
func (s *CardCalendarStore) Update(ctx context.Context, id string, expectedRevision int64, event CalendarEvent) (*CalendarEvent, error) {
	cardID, err := parseID(id)
	if err != nil {
		return nil, err
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Status == StatusCancelled {
		return nil, fmt.Errorf("%w: %s", ErrEventCancelled, id)
	}
	if event.ID == "" {
		event.ID = id
	}
	if event.ID != id {
		return nil, invalid("update id %q does not match %q", event.ID, id)
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	event.CreatedAt = current.CreatedAt
	event.UpdatedAt = time.Now().UTC()
	event.Revision = expectedRevision + 1
	data, err := marshalEnvelope(event)
	if err != nil {
		return nil, err
	}
	patch := card.PatchCardInput{Data: &data}
	lifecycle := card.EventCardUpdated
	if event.Status == StatusCancelled {
		status := card.CardStatusDismissed
		patch.Status = &status
		lifecycle = card.EventCardDismissed
	}
	updated, err := s.repo.Patch(ctx, cardID, expectedRevision, patch)
	if err != nil {
		return nil, translateCardError(err)
	}
	event.Revision = updated.Revision
	data, _ = marshalEnvelope(event)
	if string(updated.Data) != string(data) {
		// The repository patch already wrote data. This branch only protects
		// against a future repository that normalizes JSON on write.
		updated.Data = data
	}
	if _, err := s.repo.AppendEvent(ctx, cardID, card.AppendEventInput{
		EventID: uuid.New(), EventType: lifecycle, ActorKind: card.ActorSystem,
		ActorID: "calendar", Payload: data,
	}); err != nil {
		return nil, translateCardError(err)
	}
	return &event, nil
}

// Cancel marks an event cancelled and dismisses its backing card. The card is
// retained, so Get remains available and its revision/event history survives.
func (s *CardCalendarStore) Cancel(ctx context.Context, id string, expectedRevision int64) (*CalendarEvent, error) {
	cardID, err := parseID(id)
	if err != nil {
		return nil, err
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Status == StatusCancelled {
		if current.Revision != expectedRevision {
			return nil, revisionConflict(expectedRevision, current.Revision)
		}
		return current, nil
	}
	current.Status = StatusCancelled
	current.UpdatedAt = time.Now().UTC()
	current.Revision = expectedRevision + 1
	data, err := marshalEnvelope(*current)
	if err != nil {
		return nil, err
	}
	status := card.CardStatusDismissed
	updated, err := s.repo.Patch(ctx, cardID, expectedRevision, card.PatchCardInput{Data: &data, Status: &status})
	if err != nil {
		return nil, translateCardError(err)
	}
	current.Revision = updated.Revision
	data, _ = marshalEnvelope(*current)
	if _, err := s.repo.AppendEvent(ctx, cardID, card.AppendEventInput{
		EventID: uuid.New(), EventType: card.EventCardDismissed, ActorKind: card.ActorSystem,
		ActorID: "calendar", Payload: data,
	}); err != nil {
		return nil, translateCardError(err)
	}
	return current, nil
}

// Delete is the service-facing alias for Cancel; physical deletion is not
// used because the existing card lifecycle is append-only/soft-delete based.
func (s *CardCalendarStore) Delete(ctx context.Context, id string, expectedRevision int64) (*CalendarEvent, error) {
	return s.Cancel(ctx, id, expectedRevision)
}

// List returns events overlapping query in deterministic start/end/id order.
func (s *CardCalendarStore) List(ctx context.Context, query RangeQuery) ([]CalendarEvent, error) {
	if err := validateRange(query); err != nil {
		return nil, err
	}
	cards, err := s.repo.List(ctx, card.ListCardsOptions{AppID: AppID, CardType: ptr(calendarCardType), Limit: 10000})
	if err != nil {
		return nil, translateCardError(err)
	}
	out := make([]CalendarEvent, 0, len(cards))
	for i := range cards {
		event, decodeErr := s.decodeCard(&cards[i])
		if decodeErr != nil {
			return nil, decodeErr
		}
		if event.Status == StatusCancelled && !query.IncludeCancelled {
			continue
		}
		start, startErr := event.StartTime()
		end, endErr := event.EndTime()
		if startErr != nil || endErr != nil {
			return nil, invalid("stored event %q has invalid range", event.ID)
		}
		if start.Before(query.End) && end.After(query.Start) {
			out = append(out, *event)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		is, _ := out[i].StartTime()
		js, _ := out[j].StartTime()
		if !is.Equal(js) {
			return is.Before(js)
		}
		ie, _ := out[i].EndTime()
		je, _ := out[j].EndTime()
		if !ie.Equal(je) {
			return ie.Before(je)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *CardCalendarStore) decodeCard(stored *card.Card) (*CalendarEvent, error) {
	if stored.AppID != AppID || stored.CardType != calendarCardType {
		return nil, ErrEventNotFound
	}
	var envelope eventEnvelope
	if err := json.Unmarshal(stored.Data, &envelope); err != nil || envelope.Schema != CardDataSchema {
		return nil, fmt.Errorf("calendar: decode event %s: %w", stored.ID, ErrInvalidEvent)
	}
	event := envelope.Event
	event.Revision = stored.Revision
	if event.Status == "" {
		return nil, invalid("stored event %q has no status", event.ID)
	}
	return &event, nil
}

func marshalEnvelope(event CalendarEvent) (json.RawMessage, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(eventEnvelope{Schema: CardDataSchema, Event: event})
	if err != nil {
		return nil, fmt.Errorf("calendar: marshal envelope: %w", err)
	}
	if len(data) > maxEventJSON {
		return nil, invalid("event envelope exceeds %d bytes", maxEventJSON)
	}
	return data, nil
}

func parseID(value string) (uuid.UUID, error) {
	if value == "" {
		return uuid.Nil, fmt.Errorf("%w: id is empty", ErrEventNotFound)
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: invalid id %q", ErrEventNotFound, value)
	}
	return id, nil
}

func translateCardError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %v", ErrEventNotFound, err)
	}
	if errors.Is(err, card.ErrRevisionConflict) {
		return fmt.Errorf("%w: %v", ErrRevisionConflict, err)
	}
	return err
}

func revisionConflict(expected, current int64) error {
	return fmt.Errorf("%w: expected %d, current %d", ErrRevisionConflict, expected, current)
}

func validateRange(query RangeQuery) error {
	if query.Start.IsZero() || query.End.IsZero() {
		return fmt.Errorf("%w: start and end are required", ErrInvalidRange)
	}
	if !query.End.After(query.Start) {
		return fmt.Errorf("%w: end must be after start", ErrInvalidRange)
	}
	return nil
}

func ptr[T any](value T) *T { return &value }
