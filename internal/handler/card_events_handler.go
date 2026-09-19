package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// Card SSE streaming contract (SPEC-PL-03 §9).
const (
	// cardSSEEventName is the SSE event name for one stored card event
	// (SPEC-PL-03 §9.3: any row in `events` -> card_event). Its frame carries
	// `id:` = the event's sequence.
	cardSSEEventName = "card_event"

	// cardSSESnapshotEvent is the SSE event name for the materialised card
	// (SPEC-PL-03 §9.3: create/patch/dismiss/restore/archive materialisation ->
	// card_snapshot). It is emitted once, first, on connect, so a fresh client
	// renders without replaying history.
	cardSSESnapshotEvent = "card_snapshot"

	// cardSSEHeartbeatEvent is the idle keep-alive event (SPEC-PL-03 §9.3: no
	// stored event -> heartbeat).
	cardSSEHeartbeatEvent = "heartbeat"

	// cardSSEHeartbeatInterval is the idle cadence the spec fixes at 30s. The
	// handler holds it in a field so tests can inject a short interval instead
	// of waiting half a minute.
	cardSSEHeartbeatInterval = 30 * time.Second

	// cardSSEWriteDeadline is twice the heartbeat cadence: clearing the server's
	// 30s WriteTimeout keeps the idle stream open, while this bound still drops
	// a peer that stops accepting writes.
	cardSSEWriteDeadline = 2 * cardSSEHeartbeatInterval

	// cardSSEBacklogLimit is the largest replay the stream serves
	// (SPEC-PL-03 §10 CARD_SSE_BACKLOG_LIMIT). A request further behind than
	// this is answered with a JSON 413 BEFORE any SSE header, so the client
	// fetches the card snapshot and reconnects with a fresh cursor instead of
	// being streamed the whole history.
	cardSSEBacklogLimit = 10000
)

