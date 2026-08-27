package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// RunEvent is one parsed SSE event from GET /v1/runs/{id}/events.
//
// The gateway emits a single `data: {json}` payload per event. Event types
// (authoritative: hermes-agent gateway/platforms/api_server.py
// _handle_runs / _make_run_event_callback):
//
//	message.delta       — streaming assistant text (delta)
//	run.completed       — terminal success (output, usage)
//	run.failed          — terminal failure (error)
//	run.cancelled       — terminal stop/cancel
//	approval.request    — pending host-side approval (command, choices)
//	approval.responded  — an approval was resolved (choice, resolved)
//	tool.started        — tool execution began (tool, preview)
//	tool.completed      — tool execution ended (tool, duration, error)
//	reasoning.available — model reasoning text became available (text)
type RunEvent struct {
	Event      string         `json:"event"`
	RunID      string         `json:"run_id"`
	Timestamp  float64        `json:"timestamp,omitempty"`
	Delta      string         `json:"delta,omitempty"`
	Output     string         `json:"output,omitempty"`
	Error      string         `json:"error,omitempty"`
	Text       string         `json:"text,omitempty"`
	Tool       string         `json:"tool,omitempty"`
	Preview    string         `json:"preview,omitempty"`
	Duration   float64        `json:"duration,omitempty"`
	Command    string         `json:"command,omitempty"`
	Choice     string         `json:"choice,omitempty"`
	Resolved   int            `json:"resolved,omitempty"`
	ApprovalID string         `json:"approval_id,omitempty"`
	Choices    []string       `json:"choices,omitempty"`
	Usage      map[string]any `json:"usage,omitempty"`
	// Raw holds the full original payload for event types without a typed
	// field mapping (e.g. future gateway events).
	Raw map[string]any `json:"-"`
}

// StreamEvent is a parsed event plus the raw SSE data line, used by the
// service layer for fan-out and replay.
type StreamEvent struct {
	Event RunEvent
	Raw   json.RawMessage
}

// parseSSEEvent decodes one `data: {json}` SSE data line into a StreamEvent.
// Blank lines, keepalive comments (`: keepalive`) and the stream-closed
// comment (`: stream closed`) yield (nil, nil) — callers treat that as
// "no event, keep reading" except after EOF.
func parseSSEEvent(data string) (*StreamEvent, error) {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return nil, nil
	}
	raw := json.RawMessage(trimmed)
	var ev RunEvent
	if err := json.Unmarshal([]byte(trimmed), &ev); err != nil {
		return nil, fmt.Errorf("gateway: decode SSE event: %w", err)
	}
	if ev.Event == "" {
		// Payloads without an `event` field are not lifecycle events.
		var generic map[string]any
		if err := json.Unmarshal([]byte(trimmed), &generic); err == nil {
			ev.Raw = generic
		}
	}
	return &StreamEvent{Event: ev, Raw: raw}, nil
}

// SSEStream parses a gateway SSE response body line by line, yielding one
// StreamEvent per `data:` payload. It stops at EOF (the gateway closes the
// stream after the run finishes).
type SSEStream struct {
	scanner *bufio.Scanner
	// pendingData accumulates multi-line data: fields (unused by the
	// gateway but part of the SSE spec).
	pending []string
}

// NewSSEStream wraps an SSE body reader.
func NewSSEStream(r io.Reader) *SSEStream {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &SSEStream{scanner: sc}
}

// Next returns the next parsed event, or (nil, nil) at EOF.
func (s *SSEStream) Next() (*StreamEvent, error) {
	for s.scanner.Scan() {
		line := s.scanner.Text()
		switch {
		case line == "":
			if len(s.pending) > 0 {
				data := strings.Join(s.pending, "\n")
				s.pending = nil
				ev, err := parseSSEEvent(data)
				if err != nil {
					return nil, err
				}
				if ev != nil {
					return ev, nil
				}
			}
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimPrefix(line, "data:")
			payload = strings.TrimPrefix(payload, " ")
			s.pending = append(s.pending, payload)
		default:
			// comment (`: keepalive`, `: stream closed`) or other SSE
			// fields (event:, id:, retry:) — ignored.
			continue
		}
	}
	if err := s.scanner.Err(); err != nil {
		return nil, fmt.Errorf("gateway: read SSE stream: %w", err)
	}
	if len(s.pending) > 0 {
		data := strings.Join(s.pending, "\n")
		ev, err := parseSSEEvent(data)
		if err != nil {
			return nil, err
		}
		s.pending = nil
		if ev != nil {
			return ev, nil
		}
	}
	return nil, nil
}
