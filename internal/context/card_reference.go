package context

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
	"github.com/coding-hermes/hermes-canopy/internal/reference"
	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// Card-reference compilation (SPEC-PL-03 §6.4).
//
// A graph node may reference cards in its content with the #card syntax parsed
// by internal/reference (ref://card/<id>). This file turns each resolvable
// reference into the compact §6.4 reference envelope — card id, app id, status,
// context hash, selected data fields and the most recent events — and renders
// it as a labeled <canopy_card> block, following the multi-reference block
// style in multi_reference.go: the same one-attribute-per-line header, the same
// rule that selected content is DATA and may never terminate the block, and the
// same per-reference manifest accounting.
//
// Two §6.4 semantics live here:
//
//   - A missing, dismissed or archived card is still RESOLVED, with its status
//     in the envelope. Absence is a status, never a failure: only a malformed
//     reference fails (and that failure is the parser's, in
//     internal/reference/card_ref.go).
//   - A typed reference (#card:<type>/<id>) whose declared type disagrees with
//     the card's actual type still resolves — with the card's own type — and
//     reports the disagreement on the compiler's warning channel. Parse-time
//     type validation is syntactic only; the type database is selected by card
//     id on this single-id card surface, never by the reference's type keyword.

// --- §6.4 constants ----------------------------------------------------------

const (
	// CardRefMaxEvents is the §6.4 cap on the envelope's event list: the most
	// recent 20 events of the card.
	CardRefMaxEvents = 20

	// CardRefStatusMissing is the status marker a reference resolves to when
	// the card is in no card-type store. §6.4 makes a missing card a status,
	// not a failure: the reference still resolves.
	CardRefStatusMissing = "missing"

	// CardRefPrivateKeyPrefix is the interim private-data marker. §6.4 says the
	// envelope "excludes data keys marked by the adapter as private"; no
	// adapter private-marker machinery exists yet, so a top-level key starting
	// with an underscore is excluded. The marker applies to every top-level
	// JSON object in the envelope — card data and event payloads alike.
	CardRefPrivateKeyPrefix = "_"

	// cardRefBlockOpen / cardRefBlockClose delimit the §6.4 card block, in the
	// same shape as the multi-reference block's canopy_multi_reference tags.
	cardRefBlockOpen  = `<canopy_card version="1"`
	cardRefBlockClose = "</canopy_card"

	// cardRefBlockTerminatorEscaped replaces any literal block terminator
	// inside a rendered card block, so neither card data nor an adapter-chosen
	// app id can terminate the block (§6.4 / SPEC-PL-06 §6.4, same rule).
	cardRefBlockTerminatorEscaped = `<\/canopy_card`
)

// --- Reader seam -------------------------------------------------------------

// CardRefReader is the card lookup seam the §6.4 step resolves through. It is
// satisfied by the shipped card service (*card.CardServiceImpl), which resolves
// a card id across the per-type stores and returns both the summary and the
// card's event log.
//
// CardEvents are read through ListCardEvents with an explicit sequence cursor,
// so the envelope carries the most RECENT events rather than the first page.
type CardRefReader interface {
	GetCard(ctx context.Context, cardID uuid.UUID) (*service.CardSummary, error)
	ListCardEvents(ctx context.Context, cardID uuid.UUID, afterSequence int64, limit int) ([]service.CardEvent, error)
}

// The shipped card service is the production implementation of the seam;
// keeping the assertion here means a signature drift in internal/card breaks
// this build rather than silently disabling the tier at wiring time.
var _ CardRefReader = (*card.CardServiceImpl)(nil)

// --- Wire types (§6.4 envelope) ---------------------------------------------

