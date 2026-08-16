// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file implements the PR review panel surface (SPEC-023 §2 item 2,
// §4, §5).
//
// Endpoints (mounted at /api/v1/reviews by server.go):
//
//	GET  /             — list reviews (sorted, deterministic)
//	GET  /{review_id}  — review detail incl. blast radius + Chimera verdict
//	POST /{pr}/trigger — trigger a simulated Chimera review + broadcast
package handler

import (
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

// --- reviewRegistry: in-memory review store (SPEC-023 §4) -------------------
//
// Reviews are MVP-grade and live entirely in memory — no DB table. The
// registry is seeded with demo PR reviews on construction. Review IDs are
// deterministic UUIDs derived from a fixed namespace (distinct from the
// agent and channel namespaces) so they are stable across restarts and
// testable, matching the technique used by the agent and channel
// registries.

// reviewNamespace is a fixed UUID used to derive stable review IDs via
// uuid.NewSHA1(reviewNamespace, []byte(seed)). Distinct from
// agentNamespace and channelNamespace to keep the ID spaces independent.
var reviewNamespace = uuid.MustParse("c3d4e5f6-a7b8-9012-cdef-234567890112")

// reviewEventsChannel is the workspace channel review events are broadcast
// to. Mirrors the workspace handler's "general" channel (SPEC-023 §5).
var reviewEventsChannel = uuid.NewSHA1(channelNamespace, []byte("general"))

// reviewStatus is the lifecycle state of a PR review.
type reviewStatus string

const (
	statusPending          reviewStatus = "pending"
	statusReviewing        reviewStatus = "reviewing"
	statusApproved         reviewStatus = "approved"
	statusRequestedChanges reviewStatus = "requested_changes"
)

// chimeraVerdictType is the verdict produced by a simulated Chimera review.
type chimeraVerdictType string

const (
	verdictApprove        chimeraVerdictType = "approve"
	verdictRequestChanges chimeraVerdictType = "request_changes"
	verdictError          chimeraVerdictType = "error"
)

// blastRadius summarises the potential impact of a PR (SPEC-023 §4).
type blastRadius struct {
	FilesTouched    []string `json:"files_touched"`
	DependentsCount int      `json:"dependents_count"`
}

// chimeraVerdict is the full Chimera multi-model review verdict.
type chimeraVerdict struct {
	Verdict        chimeraVerdictType `json:"verdict"`
	ModelFormation string             `json:"model_formation"`
	Summary        string             `json:"summary"`
	Confidence     float64            `json:"confidence"`
	At             string             `json:"at"`
}

// reviewRecord is the full in-memory representation of a seeded or
// triggered PR review. The JSON contract is split into list vs detail
// response shapes.
type reviewRecord struct {
	ID          uuid.UUID       `json:"id"`
	PR          string          `json:"pr"`
	Title       string          `json:"title"`
	Author      string          `json:"author"`
	Status      reviewStatus    `json:"status"`
	RiskScore   float64         `json:"risk_score"`
	BlastRadius blastRadius     `json:"blast_radius"`
	Verdict     *chimeraVerdict `json:"verdict"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

// reviewListItem is a single entry in the review list response. Carries
// only the summary fields needed for the rail — blast radius and verdict
// are returned on detail.
type reviewListItem struct {
	ID        uuid.UUID    `json:"id"`
	PR        string       `json:"pr"`
	Title     string       `json:"title"`
	Author    string       `json:"author"`
	Status    reviewStatus `json:"status"`
	RiskScore float64      `json:"risk_score"`
	UpdatedAt string       `json:"updated_at"`
}

// reviewDetailItem is the full review detail response, including blast
// radius and the Chimera verdict (null when the review has not yet run).
type reviewDetailItem struct {
	ID          uuid.UUID       `json:"id"`
	PR          string          `json:"pr"`
	Title       string          `json:"title"`
	Author      string          `json:"author"`
	Status      reviewStatus    `json:"status"`
	RiskScore   float64         `json:"risk_score"`
	BlastRadius blastRadius     `json:"blast_radius"`
	Verdict     *chimeraVerdict `json:"verdict"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type reviewRegistry struct {
	mu      sync.RWMutex
	reviews map[uuid.UUID]reviewRecord
}

func newReviewRegistry() *reviewRegistry {
	r := &reviewRegistry{reviews: make(map[uuid.UUID]reviewRecord)}
	for _, seed := range defaultReviewSeeds {
		r.seed(seed)
	}
	return r
}

// seed adds a review from a declarative seed with a deterministic ID.
func (r *reviewRegistry) seed(seed reviewSeed) reviewRecord {
	id := uuid.NewSHA1(reviewNamespace, []byte(seed.Key))
	rec := reviewRecord{
		ID:        id,
		PR:        seed.PR,
		Title:     seed.Title,
		Author:    seed.Author,
		Status:    seed.Status,
		RiskScore: seed.RiskScore,
		BlastRadius: blastRadius{
			FilesTouched:    seed.FilesTouched,
			DependentsCount: seed.DependentsCount,
		},
		Verdict:   seed.Verdict,
		CreatedAt: seed.CreatedAt,
		UpdatedAt: seed.UpdatedAt,
	}
	r.reviews[id] = rec
	return rec
}

// list returns all reviews sorted by created_at descending (newest first)
// for deterministic output.
func (r *reviewRegistry) list() []reviewRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]reviewRecord, 0, len(r.reviews))
	for _, rec := range r.reviews {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out
}

