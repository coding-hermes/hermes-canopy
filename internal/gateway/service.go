package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// maxEventsPerRun bounds the in-memory event history kept per run.
const maxEventsPerRun = 300

// maxRuns bounds the in-memory run registry (oldest terminal runs are pruned).
const maxRuns = 100

// maxSubscriberBuffer bounds the per-subscriber fan-out channel.
const maxSubscriberBuffer = 256

// RunRecord is Canopy's view of one Hermes gateway run: the start request
// plus everything learned from the SSE event stream.
type RunRecord struct {
	RunID     string         `json:"run_id"`
	SessionID string         `json:"session_id"`
	Message   string         `json:"message"`
	Model     string         `json:"model"`
	Status    string         `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	LastEvent string         `json:"last_event,omitempty"`
	Output    string         `json:"output,omitempty"`
	Error     string         `json:"error,omitempty"`
	Usage     map[string]any `json:"usage,omitempty"`
	Events    []RunEvent     `json:"events"`

	// Context provenance (GAP-075). All four fields are additive and
	// omitempty, so a context-free run keeps the pre-GAP-075 JSON shape
	// byte-for-byte. SourceNodeID/TokenBudget describe the compile request,
	// ContextTokens is the compiler manifest's TokensUsed, and Manifest
	// carries the manifest verbatim — this package never interprets it.
	SourceNodeID  string          `json:"source_node_id,omitempty"`
	TokenBudget   int             `json:"token_budget,omitempty"`
	ContextTokens int             `json:"context_tokens,omitempty"`
	Manifest      json.RawMessage `json:"manifest,omitempty"`
}

// IsTerminal reports whether the run reached a terminal gateway state.
func (r *RunRecord) IsTerminal() bool {
	switch r.Status {
	case "completed", "failed", "cancelled", "not_found":
		return true
	}
	return false
}

// Service owns the gateway client plus the in-memory run registry. It starts
// real runs on the Hermes gateway, observes their SSE event streams in the
// background, keeps a bounded event history per run, and fans events out to
// live subscribers (the Canopy frontend SSE endpoints). When statePath is
// set, the registry is persisted to a JSONL state file on every status
// transition and restored on startup (GAP-054).
type Service struct {
	client *Client

	// statePath is the JSONL file the registry is persisted to ("" disables
	// persistence). Defaults to DefaultStateFile() via NewServiceWithState.
	statePath string

	// Background lifecycle (CI-006). ctx is the SERVICE-scoped context every
	// observe goroutine runs on, so cancelling it (Close) aborts an in-flight
	// SSE body read instead of leaving a reader parked on a stream nobody
	// owns. wg tracks those goroutines so Close can wait for them to exit,
	// closeOnce makes Close idempotent, and closed (guarded by mu) makes every
	// later persist a no-op — the disconnect path in observe persists AFTER
	// ctx cancellation, so cancellation alone cannot stop the write that
	// raced the caller's teardown.
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	closed    bool

	mu   sync.RWMutex
	runs map[string]*RunRecord
	subs map[string]map[chan StreamEvent]struct{}
}

// DefaultStateFile returns the path for the persisted gateway run registry,
// following the card.DataDir() convention (~/.hermes/canopy/<sub>), unless
// CANOPY_GATEWAY_STATE_FILE is set and non-empty, in which case that value is
// returned verbatim.
//
// The override is returned verbatim — nothing is joined onto it, and it names
// the JSONL file itself, not a directory — so a scratch instance can keep its
// run registry off the shared store without a scratch $HOME
// (DF-HERMES-CANOPY-9). It is resolved on every call, never cached.
func DefaultStateFile() string {
	if path := os.Getenv("CANOPY_GATEWAY_STATE_FILE"); path != "" {
		return path
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/tmp"
	}
	return filepath.Join(home, ".hermes", "canopy", "gateway", "runs.jsonl")
}

// NewService builds a gateway Service around a client. The service owns a
// cancellable background context (see Close) used by every goroutine it
// starts; a caller that never calls Close keeps today's behaviour exactly,
// because nothing about production correctness depends on Close being called.
func NewService(client *Client) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		client: client,
		ctx:    ctx,
		cancel: cancel,
		runs:   make(map[string]*RunRecord),
		subs:   make(map[string]map[chan StreamEvent]struct{}),
	}
}

// NewServiceWithState builds a gateway Service backed by a persisted run
// registry at statePath: existing records are loaded and non-terminal
// records are refreshed against the gateway (best-effort). Any failure —
// missing/corrupt state file, gateway down, gateway 405 on the list
// endpoint — is logged, never fatal, so canopyd always boots and the
// /gateway routes always mount (GAP-054).
func NewServiceWithState(client *Client, statePath string) *Service {
	s := NewService(client)
	s.statePath = statePath
	s.loadState()
	s.Backfill(context.Background())
	return s
}

// Close releases the service's background lifecycle: it marks the service
// closed (no further persists), cancels the service-scoped context (aborting
// any in-flight SSE body read in observe), then waits for the observe
// goroutines to exit. It is idempotent — a second call is a no-op — and safe
// on a service with no state path, or one that never started a run.
//
// After Close returns no persist can happen: every write goes through
// persist/persistLocked holding mu, and each of those either completed before
// Close took mu, or observes the closed flag and returns. Waiting for the
// goroutines is what closes the second half of the window — the disconnect
// path in observe persists after the context is already cancelled.
//
// Close must not hold mu while waiting: the goroutines it waits for take mu
// on their way out, so holding it would deadlock.
func (s *Service) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
	})
}

// Client exposes the underlying gateway client (used by the handler for
// operations the service does not wrap).
func (s *Service) Client() *Client { return s.client }

// Connected probes the gateway health endpoint.
func (s *Service) Connected(ctx context.Context) error {
	return s.client.Health(ctx)
}

// StartRunInput is the context-aware start request. ManifestJSON carries the
// compiler's manifest verbatim (opaque to this package).
type StartRunInput struct {
	Message       string          // raw user message (always set)
	Input         string          // what the gateway receives; "" => Message
	SessionID     string          // conversation scope, "" for a fresh one
	SourceNodeID  string          // "" for a context-free run
	TokenBudget   int             // 0 when none was applied
	ContextTokens int             // compiler manifest TokensUsed; 0 when none
	ManifestJSON  json.RawMessage // nil for a context-free run
}

// StartRun creates a real Hermes gateway run and begins observing it. The
// returned record reflects the immediate 202 response; events arrive
// asynchronously.
func (s *Service) StartRun(ctx context.Context, message, sessionID string) (*RunRecord, error) {
	return s.StartRunWithContext(ctx, StartRunInput{Message: message, Input: message, SessionID: sessionID})
}

// StartRunWithContext is the context-aware start path (GAP-075): the model
// call's payload is the COMPILED context (in.Input) rather than the raw user
// message, and the compiler's manifest travels with the run record so the
// call is auditable after the fact. A context-free StartRunInput (Input and
// ManifestJSON empty) behaves exactly like StartRun.
//
// The gateway itself is unchanged: it receives a string `input` either way
// and never learns whether that string was compiled.
func (s *Service) StartRunWithContext(ctx context.Context, in StartRunInput) (*RunRecord, error) {
	input := in.Input
	if input == "" {
		input = in.Message
	}
	req := StartRunRequest{Input: input}
	if in.SessionID != "" {
		req.SessionID = in.SessionID
	}
	ref, err := s.client.StartRun(ctx, req)
	if err != nil {
		return nil, err
	}
	// A zero-length json.RawMessage is poison for any consumer that encodes it
	// (json: error calling MarshalJSON ... unexpected end of JSON input).
	// RunRecord's omitempty tag already skips an empty slice on the wire, so
	// this is belt-and-braces: it keeps the in-memory invariant "Manifest is
	// nil or valid JSON" true for the record itself, not just its wire form.
	var manifest json.RawMessage
	if len(in.ManifestJSON) > 0 {
		manifest = make(json.RawMessage, len(in.ManifestJSON))
		copy(manifest, in.ManifestJSON)
	}
	rec := &RunRecord{
		RunID:         ref.RunID,
		SessionID:     in.SessionID,
		Message:       in.Message,
		Status:        "started",
		CreatedAt:     time.Now().UTC(),
		SourceNodeID:  in.SourceNodeID,
		TokenBudget:   in.TokenBudget,
		ContextTokens: in.ContextTokens,
		Manifest:      manifest,
	}
	// Register the observer with the service lifecycle while holding mu — the
	// same lock Close uses to set closed. A goroutine registered after Close's
	// wait had already returned would escape the lifecycle entirely (and its
	// persist would be exactly the teardown race Close exists to stop).
	s.mu.Lock()
	s.runs[ref.RunID] = rec
	s.pruneLocked()
	spawn := !s.closed
	if spawn {
		s.wg.Add(1)
	}
	s.mu.Unlock()
	s.persist()

	if spawn {
		go s.observe(ref.RunID)
	}
	return rec, nil
}

// ListRuns returns a snapshot of the registry, newest first. Non-terminal
// runs have their status refreshed live from the gateway when reachable.
func (s *Service) ListRuns(ctx context.Context) []RunRecord {
	s.mu.RLock()
	recs := make([]*RunRecord, 0, len(s.runs))
	for _, r := range s.runs {
		recs = append(recs, r)
	}
	s.mu.RUnlock()

	sort.Slice(recs, func(i, j int) bool {
		return recs[i].CreatedAt.After(recs[j].CreatedAt)
	})

	out := make([]RunRecord, 0, len(recs))
	for _, r := range recs {
		rec := s.snapshot(r)
		if !rec.IsTerminal() && ctx != nil {
			s.refreshStatus(ctx, &rec)
		}
		out = append(out, rec)
	}
	return out
}

// Run returns a snapshot of one run.
func (s *Service) Run(runID string) (RunRecord, bool) {
	s.mu.RLock()
	r, ok := s.runs[runID]
	s.mu.RUnlock()
	if !ok {
		return RunRecord{}, false
	}
	return s.snapshot(r), true
}

// refreshStatus polls the gateway for the run's current status and merges
// it into the snapshot (used when the SSE stream is not the live source,
// e.g. after a reconnect or for runs observed by another client).
func (s *Service) refreshStatus(ctx context.Context, rec *RunRecord) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	status, err := s.client.GetRun(ctx, rec.RunID)
	if err != nil {
		return
	}
	if status.Status == "not_found" {
		rec.Status = "not_found"
		return
	}
	// Never regress a terminal registry status via a stale poll.
	if rec.IsTerminal() && rec.Status != "not_found" {
		return
	}
	rec.Status = status.Status
	if v, ok := status.Extra["last_event"]; ok {
		if s, ok := v.(string); ok {
			rec.LastEvent = s
		}
	}
}

// ErrRunNotFound is returned by operations targeting a run the gateway no
// longer knows (404 run_not_found).
var ErrRunNotFound = errors.New("gateway: run not found")

// StopRun interrupts the run on the gateway and marks the record. Stopping
// an already-terminal run is idempotent: the gateway is not called and nil
// is returned with the record's status unchanged. A run tracked locally but
// no longer known to the gateway (swept race) is marked 'not_found' and
// also returns nil; only a run absent from the registry AND unknown to the
// gateway returns ErrRunNotFound.
func (s *Service) StopRun(ctx context.Context, runID string) error {
	s.mu.RLock()
	rec, ok := s.runs[runID]
	terminal := ok && rec.IsTerminal()
	s.mu.RUnlock()
	if terminal {
		return nil
	}
	_, err := s.client.StopRun(ctx, runID)
	if err != nil {
		if IsNotFound(err) {
			if ok {
				// Swept race: the gateway no longer knows a run we still
				// track — mark it terminal so it surfaces honestly.
				s.mu.Lock()
				if rec, ok := s.runs[runID]; ok {
					rec.Status = "not_found"
					rec.LastEvent = "run.not_found"
				}
				s.mu.Unlock()
				s.persist()
				return nil
			}
			return fmt.Errorf("%w: %s", ErrRunNotFound, runID)
		}
		return err
	}
	s.mu.Lock()
	if rec, ok := s.runs[runID]; ok && !rec.IsTerminal() {
		rec.Status = "stopping"
		rec.LastEvent = "run.stopping"
	}
	s.mu.Unlock()
	s.persist()
	return nil
}

// RespondApproval forwards an approval choice to the gateway.
func (s *Service) RespondApproval(ctx context.Context, runID, approvalID, choice string) error {
	return s.client.RespondApproval(ctx, runID, approvalID, choice)
}

// Events returns the event history for a run (oldest first).
func (s *Service) Events(runID string) ([]RunEvent, bool) {
	s.mu.RLock()
	r, ok := s.runs[runID]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	evs := make([]RunEvent, len(r.Events))
	s.mu.RLock()
	copy(evs, r.Events)
	s.mu.RUnlock()
	return evs, true
}

// Subscribe registers a fan-out channel for a run's live events. The
// returned cancel must be called when the subscriber disconnects. Events
// are delivered as StreamEvent values (parsed RunEvent + raw JSON). A slow
// subscriber that cannot keep up is dropped (buffered channel overflow) —
// matching the Canopy SSE hub's slow-client policy.
func (s *Service) Subscribe(runID string) (<-chan StreamEvent, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[runID]; !ok {
		return nil, nil, fmt.Errorf("gateway: run %s not found", runID)
	}
	ch := make(chan StreamEvent, maxSubscriberBuffer)
	set := s.subs[runID]
	if set == nil {
		set = make(map[chan StreamEvent]struct{})
		s.subs[runID] = set
	}
	set[ch] = struct{}{}
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if set, ok := s.subs[runID]; ok {
				delete(set, ch)
				if len(set) == 0 {
					delete(s.subs, runID)
				}
			}
			close(ch)
		})
	}
	return ch, cancel, nil
}

// observe consumes the run's SSE stream in the background, updating the
// record and fanning events out to subscribers. It runs on the SERVICE
// context (CI-006): Close cancels it, which aborts the in-flight body read
// instead of leaving this goroutine parked on a stream nobody owns.
func (s *Service) observe(runID string) {
	defer s.wg.Done()
	ctx := s.ctx
	if ctx == nil {
		// Only reachable for a Service not built by NewService (all real
		// construction goes through the constructors, which always set it).
		ctx = context.Background()
	}
	body, err := s.client.ObserveRun(ctx, runID)
	if err != nil {
		s.noteEvent(runID, RunEvent{Event: "run.observe_error", RunID: runID, Error: err.Error(), Timestamp: float64(time.Now().Unix())})
		return
	}
	defer func() { _ = body.Close() }()

	stream := NewSSEStream(body)
	streamEnded := false
	for {
		ev, err := stream.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Warn().Err(err).Str("run_id", runID).Msg("gateway observe: stream error")
				s.noteEvent(runID, RunEvent{Event: "run.observe_error", RunID: runID, Error: err.Error(), Timestamp: float64(time.Now().Unix())})
			}
			break
		}
		if ev == nil {
			streamEnded = true
			break
		}
		s.noteEvent(runID, ev.Event)
	}
	if !streamEnded {
		// Stream cut without the gateway's close sentinel: mark the run
		// disconnected so the dashboard does not show a phantom live run.
		s.mu.Lock()
		if rec, ok := s.runs[runID]; ok && !rec.IsTerminal() && rec.Status != "stopping" {
			rec.Status = "disconnected"
			rec.LastEvent = "run.stream_closed"
		}
		s.mu.Unlock()
	}
}

// noteEvent applies one streamed event to the record and broadcasts it.
func (s *Service) noteEvent(runID string, ev RunEvent) {
	s.mu.Lock()
	rec, ok := s.runs[runID]
	if !ok {
		s.mu.Unlock()
		return
	}
	rec.Events = append(rec.Events, ev)
	if len(rec.Events) > maxEventsPerRun {
		rec.Events = rec.Events[len(rec.Events)-maxEventsPerRun:]
	}
	rec.LastEvent = ev.Event
	changed := false
	switch ev.Event {
	case "run.completed":
		rec.Status = "completed"
		rec.Output = ev.Output
		rec.Usage = ev.Usage
		changed = true
	case "run.failed":
		rec.Status = "failed"
		rec.Error = ev.Error
		changed = true
	case "run.cancelled":
		rec.Status = "cancelled"
		changed = true
	case "approval.request":
		rec.Status = "waiting_for_approval"
		changed = true
	case "approval.responded", "message.delta", "tool.started", "tool.completed", "reasoning.available":
		if rec.Status == "started" || rec.Status == "waiting_for_approval" {
			rec.Status = "running"
			changed = true
		}
	case "run.observe_error":
		rec.Status = "disconnected"
		changed = true
	}

	// Fan out to subscribers (non-blocking; slow subscribers are dropped).
	raw, _ := json.Marshal(ev)
	se := StreamEvent{Event: ev, Raw: raw}
	subs := s.subs[runID]
	if len(subs) > 0 {
		for ch := range subs {
			select {
			case ch <- se:
			default:
				// Slow subscriber — drop it to keep the stream moving.
				delete(subs, ch)
				close(ch)
			}
		}
		if len(subs) == 0 {
			delete(s.subs, runID)
		}
	}
	s.mu.Unlock()
	if changed {
		s.persist()
	}
}

func (s *Service) snapshot(r *RunRecord) RunRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec := *r
	rec.Events = make([]RunEvent, len(r.Events))
	copy(rec.Events, r.Events)
	return rec
}

// pruneLocked drops the oldest terminal runs beyond maxRuns. Caller holds mu.
func (s *Service) pruneLocked() {
	if len(s.runs) <= maxRuns {
		return
	}
	type kv struct {
		created time.Time
		id      string
	}
	all := make([]kv, 0, len(s.runs))
	for id, r := range s.runs {
		all = append(all, kv{created: r.CreatedAt, id: id})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].created.Before(all[j].created) })
	removed := 0
	for _, e := range all {
		if len(s.runs) <= maxRuns {
			break
		}
		if rec, ok := s.runs[e.id]; ok && rec.IsTerminal() {
			delete(s.runs, e.id)
			removed++
		}
	}
	if removed == 0 {
		// All live runs: still bound the map by pruning the oldest anyway.
		for _, e := range all {
			if len(s.runs) <= maxRuns {
				break
			}
			delete(s.runs, e.id)
		}
	}
}

// loadState loads persisted run records from the state file. A missing file
// is a clean first boot; corrupt lines are skipped with a warning. The
// registry is never failed by a bad state file.
func (s *Service) loadState() {
	if s.statePath == "" {
		return
	}
	f, err := os.Open(s.statePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Warn().Err(err).Str("path", s.statePath).Msg("gateway state: cannot open, starting empty")
		}
		return
	}
	defer func() { _ = f.Close() }()

	s.mu.Lock()
	defer s.mu.Unlock()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec RunRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			log.Warn().Err(err).Str("path", s.statePath).Msg("gateway state: skipping corrupt line")
			continue
		}
		if rec.RunID == "" {
			continue
		}
		s.runs[rec.RunID] = &rec
	}
	s.pruneLocked()
}

// Backfill refreshes non-terminal records restored from the state file
// against the live gateway via the existing per-run GET /v1/runs/{id}
// (there is NO list endpoint — the live gateway 405s GET /v1/runs). It is
// best-effort and bounded (~5s): an unreachable or misbehaving gateway logs
// a warning and keeps the persisted statuses, so canopyd still boots.
func (s *Service) Backfill(ctx context.Context) {
	if s.statePath == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	s.mu.RLock()
	var live []*RunRecord
	for _, r := range s.runs {
		if !r.IsTerminal() {
			live = append(live, r)
		}
	}
	s.mu.RUnlock()
	if len(live) == 0 {
		return
	}

	if err := s.Connected(ctx); err != nil {
		log.Warn().Err(err).Msg("gateway backfill: unreachable, keeping persisted statuses")
		return
	}
	changed := false
	for _, rec := range live {
		before := rec.Status
		// Startup-only window: no concurrent access to these records.
		s.refreshStatus(ctx, rec)
		if rec.Status != before {
			changed = true
		}
	}
	if changed {
		s.persist()
	}
}

// persist writes the registry to the state file atomically (tmp + rename).
// A failed write only logs; it never fails the caller. Once Close has run
// this is a no-op (see persistLocked): a store that is being torn down must
// not drop a temp file into a directory its owner is removing.
func (s *Service) persist() {
	if s.statePath == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistLocked()
}

// persistLocked writes the registry while the caller holds mu. It is a no-op
// on a closed service (CI-006): the flag is read under the same lock that
// Close takes before cancelling, so a persist racing Close either completes
// before Close returns or is skipped — never lands after it.
func (s *Service) persistLocked() {
	if s.closed {
		return
	}
	if s.statePath == "" {
		return
	}
	dir := filepath.Dir(s.statePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Warn().Err(err).Str("path", s.statePath).Msg("gateway state: mkdir failed")
		return
	}
	tmp, err := os.CreateTemp(dir, ".runs-*.jsonl.tmp")
	if err != nil {
		log.Warn().Err(err).Str("path", s.statePath).Msg("gateway state: create temp failed")
		return
	}
	tmpName := tmp.Name()
	w := bufio.NewWriter(tmp)
	for _, r := range s.runs {
		line, err := json.Marshal(r)
		if err != nil {
			continue
		}
		_, _ = w.Write(line)
		_ = w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		log.Warn().Err(err).Str("path", s.statePath).Msg("gateway state: write failed")
		return
	}
	if err := tmp.Chmod(0o600); err != nil {
		log.Warn().Err(err).Str("path", s.statePath).Msg("gateway state: chmod failed")
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		log.Warn().Err(err).Str("path", s.statePath).Msg("gateway state: close failed")
		return
	}
	if err := os.Rename(tmpName, s.statePath); err != nil {
		_ = os.Remove(tmpName)
		log.Warn().Err(err).Str("path", s.statePath).Msg("gateway state: rename failed")
	}
}
