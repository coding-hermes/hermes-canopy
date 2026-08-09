package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newAgentTestServer builds a minimal router with ONLY the agent roster
// routes (no auth, no DB). Used for pure unit tests that must run without
// PostgreSQL. Returns the httptest.Server and a map of seeded agent names
// to their deterministic UUIDs.
func newAgentTestServer(t *testing.T) (*httptest.Server, map[string]uuid.UUID) {
	t.Helper()
	h := NewAgentHandler()
	r := chi.NewRouter()
	r.Mount("/api/v1/agents", h.Routes())
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	ids := make(map[string]uuid.UUID)
	for _, name := range []string{"helix-foreman", "codex-worker", "kimi-scout"} {
		ids[name] = uuid.NewSHA1(agentNamespace, []byte(name))
	}
	return srv, ids
}

// agentGet issues a GET against the test server.
func agentGet(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	return resp
}

// ---------------------------------------------------------------------------
// ListAgents
// ---------------------------------------------------------------------------

func TestAgent_ListAgents(t *testing.T) {
	srv, ids := newAgentTestServer(t)
	resp := agentGet(t, srv, "/api/v1/agents")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, string(body))
	}

	var list []agentListItem
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// AC-1: at least 3 seeded agents.
	if len(list) < 3 {
		t.Fatalf("expected at least 3 seeded agents, got %d", len(list))
	}

	// AC-1: every entry has the required roster fields + deterministic IDs.
	names := make(map[string]bool)
	for _, a := range list {
		if a.ID == uuid.Nil {
			t.Error("agent id is nil")
		}
		if a.Name == "" {
			t.Error("agent name is empty")
		}
		if a.Tier != tierProvisional && a.Tier != tierEstablished && a.Tier != tierVeteran {
			t.Errorf("agent %q has invalid tier %q", a.Name, a.Tier)
		}
		if a.LastActive == "" {
			t.Errorf("agent %q has empty last_active", a.Name)
		}
		if a.Capabilities == nil {
			t.Errorf("agent %q has nil capabilities", a.Name)
		}
		names[a.Name] = true

		// Verify deterministic ID matches uuid.NewSHA1(namespace, name).
		wantID := uuid.NewSHA1(agentNamespace, []byte(a.Name))
		if a.ID != wantID {
			t.Errorf("agent %q id = %s, want %s", a.Name, a.ID, wantID)
		}
	}

	// All three seeded names present.
	for _, name := range []string{"helix-foreman", "codex-worker", "kimi-scout"} {
		if !names[name] {
			t.Errorf("expected seeded agent %q in roster", name)
		}
	}

	// A known ID matches (spot-check the veteran).
	if hf, ok := ids["helix-foreman"]; ok {
		var found bool
		for _, a := range list {
			if a.ID == hf {
				found = true
				if a.Tier != tierVeteran {
					t.Errorf("helix-foreman tier = %q, want veteran", a.Tier)
				}
			}
		}
		if !found {
			t.Errorf("helix-foreman id %s not in roster", hf)
		}
	}

	t.Logf("roster: %d agents", len(list))
}

func TestAgent_ListAgents_CapabilityShape(t *testing.T) {
	srv, _ := newAgentTestServer(t)
	resp := agentGet(t, srv, "/api/v1/agents")
	defer resp.Body.Close()

	var list []agentListItem
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Find helix-foreman and verify its capability shape: success + total ints.
	var hf *agentListItem
	for i := range list {
		if list[i].Name == "helix-foreman" {
			hf = &list[i]
			break
		}
	}
	if hf == nil {
		t.Fatal("helix-foreman not found in roster")
	}
	cap, ok := hf.Capabilities["go-feature"]
	if !ok {
		t.Fatal("expected 'go-feature' capability on helix-foreman")
	}
	if cap.Success <= 0 || cap.Total <= 0 || cap.Success > cap.Total {
		t.Errorf("go-feature capability = {success:%d, total:%d}, want success>0, total>0, success<=total",
			cap.Success, cap.Total)
	}
}

// ---------------------------------------------------------------------------
// AgentDetail
// ---------------------------------------------------------------------------

func TestAgent_Detail_FullShape(t *testing.T) {
	srv, ids := newAgentTestServer(t)
	id := ids["helix-foreman"]
	resp := agentGet(t, srv, "/api/v1/agents/"+id.String())
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, string(body))
	}

	var detail agentDetailItem
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// AC-2: detail includes all roster fields.
	if detail.ID != id {
		t.Errorf("id = %s, want %s", detail.ID, id)
	}
	if detail.Name != "helix-foreman" {
		t.Errorf("name = %q, want helix-foreman", detail.Name)
	}
	if detail.Tier != tierVeteran {
		t.Errorf("tier = %q, want veteran", detail.Tier)
	}
	if detail.TrustScore <= 0 || detail.TrustScore > 1 {
		t.Errorf("trust_score = %v, want (0,1]", detail.TrustScore)
	}
	if detail.LastActive == "" {
		t.Error("last_active is empty")
	}
	if detail.Capabilities == nil || len(detail.Capabilities) == 0 {
		t.Error("expected non-empty capabilities map")
	}

	// AC-2: detail includes trust_history with ≥3 entries.
	if len(detail.TrustHistory) < 3 {
		t.Fatalf("trust_history len = %d, want >= 3", len(detail.TrustHistory))
	}
	// Each entry has score + RFC3339 at.
	for i, e := range detail.TrustHistory {
		if e.At == "" {
			t.Errorf("trust_history[%d].at is empty", i)
		}
		// Score should be a sane trust value (0..1 range).
		if e.Score < 0 || e.Score > 1 {
			t.Errorf("trust_history[%d].score = %v, want [0,1]", i, e.Score)
		}
	}
	// Trust history should be ascending (timeline).
	for i := 1; i < len(detail.TrustHistory); i++ {
		if detail.TrustHistory[i].Score < detail.TrustHistory[i-1].Score {
			t.Errorf("trust_history not ascending at idx %d: %v < %v",
				i, detail.TrustHistory[i].Score, detail.TrustHistory[i-1].Score)
		}
	}
	t.Logf("detail: %s (%s), trust_history=%d entries", detail.Name, detail.Tier, len(detail.TrustHistory))
}

func TestAgent_Detail_UnknownID(t *testing.T) {
	srv, _ := newAgentTestServer(t)
	unknown := uuid.New()
	resp := agentGet(t, srv, "/api/v1/agents/"+unknown.String())
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}

	var errBody apiErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody.Error.Code != "AGENT_NOT_FOUND" {
		t.Errorf("error code = %q, want AGENT_NOT_FOUND", errBody.Error.Code)
	}
}

func TestAgent_Detail_InvalidUUID(t *testing.T) {
	srv, _ := newAgentTestServer(t)
	resp := agentGet(t, srv, "/api/v1/agents/not-a-uuid")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}

	var errBody apiErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody.Error.Code != "INVALID_AGENT_ID" {
		t.Errorf("error code = %q, want INVALID_AGENT_ID", errBody.Error.Code)
	}
}
