package handler

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	ctxpkg "github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/gateway"
	"github.com/google/uuid"
)

// TestGatewayStartRunEnvelopeParity exercises both variants through the mounted route.
func TestGatewayStartRunEnvelopeParity(t *testing.T) {
	nodeID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	manifest := &ctxpkg.Manifest{RequestID: "req-1", NodeID: nodeID, CompiledAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), TokenBudget: 8000, TokensUsed: 42}
	for _, tc := range []struct {
		name, request string
		context       bool
	}{
		{"raw", `{"message":"hello","session_id":"s1"}`, false},
		{"context", `{"message":"hello","session_id":"s1","node_id":"11111111-1111-1111-1111-111111111111","token_budget":8000}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newContextGatewayStub()
			defer stub.Close()
			r, svc := newContextGatewayRouter(t, stub, WithContextCompiler(&capturingCompiler{content: "compiled", manifest: manifest}, 8000))
			defer svc.Close()
			resp, body := gwDoJSON(t, r, http.MethodPost, "/runs", tc.request)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("start: %d %s", resp.StatusCode, body)
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &envelope); err != nil {
				t.Fatal(err)
			}
			if len(envelope) != 3 || envelope["run_id"] == nil || envelope["status"] == nil || envelope["run"] == nil {
				t.Fatalf("unexpected envelope: %s", body)
			}
			var id, status string
			if err := json.Unmarshal(envelope["run_id"], &id); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(envelope["status"], &status); err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(envelope["run"], &fields); err != nil {
				t.Fatal(err)
			}
			var run gateway.RunRecord
			if err := json.Unmarshal(envelope["run"], &run); err != nil {
				t.Fatal(err)
			}
			if id != "run_ctx" || status != "started" || run.RunID != id || run.Status != status || run.Message != "hello" || run.SessionID != "s1" || run.CreatedAt.IsZero() {
				t.Fatalf("inconsistent response: %s", body)
			}
			want := []string{"run_id", "session_id", "message", "model", "status", "created_at", "events"}
			if tc.context {
				want = append(want, "source_node_id", "token_budget", "context_tokens", "manifest")
			}
			if len(fields) != len(want) {
				t.Fatalf("unexpected run fields: %s", body)
			}
			for _, key := range want {
				if fields[key] == nil {
					t.Fatalf("missing %s: %s", key, body)
				}
			}
			if tc.context && (run.SourceNodeID != nodeID.String() || run.TokenBudget != 8000 || run.ContextTokens != 42 || len(run.Manifest) == 0) {
				t.Fatalf("unexpected provenance: %s", body)
			}
			t.Logf("202 response: %s", body)
		})
	}
}
