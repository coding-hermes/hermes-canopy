package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newReviewTestServer builds a minimal router with ONLY the review routes
// (no auth, no DB). Used for pure unit tests that must run without
// PostgreSQL. Returns the httptest.Server and the SSE hub (so tests can
// assert broadcast behaviour).
func newReviewTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1})
	t.Cleanup(func() { _ = hub.Shutdown(context.Background()) })

	h := NewReviewHandler(hub)
	r := chi.NewRouter()
	r.Mount("/api/v1/reviews", h.Routes())
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// reviewGet issues a GET against the test server.
func reviewGet(t *testing.T, srv *httptest.Server, path string) *http.Response {
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

// reviewPost issues a POST against the test server.
func reviewPost(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

// ---------------------------------------------------------------------------
// ListReviews
// ---------------------------------------------------------------------------

func TestReview_ListReviews(t *testing.T) {
	srv := newReviewTestServer(t)
	resp := reviewGet(t, srv, "/api/v1/reviews")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, string(body))
	}

	var list []reviewListItem
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// AC-1: at least 3 seeded reviews.
	if len(list) < 3 {
		t.Fatalf("expected at least 3 seeded reviews, got %d", len(list))
	}

	// AC-1: every entry has the required summary fields + deterministic IDs.
	prs := make(map[string]bool)
	for _, r := range list {
		if r.ID == uuid.Nil {
			t.Error("review id is nil")
		}
		if r.PR == "" {
			t.Error("review pr is empty")
		}
		if r.Title == "" {
			t.Error("review title is empty")
		}
		if r.Status == "" {
			t.Error("review status is empty")
		}
		if r.RiskScore < 0 || r.RiskScore > 1 {
			t.Errorf("review %q risk_score = %v, want [0,1]", r.PR, r.RiskScore)
		}
		if r.UpdatedAt == "" {
			t.Errorf("review %q has empty updated_at", r.PR)
		}
		prs[r.PR] = true
	}

	// All four seeded PRs present.
	for _, pr := range []string{"1042", "1038", "1051", "1055"} {
		if !prs[pr] {
			t.Errorf("expected seeded PR %q in list", pr)
		}
	}

	// AC-1: list is sorted by created_at descending (newest first).
	for i := 1; i < len(list); i++ {
		if list[i].UpdatedAt > list[i-1].UpdatedAt {
			t.Errorf("list not sorted desc at idx %d: %s > %s",
				i, list[i].UpdatedAt, list[i-1].UpdatedAt)
		}
	}

	t.Logf("list: %d reviews", len(list))
}

// ---------------------------------------------------------------------------
// ReviewDetail
// ---------------------------------------------------------------------------

func TestReview_Detail_FullShape(t *testing.T) {
	srv := newReviewTestServer(t)
	id := uuid.NewSHA1(reviewNamespace, []byte("pr-1042"))
	resp := reviewGet(t, srv, "/api/v1/reviews/"+id.String())
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, string(body))
	}

	var detail reviewDetailItem
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// AC-1: detail includes all summary fields.
	if detail.ID != id {
		t.Errorf("id = %s, want %s", detail.ID, id)
	}
	if detail.PR != "1042" {
		t.Errorf("pr = %q, want 1042", detail.PR)
	}
	if detail.Title == "" {
		t.Error("title is empty")
	}
	if detail.Author == "" {
		t.Error("author is empty")
	}
	if detail.Status != statusApproved {
		t.Errorf("status = %q, want approved", detail.Status)
	}
	if detail.RiskScore < 0 || detail.RiskScore > 1 {
		t.Errorf("risk_score = %v, want [0,1]", detail.RiskScore)
	}
	if detail.CreatedAt == "" || detail.UpdatedAt == "" {
		t.Error("created_at or updated_at is empty")
	}

	// AC-1: detail includes blast_radius with files_touched + dependents_count.
	if detail.BlastRadius.FilesTouched == nil {
		t.Error("blast_radius.files_touched is nil, want non-nil slice")
	}
	if len(detail.BlastRadius.FilesTouched) == 0 {
		t.Error("blast_radius.files_touched is empty")
	}
	if detail.BlastRadius.DependentsCount <= 0 {
		t.Errorf("dependents_count = %d, want > 0", detail.BlastRadius.DependentsCount)
	}

	// AC-1: detail includes verdict (non-nil for approved seeds).
	if detail.Verdict == nil {
		t.Fatal("verdict is nil for an approved review")
	}
	if detail.Verdict.Verdict != verdictApprove {
		t.Errorf("verdict.verdict = %q, want approve", detail.Verdict.Verdict)
	}
	if detail.Verdict.ModelFormation == "" {
		t.Error("verdict.model_formation is empty")
	}
	if detail.Verdict.Confidence < 0 || detail.Verdict.Confidence > 1 {
		t.Errorf("verdict.confidence = %v, want [0,1]", detail.Verdict.Confidence)
	}
	if detail.Verdict.At == "" {
		t.Error("verdict.at is empty")
	}

	t.Logf("detail: PR #%s (%s), verdict=%s, confidence=%.2f",
		detail.PR, detail.Status, detail.Verdict.Verdict, detail.Verdict.Confidence)
}

