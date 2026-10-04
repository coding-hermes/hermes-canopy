package iteration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func progressFixture(id string, phase string, current, total int, status ProgressStatus, updated string) CardProgress {
	return CardProgress{
		CardID: uuid.MustParse(id), Type: ProgressTypeThinking, Title: id,
		Current: current, Total: total, Status: status, Phase: phase,
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(len(updated)) * time.Minute),
	}
}

func TestAggregateHeaderAlgorithm(t *testing.T) {
	first := progressFixture("0191a9c3-0000-7000-8000-000000000001", "search", 2, 5, ProgressStatusRunning, "a")
	second := progressFixture("0191a9c3-0000-7000-8000-000000000002", "search", 0, 0, ProgressStatusRunning, "b")
	ungrouped := progressFixture("0191a9c3-0000-7000-8000-000000000003", "", 1, 1, ProgressStatusCompleted, "c")

	header := AggregateHeader([]CardProgress{first, second, ungrouped})
	if len(header.Groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(header.Groups))
	}
	if header.Groups[0].Name != "search" || header.Groups[0].Current != 2 || header.Groups[0].Total != 5 || header.Groups[0].Indeterminate != 1 {
		t.Fatalf("search group = %+v", header.Groups[0])
	}
	if header.Groups[1].Name != "Ungrouped" || header.Groups[1].Current != 1 || header.Groups[1].Total != 1 {
		t.Fatalf("ungrouped group = %+v", header.Groups[1])
	}
	if !header.Segments[1].Indeterminate || header.Segments[1].Total != 0 {
		t.Fatalf("indeterminate segment = %+v", header.Segments[1])
	}
}

func TestAggregateHeaderSortsByEarliestUpdateOrDeclaredOrdinal(t *testing.T) {
	cases := []struct {
		name    string
		records []CardProgress
		want    []string
	}{
		{
			name: "earliest updated at",
			records: []CardProgress{
				progressFixture("0191a9c3-0000-7000-8000-000000000011", "late", 0, 0, ProgressStatusRunning, "zzzz"),
				progressFixture("0191a9c3-0000-7000-8000-000000000012", "early", 0, 0, ProgressStatusRunning, "a"),
			},
			want: []string{"early", "late"},
		},
		{
			name: "declared ordinal",
			records: []CardProgress{
				{CardID: uuid.MustParse("0191a9c3-0000-7000-8000-000000000021"), Type: ProgressTypeCodeExec, Title: "two", Phase: "two", PhaseOrdinal: intPointer(2), UpdatedAt: time.Unix(1, 0).UTC()},
				{CardID: uuid.MustParse("0191a9c3-0000-7000-8000-000000000022"), Type: ProgressTypeSearch, Title: "one", Phase: "one", PhaseOrdinal: intPointer(1), UpdatedAt: time.Unix(2, 0).UTC()},
			},
			want: []string{"one", "two"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := AggregateHeader(tc.records)
			for index, want := range tc.want {
				if got := header.Groups[index].Name; got != want {
					t.Fatalf("group[%d] = %q, want %q", index, got, want)
				}
			}
		})
	}
}

func intPointer(value int) *int { return &value }

func TestAggregateHeaderCapsSegmentsByPriorityAndKeepsOverflow(t *testing.T) {
	records := []progressRecord{
		{progress: progressFixture("0191a9c3-0000-7000-8000-000000000031", "approval", 0, 1, ProgressStatusRunning, "a"), segmentStatus: ProgressSegmentPendingApproval},
		{progress: progressFixture("0191a9c3-0000-7000-8000-000000000032", "running", 1, 3, ProgressStatusRunning, "b"), segmentStatus: ProgressSegmentRunning},
		{progress: progressFixture("0191a9c3-0000-7000-8000-000000000033", "failed", 0, 1, ProgressStatusFailed, "c"), segmentStatus: ProgressSegmentFailed},
		{progress: progressFixture("0191a9c3-0000-7000-8000-000000000034", "cancelled", 0, 1, ProgressStatusCancelled, "d"), segmentStatus: ProgressSegmentCancelled},
	}
	header := aggregateProgressHeader(records)
	if len(header.Segments) != 3 || len(header.AllSegments) != 4 {
		t.Fatalf("segments = %d/all = %d", len(header.Segments), len(header.AllSegments))
	}
	want := []ProgressSegmentStatus{ProgressSegmentPendingApproval, ProgressSegmentRunning, ProgressSegmentFailed}
	for index, status := range want {
		if header.Segments[index].Status != status {
			t.Fatalf("segment[%d] status = %q, want %q", index, header.Segments[index].Status, status)
		}
	}
	if header.AllSegments[3].Status != ProgressSegmentCancelled {
		t.Fatalf("overflow segment = %+v", header.AllSegments[3])
	}
}

func TestSubtypeProgressCardinality(t *testing.T) {
	base := json.RawMessage(`{"progress":{"current":0,"total":0,"status":"running"},"state":"running"}`)
	cases := []struct {
		name       string
		subtype    IterationSubtype
		event      string
		data       string
		wantCur    int
		wantTotal  int
		wantStatus ProgressStatus
	}{
		{name: "code running", subtype: IterationSubtypeCodeExec, event: EventExecStart, data: `{"command":"go test"}`, wantCur: 0, wantTotal: 0, wantStatus: ProgressStatusRunning},
		{name: "code completed", subtype: IterationSubtypeCodeExec, event: EventExecComplete, data: `{"exit_code":0,"cancelled":false}`, wantCur: 1, wantTotal: 1, wantStatus: ProgressStatusCompleted},
		{name: "tool waiting", subtype: IterationSubtypeToolCall, event: EventToolCallStarted, data: `{"gated":true}`, wantCur: 0, wantTotal: 1, wantStatus: ProgressStatusRunning},
		{name: "tool terminal", subtype: IterationSubtypeToolCall, event: EventToolCallResult, data: `{}`, wantCur: 1, wantTotal: 1, wantStatus: ProgressStatusCompleted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			updated, err := reduceEvent(base, tc.subtype, tc.event, json.RawMessage(tc.data))
			if err != nil {
				t.Fatal(err)
			}
			var value struct {
				Progress CardProgress   `json:"progress"`
				State    IterationState `json:"state"`
			}
			if err := json.Unmarshal(updated, &value); err != nil {
				t.Fatal(err)
			}
			if value.Progress.Current != tc.wantCur || value.Progress.Total != tc.wantTotal || value.Progress.Status != tc.wantStatus {
				t.Fatalf("progress = %+v", value.Progress)
			}
			if tc.name == "tool waiting" && value.State != IterationStateWaitingForUser {
				t.Fatalf("tool state = %q, want waiting_for_user", value.State)
			}
		})
	}
}

func TestCardProgressUsesCanonicalCamelCase(t *testing.T) {
	value := CardProgress{CardID: uuid.MustParse("0191a9c3-0000-7000-8000-000000000041"), Type: ProgressTypeSearch, Title: "Find", Status: ProgressStatusRunning, UpdatedAt: time.Unix(1, 0).UTC()}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, field := range []string{"cardId", "updatedAt"} {
		if !containsJSONField(text, field) {
			t.Fatalf("%s missing from %s", field, text)
		}
	}
}

func containsJSONField(text, field string) bool {
	return len(text) > 0 && string([]byte(text)) != "" &&
		(len(field) > 0 && contains(text, `"`+field+`"`))
}

func contains(text, fragment string) bool {
	for index := 0; index+len(fragment) <= len(text); index++ {
		if text[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
