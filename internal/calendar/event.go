package calendar

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// AppID is the stable card owner id for calendar event cards.
	AppID = "hermes.canopy.calendar"
	// CardDataSchema identifies the calendar card data envelope.
	CardDataSchema = "hermes.canopy.calendar.event.v1"
	maxEventJSON   = 1 << 20
	maxTextLength  = 32 << 10
)

// Status is the provider-independent participation state of an event.
type Status string

const (
	StatusConfirmed Status = "confirmed"
	StatusTentative Status = "tentative"
	StatusCancelled Status = "cancelled"
)

// Attendee is an organizer or event participant. Status is optional for an
// organizer and for providers that do not expose a response state.
type Attendee struct {
	Email    string `json:"email"`
	Name     string `json:"name,omitempty"`
	Status   string `json:"status,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// Recurrence contains the provider-neutral recurrence metadata retained by
// phase 1. RRULE is kept as an RFC 5545-style string; expansion is deferred.
type Recurrence struct {
	RRule    string   `json:"rrule,omitempty"`
	ExDates  []string `json:"exdates,omitempty"`
	ParentID string   `json:"recurrence_parent_id,omitempty"`
}

// CalendarEvent is the provider-agnostic event domain model. Start and End
// are RFC3339 timestamps for timed events, or inclusive start/exclusive end
// YYYY-MM-DD dates when AllDay is true. JSON uses the ICALENDAR-compatible
// dtstart/dtend names in the card envelope.
//
// ID is the Canopy event/card UUID. ProviderID, SourceID, and ProviderEventID
// identify the source without making this package depend on a provider.
type CalendarEvent struct {
	ID             string         `json:"id"`
	Title          string         `json:"title"`
	Summary        string         `json:"summary,omitempty"`
	Description    string         `json:"description,omitempty"`
	Location       string         `json:"location,omitempty"`
	Start          string         `json:"dtstart"`
	End            string         `json:"dtend"`
	AllDay         bool           `json:"all_day"`
	Timezone       string         `json:"timezone,omitempty"`
	Status         Status         `json:"status"`
	Organizer      *Attendee      `json:"organizer,omitempty"`
	Attendees      []Attendee     `json:"attendees,omitempty"`
	ProviderID     string         `json:"provider_id,omitempty"`
	SourceID       string         `json:"source_id,omitempty"`
	ProviderCardID string         `json:"provider_card_id,omitempty"`
	Recurrence     *Recurrence    `json:"recurrence,omitempty"`
	CreatedAt      time.Time      `json:"created_at,omitempty"`
	UpdatedAt      time.Time      `json:"updated_at,omitempty"`
	Revision       int64          `json:"revision"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// RangeQuery is a half-open [Start, End) UTC range. Events are returned when
// their intervals overlap the query. Cancelled events are omitted unless
// IncludeCancelled is true.
type RangeQuery struct {
	Start            time.Time
	End              time.Time
	IncludeCancelled bool
}

// Sentinel errors returned by the calendar domain and store.
var (
	ErrInvalidEvent     = errors.New("calendar: invalid event")
	ErrInvalidRange     = errors.New("calendar: invalid range")
	ErrEventNotFound    = errors.New("calendar: event not found")
	ErrRevisionConflict = errors.New("calendar: revision conflict")
	ErrEventCancelled   = errors.New("calendar: event is cancelled")
)

// Validate rejects values that cannot be represented by the phase-one wire and
// card envelope. It does not mutate the event.
func (e CalendarEvent) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return invalid("id is required")
	}
	if _, err := uuid.Parse(e.ID); err != nil {
		return invalid("id must be a UUID: %v", err)
	}
	if strings.TrimSpace(e.Title) == "" && strings.TrimSpace(e.Summary) == "" {
		return invalid("title or summary is required")
	}
	if len(e.Title) > 1024 || len(e.Summary) > 1024 {
		return invalid("title and summary must be at most 1024 bytes")
	}
	if len(e.Description) > maxTextLength || len(e.Location) > maxTextLength {
		return invalid("description and location exceed the size limit")
	}
	if !validStatus(e.Status) {
		return invalid("status %q is not confirmed, tentative, or cancelled", e.Status)
	}
	if e.Timezone != "" {
		if _, err := time.LoadLocation(e.Timezone); err != nil {
			return invalid("timezone %q is invalid: %v", e.Timezone, err)
		}
	}

	if e.AllDay {
		if !dateOnly(e.Start) || !dateOnly(e.End) {
			return invalid("all-day start and end must be YYYY-MM-DD dates")
		}
		start, _ := time.Parse("2006-01-02", e.Start)
		end, _ := time.Parse("2006-01-02", e.End)
		if !end.After(start) {
			return invalid("all-day end must be after start")
		}
	} else {
		start, err := parseRFC3339(e.Start)
		if err != nil {
			return invalid("start must be RFC3339 with an explicit offset: %v", err)
		}
		end, err := parseRFC3339(e.End)
		if err != nil {
			return invalid("end must be RFC3339 with an explicit offset: %v", err)
		}
		if !end.After(start) {
			return invalid("end must be after start")
		}
	}
	if err := validateAttendee(e.Organizer, true); err != nil {
		return err
	}
	for i := range e.Attendees {
		if err := validateAttendee(&e.Attendees[i], false); err != nil {
			return invalid("attendee %d: %v", i, err)
		}
	}
	for field, value := range map[string]string{
		"provider_id": e.ProviderID, "source_id": e.SourceID, "provider_card_id": e.ProviderCardID,
	} {
		if len(value) > 512 || strings.ContainsAny(value, "\r\n") {
			return invalid("%s is invalid", field)
		}
	}
	if e.Recurrence != nil {
		if len(e.Recurrence.RRule) > 4096 || (e.Recurrence.RRule != "" && !strings.HasPrefix(strings.ToUpper(e.Recurrence.RRule), "FREQ=")) {
			return invalid("recurrence rrule is invalid")
		}
		if e.Recurrence.ParentID != "" {
			if _, err := uuid.Parse(e.Recurrence.ParentID); err != nil {
				return invalid("recurrence parent id must be a UUID: %v", err)
			}
		}
		for i, date := range e.Recurrence.ExDates {
			if !dateOnly(date) {
				if _, err := parseRFC3339(date); err != nil {
					return invalid("recurrence exdate %d is invalid", i)
				}
			}
		}
	}
	encoded, err := json.Marshal(e)
	if err != nil {
		return invalid("event data is not valid JSON: %v", err)
	}
	if len(encoded) > maxEventJSON {
		return invalid("event data exceeds %d bytes", maxEventJSON)
	}
	return nil
}

