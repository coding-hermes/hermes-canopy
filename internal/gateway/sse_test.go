package gateway

import (
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
