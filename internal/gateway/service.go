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

	mu   sync.RWMutex
	runs map[string]*RunRecord
	subs map[string]map[chan StreamEvent]struct{}
}

// DefaultStateFile returns the default path for the persisted gateway run
// registry, following the card.DataDir() convention (~/.hermes/canopy/<sub>).
func DefaultStateFile() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/tmp"
	}
	return filepath.Join(home, ".hermes", "canopy", "gateway", "runs.jsonl")
}

// NewService builds a gateway Service around a client.
func NewService(client *Client) *Service {
	return &Service{
		client: client,
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

// Client exposes the underlying gateway client (used by the handler for
// operations the service does not wrap).
func (s *Service) Client() *Client { return s.client }

// Connected probes the gateway health endpoint.
func (s *Service) Connected(ctx context.Context) error {
	return s.client.Health(ctx)
}

// StartRun creates a real Hermes gateway run and begins observing it. The
// returned record reflects the immediate 202 response; events arrive
// asynchronously.
func (s *Service) StartRun(ctx context.Context, message, sessionID string) (*RunRecord, error) {
	req := StartRunRequest{Input: message}
	if sessionID != "" {
		req.SessionID = sessionID
	}
	ref, err := s.client.StartRun(ctx, req)
	if err != nil {
		return nil, err
	}
	rec := &RunRecord{
		RunID:     ref.RunID,
		SessionID: sessionID,
		Message:   message,
		Status:    "started",
		CreatedAt: time.Now().UTC(),
	}
	s.mu.Lock()
	s.runs[ref.RunID] = rec
	s.pruneLocked()
	s.mu.Unlock()
	s.persist()

	go s.observe(ref.RunID)
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
// record and fanning events out to subscribers.
func (s *Service) observe(runID string) {
	ctx := context.Background()
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
// A failed write only logs; it never fails the caller.
func (s *Service) persist() {
	if s.statePath == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistLocked()
}

// persistLocked writes the registry while the caller holds mu.
func (s *Service) persistLocked() {
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
