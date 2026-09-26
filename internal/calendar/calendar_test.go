package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
)

func newTestService(t *testing.T) (*CalendarService, *card.CardDBManager) {
	t.Helper()
	manager := card.NewCardDBManager(t.TempDir())
	store, err := NewCardCalendarStore(manager)
	if err != nil {
		t.Fatalf("NewCardCalendarStore: %v", err)
	}
	return NewService(store), manager
}

func testEvent(id, start, end string) CalendarEvent {
	return CalendarEvent{
		ID:         id,
		Title:      "Planning",
		Start:      start,
		End:        end,
		Status:     StatusConfirmed,
		Timezone:   "America/New_York",
		ProviderID: "local",
		SourceID:   "work",
		Organizer:  &Attendee{Email: "owner@example.com", Name: "Owner"},
		Attendees:  []Attendee{{Email: "person@example.com", Status: "accepted"}},
	}
}

func TestCalendarEventValidationRejectsAmbiguousInput(t *testing.T) {
	valid := testEvent(uuid.NewString(), "2026-07-22T14:00:00-04:00", "2026-07-22T15:00:00-04:00")
	cases := []struct {
		name   string
		mutate func(*CalendarEvent)
	}{
		{"empty id", func(e *CalendarEvent) { e.ID = "" }},
		{"invalid timestamp", func(e *CalendarEvent) { e.Start = "2026-07-22 14:00" }},
		{"end before start", func(e *CalendarEvent) { e.End = "2026-07-22T13:00:00-04:00" }},
		{"invalid status", func(e *CalendarEvent) { e.Status = "free" }},
		{"invalid timezone", func(e *CalendarEvent) { e.Timezone = "Not/A_Timezone" }},
		{"invalid attendee", func(e *CalendarEvent) { e.Attendees[0].Email = "not-an-email" }},
		{"invalid all day range", func(e *CalendarEvent) {
			e.AllDay, e.Start, e.End = true, "2026-07-22", "2026-07-22"
		}},
		{"all day timestamp", func(e *CalendarEvent) {
			e.AllDay, e.Start, e.End = true, "2026-07-22T00:00:00Z", "2026-07-23T00:00:00Z"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := valid
			tc.mutate(&event)
			if err := event.Validate(); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("Validate() = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

func TestCalendarEventJSONRoundTripPreservesTimedMeaningAndAllDaySemantics(t *testing.T) {
	timed := testEvent(uuid.NewString(), "2026-07-22T14:00:00-04:00", "2026-07-22T15:00:00-04:00")
	encoded, err := json.Marshal(timed)
	if err != nil {
		t.Fatal(err)
	}
	var decoded CalendarEvent
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	start, _ := timed.StartTime()
	decodedStart, _ := decoded.StartTime()
	if !start.Equal(decodedStart) || timed.Start != decoded.Start || timed.End != decoded.End {
		t.Fatalf("timed round trip changed meaning: before=%+v after=%+v", timed, decoded)
	}

	allDay := CalendarEvent{
		ID:       uuid.NewString(),
		Title:    "Conference",
		Start:    "2026-07-22",
		End:      "2026-07-24",
		AllDay:   true,
		Timezone: "America/New_York",
		Status:   StatusTentative,
	}
	encoded, err = json.Marshal(allDay)
	if err != nil {
		t.Fatal(err)
	}
	decoded = CalendarEvent{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.AllDay || decoded.Start != allDay.Start || decoded.End != allDay.End {
		t.Fatalf("all-day round trip changed date semantics: before=%+v after=%+v", allDay, decoded)
	}
}

func TestCalendarServiceSQLiteCreateGetUpdateCancelAndErrors(t *testing.T) {
	svc, manager := newTestService(t)
	defer manager.Close()
	ctx := context.Background()
	id := uuid.NewString()
	event := testEvent(id, "2026-07-22T14:00:00-04:00", "2026-07-22T15:00:00-04:00")

	created, err := svc.Create(ctx, event)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Revision != 1 || created.ID != id {
		t.Fatalf("created = %+v, want revision 1 and id %s", created, id)
	}
	got, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Start != event.Start || got.Status != StatusConfirmed {
		t.Fatalf("Get = %+v", got)
	}

	changed := *got
	changed.Title = "Rescheduled planning"
	changed.Start = "2026-07-22T16:00:00-04:00"
	changed.End = "2026-07-22T17:00:00-04:00"
	updated, err := svc.Update(ctx, id, 1, changed)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Revision != 2 || updated.Title != changed.Title {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := svc.Update(ctx, id, 1, changed); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update error = %v, want ErrRevisionConflict", err)
	}

	cancelled, err := svc.Cancel(ctx, id, 2)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelled.Status != StatusCancelled || cancelled.Revision != 3 {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	if _, err := svc.Get(ctx, uuid.NewString()); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("missing get error = %v, want ErrEventNotFound", err)
	}
	if _, err := svc.Delete(ctx, id, 2); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale delete error = %v, want ErrRevisionConflict", err)
	}

	deleted, err := svc.Delete(ctx, uuid.NewString(), 1)
	if !errors.Is(err, ErrEventNotFound) || deleted != nil {
		t.Fatalf("missing delete = (%+v, %v), want ErrEventNotFound", deleted, err)
	}
	second := testEvent(uuid.NewString(), "2026-07-23T14:00:00-04:00", "2026-07-23T15:00:00-04:00")
	if _, err := svc.Create(ctx, second); err != nil {
		t.Fatal(err)
	}
	deleted, err = svc.Delete(ctx, second.ID, 1)
	if err != nil || deleted.Status != StatusCancelled {
		t.Fatalf("Delete = (%+v, %v), want cancelled event", deleted, err)
	}
}

func TestCalendarServiceSQLiteRangeFilteringAllDayAndDeterministicOrder(t *testing.T) {
	svc, manager := newTestService(t)
	defer manager.Close()
	ctx := context.Background()
	for _, event := range []CalendarEvent{
		testEvent("00000000-0000-0000-0000-000000000003", "2026-07-22T14:00:00-04:00", "2026-07-22T15:00:00-04:00"),
		testEvent("00000000-0000-0000-0000-000000000001", "2026-07-22T14:00:00Z", "2026-07-22T15:00:00Z"),
		{
			ID: "00000000-0000-0000-0000-000000000002", Title: "Holiday", Start: "2026-07-22", End: "2026-07-24",
			AllDay: true, Timezone: "America/New_York", Status: StatusTentative,
		},
	} {
		if _, err := svc.Create(ctx, event); err != nil {
			t.Fatalf("Create %s: %v", event.ID, err)
		}
	}
	cancelled := testEvent("00000000-0000-0000-0000-000000000004", "2026-07-22T18:00:00Z", "2026-07-22T19:00:00Z")
	if _, err := svc.Create(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(ctx, cancelled.ID, 1); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	got, err := svc.List(ctx, RangeQuery{Start: start, End: end})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List returned %d events, want 3 non-cancelled overlaps: %+v", len(got), got)
	}
	for i := 1; i < len(got); i++ {
		previous, _ := got[i-1].StartTime()
		current, _ := got[i].StartTime()
		if current.Before(previous) {
			t.Fatalf("List order is not deterministic: %+v", got)
		}
	}
	withCancelled, err := svc.List(ctx, RangeQuery{Start: start, End: end.Add(24 * time.Hour), IncludeCancelled: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(withCancelled) != 4 {
		t.Fatalf("IncludeCancelled returned %d events, want 4", len(withCancelled))
	}

	for _, query := range []RangeQuery{{}, {Start: end, End: start}, {Start: start, End: start}} {
		if _, err := svc.List(ctx, query); !errors.Is(err, ErrInvalidRange) {
			t.Fatalf("invalid query %+v error = %v, want ErrInvalidRange", query, err)
		}
	}
}

func TestCalendarStoreUsesExpandedCardEnvelope(t *testing.T) {
	svc, manager := newTestService(t)
	defer manager.Close()
	created, err := svc.Create(context.Background(), testEvent(uuid.NewString(), "2026-07-22T14:00:00Z", "2026-07-22T15:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := manager.Repository(card.CardTypeExpanded)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repo.Get(context.Background(), uuid.MustParse(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(stored.Data, &envelope); err != nil {
		t.Fatal(err)
	}
	if string(envelope["schema"]) != `"`+CardDataSchema+`"` || len(envelope["event"]) == 0 {
		t.Fatalf("stored data is not the documented envelope: %s", stored.Data)
	}
	if stored.AppID != AppID || stored.CardType != card.CardTypeExpanded {
		t.Fatalf("stored card = %+v, want calendar expanded card", stored)
	}
}
