// Unit tests for the SPEC-PL-06 §6 compile-surface wiring: the handler's
// loader → CompileRequest.MultiReference seam and the §9.4 → HTTP status
// mapping. No database is required — the loader and the compiler are stubs.
package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	ctxpkg "github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// --- Stubs -------------------------------------------------------------------

// recordingCompiler captures the request the handler built.
type recordingCompiler struct {
	last   ctxpkg.CompileRequest
	calls  int
	result *ctxpkg.CompiledContext
	err    error
}

func (c *recordingCompiler) Compile(_ context.Context, req ctxpkg.CompileRequest) (*ctxpkg.CompiledContext, error) {
	c.calls++
	c.last = req
	if c.err != nil {
		return nil, c.err
	}
	if c.result != nil {
		return c.result, nil
	}
	return &ctxpkg.CompiledContext{Content: "compiled", Manifest: &ctxpkg.Manifest{}}, nil
}

// stubSelectionLoader stands in for *service.TreeServiceImpl.
type stubSelectionLoader struct {
	sel   *service.CompileSelection
	err   error
	gotID uuid.UUID
	calls int
}

func (s *stubSelectionLoader) LoadCompileSelection(_ context.Context, nodeID uuid.UUID) (*service.CompileSelection, error) {
	s.calls++
	s.gotID = nodeID
	return s.sel, s.err
}

// ctxHandlerRouter mounts the compile handler at the production route shape.
func ctxHandlerRouter(h *ContextHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/context/{node_id}", h.Compile)
	return r
}

func ctxHandlerGet(t *testing.T, r *chi.Mux, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// --- Wiring ------------------------------------------------------------------

func TestContextHandler_WiresPersistedSelectionIntoCompileRequest(t *testing.T) {
	nodeID := uuid.New()
	treeID := uuid.New()
	srcA := uuid.New()
	srcB := uuid.New()
	created := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	loader := &stubSelectionLoader{sel: &service.CompileSelection{
		TreeID: treeID,
		Metadata: db.MultiReferenceMetadata{
			Version:             db.MultiReferenceMetadataVersion,
			PrimarySourceID:     srcA,
			CanonicalSourceIDs:  []uuid.UUID{srcA, srcB},
			ContextManifestHash: hash,
			ContextTokenBudget:  4000,
		},
		Sources: []service.CompileSelectionSource{
			{
				NodeID: srcA, AuthorID: uuid.New(), NodeType: "message", SequenceNum: 2,
				CreatedAt: created, BranchRootID: srcA, ContentHash: "hash-a",
				Content: "source A", Label: "R1", ColorKey: "ref-3",
			},
			{
				NodeID: srcB, AuthorID: uuid.New(), NodeType: "message", SequenceNum: 3,
				CreatedAt: created.Add(time.Minute), BranchRootID: uuid.New(), ContentHash: "hash-b",
				Content: "source B", Label: "R2", ColorKey: "ref-5",
			},
		},
	}}
	compiler := &recordingCompiler{}
	h := NewContextHandler(compiler, 8000).WithReferenceSelectionLoader(loader)

	w := ctxHandlerGet(t, ctxHandlerRouter(h), "/context/"+nodeID.String()+"?budget=4200")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if loader.calls != 1 || loader.gotID != nodeID {
		t.Fatalf("loader calls=%d gotID=%s, want 1 call for %s", loader.calls, loader.gotID, nodeID)
	}

	got := compiler.last.MultiReference
	if got == nil {
		t.Fatal("CompileRequest.MultiReference is nil for a multi-reference target")
	}
	if got.TreeID != treeID {
		t.Errorf("TreeID = %s, want %s", got.TreeID, treeID)
	}
	if got.Metadata.ContextManifestHash != hash {
		t.Errorf("Metadata.ContextManifestHash = %q, want the stored %q", got.Metadata.ContextManifestHash, hash)
	}
	// ProfileBudget is the EFFECTIVE TURN BUDGET of the request — the same
	// number in CompileRequest.TokenBudget — never a second budget source.
	if got.ProfileBudget != 4200 || compiler.last.TokenBudget != 4200 {
		t.Errorf("ProfileBudget = %d, CompileRequest.TokenBudget = %d, want 4200 (one budget source)",
			got.ProfileBudget, compiler.last.TokenBudget)
	}
	if len(got.Sources) != 2 {
		t.Fatalf("only %d sources reached the compiler", len(got.Sources))
	}

	want := []struct {
		nodeID    uuid.UUID
		content   string
		hash      string
		label     string
		colorKey  string
		branchTag uuid.UUID
	}{
		{nodeID: srcA, content: "source A", hash: "hash-a", label: "R1", colorKey: "ref-3", branchTag: srcA},
		{nodeID: srcB, content: "source B", hash: "hash-b", label: "R2", colorKey: "ref-5"},
	}
	for i, w := range want {
		src := got.Sources[i]
		if src.NodeID != w.nodeID {
			t.Errorf("source %d NodeID = %s, want %s", i, src.NodeID, w.nodeID)
		}
		if src.Content != w.content {
			t.Errorf("source %d Content = %q, want %q", i, src.Content, w.content)
		}
		if src.Label != w.label || src.ColorKey != w.colorKey {
			t.Errorf("source %d label/color = %q/%q, want %q/%q", i, src.Label, src.ColorKey, w.label, w.colorKey)
		}
		if src.ContentHash != w.hash {
			t.Errorf("source %d ContentHash = %q, want %q", i, src.ContentHash, w.hash)
		}
		// The loader has already verified the live set against the stored
		// digest, so the §6.4 per-source check must not manufacture a false
		// staleness.
		if src.SignedContentHash != src.ContentHash {
			t.Errorf("source %d SignedContentHash = %q, want the verified live hash %q",
				i, src.SignedContentHash, src.ContentHash)
		}
		if src.BranchRootID == uuid.Nil {
			t.Errorf("source %d has no BranchRootID", i)
		}
		if w.branchTag != uuid.Nil && src.BranchRootID != w.branchTag {
			t.Errorf("source %d BranchRootID = %s, want %s", i, src.BranchRootID, w.branchTag)
		}
		if src.CreatedAt.Location() != time.UTC {
			t.Errorf("source %d CreatedAt is not UTC: %v", i, src.CreatedAt)
		}
	}

	if len(compiler.last.MultiReference.Sources) != 2 {
		t.Fatal("source order was not preserved")
	}
}

func TestContextHandler_NoSelectionLeavesCompileRequestUntouched(t *testing.T) {
	nodeID := uuid.New()
	loader := &stubSelectionLoader{}
	compiler := &recordingCompiler{}
	h := NewContextHandler(compiler, 8000).WithReferenceSelectionLoader(loader)

	w := ctxHandlerGet(t, ctxHandlerRouter(h), "/context/"+nodeID.String())
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if loader.calls != 1 {
		t.Errorf("loader calls = %d, want 1", loader.calls)
	}
	if compiler.last.MultiReference != nil {
		t.Error("an ordinary node must compile with MultiReference nil")
	}
}

func TestContextHandler_WithoutLoaderBehaviourIsUnchanged(t *testing.T) {
	nodeID := uuid.New()
	compiler := &recordingCompiler{}
	h := NewContextHandler(compiler, 8000) // no loader wired

	w := ctxHandlerGet(t, ctxHandlerRouter(h), "/context/"+nodeID.String())
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if compiler.last.MultiReference != nil {
		t.Error("unwired handler must never build a selection")
	}
}

// --- §9.4 mapping ------------------------------------------------------------

func TestContextHandler_MultiReferenceCompileCodesMapToStatus(t *testing.T) {
	cases := []struct {
		code   string
		status int
	}{
		{ctxpkg.CodeReferenceContextBudgetExceeded, http.StatusUnprocessableEntity},
		{ctxpkg.CodeReferenceSelectionStale, http.StatusConflict},
		{ctxpkg.CodeReferenceSourceNotFound, http.StatusNotFound},
		{ctxpkg.CodeReferenceSourceCountTooLow, http.StatusBadRequest},
		{ctxpkg.CodeReferenceSourceCountTooHigh, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			compiler := &recordingCompiler{err: &ctxpkg.MultiReferenceError{Code: tc.code, Message: "selection rejected"}}
			h := NewContextHandler(compiler, 8000)

			w := ctxHandlerGet(t, ctxHandlerRouter(h), "/context/"+uuid.New().String())
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if code := mrErrorCode(t, w.Body.Bytes()); code != tc.code {
				t.Errorf("error code = %q, want %q", code, tc.code)
			}
		})
	}
}

