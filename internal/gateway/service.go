package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
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
// live subscribers (the Canopy frontend SSE endpoints).
type Service struct {
	client *Client

	mu   sync.RWMutex
	runs map[string]*RunRecord
	subs map[string]map[chan StreamEvent]struct{}
}

// NewService builds a gateway Service around a client.
func NewService(client *Client) *Service {
	return &Service{
		client: client,
		runs:   make(map[string]*RunRecord),
		subs:   make(map[string]map[chan StreamEvent]struct{}),
	}
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

// StopRun interrupts the run on the gateway and marks the record.
func (s *Service) StopRun(ctx context.Context, runID string) error {
	_, err := s.client.StopRun(ctx, runID)
	if err != nil {
		if IsNotFound(err) {
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
	defer body.Close()

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
	switch ev.Event {
	case "run.completed":
		rec.Status = "completed"
		rec.Output = ev.Output
		rec.Usage = ev.Usage
	case "run.failed":
		rec.Status = "failed"
		rec.Error = ev.Error
	case "run.cancelled":
		rec.Status = "cancelled"
	case "approval.request":
		rec.Status = "waiting_for_approval"
	case "approval.responded", "message.delta", "tool.started", "tool.completed", "reasoning.available":
		if rec.Status == "started" || rec.Status == "waiting_for_approval" {
			rec.Status = "running"
		}
	case "run.observe_error":
		rec.Status = "disconnected"
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