// CardReferenceEnvelope is the compact §6.4 reference envelope: card id, app
// id, status, context hash, the card's selected (non-private) data fields and
// the most recent events. It is what the <canopy_card> block carries.
type CardReferenceEnvelope struct {
	// ID is the resolved card id.
	ID uuid.UUID `json:"id"`
	// URI is the internal reference URI the envelope answers:
	// ref://card/<type>/<id> for the typed form, ref://card/<id> for the bare
	// single-id form (§6.4).
	URI string `json:"uri"`
	// CardType is the card's ACTUAL type, from the store.
	CardType string `json:"cardType,omitempty"`
	// DeclaredType is the type keyword the reference's typed form carried, when
	// it carried one. It is kept so a declared/actual disagreement stays
	// auditable in the envelope, not only in the warning.
	DeclaredType string `json:"declaredType,omitempty"`
	// AppID is the owning app's id.
	AppID string `json:"appId,omitempty"`
	// Status is the card's lifecycle status: active | dismissed | archived —
	// or "missing" when no store holds the card (§6.4).
	Status string `json:"status"`
	// Revision is the card's optimistic-concurrency revision.
	Revision int64 `json:"revision,omitempty"`
	// ContextHash is the card's context binding (lowercase SHA-256 of the
	// compiled context manifest), when the card carries one.
	ContextHash string `json:"contextHash,omitempty"`
	// Data carries the card's selected top-level data fields, private-marked
	// keys excluded.
	Data json.RawMessage `json:"data,omitempty"`
	// Events are the card's most recent events, in chronological (ascending
	// sequence) order — the order the agent boundary reads them in (§12.4
	// scenario 70).
	Events []CardRefEvent `json:"events"`
}