// get returns a review by ID and whether it exists.
func (r *reviewRegistry) get(id uuid.UUID) (reviewRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.reviews[id]
	return rec, ok
}

// getOrCreateByPR looks up a review by PR identifier, creating a pending
// review if none exists. Returns the record and whether it was created.
func (r *reviewRegistry) getOrCreateByPR(pr string) (reviewRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.reviews {
		if rec.PR == pr {
			return rec, false
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec := reviewRecord{
		ID:        uuid.NewSHA1(reviewNamespace, []byte("pr:"+pr)),
		PR:        pr,
		Title:     fmt.Sprintf("PR #%s", pr),
		Author:    "external",
		Status:    statusPending,
		RiskScore: 0,
		BlastRadius: blastRadius{
			FilesTouched:    []string{},
			DependentsCount: 0,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	r.reviews[rec.ID] = rec
	return rec, true
}

// upsert inserts or replaces a review record by ID.
func (r *reviewRegistry) upsert(rec reviewRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reviews[rec.ID] = rec
}

// --- reviewSeed: declarative seed data --------------------------------------

type reviewSeed struct {
	Key             string
	PR              string
	Title           string
	Author          string
	Status          reviewStatus
	RiskScore       float64
	FilesTouched    []string
	DependentsCount int
	Verdict         *chimeraVerdict
	CreatedAt       string
	UpdatedAt       string
}

// defaultReviewSeeds are seeded on registry construction (≥3 per AC-1).
// Timestamps are fixed RFC3339 strings for fully deterministic tests.
var defaultReviewSeeds = []reviewSeed{
	{
		Key:       "pr-1042",
		PR:        "1042",
		Title:     "feat: add agent roster surface",
		Author:    "helix-foreman",
		Status:    statusApproved,
		RiskScore: 0.12,
		FilesTouched: []string{
			"internal/handler/agent_handler.go",
			"internal/server/server.go",
			"frontend/src/pages/AgentsPage.tsx",
		},
		DependentsCount: 7,
		Verdict: &chimeraVerdict{
			Verdict:        verdictApprove,
			ModelFormation: "single-judge",
			Summary:        "Low risk — safe to merge.",
			Confidence:     0.94,
			At:             "2026-08-09T12:30:00Z",
		},
		CreatedAt: "2026-08-09T12:00:00Z",
		UpdatedAt: "2026-08-09T12:30:00Z",
	},
	{
		Key:       "pr-1038",
		PR:        "1038",
		Title:     "refactor: workspace SSE hub extraction",
		Author:    "codex-worker",
		Status:    statusRequestedChanges,
		RiskScore: 0.34,
		FilesTouched: []string{
			"internal/sse/sse_hub.go",
			"internal/handler/workspace_handler.go",
		},
		DependentsCount: 12,
		Verdict: &chimeraVerdict{
			Verdict:        verdictRequestChanges,
			ModelFormation: "dual-review",
			Summary:        "Moderate risk — changes requested before merge.",
			Confidence:     0.83,
			At:             "2026-08-09T11:15:00Z",
		},
		CreatedAt: "2026-08-09T10:00:00Z",
		UpdatedAt: "2026-08-09T11:15:00Z",
	},
	{
		Key:       "pr-1051",
		PR:        "1051",
		Title:     "fix: recursive CTE depth calculation",
		Author:    "kimi-scout",
		Status:    statusReviewing,
		RiskScore: 0.58,
		FilesTouched: []string{
			"internal/service/node_service.go",
			"migrations/000021_depth.up.sql",
		},
		DependentsCount: 23,
		Verdict:         nil, // still under review
		CreatedAt:       "2026-08-09T09:00:00Z",
		UpdatedAt:       "2026-08-09T09:30:00Z",
	},
	{
		Key:       "pr-1055",
		PR:        "1055",
		Title:     "chore: bump go-chi to v5.2.1",
		Author:    "helix-foreman",
		Status:    statusPending,
		RiskScore: 0.05,
		FilesTouched: []string{
			"go.mod",
			"go.sum",
		},
		DependentsCount: 2,
		Verdict:         nil, // not yet reviewed
		CreatedAt:       "2026-08-09T08:00:00Z",
		UpdatedAt:       "2026-08-09T08:00:00Z",
	},
}

// --- ReviewHandler ----------------------------------------------------------

// ReviewHandler exposes the PR review panel surface (SPEC-023 §2, §4, §5).
type ReviewHandler struct {
	reviews *reviewRegistry
	hub     sse.SSEHub
}

// NewReviewHandler returns a handler with the default demo reviews seeded.
// The hub is used to broadcast review_event messages on the workspace
// channel SSE feed when a review is triggered. No DB dependency for MVP.
func NewReviewHandler(hub sse.SSEHub) *ReviewHandler {
	return &ReviewHandler{
		reviews: newReviewRegistry(),
		hub:     hub,
	}
}

// Routes mounts the review endpoints.
func (h *ReviewHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.ListReviews)
	r.Get("/{review_id}", h.ReviewDetail)
	r.Post("/{pr}/trigger", h.TriggerReview)
	return r
}

// --- ListReviews ------------------------------------------------------------

// ListReviews returns all reviews as a JSON array (SPEC-023 §4).
func (h *ReviewHandler) ListReviews(w http.ResponseWriter, _ *http.Request) {
	reviews := h.reviews.list()
	items := make([]reviewListItem, 0, len(reviews))
	for _, rec := range reviews {
		items = append(items, reviewListItem{
			ID:        rec.ID,
			PR:        rec.PR,
			Title:     rec.Title,
			Author:    rec.Author,
			Status:    rec.Status,
			RiskScore: rec.RiskScore,
			UpdatedAt: rec.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, items)
}

// --- ReviewDetail -----------------------------------------------------------

// ReviewDetail returns a single review's full detail, including blast
// radius and the Chimera verdict (SPEC-023 §4).
func (h *ReviewHandler) ReviewDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := parseReviewID(w, r)
	if !ok {
		return
	}

	rec, exists := h.reviews.get(id)
	if !exists {
		writeError(w, http.StatusNotFound, "REVIEW_NOT_FOUND",
			fmt.Sprintf("review %s does not exist", id))
		return
	}

	writeJSON(w, http.StatusOK, reviewDetailFromRecord(rec))
}

// --- TriggerReview ----------------------------------------------------------

// reviewEventData is the SSE payload broadcast when a review is triggered.
type reviewEventData struct {
	ReviewID    uuid.UUID          `json:"review_id"`
	PR          string             `json:"pr"`
	Title       string             `json:"title"`
	Status      reviewStatus       `json:"status"`
	Verdict     chimeraVerdictType `json:"verdict"`
	RiskScore   float64            `json:"risk_score"`
	TriggeredAt string             `json:"triggered_at"`
}

// TriggerReview runs a simulated Chimera review for the given PR,
// creating or updating the review record, and broadcasts a review_event
// on the workspace channel SSE feed (SPEC-023 §5).
func (h *ReviewHandler) TriggerReview(w http.ResponseWriter, r *http.Request) {
	pr := chi.URLParam(r, "pr")
	if pr == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PR",
			"pr must not be empty")
		return
	}

	rec, _ := h.reviews.getOrCreateByPR(pr)

	// Simulated Chimera review — deterministic per PR so tests are stable.
	risk := deriveRiskScore(pr)
	verdict := deriveVerdict(risk)
	formation := deriveModelFormation(risk)
	confidence := 1.0 - risk*0.5

	now := time.Now().UTC().Format(time.RFC3339)
	rec.RiskScore = risk
	rec.Verdict = &chimeraVerdict{
		Verdict:        verdict,
		ModelFormation: formation,
		Summary:        verdictSummary(verdict),
		Confidence:     confidence,
		At:             now,
	}
	switch verdict {
	case verdictApprove:
		rec.Status = statusApproved
	case verdictRequestChanges:
		rec.Status = statusRequestedChanges
	case verdictError:
		rec.Status = statusReviewing
	}
	rec.UpdatedAt = now

	h.reviews.upsert(rec)

	// Broadcast review_event on the general workspace channel so the
	// review panel surfaces it live via useChannelFeed / useReviewFeed.
	data := reviewEventData{
		ReviewID:    rec.ID,
		PR:          rec.PR,
		Title:       rec.Title,
		Status:      rec.Status,
		Verdict:     verdict,
		RiskScore:   rec.RiskScore,
		TriggeredAt: now,
	}
	h.hub.Broadcast(reviewEventsChannel,
		sse.ComposeEvent(reviewEventsChannel, uuid.Nil, "review_event", data))

	writeJSON(w, http.StatusOK, reviewDetailFromRecord(rec))
}

// --- helpers ----------------------------------------------------------------

func reviewDetailFromRecord(r reviewRecord) reviewDetailItem {
	return reviewDetailItem{
		ID:          r.ID,
		PR:          r.PR,
		Title:       r.Title,
		Author:      r.Author,
		Status:      r.Status,
		RiskScore:   r.RiskScore,
		BlastRadius: blastRadius{FilesTouched: nonNilFiles(r.BlastRadius.FilesTouched), DependentsCount: r.BlastRadius.DependentsCount},
		Verdict:     r.Verdict,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

// nonNilFiles returns the given slice, or an empty (non-nil) slice if nil,
// so the detail JSON always emits files_touched: [] rather than null.
func nonNilFiles(files []string) []string {
	if files == nil {
		return []string{}
	}
	return files
}

// parseReviewID reads and validates the {review_id} chi URL parameter.
func parseReviewID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "review_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REVIEW_ID",
			"review_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}

// --- deterministic Chimera simulation helpers -------------------------------

// deriveRiskScore maps a PR identifier to a deterministic 0.0..0.99 risk
// score using FNV-1a hashing. Same PR → same score on every call.
func deriveRiskScore(pr string) float64 {
	h := fnv.New64a()
	_, _ = io.WriteString(h, pr)
	return float64(h.Sum64()%100) / 100.0
}

// deriveVerdict maps a risk score to a Chimera verdict type.
func deriveVerdict(risk float64) chimeraVerdictType {
	switch {
	case risk < 0.40:
		return verdictApprove
	case risk < 0.70:
		return verdictRequestChanges
	default:
		return verdictError
	}
}

// deriveModelFormation maps a risk score to a Chimera model formation.
func deriveModelFormation(risk float64) string {
	switch {
	case risk < 0.40:
		return "single-judge"
	case risk < 0.70:
		return "dual-review"
	default:
		return "triple-jury"
	}
}

// verdictSummary returns a human-readable summary for a verdict type.
func verdictSummary(verdict chimeraVerdictType) string {
	switch verdict {
	case verdictApprove:
		return "Low risk — safe to merge."
	case verdictRequestChanges:
		return "Moderate risk — changes requested before merge."
	case verdictError:
		return "High risk — review failed, manual intervention required."
	default:
		return "Review pending."
	}
}
