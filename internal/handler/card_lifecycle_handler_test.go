// Package handler tests the card lifecycle HTTP contract.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/card"
	"github.com/coding-hermes/hermes-canopy/internal/service"
)

func cardPatchRequest(t *testing.T, h http.Handler, cardID, body, revision string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/cards/"+cardID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if revision != "" {
		req.Header.Set("If-Match", revision)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func cardDeleteRequest(t *testing.T, h http.Handler, cardID, revision string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/cards/"+cardID, nil)
	if revision != "" {
		req.Header.Set("If-Match", revision)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func cardErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body apiErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}

func TestCardLifecycleAndIfMatchHTTP(t *testing.T) {
	svc, repo, cardID := cardActionStore(t)
	h := newCardActionRouter(svc)

	if got := cardPatchRequest(t, h, cardID.String(), `{}`, "").Code; got != http.StatusPreconditionRequired {
		t.Fatalf("PATCH without If-Match status = %d, want 428", got)
	}
	if got := cardDeleteRequest(t, h, cardID.String(), "").Code; got != http.StatusPreconditionRequired {
		t.Fatalf("DELETE without If-Match status = %d, want 428", got)
	}
	if rec := cardPatchRequest(t, h, cardID.String(), `{"data":{"x":1}}`, "not-an-integer"); rec.Code != http.StatusBadRequest || cardErrorCode(t, rec) != "CARD_REVISION_INVALID" {
		t.Fatalf("invalid If-Match response = %d %s", rec.Code, rec.Body.String())
	}
	if rec := cardPatchRequest(t, h, cardID.String(), `{}`, "1"); rec.Code != http.StatusBadRequest || cardErrorCode(t, rec) != "CARD_PATCH_EMPTY" {
		t.Fatalf("empty patch response = %d %s", rec.Code, rec.Body.String())
	}

	dismissed := cardPatchRequest(t, h, cardID.String(), `{"status":"dismissed"}`, "1")
	if dismissed.Code != http.StatusOK {
		t.Fatalf("dismiss PATCH status = %d, want 200; body=%s", dismissed.Code, dismissed.Body.String())
	}
	var dismissedSummary service.CardSummary
	if err := json.Unmarshal(dismissed.Body.Bytes(), &dismissedSummary); err != nil {
		t.Fatal(err)
	}
	if dismissedSummary.Status != "dismissed" || dismissedSummary.Revision != 2 {
		t.Fatalf("dismissed response = %+v", dismissedSummary)
	}
	stored, err := repo.Get(context.Background(), cardID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DismissedAt == nil {
		t.Fatal("dismiss PATCH did not set dismissed_at")
	}

	// The lifecycle event is observable through the same GET events route used by
	// clients, not only by inspecting SQLite directly.
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/cards/"+cardID.String()+"/events", nil).WithContext(ctx)
	stream := newStreamRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(stream, req)
		close(done)
	}()
	stream.waitFor(t, `"event_type":"card_dismissed"`)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("card events GET did not stop after request cancellation")
	}

	stale := cardPatchRequest(t, h, cardID.String(), `{"data":{"blocked":true}}`, "1")
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale PATCH status = %d, want 412; body=%s", stale.Code, stale.Body.String())
	}
	var conflict cardRevisionConflictBody
	if err := json.Unmarshal(stale.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Error.Code != "CARD_REVISION_CONFLICT" || conflict.Card == nil || conflict.Card.Revision != 2 {
		t.Fatalf("stale PATCH body = %+v, want current revision 2", conflict)
	}

	dismissedData := cardPatchRequest(t, h, cardID.String(), `{"data":{"blocked":true}}`, "2")
	if dismissedData.Code != http.StatusConflict || cardErrorCode(t, dismissedData) != "CARD_STATUS_DISMISSED" {
		t.Fatalf("dismissed data PATCH = %d %s", dismissedData.Code, dismissedData.Body.String())
	}

	restored := cardPatchRequest(t, h, cardID.String(), `{"status":"active"}`, "2")
	if restored.Code != http.StatusOK {
		t.Fatalf("restore PATCH status = %d, want 200; body=%s", restored.Code, restored.Body.String())
	}
	stored, err = repo.Get(context.Background(), cardID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != card.CardStatusActive || stored.DismissedAt != nil {
		t.Fatalf("restored row = %+v, want active with nil dismissed_at", stored)
	}

	// The restore transition emits card_restored, observable on the same
	// stream clients use — every lifecycle event type must be wire-asserted.
	ctx2, cancel2 := context.WithCancel(context.Background())
	req2 := httptest.NewRequest(http.MethodGet, "/cards/"+cardID.String()+"/events", nil).WithContext(ctx2)
	stream2 := newStreamRecorder()
	done2 := make(chan struct{})
	go func() {
		h.ServeHTTP(stream2, req2)
		close(done2)
	}()
	stream2.waitFor(t, `"event_type":"card_restored"`)
	cancel2()
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("card events GET did not stop after request cancellation")
	}

	archived := cardDeleteRequest(t, h, cardID.String(), "3")
	if archived.Code != http.StatusNoContent {
		t.Fatalf("archive DELETE status = %d, want 204; body=%s", archived.Code, archived.Body.String())
	}
	stored, err = repo.Get(context.Background(), cardID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != card.CardStatusArchived || stored.ArchivedAt == nil {
		t.Fatalf("archived row = %+v, want archived_at", stored)
	}

	terminal := cardPatchRequest(t, h, cardID.String(), `{"status":"active"}`, "4")
	if terminal.Code != http.StatusConflict || cardErrorCode(t, terminal) != "CARD_STATUS_ARCHIVED" {
		t.Fatalf("archived PATCH = %d %s", terminal.Code, terminal.Body.String())
	}

	// DELETE is terminal too: an already-archived card cannot be re-archived.
	redelete := cardDeleteRequest(t, h, cardID.String(), "4")
	if redelete.Code != http.StatusConflict || cardErrorCode(t, redelete) != "CARD_STATUS_ARCHIVED" {
		t.Fatalf("archived DELETE = %d %s", redelete.Code, redelete.Body.String())
	}

	archivedData := cardPatchRequest(t, h, cardID.String(), `{"data":{"blocked":true}}`, "4")
	if archivedData.Code != http.StatusConflict || cardErrorCode(t, archivedData) != "CARD_STATUS_ARCHIVED" {
		t.Fatalf("archived data PATCH = %d %s", archivedData.Code, archivedData.Body.String())
	}
}