// CardRefEvent is the compact event view an envelope carries. The card id is
// omitted: the envelope names the card.
type CardRefEvent struct {
	Sequence  int64           `json:"sequence"`
	EventID   uuid.UUID       `json:"eventId"`
	EventType string          `json:"eventType"`
	ActorKind string          `json:"actorKind"`
	ActorID   string          `json:"actorId,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

// --- Resolution (§6.4) -------------------------------------------------------

// ResolveCardReference builds the §6.4 envelope for one parsed card reference.
// It never fails: a lookup error resolves to the "missing" status marker, and
// every degraded outcome is reported on the returned warning list.
func ResolveCardReference(ctx context.Context, reader CardRefReader, ref reference.CardReference) (*CardReferenceEnvelope, []string) {
	env := &CardReferenceEnvelope{
		ID:           ref.CardID,
		URI:          ref.URI,
		CardType:     ref.CardType,
		DeclaredType: ref.CardType,
		Status:       CardRefStatusMissing,
		Events:       []CardRefEvent{},
	}

	if reader == nil {
		return env, []string{"card references: no card reader wired"}
	}

	summary, err := reader.GetCard(ctx, ref.CardID)
	if err != nil {
		// The shipped CardService.GetCard reports a miss as a plain formatted
		// error ("card: card <id> not found") without the ErrCardNotFound
		// sentinel, so BOTH a miss and a store failure land on the missing
		// status here; the warning distinguishes them for the reader.
		if errors.Is(err, service.ErrCardNotFound) {
			return env, []string{fmt.Sprintf(
				"card reference %s: card %s not found (resolved with status %q)",
				ref.Raw, ref.CardID, CardRefStatusMissing)}
		}
		return env, []string{fmt.Sprintf(
			"card reference %s: card lookup failed: %v (resolved with status %q)",
			ref.Raw, err, CardRefStatusMissing)}
	}

	if summary == nil {
		return env, []string{fmt.Sprintf(
			"card reference %s: card %s returned no summary (resolved with status %q)",
			ref.Raw, ref.CardID, CardRefStatusMissing)}
	}

	env.CardType = string(summary.Type)
	env.AppID = summary.AppID
	env.Status = summary.Status
	env.Revision = summary.Revision
	env.ContextHash = summary.ContextHash
	env.Data = selectCardRefData(summary.Data)

	events, eventWarnings := loadCardRefEvents(ctx, reader, summary)
	if events == nil {
		events = []CardRefEvent{}
	}
	env.Events = events

	warnings := eventWarnings

	// The typed form's type is a SYNTAX check only (§6.4): a reference that
	// names the wrong type still resolves, with the card's own type, and the
	// disagreement is visible on the warning channel.
	if ref.CardType != "" && env.CardType != "" && ref.CardType != env.CardType {
		warnings = append(warnings, fmt.Sprintf(
			"card reference %s declares card type %q but card %s is %q; resolved with the card's own type",
			ref.Raw, ref.CardType, ref.CardID, env.CardType))
	}

	return env, warnings
}

// loadCardRefEvents reads the card's most recent events into the compact view.
// The window is derived from the card's highest sequence — read as the
// summary's LastEventSeq — so the envelope carries the LAST CardRefMaxEvents
// events rather than the first page of the log. Events come back in ascending
// sequence order, which is the chronological order §12.4 scenario 70 requires.
func loadCardRefEvents(ctx context.Context, reader CardRefReader, summary *service.CardSummary) ([]CardRefEvent, []string) {
	after := summary.LastEventSeq - CardRefMaxEvents
	if after < 0 {
		after = 0
	}

	events, err := reader.ListCardEvents(ctx, summary.ID, after, CardRefMaxEvents)
	if err != nil {
		return nil, []string{fmt.Sprintf(
			"card reference %s: event lookup failed: %v", summary.ID, err)}
	}

	// Defensive cap: a reader that ignores the limit must not widen the §6.4
	// window. When more than the cap comes back, keep the most recent ones.
	if len(events) > CardRefMaxEvents {
		events = events[len(events)-CardRefMaxEvents:]
	}

	out := make([]CardRefEvent, 0, len(events))
	for _, ev := range events {
		out = append(out, CardRefEvent{
			Sequence:  ev.Sequence,
			EventID:   ev.EventID,
			EventType: ev.EventType,
			ActorKind: ev.ActorKind,
			ActorID:   ev.ActorID,
			Payload:   selectCardRefPayload(ev.Payload),
			CreatedAt: ev.CreatedAt.UTC(),
		})
	}
	return out, nil
}

// --- Private-data rule (§6.4) ------------------------------------------------

// selectCardRefData applies the interim private-data marker to a card's data:
// every top-level key starting with "_" is dropped. Non-object data carries no
// top-level keys, so it passes through unchanged.
func selectCardRefData(data any) json.RawMessage {
	if data == nil {
		return nil
	}
	if obj, ok := data.(map[string]any); ok {
		selected := make(map[string]any, len(obj))
		for key, value := range obj {
			if strings.HasPrefix(key, CardRefPrivateKeyPrefix) {
				continue
			}
			selected[key] = value
		}
		raw, err := json.Marshal(selected)
		if err != nil {
			return nil
		}
		return raw
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	return raw
}

// selectCardRefPayload applies the same top-level marker to an event payload.
// A payload that is not JSON at all is passed through rather than dropped —
// the marker rule hides marked keys, it never removes evidence.
func selectCardRefPayload(payload json.RawMessage) json.RawMessage {
	if len(payload) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return payload
	}
	return selectCardRefData(decoded)
}

// --- Rendering (§6.1-style labeled block) -----------------------------------

// renderCardReferenceBlock renders one envelope as a labeled <canopy_card>
// block. The returned bool reports that the block terminator was found in the
// rendered content and escaped.
func renderCardReferenceBlock(env *CardReferenceEnvelope) (string, bool) {
	body, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		// Unreachable for these field types; an empty object is the honest
		// degraded rendering rather than a dropped block.
		body = []byte("{}")
	}

	attrs := []string{
		"  id=" + strconv.Quote(env.ID.String()),
		"  uri=" + strconv.Quote(env.URI),
		"  card_type=" + strconv.Quote(env.CardType),
		"  app_id=" + strconv.Quote(env.AppID),
		"  status=" + strconv.Quote(env.Status),
	}
	if env.ContextHash != "" {
		attrs = append(attrs, "  context_hash="+strconv.Quote(env.ContextHash))
	}

	header := cardRefBlockOpen + "\n"
	for i, attr := range attrs {
		header += attr
		if i == len(attrs)-1 {
			header += ">"
		}
		header += "\n"
	}

	// Escape the terminator across header AND body: the app id is
	// adapter-controlled, so the header is untrusted content too, and card data
	// or an event payload that contains the terminator must not close the block
	// (§6.4 — card content is data, never instructions).
	escaped, didEscape := escapeCardRefTerminator(header + string(body))
	return escaped + "\n" + cardRefBlockClose + ">", didEscape
}

// escapeCardRefTerminator neutralizes any literal card-block terminator inside
// rendered content.
func escapeCardRefTerminator(s string) (string, bool) {
	if !strings.Contains(s, cardRefBlockClose) {
		return s, false
	}
	return strings.ReplaceAll(s, cardRefBlockClose, cardRefBlockTerminatorEscaped), true
}
