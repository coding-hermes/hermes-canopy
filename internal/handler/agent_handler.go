// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file implements the agent roster surface (SPEC-023 §5 + §7).
//
// Endpoints (mounted at /api/v1/agents by server.go):
//
//	GET /            — list agents (roster summary)
//	GET /{agent_id}  — agent detail with trust history timeline
package handler

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// --- agentRegistry: in-memory agent roster (SPEC-023 §5 + §7) ---------------
//
// Agents are MVP-grade and live entirely in memory — no DB table. The
// registry is seeded with demo agents on construction. Agent IDs are
// deterministic UUIDs derived from a fixed namespace (distinct from the
// channel namespace) so they are stable across restarts and testable,
// matching the technique used by the workspace channel registry.

// agentNamespace is a fixed UUID used to derive stable agent IDs via
// uuid.NewSHA1(agentNamespace, []byte(name)). Distinct from
// channelNamespace to keep the ID spaces independent.
var agentNamespace = uuid.MustParse("b2c3d4e5-f6a7-8901-bcde-f12345678901")

// agentTier is the trust tier of an agent (SPEC-023 §7 AgentState.tier).
type agentTier string

const (
	tierProvisional agentTier = "provisional"
	tierEstablished agentTier = "established"
	tierVeteran     agentTier = "veteran"
)

// capabilityStat is a per-capability success/total tally (SPEC-023 §7).
type capabilityStat struct {
	Success int `json:"success"`
	Total   int `json:"total"`
}

// trustHistoryEntry is a single point on the trust timeline (SPEC-023 §5).
type trustHistoryEntry struct {
	Score float64 `json:"score"`
	At    string  `json:"at"`
}

// agentRecord is the full in-memory representation of a seeded agent. The
// JSON contract is split into two response shapes (roster summary vs
// detail) so the list endpoint doesn't carry the trust timeline.
type agentRecord struct {
	ID           uuid.UUID                 `json:"id"`
	Name         string                    `json:"name"`
	Tier         agentTier                 `json:"tier"`
	TrustScore   float64                   `json:"trust_score"`
	Capabilities map[string]capabilityStat `json:"capabilities"`
	Incidents    int                       `json:"incidents"`
	LastActive   string                    `json:"last_active"`
	TrustHistory []trustHistoryEntry       `json:"-"`
}

// agentListItem is a single entry in the roster list response. It mirrors
// agentRecord minus the trust history (returned only on detail).
type agentListItem struct {
	ID           uuid.UUID                 `json:"id"`
	Name         string                    `json:"name"`
	Tier         agentTier                 `json:"tier"`
	TrustScore   float64                   `json:"trust_score"`
	Capabilities map[string]capabilityStat `json:"capabilities"`
	Incidents    int                       `json:"incidents"`
	LastActive   string                    `json:"last_active"`
}

// agentDetailItem is the full agent detail response, including the trust
// history timeline (SPEC-023 §5).
type agentDetailItem struct {
	ID           uuid.UUID                 `json:"id"`
	Name         string                    `json:"name"`
	Tier         agentTier                 `json:"tier"`
	TrustScore   float64                   `json:"trust_score"`
	Capabilities map[string]capabilityStat `json:"capabilities"`
	Incidents    int                       `json:"incidents"`
	LastActive   string                    `json:"last_active"`
	TrustHistory []trustHistoryEntry       `json:"trust_history"`
}

type agentRegistry struct {
	agents map[uuid.UUID]agentRecord
}

func newAgentRegistry() *agentRegistry {
	r := &agentRegistry{agents: make(map[uuid.UUID]agentRecord)}
	for _, seed := range defaultAgentSeeds {
		r.seed(seed)
	}
	return r
}

// seed adds a named agent with a deterministic ID. Idempotent.
func (r *agentRegistry) seed(seed agentSeed) agentRecord {
	id := uuid.NewSHA1(agentNamespace, []byte(seed.Name))
	rec := agentRecord{
		ID:           id,
		Name:         seed.Name,
		Tier:         seed.Tier,
		TrustScore:   seed.TrustScore,
		Capabilities: seed.Capabilities,
		Incidents:    seed.Incidents,
		LastActive:   seed.LastActive,
		TrustHistory: seed.TrustHistory,
	}
	r.agents[id] = rec
	return rec
}