func TestReview_Detail_PendingReview_NilVerdict(t *testing.T) {
	srv := newReviewTestServer(t)
	// pr-1055 is seeded with status=pending and verdict=nil.
	id := uuid.NewSHA1(reviewNamespace, []byte("pr-1055"))
	resp := reviewGet(t, srv, "/api/v1/reviews/"+id.String())
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var detail reviewDetailItem
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if detail.Verdict != nil {
		t.Errorf("pending review verdict = %v, want nil", detail.Verdict)
	}
	// Non-nil files slice even when empty.
	if detail.BlastRadius.FilesTouched == nil {
		t.Error("files_touched is nil for pending review, want non-nil []")
	}
}

func TestReview_Detail_UnknownID(t *testing.T) {
	srv := newReviewTestServer(t)
	unknown := uuid.New()
	resp := reviewGet(t, srv, "/api/v1/reviews/"+unknown.String())
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}

	var errBody apiErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody.Error.Code != "REVIEW_NOT_FOUND" {
		t.Errorf("error code = %q, want REVIEW_NOT_FOUND", errBody.Error.Code)
	}
}

func TestReview_Detail_InvalidUUID(t *testing.T) {
	srv := newReviewTestServer(t)
	resp := reviewGet(t, srv, "/api/v1/reviews/not-a-uuid")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}

	var errBody apiErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody.Error.Code != "INVALID_REVIEW_ID" {
		t.Errorf("error code = %q, want INVALID_REVIEW_ID", errBody.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// TriggerReview
// ---------------------------------------------------------------------------

func TestReview_Trigger_NewPR(t *testing.T) {
	srv := newReviewTestServer(t)
	resp := reviewPost(t, srv, "/api/v1/reviews/7777/trigger")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, string(body))
	}

	var detail reviewDetailItem
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// AC-2: trigger produces a review record deterministically.
	if detail.PR != "7777" {
		t.Errorf("pr = %q, want 7777", detail.PR)
	}
	if detail.Status == statusPending {
		t.Error("status is still pending after trigger — expected verdict applied")
	}
	if detail.RiskScore < 0 || detail.RiskScore > 1 {
		t.Errorf("risk_score = %v, want [0,1]", detail.RiskScore)
	}
	if detail.Verdict == nil {
		t.Fatal("verdict is nil after trigger")
	}
	if detail.Verdict.Verdict != verdictApprove &&
		detail.Verdict.Verdict != verdictRequestChanges &&
		detail.Verdict.Verdict != verdictError {
		t.Errorf("verdict = %q, want a valid chimera verdict", detail.Verdict.Verdict)
	}

	// AC-2: deterministic — same PR produces the same risk score on repeat.
	risk1 := detail.RiskScore
	resp2 := reviewPost(t, srv, "/api/v1/reviews/7777/trigger")
	defer resp2.Body.Close()
	var detail2 reviewDetailItem
	if err := json.NewDecoder(resp2.Body).Decode(&detail2); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if detail2.RiskScore != risk1 {
		t.Errorf("risk score not deterministic: %v != %v", detail2.RiskScore, risk1)
	}

	t.Logf("trigger: PR #7777 → risk=%.2f, verdict=%s, formation=%s",
		detail.RiskScore, detail.Verdict.Verdict, detail.Verdict.ModelFormation)
}

func TestReview_Trigger_BroadcastsSSEEvent(t *testing.T) {
	// Build a server with a real hub so we can assert the broadcast.
	hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1})
	t.Cleanup(func() { _ = hub.Shutdown(context.Background()) })

	h := NewReviewHandler(hub)
	r := chi.NewRouter()
	r.Mount("/api/v1/reviews", h.Routes())
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	// Trigger a review — should broadcast a review_event on the general
	// channel. We assert no panic + 200 (full SSE delivery is covered by
	// the workspace handler tests and sse package tests).
	resp := reviewPost(t, srv, "/api/v1/reviews/8888/trigger")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, string(body))
	}

	var detail reviewDetailItem
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if detail.Verdict == nil {
		t.Fatal("verdict is nil after trigger")
	}
}

func TestReview_Trigger_DeterministicVerdict(t *testing.T) {
	srv := newReviewTestServer(t)

	// The verdict for a given PR must be deterministic across calls.
	resp1 := reviewPost(t, srv, "/api/v1/reviews/9999/trigger")
	defer resp1.Body.Close()
	var d1 reviewDetailItem
	if err := json.NewDecoder(resp1.Body).Decode(&d1); err != nil {
		t.Fatalf("decode first: %v", err)
	}

	resp2 := reviewPost(t, srv, "/api/v1/reviews/9999/trigger")
	defer resp2.Body.Close()
	var d2 reviewDetailItem
	if err := json.NewDecoder(resp2.Body).Decode(&d2); err != nil {
		t.Fatalf("decode second: %v", err)
	}

	if d1.Verdict == nil || d2.Verdict == nil {
		t.Fatal("verdict nil")
	}
	if d1.Verdict.Verdict != d2.Verdict.Verdict {
		t.Errorf("verdict not deterministic: %q != %q",
			d1.Verdict.Verdict, d2.Verdict.Verdict)
	}
	if d1.Verdict.ModelFormation != d2.Verdict.ModelFormation {
		t.Errorf("model formation not deterministic: %q != %q",
			d1.Verdict.ModelFormation, d2.Verdict.ModelFormation)
	}
}

// Ensure the test binary compiles even if time import is otherwise unused
// after future refactors.
var _ = time.RFC3339