// cardSSEEnvelope is the compact single-line JSON body of one `data:` line.
//
// SPEC-PL-03 §9.1 shows the envelope shape:
//
//	id: 42
//	event: card_event
//	data: {"event_type":"card_event","card_type":"iteration","card_id":"...",
//	       "sequence":42,"timestamp":"...","data":{...}}
//
// The nested `data` object is this repo's stored event row (snake_case, the
// first-party REST convention here) and, for a snapshot, the materialised card
// summary the REST card endpoints already return — one card JSON shape for both
// the stream and the GET routes.
type cardSSEEnvelope struct {
	EventType string          `json:"event_type"`
	CardID    uuid.UUID       `json:"card_id"`
	CardType  string          `json:"card_type,omitempty"`
	Sequence  int64           `json:"sequence"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// StreamCardEvents streams a card's events as Server-Sent Events.
//
//	GET /api/v1/cards/{card_id}/events?after_sequence={n}
//	Last-Event-ID: {n}
//
// Adaptation note (this repo's card surface is the single-id form, not the
// spec's /api/cards/{type}/{id}): the route carries only {card_id} and the card
// type comes from the stored card itself. Everything else follows SPEC-PL-03
// §9:
//
//  1. The replay cursor is the LARGER of `after_sequence` and `Last-Event-ID`.
//     Both are validated — and every error is answered as a normal JSON
//     response — BEFORE any SSE header is written, so a client that sent an
//     invalid cursor gets a real HTTP error rather than an unparseable stream.
//  2. On connect the stream emits one `card_snapshot` frame carrying the
//     current materialised card, then replays stored events after the cursor in
//     sequence order, then streams live events.
//  3. `heartbeat` frames keep an idle connection warm every 30s (injectable).
//  4. The subscription is registered before the first frame and released on
//     return, so a cancelled request context ends the stream promptly and leaks
//     nothing. Because the subscription precedes the replay, an append landing
//     between the two is returned by the replay AND fanned out live; the live
//     loop therefore drops any event the replay already framed, so each
//     sequence reaches the client exactly once without ever being skipped.
//
// The snapshot frame deliberately carries no `id:` line. An SSE client that
// sees no id keeps its previous Last-Event-ID, which is exactly right: the
// snapshot is not a position in the event log, and stamping it with the card's
// current max sequence would let a client that disconnects mid-replay reconnect
// past events it never received. The card's sequence is still visible as
// `data.last_event_seq`.
func (h *CardHandler) StreamCardEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 1. card_id and cursor are validated first: both are "normal JSON error,
	// no SSE headers" cases (SPEC-PL-03 §10).
	cardID, ok := parseCardID(w, r)
	if !ok {
		return
	}

	cursor, ok := parseCardEventCursor(w, r)
	if !ok {
		return
	}

	// 2. The card must exist (404 CARD_NOT_FOUND), and the snapshot frame
	// needs the materialised card.
	card, err := h.svc.GetCard(ctx, cardID)
	if err != nil {
		writeError(w, http.StatusNotFound, "CARD_NOT_FOUND", "card not found")
		return
	}

	// 3. Backlog guard: the last refusal before the stream is committed.
	// A request whose replay would exceed the limit is told to GET the snapshot
	// instead. A failure to read the max sequence is not fatal — replay below
	// is bounded by the same limit — so it is logged, not turned into a 500.
	maxSeq, err := h.svc.MaxCardEventSequence(ctx, cardID)
	if err != nil {
		log.Ctx(ctx).Warn().Err(err).Str("card_id", cardID.String()).
			Msg("card sse: max event sequence unavailable; replay is bounded by the backlog limit")
	} else if pending := maxSeq - cursor; pending > cardSSEBacklogLimit {
		writeError(w, http.StatusRequestEntityTooLarge, "CARD_SSE_BACKLOG_LIMIT",
			fmt.Sprintf("replay backlog of %d events exceeds the %d-event limit; GET the card snapshot and reconnect with a fresh cursor",
				pending, cardSSEBacklogLimit))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STREAMING_NOT_SUPPORTED",
			"streaming responses are not supported on this transport")
		return
	}

	// 4. Subscribe BEFORE the first frame is written, so an append that lands
	// while the snapshot/replay is being written is still delivered (and one
	// that lands before is covered by the replay cursor).
	events, unsubscribe := h.svc.SubscribeCardEvents(cardID)
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// The server's WriteTimeout covers the whole response, so clear it after
	// the initial flush. Each later frame gets its own bounded write deadline.
	rc := http.NewResponseController(w)
	deadlineSupported := true
	warnUnsupportedDeadline := func(err error) {
		log.Ctx(ctx).Warn().Err(err).Str("card_id", cardID.String()).
			Msg("card sse: response writer does not support write deadlines")
		deadlineSupported = false
	}
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		warnUnsupportedDeadline(err)
	}
	setSSEWriteDeadline := func() {
		if !deadlineSupported {
			return
		}
		if err := rc.SetWriteDeadline(time.Now().Add(cardSSEWriteDeadline)); err != nil {
			warnUnsupportedDeadline(err)
		}
	}

	// 5. Snapshot, then replay. The replay reports the highest sequence it
	// framed — the dedupe watermark the live loop below applies to an event
	// that arrived on BOTH paths (see the loop's comment).
	snapshot, err := cardSSESnapshotBody(card)
	if err != nil {
		log.Ctx(ctx).Error().Err(err).Str("card_id", cardID.String()).Msg("card sse: snapshot frame")
		return
	}
	setSSEWriteDeadline()
	if err := writeSSEFrame(w, "", cardSSESnapshotEvent, snapshot); err != nil {
		return
	}
	replayedThrough, err := h.replayCardEvents(ctx, w, cardID, cursor, card.Type, setSSEWriteDeadline)
	if err != nil {
		log.Ctx(ctx).Warn().Err(err).Str("card_id", cardID.String()).Msg("card sse: replay aborted")
		return
	}
	flusher.Flush()

	// 6. Live stream + heartbeat. A service without a hub hands back a nil
	// channel, which simply never fires: the stream then serves snapshot,
	// replay and heartbeats.
	heartbeat := time.NewTicker(h.sseHeartbeat())
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			// Dedupe: the subscription is registered BEFORE the replay, so an
			// append landing between the two is returned by the replay query
			// AND fanned out to this channel — the client would be framed the
			// same id twice. `replayedThrough` is the replay's highest framed
			// sequence. It is deliberately NOT floored at the client's cursor:
			// a cursor ahead of the log replays nothing, and flooring there
			// would silently swallow the next real append.
			if ev.Sequence <= replayedThrough {
				continue
			}
			body, err := cardSSEEventBody(ev, card.Type)
			if err != nil {
				log.Ctx(ctx).Error().Err(err).Str("card_id", cardID.String()).Msg("card sse: event frame")
				return
			}
			setSSEWriteDeadline()
			if err := writeSSEFrame(w, strconv.FormatInt(ev.Sequence, 10), cardSSEEventName, body); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			body, err := cardSSEHeartbeatBody(cardID)
			if err != nil {
				log.Ctx(ctx).Error().Err(err).Str("card_id", cardID.String()).Msg("card sse: heartbeat frame")
				return
			}
			setSSEWriteDeadline()
			if err := writeSSEFrame(w, "", cardSSEHeartbeatEvent, body); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// replayCardEvents writes every stored event after cursor, in sequence order,
// and reports the highest sequence it wrote (0 when it wrote none).
//
// The returned watermark is what lets the stream's live loop drop an event the
// replay already delivered. The subscription is registered BEFORE the replay
// runs (subscribe-before-replay, so no append is missed), which means an append
// landing in that window is published to the live channel AND returned by this
// query: the client would otherwise be framed the same sequence twice.
//
// The replay is fetched at limit+1 on purpose: if the log grew past the backlog
// limit between the pre-stream guard and this read, the extra row is the
// evidence, and the stream says so instead of silently truncating.
func (h *CardHandler) replayCardEvents(
	ctx context.Context,
	w io.Writer,
	cardID uuid.UUID,
	cursor int64,
	cardType service.CardType,
	setSSEWriteDeadline func(),
) (int64, error) {
	var replayedThrough int64

	events, err := h.svc.ListCardEvents(ctx, cardID, cursor, cardSSEBacklogLimit+1)
	if err != nil {
		return replayedThrough, err
	}

	if len(events) > cardSSEBacklogLimit {
		log.Ctx(ctx).Warn().Str("card_id", cardID.String()).
			Int("fetched", len(events)).
			Msg("card sse: replay grew past the backlog limit after the pre-stream check; truncating")
		events = events[:cardSSEBacklogLimit]
	}

	for _, ev := range events {
		body, err := cardSSEEventBody(ev, cardType)
		if err != nil {
			return replayedThrough, err
		}
		setSSEWriteDeadline()
		if err := writeSSEFrame(w, strconv.FormatInt(ev.Sequence, 10), cardSSEEventName, body); err != nil {
			return replayedThrough, err
		}
		if ev.Sequence > replayedThrough {
			replayedThrough = ev.Sequence
		}
	}
	return replayedThrough, nil
}

// parseCardEventCursor resolves the replay cursor from the request: the LARGER
// of the `after_sequence` query parameter and the `Last-Event-ID` header
// (SPEC-PL-03 §9.1). Either being non-integer or negative is a 400
// CARD_SSE_CURSOR_INVALID written as JSON, before any SSE header.
func parseCardEventCursor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	var cursor int64

	if raw := strings.TrimSpace(r.URL.Query().Get("after_sequence")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "CARD_SSE_CURSOR_INVALID",
				"after_sequence must be a non-negative integer")
			return 0, false
		}
		cursor = n
	}

	if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "CARD_SSE_CURSOR_INVALID",
				"Last-Event-ID must be a non-negative integer")
			return 0, false
		}
		if n > cursor {
			cursor = n
		}
	}

	return cursor, true
}

// ── frame bodies ──────────────────────────────────────────────────────

// cardSSEEventBody renders one stored event as the data body of a card_event
// frame.
func cardSSEEventBody(ev service.CardEvent, cardType service.CardType) ([]byte, error) {
	data, err := compactJSON(ev)
	if err != nil {
		return nil, fmt.Errorf("card sse: encode event %d: %w", ev.Sequence, err)
	}
	return compactJSON(cardSSEEnvelope{
		EventType: cardSSEEventName,
		CardID:    ev.CardID,
		CardType:  string(cardType),
		Sequence:  ev.Sequence,
		Timestamp: ev.CreatedAt,
		Data:      data,
	})
}

// cardSSESnapshotBody renders the materialised card as the data body of the
// card_snapshot frame. The body is the same card JSON the REST card routes
// return, so a stream client and a polling client decode one shape.
func cardSSESnapshotBody(card *service.CardSummary) ([]byte, error) {
	data, err := compactJSON(card)
	if err != nil {
		return nil, fmt.Errorf("card sse: encode snapshot: %w", err)
	}
	return compactJSON(cardSSEEnvelope{
		EventType: cardSSESnapshotEvent,
		CardID:    card.ID,
		CardType:  string(card.Type),
		Sequence:  card.LastEventSeq,
		Timestamp: time.Now().UTC(),
		Data:      data,
	})
}

// cardSSEHeartbeatBody renders the idle keep-alive body. It carries no sequence
// because it is not an event: §9.3 maps "no stored event" to a liveness update
// only.
func cardSSEHeartbeatBody(cardID uuid.UUID) ([]byte, error) {
	data, err := compactJSON(map[string]any{"card_id": cardID.String()})
	if err != nil {
		return nil, fmt.Errorf("card sse: encode heartbeat: %w", err)
	}
	return compactJSON(cardSSEEnvelope{
		EventType: cardSSEHeartbeatEvent,
		CardID:    cardID,
		Timestamp: time.Now().UTC(),
		Data:      data,
	})
}

// ── frame writing ─────────────────────────────────────────────────────

// writeSSEFrame writes one SSE frame: optional `id:`, `event:`, one `data:`
// line, and the terminating blank line.
//
// The data payload must be a single line — an embedded newline would end the
// data block early and the client would see a truncated event — so a payload
// carrying CR/LF is refused loudly rather than written. compactJSON never
// produces one (control characters are escaped inside JSON strings), which is
// what makes this an invariant check rather than a sanitiser.
func writeSSEFrame(w io.Writer, id, event string, data []byte) error {
	if bytes.ContainsAny(data, "\r\n") {
		return fmt.Errorf("card sse: %s frame payload contains a line break", event)
	}

	var b bytes.Buffer
	if id != "" {
		b.WriteString("id: ")
		b.WriteString(id)
		b.WriteByte('\n')
	}
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteByte('\n')
	b.WriteString("data: ")
	b.Write(data)
	b.WriteByte('\n')
	b.WriteByte('\n')

	_, err := w.Write(b.Bytes())
	return err
}

// compactJSON renders v as compact single-line JSON: json.Marshal, then
// json.Compact to strip the whitespace Marshal never emits anyway. Both are
// deterministic (Go sorts struct fields by declaration and map keys by key),
// so a frame body is stable for a given event.
func compactJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
