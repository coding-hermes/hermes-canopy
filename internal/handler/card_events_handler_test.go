package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	_ "modernc.org/sqlite" // CGo-free SQLite driver, for the bulk backlog seed

	"github.com/coding-hermes/hermes-canopy/internal/card"
	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// ── test harness ──────────────────────────────────────────────────────

// streamRecorder is a race-safe http.ResponseWriter for SSE handlers.
//
// The handler writes frames from its own goroutine while the test reads the
// stream (httptest.ResponseRecorder wraps an unsynchronised bytes.Buffer, which
// is a data race under `go test -race`). Flush is signalled so a test can wait
// for a frame deterministically instead of sleeping.
type streamRecorder struct {
	mu      sync.Mutex
	header  http.Header
	status  int
	body    bytes.Buffer
	flushed chan struct{}
}

func newStreamRecorder() *streamRecorder {
	return &streamRecorder{header: http.Header{}, flushed: make(chan struct{}, 256)}
}

func (r *streamRecorder) Header() http.Header { return r.header }

func (r *streamRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = status
	}
}

func (r *streamRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(p)
}

func (r *streamRecorder) Flush() {
	select {
	case r.flushed <- struct{}{}:
	default:
	}
}

// snapshot returns the status, the bytes written so far, and a copy of the
// headers. Headers are only read once the handler has written them (after the
// first flush, or after it returned), which is the same contract a real
// ResponseWriter has.
func (r *streamRecorder) snapshot() (int, string, http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status, r.body.String(), r.header.Clone()
}

// waitFor waits until the stream contains substr (a frame that has been
// flushed), and returns the stream body at that moment.
func (r *streamRecorder) waitFor(t *testing.T, substr string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, body, _ := r.snapshot()
		if strings.Contains(body, substr) {
			return body
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out waiting for %q in the card event stream; stream so far:\n%s", substr, body)
		}
		if remaining > 25*time.Millisecond {
			remaining = 25 * time.Millisecond
		}
		select {
		case <-r.flushed:
		case <-time.After(remaining):
		}
	}
}

// sseFrame is one parsed SSE frame.
type sseFrame struct {
	ID    string
	Event string
	Data  string
}

// parseSSEFrames splits a stream body into frames. A malformed frame (a line
// that is neither blank nor `field: value`) fails the test: the wire contract
// is `id:` / `event:` / one `data:` line / blank.
func parseSSEFrames(t *testing.T, body string) []sseFrame {
	t.Helper()
	var frames []sseFrame

	for _, block := range strings.Split(body, "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		var f sseFrame
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "id: "):
				f.ID = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if f.Data != "" {
					t.Fatalf("frame carries more than one data line (an embedded newline split it):\n%s", block)
				}
				f.Data = strings.TrimPrefix(line, "data: ")
			default:
				t.Fatalf("unexpected SSE line %q in frame:\n%s", line, block)
			}
		}
		frames = append(frames, f)
	}
	return frames
}

// eventFrames filters a parsed stream to `card_event` frames, in stream order.
func eventFrames(t *testing.T, body string) []sseFrame {
	t.Helper()
	var out []sseFrame
	for _, f := range parseSSEFrames(t, body) {
		if f.Event == "card_event" {
			out = append(out, f)
		}
	}
	return out
}

// cardSSEStore builds a card service over a scratch card store, wired to a hub
// the test keeps a handle on so it can assert subscriber counts.
func cardSSEStore(t *testing.T) (*card.CardServiceImpl, *card.CardEventHub, card.CardRepository, string) {
	t.Helper()
	dir := t.TempDir()
	mgr := card.NewCardDBManager(dir)
	t.Cleanup(func() { _ = mgr.Close() })

	hub := card.NewCardEventHub()
	svc := card.NewCardServiceImpl(mgr).WithEventHub(hub)

	repo, err := mgr.Repository(card.CardTypeCompact)
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	return svc, hub, repo, dir
}