// list returns all registered agents sorted by name for deterministic output.
func (r *agentRegistry) list() []agentRecord {
	out := make([]agentRecord, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// get returns an agent by ID and whether it exists.
func (r *agentRegistry) get(id uuid.UUID) (agentRecord, bool) {
	a, ok := r.agents[id]
	return a, ok
}

// --- agentSeed: declarative seed data ---------------------------------------

// agentSeed is the declarative shape used to seed demo agents on
// construction. Kept separate from agentRecord so the seed table is
// self-documenting and easy to audit.
type agentSeed struct {
	Name         string
	Tier         agentTier
	TrustScore   float64
	Capabilities map[string]capabilityStat
	Incidents    int
	LastActive   string
	TrustHistory []trustHistoryEntry
}

// defaultAgentSeeds are seeded on registry construction (≥3 per AC-1).
// Timestamps are fixed RFC3339 strings so tests are fully deterministic.
var defaultAgentSeeds = []agentSeed{
	{
		Name:       "helix-foreman",
		Tier:       tierVeteran,
		TrustScore: 0.94,
		Capabilities: map[string]capabilityStat{
			"spec-writing": {Success: 48, Total: 50},
			"go-feature":   {Success: 120, Total: 135},
			"code-review":  {Success: 76, Total: 80},
		},
		Incidents:  2,
		LastActive: "2026-08-09T12:00:00Z",
		TrustHistory: []trustHistoryEntry{
			{Score: 0.70, At: "2026-06-01T00:00:00Z"},
			{Score: 0.85, At: "2026-07-01T00:00:00Z"},
			{Score: 0.94, At: "2026-08-01T00:00:00Z"},
		},
	},
	{
		Name:       "codex-worker",
		Tier:       tierEstablished,
		TrustScore: 0.81,
		Capabilities: map[string]capabilityStat{
			"typescript": {Success: 60, Total: 75},
			"react":      {Success: 40, Total: 48},
			"refactor":   {Success: 22, Total: 25},
		},
		Incidents:  1,
		LastActive: "2026-08-09T11:30:00Z",
		TrustHistory: []trustHistoryEntry{
			{Score: 0.60, At: "2026-07-01T00:00:00Z"},
			{Score: 0.72, At: "2026-07-15T00:00:00Z"},
			{Score: 0.81, At: "2026-08-01T00:00:00Z"},
		},
	},
	{
		Name:       "kimi-scout",
		Tier:       tierProvisional,
		TrustScore: 0.58,
		Capabilities: map[string]capabilityStat{
			"investigation": {Success: 9, Total: 16},
			"reporting":     {Success: 7, Total: 10},
		},
		Incidents:  0,
		LastActive: "2026-08-09T10:15:00Z",
		TrustHistory: []trustHistoryEntry{
			{Score: 0.40, At: "2026-08-01T00:00:00Z"},
			{Score: 0.50, At: "2026-08-05T00:00:00Z"},
			{Score: 0.58, At: "2026-08-09T00:00:00Z"},
		},
	},
}

// --- AgentHandler -----------------------------------------------------------

// AgentHandler exposes the agent roster surface (SPEC-023 §5 + §7).
type AgentHandler struct {
	agents *agentRegistry
}

// NewAgentHandler returns a handler with the default demo agents seeded.
// No DB dependency for MVP — the registry is in-memory.
func NewAgentHandler() *AgentHandler {
	return &AgentHandler{agents: newAgentRegistry()}
}

// Routes mounts the agent roster endpoints.
func (h *AgentHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.ListAgents)
	r.Get("/{agent_id}", h.AgentDetail)
	return r
}

// --- ListAgents -------------------------------------------------------------

// ListAgents returns the agent roster as a JSON array (SPEC-023 §5).
func (h *AgentHandler) ListAgents(w http.ResponseWriter, r *http.Request) {
	agents := h.agents.list()
	items := make([]agentListItem, 0, len(agents))
	for _, a := range agents {
		items = append(items, agentListItem{
			ID:           a.ID,
			Name:         a.Name,
			Tier:         a.Tier,
			TrustScore:   a.TrustScore,
			Capabilities: a.Capabilities,
			Incidents:    a.Incidents,
			LastActive:   a.LastActive,
		})
	}
	writeJSON(w, http.StatusOK, items)
}

// --- AgentDetail ------------------------------------------------------------

// AgentDetail returns a single agent's full detail, including the trust
// history timeline (SPEC-023 §5).
func (h *AgentHandler) AgentDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := parseAgentID(w, r)
	if !ok {
		return
	}

	a, exists := h.agents.get(id)
	if !exists {
		writeError(w, http.StatusNotFound, "AGENT_NOT_FOUND",
			fmt.Sprintf("agent %s does not exist", id))
		return
	}

	writeJSON(w, http.StatusOK, agentDetailItem{
		ID:           a.ID,
		Name:         a.Name,
		Tier:         a.Tier,
		TrustScore:   a.TrustScore,
		Capabilities: a.Capabilities,
		Incidents:    a.Incidents,
		LastActive:   a.LastActive,
		// Non-nil slice so the JSON encodes `[]` rather than `null`
		// for a (hypothetical) agent with no history.
		TrustHistory: nonNilHistory(a.TrustHistory),
	})
}

// nonNilHistory returns the given history, or an empty (non-nil) slice if
// it is nil, so the detail JSON always emits `trust_history: []` rather
// than `null`.
func nonNilHistory(h []trustHistoryEntry) []trustHistoryEntry {
	if h == nil {
		return []trustHistoryEntry{}
	}
	return h
}

// --- helpers ----------------------------------------------------------------

// parseAgentID reads and validates the {agent_id} chi URL parameter.
func parseAgentID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "agent_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_AGENT_ID",
			"agent_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}