// StartTime returns the event start as an instant. All-day dates use the event
// timezone when present, otherwise UTC.
func (e CalendarEvent) StartTime() (time.Time, error) {
	if e.AllDay {
		return dateTime(e.Start, e.Timezone)
	}
	return parseRFC3339(e.Start)
}

// EndTime returns the event end as an instant. All-day end is exclusive.
func (e CalendarEvent) EndTime() (time.Time, error) {
	if e.AllDay {
		return dateTime(e.End, e.Timezone)
	}
	return parseRFC3339(e.End)
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidEvent, fmt.Sprintf(format, args...))
}

func validStatus(status Status) bool {
	return status == StatusConfirmed || status == StatusTentative || status == StatusCancelled
}

func validateAttendee(a *Attendee, organizer bool) error {
	if a == nil {
		return nil
	}
	address := strings.TrimSpace(a.Email)
	if address == "" {
		return invalid("%s email is required", attendeeLabel(organizer))
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Address != address || !strings.Contains(address, "@") {
		return invalid("%s email %q is invalid", attendeeLabel(organizer), a.Email)
	}
	if len(a.Name) > 1024 || strings.ContainsAny(a.Name, "\r\n") {
		return invalid("%s name is invalid", attendeeLabel(organizer))
	}
	if a.Status != "" {
		switch a.Status {
		case "accepted", "declined", "tentative", "needs_action":
		default:
			return invalid("%s status %q is invalid", attendeeLabel(organizer), a.Status)
		}
	}
	return nil
}

func attendeeLabel(organizer bool) string {
	if organizer {
		return "organizer"
	}
	return "attendee"
}

func dateOnly(value string) bool {
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func parseRFC3339(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, errors.New("value is empty")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed, nil
}

func dateTime(value, timezone string) (time.Time, error) {
	location := time.UTC
	if timezone != "" {
		var err error
		location, err = time.LoadLocation(timezone)
		if err != nil {
			return time.Time{}, err
		}
	}
	date, err := time.ParseInLocation("2006-01-02", value, location)
	if err != nil {
		return time.Time{}, err
	}
	return date, nil
}
