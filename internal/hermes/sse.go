package hermes

// sse.go implements the incremental SSE (Server-Sent Events) parser used by
// StreamMessages (SPEC-FTR-07 §8 Phase 1 item 4).

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

// parseSSE reads an SSE stream from r and invokes emit for each complete
// event. SSE parse rules (https://html.spec.whatwg.org/multipage/server-sent-events.html):
//
//   - fields are "event: X", "data: Y", "id: Z" lines
//   - multi-line data fields are joined with "\n"
//   - events are separated by blank lines
//   - comment lines (starting with ":") are ignored
//   - a field with no colon is ignored
//
// The raw event text (the full "event:...\n...\n\n" block) is passed to emit
// alongside the parsed event so downstream translation can inspect it.
func parseSSE(r io.Reader, emit func(ev HermesSSEEvent, raw string)) error {
	br := bufio.NewReader(r)
	var (
		eventName string
		dataLines []string
		eventID   string
		rawBuf    strings.Builder
	)
	flush := func() error {
		if len(dataLines) == 0 && eventName == "" && eventID == "" {
			rawBuf.Reset()
			return nil
		}
		ev := HermesSSEEvent{
			Event: eventName,
			Data:  strings.Join(dataLines, "\n"),
			ID:    eventID,
		}
		emit(ev, rawBuf.String())
		eventName = ""
		dataLines = nil
		eventID = ""
		rawBuf.Reset()
		return nil
	}

	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			rawBuf.WriteString(line)
			trimmed := strings.TrimRight(line, "\r\n")
			switch {
			case trimmed == "":
				if err := flush(); err != nil {
					return err
				}
			case strings.HasPrefix(trimmed, ":"):
				// comment line: ignore
			default:
				field, value, hasColon := strings.Cut(trimmed, ":")
				if !hasColon {
					// Line with no colon: ignore per spec.
					break
				}
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "event":
					eventName = value
				case "data":
					dataLines = append(dataLines, value)
				case "id":
					eventID = value
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Flush any trailing event not terminated by a blank line.
				return flush()
			}
			return err
		}
	}
}

// mapSSEEvent converts a parsed SSE event into an SSEDelta (SPEC-FTR-07 §8
// Phase 1 item 5):
//
//	"delta"     → Type "delta", Content = parsed data's content field if JSON, else raw data
//	"tool_call" → Type "tool_call", ToolID from data
//	"done"      → Type "done"
//	"error"     → Type "error"
//	unknown     → Type "delta" with raw data (graceful fallback)
func mapSSEEvent(ev HermesSSEEvent, raw string) SSEDelta {
	delta := SSEDelta{Type: ev.Event, RawEvent: raw}
	switch ev.Event {
	case "delta":
		delta.Content = extractContent(ev.Data)
	case "tool_call":
		delta.ToolID = extractToolID(ev.Data)
		delta.Content = ev.Data
	case "done", "error":
		delta.Content = ev.Data
	default:
		// Unknown event name: graceful fallback to a delta with raw data.
		delta.Type = "delta"
		delta.Content = ev.Data
	}
	return delta
}

// extractContent pulls the "content" field out of a JSON delta payload;
// non-JSON data is returned verbatim.
func extractContent(data string) string {
	var payload struct {
		Content string `json:"content"`
	}
	if json.Unmarshal([]byte(data), &payload) == nil && payload.Content != "" {
		return payload.Content
	}
	return data
}

// extractToolID pulls the tool call ID out of a tool_call payload. The
// payload may be shaped as {"tool_call_id": "..."} or {"id": "..."}.
func extractToolID(data string) string {
	var payload struct {
		ToolCallID string `json:"tool_call_id"`
		ID         string `json:"id"`
	}
	if json.Unmarshal([]byte(data), &payload) == nil {
		if payload.ToolCallID != "" {
			return payload.ToolCallID
		}
		return payload.ID
	}
	return ""
}

// parseRawEvent reconstructs a HermesSSEEvent from a raw SSE event block.
// It is used by SendMessage's streaming aggregation to populate RawEvents.
// A malformed block yields an event with only the raw data preserved.
func parseRawEvent(raw string) HermesSSEEvent {
	ev := HermesSSEEvent{Data: raw}
	if raw == "" {
		return ev
	}
	var (
		eventName string
		dataLines []string
		eventID   string
	)
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "" || strings.HasPrefix(trimmed, ":") {
			continue
		}
		field, value, hasColon := strings.Cut(trimmed, ":")
		if !hasColon {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			eventName = value
		case "data":
			dataLines = append(dataLines, value)
		case "id":
			eventID = value
		}
	}
	ev.Event = eventName
	ev.Data = strings.Join(dataLines, "\n")
	ev.ID = eventID
	return ev
}

// parseToolCallFromRaw extracts a ToolCall from a raw tool_call SSE block.
// The data payload may be shaped as {"id": "...", "type": "...", "function":
// {"name": "...", "arguments": "..."}} or {"tool_call_id": "...", "name":
// "...", "arguments": "..."}.
func parseToolCallFromRaw(raw string) (ToolCall, bool) {
	ev := parseRawEvent(raw)
	if ev.Event != "tool_call" || ev.Data == "" {
		return ToolCall{}, false
	}
	var tc ToolCall
	if err := json.Unmarshal([]byte(ev.Data), &tc); err == nil && tc.ID != "" {
		return tc, true
	}
	var flat struct {
		ToolCallID string `json:"tool_call_id"`
		ID         string `json:"id"`
		Name       string `json:"name"`
		Arguments  string `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(ev.Data), &flat); err == nil {
		id := flat.ToolCallID
		if id == "" {
			id = flat.ID
		}
		if id != "" {
			return ToolCall{ID: id, Type: "function", Function: ToolCallFunction{Name: flat.Name, Arguments: flat.Arguments}}, true
		}
	}
	return ToolCall{}, false
}

// closeAwareBody wraps an io.ReadCloser so streamLoop can distinguish "the
// caller closed the body" (clean stop) from "the server dropped the
// connection" (stream failure). It is safe for concurrent use: Close may be
// called from the caller's goroutine while the SSE read is blocked.
type closeAwareBody struct {
	rc     io.ReadCloser
	closed atomic.Bool
	once   sync.Once
}

func newCloseAwareBody(rc io.ReadCloser) *closeAwareBody {
	return &closeAwareBody{rc: rc}
}

func (b *closeAwareBody) Read(p []byte) (int, error) {
	return b.rc.Read(p)
}

func (b *closeAwareBody) Close() error {
	b.once.Do(func() { b.closed.Store(true) })
	return b.rc.Close()
}