func TestContextHandler_UnknownMultiReferenceCodeIsAServerError(t *testing.T) {
	compiler := &recordingCompiler{err: &ctxpkg.MultiReferenceError{Code: "REFERENCE_SOMETHING_NEW", Message: "?"}}
	h := NewContextHandler(compiler, 8000)

	w := ctxHandlerGet(t, ctxHandlerRouter(h), "/context/"+uuid.New().String())
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	if code := mrErrorCode(t, w.Body.Bytes()); code != "CONTEXT_COMPILE_ERROR" {
		t.Errorf("error code = %q, want CONTEXT_COMPILE_ERROR", code)
	}
}

func TestContextHandler_SelectionLoadErrorsMapToCatalog(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{
			name:   "stale selection",
			err:    service.NewReferenceAPIError(service.ErrReferenceSelectionStale, "the selected messages changed"),
			status: http.StatusConflict,
			code:   "REFERENCE_SELECTION_STALE",
		},
		{
			name:   "database unavailable",
			err:    service.ErrDatabaseUnavailable,
			status: http.StatusServiceUnavailable,
			code:   "SERVICE_UNAVAILABLE",
		},
		{
			name:   "corrupt reserved manifest",
			err:    errors.New("service: multi-reference node has no readable context manifest"),
			status: http.StatusInternalServerError,
			code:   "CONTEXT_COMPILE_ERROR",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loader := &stubSelectionLoader{err: tc.err}
			compiler := &recordingCompiler{}
			h := NewContextHandler(compiler, 8000).WithReferenceSelectionLoader(loader)

			w := ctxHandlerGet(t, ctxHandlerRouter(h), "/context/"+uuid.New().String())
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if code := mrErrorCode(t, w.Body.Bytes()); code != tc.code {
				t.Errorf("error code = %q, want %q", code, tc.code)
			}
			// A selection that cannot be loaded whole never reaches the
			// compiler: no partial source set, never a block plus an error.
			if compiler.calls != 0 {
				t.Errorf("compiler ran %d times for an unloadable selection", compiler.calls)
			}
		})
	}
}