// seedCard creates a card directly in the store (no service, so no publish) and
// appends n events with predictable payloads.
func seedCard(t *testing.T, repo card.CardRepository, n int) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	created, err := repo.Create(ctx, card.CreateCardInput{
		ID:          uuid.New(),
		TreeID:      uuid.New(),
		NodeID:      uuid.New(),
		AppID:       "card-sse-test",
		CardType:    card.CardTypeCompact,
		Data:        json.RawMessage(`{"title":"stream me"}`),
		Actions:     []card.CardAction{},
		ContextHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for i := 1; i <= n; i++ {
		if _, err := repo.AppendEvent(ctx, created.ID, card.AppendEventInput{
			EventID:   uuid.New(),
			EventType: card.EventAgentProgress,
			ActorKind: card.ActorAgent,
			ActorID:   "coding",
			Payload:   json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)),
		}); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}
	return created.ID
}

// seedCardEventsBulk appends count events to a card in ONE transaction. The
// backlog-limit case needs >10,000 rows, and per-row autocommit appends would
// take seconds; this is the same table the repository reads.
func seedCardEventsBulk(t *testing.T, dir string, ctype card.CardType, cardID uuid.UUID, count int) {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(dir, string(ctype)+".db")+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open card db: %v", err)
	}
	defer func() { _ = db.Close() }()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(
		`INSERT INTO events (event_id, card_id, event_type, actor_kind, actor_id, payload, created_at)
		 VALUES (?, ?, 'agent_progress', 'agent', 'bulk', ?, ?)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < count; i++ {
		if _, err := stmt.Exec(uuid.NewString(), cardID.String(), fmt.Sprintf(`{"i":%d}`, i), now); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatalf("close stmt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// newCardSSERouter mounts the card routes the way production does, with an
// injectable heartbeat interval.
func newCardSSERouter(svc service.CardService, heartbeat time.Duration) http.Handler {
	r := chi.NewRouter()
	r.Mount("/cards", NewCardHandler(svc).WithSSEHeartbeat(heartbeat).Routes())
	return r
}

// runCardSSEStream serves one SSE request on its own goroutine and returns the
// recorder plus a stop func that cancels the request context and waits for the
// handler to return (so a test can never end with a live stream).
func runCardSSEStream(
	t *testing.T,
	h http.Handler,
	target string,
	headers map[string]string,
) (*streamRecorder, func()) {
	t.Helper()

	rec := newStreamRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()

	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("card SSE handler did not return after its request context was cancelled")
		}
	}
	return rec, stop
}

// decodeCardSSEEnvelope decodes one frame's data body.
func decodeCardSSEEnvelope(t *testing.T, frame sseFrame) cardSSEEnvelope {
	t.Helper()
	var env cardSSEEnvelope
	if err := json.Unmarshal([]byte(frame.Data), &env); err != nil {
		t.Fatalf("decode SSE data %q: %v", frame.Data, err)
	}
	return env
}

// ── replay (SPEC-PL-03 §9.1/§9.2) ─────────────────────────────────────

func TestCardEventsSSEReplayFromCursor(t *testing.T) {
	svc, _, repo, _ := cardSSEStore(t)
	cardID := seedCard(t, repo, 5)

	rec, stop := runCardSSEStream(t, newCardSSERouter(svc, time.Hour),
		"/cards/"+cardID.String()+"/events?after_sequence=3", nil)
	// Wait for the last replayed event, then for the handler to be idle.
	rec.waitFor(t, `"payload":{"i":5}`)
	stop()

	_, body, header := rec.snapshot()
	if got := header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if got := header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	if got := header.Get("Connection"); got != "keep-alive" {
		t.Fatalf("Connection = %q, want keep-alive", got)
	}

	frames := eventFrames(t, body)
	if len(frames) != 2 {
		t.Fatalf("replayed %d card_event frames, want 2 (sequences 4 and 5); stream:\n%s", len(frames), body)
	}

	// The card's first event is sequence 1 (the seed card), so a cursor of 3
	// replays 4 and 5 — ids are the stored sequences, in order.
	for i, wantSeq := range []int64{4, 5} {
		got := frames[i]
		if got.ID != strconv.FormatInt(wantSeq, 10) {
			t.Errorf("frame %d id = %q, want %d", i, got.ID, wantSeq)
		}
		if got.Event != "card_event" {
			t.Errorf("frame %d event = %q, want card_event", i, got.Event)
		}
		env := decodeCardSSEEnvelope(t, got)
		if env.Sequence != wantSeq {
			t.Errorf("frame %d envelope sequence = %d, want %d", i, env.Sequence, wantSeq)
		}
		if env.EventType != "card_event" {
			t.Errorf("frame %d envelope event_type = %q, want card_event", i, env.EventType)
		}
		if env.CardID != cardID {
			t.Errorf("frame %d envelope card_id = %s, want %s", i, env.CardID, cardID)
		}
		if env.CardType != string(card.CardTypeCompact) {
			t.Errorf("frame %d envelope card_type = %q, want compact", i, env.CardType)
		}
		if !strings.Contains(string(env.Data), fmt.Sprintf(`"payload":{"i":%d}`, wantSeq)) {
			t.Errorf("frame %d data = %s, want the stored payload for sequence %d", i, env.Data, wantSeq)
		}
		// The data body is compact single-line JSON (no embedded newlines).
		if strings.Contains(string(env.Data), "\n") {
			t.Errorf("frame %d data carries a line break: %s", i, env.Data)
		}
	}
}

func TestCardEventsSSECursorIsLargerOfParamAndHeader(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		headers map[string]string
		wantIDs []string
	}{
		{
			name:    "header larger than param",
			target:  "?after_sequence=2",
			headers: map[string]string{"Last-Event-ID": "5"},
			wantIDs: []string{"6"},
		},
		{
			name:    "param larger than header",
			target:  "?after_sequence=5",
			headers: map[string]string{"Last-Event-ID": "2"},
			wantIDs: []string{"6"},
		},
		{
			name:    "header only",
			target:  "",
			headers: map[string]string{"Last-Event-ID": "3"},
			wantIDs: []string{"4", "5", "6"},
		},
		{
			name:    "no cursor replays everything",
			target:  "",
			headers: nil,
			wantIDs: []string{"1", "2", "3", "4", "5", "6"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, repo, _ := cardSSEStore(t)
			cardID := seedCard(t, repo, 6)

			rec, stop := runCardSSEStream(t, newCardSSERouter(svc, time.Hour),
				"/cards/"+cardID.String()+"/events"+tc.target, tc.headers)
			rec.waitFor(t, fmt.Sprintf(`"payload":{"i":%s}`, tc.wantIDs[len(tc.wantIDs)-1]))
			stop()

			_, body, _ := rec.snapshot()
			frames := eventFrames(t, body)
			got := make([]string, 0, len(frames))
			for _, f := range frames {
				got = append(got, f.ID)
			}
			if strings.Join(got, ",") != strings.Join(tc.wantIDs, ",") {
				t.Fatalf("replayed ids = %v, want %v (cursor = larger of after_sequence and Last-Event-ID); stream:\n%s",
					got, tc.wantIDs, body)
			}
		})
	}
}

// ── snapshot + heartbeat (§9.3) ───────────────────────────────────────

func TestCardEventsSSESnapshotIsFirstFrame(t *testing.T) {
	svc, _, repo, _ := cardSSEStore(t)
	cardID := seedCard(t, repo, 3)

	rec, stop := runCardSSEStream(t, newCardSSERouter(svc, time.Hour),
		"/cards/"+cardID.String()+"/events", nil)
	rec.waitFor(t, `"payload":{"i":3}`)
	stop()

	_, body, _ := rec.snapshot()
	frames := parseSSEFrames(t, body)
	if len(frames) == 0 {
		t.Fatal("stream carries no frames")
	}

	first := frames[0]
	if first.Event != "card_snapshot" {
		t.Fatalf("first frame event = %q, want card_snapshot", first.Event)
	}
	if first.ID != "" {
		t.Errorf("snapshot frame carries id %q; a snapshot is not a position in the event log, so a client that reconnects mid-replay must keep its previous Last-Event-ID", first.ID)
	}

	env := decodeCardSSEEnvelope(t, first)
	if env.EventType != "card_snapshot" || env.CardID != cardID || env.CardType != string(card.CardTypeCompact) {
		t.Fatalf("snapshot envelope = %+v, want card_snapshot for %s (compact)", env, cardID)
	}

	var summary service.CardSummary
	if err := json.Unmarshal(env.Data, &summary); err != nil {
		t.Fatalf("decode snapshot card %s: %v", env.Data, err)
	}
	if summary.ID != cardID || summary.Status != "active" || summary.Type != service.CardTypeCompact {
		t.Fatalf("snapshot card = {id:%s status:%s type:%s}, want the materialised card", summary.ID, summary.Status, summary.Type)
	}
	if summary.LastEventSeq != 3 {
		t.Fatalf("snapshot last_event_seq = %d, want 3", summary.LastEventSeq)
	}

	// The snapshot precedes the replayed events.
	if len(frames) < 4 || frames[1].Event != "card_event" {
		t.Fatalf("frame 1 = %q, want the first replayed card_event", frames[1].Event)
	}
}

func TestCardEventsSSEHeartbeatOnIdleStream(t *testing.T) {
	svc, _, repo, _ := cardSSEStore(t)
	cardID := seedCard(t, repo, 1)

	const heartbeat = 40 * time.Millisecond
	rec, stop := runCardSSEStream(t, newCardSSERouter(svc, heartbeat),
		"/cards/"+cardID.String()+"/events", nil)
	rec.waitFor(t, "event: heartbeat")
	stop()

	_, body, _ := rec.snapshot()
	var heartbeats []sseFrame
	for _, f := range parseSSEFrames(t, body) {
		if f.Event == "heartbeat" {
			heartbeats = append(heartbeats, f)
		}
	}
	if len(heartbeats) == 0 {
		t.Fatalf("no heartbeat frame on an idle stream; stream:\n%s", body)
	}

	last := heartbeats[len(heartbeats)-1]
	if last.ID != "" {
		t.Errorf("heartbeat frame carries id %q; a heartbeat is not an event (§9.3: liveness only)", last.ID)
	}
	env := decodeCardSSEEnvelope(t, last)
	if env.EventType != "heartbeat" || env.CardID != cardID {
		t.Fatalf("heartbeat envelope = %+v, want heartbeat for %s", env, cardID)
	}
}

// ── live delivery + teardown (§9.2) ───────────────────────────────────

func TestCardEventsSSELiveDeliveryAndUnsubscribe(t *testing.T) {
	svc, hub, repo, _ := cardSSEStore(t)
	cardID := seedCard(t, repo, 1)

	rec, stop := runCardSSEStream(t, newCardSSERouter(svc, time.Hour),
		"/cards/"+cardID.String()+"/events?after_sequence=1", nil)
	// The subscription is registered before the first frame, so seeing the
	// snapshot proves a live append cannot be missed. The cursor consumes the
	// seeded event, so the ONLY card_event frame below is the live one.
	rec.waitFor(t, "event: card_snapshot")

	updated, err := svc.UpdateCardData(context.Background(), cardID, map[string]any{"title": "live"})
	if err != nil {
		t.Fatalf("UpdateCardData: %v", err)
	}

	rec.waitFor(t, `"event_type":"card_updated"`)
	_, body, _ := rec.snapshot()

	frames := eventFrames(t, body)
	if len(frames) != 1 {
		t.Fatalf("card_event frames = %d, want 1 (the live append); stream:\n%s", len(frames), body)
	}
	if frames[0].ID != strconv.FormatInt(updated.LastEventSeq, 10) {
		t.Fatalf("live frame id = %q, want the appended sequence %d", frames[0].ID, updated.LastEventSeq)
	}

	// Cancelling the request must end the handler and release the subscription.
	stop()

	deadline := time.Now().Add(5 * time.Second)
	for hub.SubscriberCount(cardID) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("hub subscriber count = %d after the request was cancelled, want 0 (leaked subscription)",
				hub.SubscriberCount(cardID))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := hub.TotalSubscribers(); got != 0 {
		t.Fatalf("hub total subscribers = %d after the request was cancelled, want 0", got)
	}
}

// appendDuringReplayService forces the subscribe->replay duplicate-delivery
// window deterministically, with no sleeps: the handler registers its live
// subscription BEFORE it reads the event log (so no append is missed), which
// means an append landing in that window is published to the hub AND returned
// by the replay query. This decorator creates exactly that state — the first
// replay read appends through the real service and only then delegates to the
// real read — so the stream sees one sequence on both paths.
//
// The embedded interface supplies every other method unchanged: the stream
// still talks to the real service for the snapshot, the backlog guard, the
// cursor and the live subscription.
type appendDuringReplayService struct {
	service.CardService

	cardID   uuid.UUID
	mutation map[string]any
	once     sync.Once
	err      error
}

func (s *appendDuringReplayService) ListCardEvents(
	ctx context.Context,
	cardID uuid.UUID,
	afterSequence int64,
	limit int,
) ([]service.CardEvent, error) {
	s.once.Do(func() {
		_, s.err = s.CardService.UpdateCardData(ctx, s.cardID, s.mutation)
	})
	return s.CardService.ListCardEvents(ctx, cardID, afterSequence, limit)
}

// TestCardEventsSSEDedupesAppendDuringReplay is the regression test for the
// duplicate-frame race: an append that lands while the handler is replaying is
// delivered by the replay AND by the live hub. The client must still see one
// frame per sequence, and a later live append must still be delivered.
func TestCardEventsSSEDedupesAppendDuringReplay(t *testing.T) {
	svc, _, repo, _ := cardSSEStore(t)
	cardID := seedCard(t, repo, 2)

	// Cursor 2 consumes both seeded events, so every card_event frame below is
	// produced by the race window or by the live append — nothing else.
	race := &appendDuringReplayService{
		CardService: svc,
		cardID:      cardID,
		mutation:    map[string]any{"race": "during-replay"},
	}

	rec, stop := runCardSSEStream(t, newCardSSERouter(race, time.Hour),
		"/cards/"+cardID.String()+"/events?after_sequence=2", nil)

	// The forced append is published to the hub before the replay query runs,
	// so this frame proves the race state was reached (and that the live
	// subscription was already registered when the append happened).
	rec.waitFor(t, `"race":"during-replay"`)

	// A later append happens strictly after replay: it can only arrive live.
	updated, err := svc.UpdateCardData(context.Background(), cardID, map[string]any{"later": "after-replay"})
	if err != nil {
		t.Fatalf("UpdateCardData (later live append): %v", err)
	}
	rec.waitFor(t, `"later":"after-replay"`)

	stop()
	if race.err != nil {
		t.Fatalf("forced append during replay failed: %v", race.err)
	}

	_, body, _ := rec.snapshot()
	frames := eventFrames(t, body)

	// Exactly one frame per sequence: the raced append (sequence 3, framed by
	// replay and suppressed on the live path) and the later live append.
	if len(frames) != 2 {
		t.Fatalf("card_event frames = %d, want 2 (one per sequence); stream:\n%s", len(frames), body)
	}
	racedSeq := int64(3) // seedCard(2) wrote sequences 1 and 2
	if got := frames[0].ID; got != strconv.FormatInt(racedSeq, 10) {
		t.Errorf("first frame id = %q, want %d (the append that landed during replay, delivered by replay)", got, racedSeq)
	}
	if got := frames[1].ID; got != strconv.FormatInt(updated.LastEventSeq, 10) {
		t.Errorf("second frame id = %q, want the later live append %d", got, updated.LastEventSeq)
	}
	if updated.LastEventSeq != racedSeq+1 {
		t.Fatalf("later append sequence = %d, want %d", updated.LastEventSeq, racedSeq+1)
	}

	seen := map[string]int{}
	for _, f := range frames {
		seen[f.ID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("sequence %s was framed %d times, want 1; stream:\n%s", id, n, body)
		}
	}
}

// TestCardEventsSSELiveAppendAfterCursorAheadOfLog pins the loss side of the
// dedupe boundary. A client may present a cursor beyond the log (nothing is
// replayed, so there is no replay watermark); the next real append must still
// be delivered. A boundary floored at the cursor instead of the replay
// watermark would silently swallow it.
func TestCardEventsSSELiveAppendAfterCursorAheadOfLog(t *testing.T) {
	svc, _, repo, _ := cardSSEStore(t)
	cardID := seedCard(t, repo, 2)

	rec, stop := runCardSSEStream(t, newCardSSERouter(svc, time.Hour),
		"/cards/"+cardID.String()+"/events?after_sequence=999", nil)
	rec.waitFor(t, "event: card_snapshot")

	updated, err := svc.UpdateCardData(context.Background(), cardID, map[string]any{"later": "after-replay"})
	if err != nil {
		t.Fatalf("UpdateCardData: %v", err)
	}
	rec.waitFor(t, `"later":"after-replay"`)
	stop()

	_, body, _ := rec.snapshot()
	frames := eventFrames(t, body)
	if len(frames) != 1 {
		t.Fatalf("card_event frames = %d, want 1 (the live append; the cursor replays nothing); stream:\n%s", len(frames), body)
	}
	if got := frames[0].ID; got != strconv.FormatInt(updated.LastEventSeq, 10) {
		t.Fatalf("live frame id = %q, want the appended sequence %d", got, updated.LastEventSeq)
	}
}

// TestCardEventsSSEServiceWithoutHubStillStreams pins the no-hub degradation:
// snapshot + replay are served from the store, and no live channel exists (a
// nil channel never fires).
func TestCardEventsSSEServiceWithoutHubStillStreams(t *testing.T) {
	dir := t.TempDir()
	mgr := card.NewCardDBManager(dir)
	t.Cleanup(func() { _ = mgr.Close() })

	svc := card.NewCardServiceImpl(mgr) // no hub
	repo, err := mgr.Repository(card.CardTypeCompact)
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	cardID := seedCard(t, repo, 2)

	rec, stop := runCardSSEStream(t, newCardSSERouter(svc, time.Hour),
		"/cards/"+cardID.String()+"/events", nil)
	rec.waitFor(t, `"payload":{"i":2}`)
	stop()

	_, body, _ := rec.snapshot()
	if got := len(eventFrames(t, body)); got != 2 {
		t.Fatalf("replayed %d events without a hub, want 2; stream:\n%s", got, body)
	}
}

// ── errors (§10) ──────────────────────────────────────────────────────

func TestCardEventsSSEErrorsAreJSONBeforeAnySSEHeader(t *testing.T) {
	svc, _, repo, _ := cardSSEStore(t)
	cardID := seedCard(t, repo, 1)

	cases := []struct {
		name     string
		target   string
		headers  map[string]string
		wantCode int
		wantErr  string
	}{
		{
			name:     "non-integer cursor",
			target:   "/cards/" + cardID.String() + "/events?after_sequence=abc",
			wantCode: http.StatusBadRequest,
			wantErr:  "CARD_SSE_CURSOR_INVALID",
		},
		{
			name:     "negative cursor",
			target:   "/cards/" + cardID.String() + "/events?after_sequence=-1",
			wantCode: http.StatusBadRequest,
			wantErr:  "CARD_SSE_CURSOR_INVALID",
		},
		{
			name:     "float cursor",
			target:   "/cards/" + cardID.String() + "/events?after_sequence=1.5",
			wantCode: http.StatusBadRequest,
			wantErr:  "CARD_SSE_CURSOR_INVALID",
		},
		{
			name:     "non-integer Last-Event-ID",
			target:   "/cards/" + cardID.String() + "/events",
			headers:  map[string]string{"Last-Event-ID": "not-a-number"},
			wantCode: http.StatusBadRequest,
			wantErr:  "CARD_SSE_CURSOR_INVALID",
		},
		{
			name:     "negative Last-Event-ID",
			target:   "/cards/" + cardID.String() + "/events",
			headers:  map[string]string{"Last-Event-ID": "-7"},
			wantCode: http.StatusBadRequest,
			wantErr:  "CARD_SSE_CURSOR_INVALID",
		},
		{
			name:     "cursor overflow",
			target:   "/cards/" + cardID.String() + "/events?after_sequence=99999999999999999999",
			wantCode: http.StatusBadRequest,
			wantErr:  "CARD_SSE_CURSOR_INVALID",
		},
		{
			name:     "unknown card",
			target:   "/cards/" + uuid.NewString() + "/events",
			wantCode: http.StatusNotFound,
			wantErr:  "CARD_NOT_FOUND",
		},
		{
			name:     "invalid card id",
			target:   "/cards/not-a-uuid/events",
			wantCode: http.StatusBadRequest,
			wantErr:  "INVALID_CARD_ID",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			newCardSSERouter(svc, time.Hour).ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json — an error must be a normal JSON response, never an SSE stream", got)
			}

			var body apiErrorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error envelope %q: %v", rec.Body.String(), err)
			}
			if body.Error.Code != tc.wantErr {
				t.Fatalf("error code = %q, want %q", body.Error.Code, tc.wantErr)
			}
			// No SSE header and no frame was written.
			if strings.Contains(rec.Body.String(), "event: ") || strings.Contains(rec.Body.String(), "data: ") {
				t.Fatalf("error response carries SSE syntax: %s", rec.Body.String())
			}
		})
	}
}

func TestCardEventsSSEBacklogLimit(t *testing.T) {
	const seeded = 10002 // card_created + 10001 progress events

	svc, _, repo, dir := cardSSEStore(t)
	cardID := seedCard(t, repo, 1) // one normal event (sequence 1)
	seedCardEventsBulk(t, dir, card.CardTypeCompact, cardID, seeded-1)

	maxSeq, err := repo.MaxSequence(context.Background(), cardID)
	if err != nil {
		t.Fatalf("MaxSequence: %v", err)
	}
	if maxSeq != seeded {
		t.Fatalf("seeded max sequence = %d, want %d", maxSeq, seeded)
	}

	// cursor 1 => 10,001 pending events: above the 10,000 limit.
	rec := httptest.NewRecorder()
	newCardSSERouter(svc, time.Hour).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/cards/"+cardID.String()+"/events?after_sequence=1", nil))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json (a 413 must not start an SSE stream)", got)
	}
	var body apiErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error envelope %q: %v", rec.Body.String(), err)
	}
	if body.Error.Code != "CARD_SSE_BACKLOG_LIMIT" {
		t.Fatalf("error code = %q, want CARD_SSE_BACKLOG_LIMIT", body.Error.Code)
	}

	// Boundary: exactly 10,000 pending is served. The replay is bounded by the
	// same limit, so this also pins "above 10,000" rather than "10,000 or more".
	rec2, stop := runCardSSEStream(t, newCardSSERouter(svc, time.Hour),
		"/cards/"+cardID.String()+"/events?after_sequence=2", nil)
	// The last bulk row's payload, not `"sequence":N`: the snapshot frame also
	// carries the card's max sequence, so a sequence marker would be satisfied
	// before the replay finished.
	rec2.waitFor(t, fmt.Sprintf(`"payload":{"i":%d}`, seeded-2))
	stop()

	_, stream, header := rec2.snapshot()
	if got := header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream at the limit boundary", got)
	}
	if got := len(eventFrames(t, stream)); got != 10000 {
		t.Fatalf("replayed %d frames at the boundary, want 10000", got)
	}
}

// ── frame-body invariants ─────────────────────────────────────────────

func TestCardSSEFrameWriterRejectsEmbeddedNewlines(t *testing.T) {
	var buf bytes.Buffer
	if err := writeSSEFrame(&buf, "1", "card_event", []byte("{\"a\":\n\"b\"}")); err == nil {
		t.Fatal("writeSSEFrame accepted a payload with a newline — the client would read a truncated event")
	}
	if buf.Len() != 0 {
		t.Fatalf("writeSSEFrame wrote %d bytes for a rejected frame, want none", buf.Len())
	}
}

func TestCompactJSONIsSingleLineAndStable(t *testing.T) {
	payload := map[string]any{"message": "tests running", "nested": map[string]any{"b": 2, "a": 1}}
	first, err := compactJSON(payload)
	if err != nil {
		t.Fatalf("compactJSON: %v", err)
	}
	second, err := compactJSON(payload)
	if err != nil {
		t.Fatalf("compactJSON: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("compactJSON is not stable: %s vs %s", first, second)
	}
	if bytes.ContainsAny(first, "\r\n") {
		t.Fatalf("compactJSON produced a multi-line body: %s", first)
	}
	// Go sorts map keys, so the single-line form is deterministic.
	if string(first) != `{"message":"tests running","nested":{"a":1,"b":2}}` {
		t.Fatalf("compactJSON = %s, want compact single-line JSON", first)
	}
}
