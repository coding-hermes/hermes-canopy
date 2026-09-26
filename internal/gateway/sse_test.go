package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func sseFeed(t *testing.T, body string) []RunEvent {
	t.Helper()
	s := NewSSEStream(strings.NewReader(body))
	var evs []RunEvent
	for {
		ev, err := s.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if ev == nil {
			return evs
		}
		evs = append(evs, ev.Event)
	}
}

func TestSSEParserLifecycle(t *testing.T) {
	body := strings.Join([]string{
		": keepalive\n\n",
		`data: {"event":"message.delta","run_id":"run_1","timestamp":1.0,"delta":"Hel"}` + "\n\n",
		`data: {"event":"message.delta","run_id":"run_1","timestamp":1.1,"delta":"lo"}` + "\n\n",
		`data: {"event":"tool.started","run_id":"run_1","timestamp":2.0,"tool":"terminal","preview":"ls"}` + "\n\n",
		`data: {"event":"approval.request","run_id":"run_1","timestamp":3.0,"command":"rm -rf x","choices":["once","session","always","deny"]}` + "\n\n",
		`data: {"event":"run.completed","run_id":"run_1","timestamp":4.0,"output":"done","usage":{"total_tokens":42}}` + "\n\n",
		": stream closed\n\n",
	}, "")
	evs := sseFeed(t, body)
	if len(evs) != 5 {
		t.Fatalf("want 5 events, got %d: %+v", len(evs), evs)
	}
	if evs[0].Event != "message.delta" || evs[0].Delta != "Hel" {
		t.Fatalf("bad delta event: %+v", evs[0])
	}
	if evs[3].Event != "approval.request" || evs[3].Command != "rm -rf x" {
		t.Fatalf("bad approval event: %+v", evs[3])
	}
	if len(evs[3].Choices) != 4 || evs[3].Choices[0] != "once" {
		t.Fatalf("bad choices: %+v", evs[3].Choices)
	}
	if evs[4].Event != "run.completed" || evs[4].Output != "done" {
		t.Fatalf("bad completed event: %+v", evs[4])
	}
	if evs[4].Usage["total_tokens"] != float64(42) {
		t.Fatalf("bad usage: %+v", evs[4].Usage)
	}
}

func TestSSEParserIgnoresCommentsAndBlanks(t *testing.T) {
	evs := sseFeed(t, ": keepalive\n\ndata: {\"event\":\"run.failed\",\"run_id\":\"r\",\"error\":\"boom\"}\n\n: stream closed\n\n")
	if len(evs) != 1 || evs[0].Event != "run.failed" || evs[0].Error != "boom" {
		t.Fatalf("unexpected: %+v", evs)
	}
}

func TestSSEParserEmptyFeed(t *testing.T) {
	if evs := sseFeed(t, ""); len(evs) != 0 {
		t.Fatalf("want 0 events, got %d", len(evs))
	}
}

func TestSSEParserMalformedJSON(t *testing.T) {
	s := NewSSEStream(strings.NewReader("data: {not json}\n\n"))
	if _, err := s.Next(); err == nil {
		t.Fatal("want parse error")
	}
}

func TestSSEParserMultilineData(t *testing.T) {
	body := "data: {\"event\":\"run.completed\",\ndata: \"run_id\":\"r\",\"output\":\"ok\"}\n\n"
	evs := sseFeed(t, body)
	if len(evs) != 1 {
		t.Fatalf("want 1 event from multi-line data, got %d", len(evs))
	}
}

func TestRunEventDecodesCapturedBooleanToolError(t *testing.T) {
	const payload = `{"event": "tool.completed", "run_id": "run_c910ab4bc69547bc8fddbfc3c13df034", "timestamp": 1790386618.3744113, "tool": "terminal", "duration": 0.217, "error": false}`

	var ev RunEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("decode captured payload: %v", err)
	}
	if ev.Event != "tool.completed" || ev.Tool != "terminal" {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if ev.Timestamp != 1790386618.3744113 || ev.Duration != 0.217 {
		t.Fatalf("numeric fields changed: %+v", ev)
	}
	if ev.Error != "" {
		t.Fatalf("boolean error should map to empty string, got %q", ev.Error)
	}
	encoded, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("re-encode event: %v", err)
	}
	if strings.Contains(string(encoded), `"error"`) {
		t.Fatalf("empty decoded error should be omitted on re-encode: %s", encoded)
	}
}

func TestRunEventDecodesStringBooleanAndAbsentErrors(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "string", payload: `{"event":"run.failed","error":"boom"}`, want: "boom"},
		// Boolean gateway errors are status flags, not error messages.
		{name: "true", payload: `{"event":"tool.completed","error":true}`, want: ""},
		{name: "false", payload: `{"event":"tool.completed","error":false}`, want: ""},
		{name: "absent", payload: `{"event":"tool.completed"}`, want: ""},
		{name: "null", payload: `{"event":"tool.completed","error":null}`, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ev RunEvent
			if err := json.Unmarshal([]byte(tt.payload), &ev); err != nil {
				t.Fatalf("decode %s error: %v", tt.name, err)
			}
			if ev.Error != tt.want {
				t.Fatalf("Error = %q, want %q", ev.Error, tt.want)
			}
		})
	}
}

func TestParseSSEEventDecodesBooleanToolError(t *testing.T) {
	const payload = `{"event": "tool.completed", "run_id": "run_c910ab4bc69547bc8fddbfc3c13df034", "timestamp": 1790386618.3744113, "tool": "terminal", "duration": 0.217, "error": false}`

	got, err := parseSSEEvent(payload)
	if err != nil {
		t.Fatalf("parse SSE event: %v", err)
	}
	if got == nil || got.Event.Event != "tool.completed" || got.Event.Tool != "terminal" || got.Event.Error != "" {
		t.Fatalf("unexpected parsed event: %+v", got)
	}
	if string(got.Raw) != payload {
		t.Fatalf("raw payload changed: %s", got.Raw)
	}
}

func TestSSEStreamDecodesBooleanToolError(t *testing.T) {
	const payload = `{"event": "tool.completed", "run_id": "run_c910ab4bc69547bc8fddbfc3c13df034", "timestamp": 1790386618.3744113, "tool": "terminal", "duration": 0.217, "error": false}`

	evs := sseFeed(t, "data: "+payload+"\n\n: stream closed\n\n")
	if len(evs) != 1 || evs[0].Event != "tool.completed" || evs[0].Tool != "terminal" || evs[0].Error != "" {
		t.Fatalf("unexpected stream event: %+v", evs)
	}
}

func TestParseSSEEventPreservesRawForUntypedEvent(t *testing.T) {
	got, err := parseSSEEvent(`{"type":"future.event","value":42}`)
	if err != nil {
		t.Fatalf("parse SSE event: %v", err)
	}
	if got == nil || got.Event.Raw["type"] != "future.event" || got.Event.Raw["value"] != float64(42) {
		t.Fatalf("raw event payload was not captured: %+v", got)
	}
}
