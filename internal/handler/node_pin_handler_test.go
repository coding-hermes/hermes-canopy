// GAP-080 phase 1 — the PATCH node body carries an optional strict boolean
// `pinned`, and it reaches the service on BOTH node update routes (tree-scoped
// /trees/{tree_id}/nodes/{node_id} and flat /nodes/{node_id}).
//
// No database: the node service is a recording stub, so the assertions are
// about the handler's request contract.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// gap080NodeService records the UpdateNodeInput the handler hands to the
// service. It embeds the interface so unused methods stay unimplemented.
type gap080NodeService struct {
	service.NodeService

	updates []service.UpdateNodeInput
}

func (s *gap080NodeService) Update(_ context.Context, nodeID uuid.UUID, in service.UpdateNodeInput) (*service.NodeDetail, error) {
	s.updates = append(s.updates, in)
	return &service.NodeDetail{
		ID:            nodeID,
		TreeID:        uuid.New(),
		Content:       "updated",
		ContentFormat: "markdown",
		NodeType:      "message",
	}, nil
}

// gap080FlatRouter mounts the flat node surface exactly as server.go does
// (r.Mount("/nodes", nodeHandler.FlatRoutes())).
func gap080FlatRouter(h *NodeHandler) chi.Router {
	outer := chi.NewRouter()
	outer.Mount("/nodes", h.FlatRoutes())
	return outer
}

func gap080Patch(t *testing.T, r chi.Router, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, uuid.New()))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// TestGAP080P1_HandlerPinnedPassThrough proves the body field reaches the
// service on both routes, and that an absent key stays nil.
func TestGAP080P1_HandlerPinnedPassThrough(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantSet  bool
		wantBool bool
	}{
		{"pin true", `{"pinned":true}`, true, true},
		{"pin false", `{"pinned":false}`, true, false},
		{"pin with content", `{"content":"hi","pinned":true}`, true, true},
		{"absent key leaves nil", `{"content":"hi"}`, false, false},
		{"metadata only leaves nil", `{"metadata":{"a":1}}`, false, false},
		// JSON null means "unset" — a nil pointer, so the pin is left alone
		// (never a silent unpin); other non-boolean types are rejected below.
		{"explicit null leaves nil", `{"pinned":null}`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			treeID, nodeID := uuid.New(), uuid.New()
			svc := &gap080NodeService{}
			h := NewNodeHandler(svc, nil)

			// Tree-scoped route.
			rr := gap080Patch(t, gap072Router(h),
				"/trees/"+treeID.String()+"/nodes/"+nodeID.String(), tc.body)
			if rr.Code != http.StatusOK {
				t.Fatalf("tree route status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
			}
			// Flat route.
			rrFlat := gap080Patch(t, gap080FlatRouter(h), "/nodes/"+nodeID.String(), tc.body)
			if rrFlat.Code != http.StatusOK {
				t.Fatalf("flat route status = %d, want 200 (body=%s)", rrFlat.Code, rrFlat.Body.String())
			}
			if len(svc.updates) != 2 {
				t.Fatalf("service Update calls = %d, want 2 (tree + flat)", len(svc.updates))
			}
			for i, in := range svc.updates {
				if tc.wantSet {
					if in.Pinned == nil {
						t.Fatalf("call %d: Pinned = nil, want %v", i, tc.wantBool)
					}
					if *in.Pinned != tc.wantBool {
						t.Errorf("call %d: *Pinned = %v, want %v", i, *in.Pinned, tc.wantBool)
					}
				} else if in.Pinned != nil {
					t.Errorf("call %d: Pinned = %v, want nil (key absent)", i, *in.Pinned)
				}
			}
		})
	}
}

// TestGAP080P1_HandlerPinnedRejectsNonBoolean proves a non-boolean value is a
// 400 INVALID_BODY — never a silent zero that would unpin a message.
func TestGAP080P1_HandlerPinnedRejectsNonBoolean(t *testing.T) {
	for _, body := range []string{
		`{"pinned":"yes"}`,
		`{"pinned":1}`,
		`{"pinned":{}}`,
		`{"pinned":[]}`,
	} {
		t.Run(body, func(t *testing.T) {
			treeID, nodeID := uuid.New(), uuid.New()
			svc := &gap080NodeService{}
			h := NewNodeHandler(svc, nil)

			rr := gap080Patch(t, gap072Router(h),
				"/trees/"+treeID.String()+"/nodes/"+nodeID.String(), body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rr.Code, rr.Body.String())
			}
			var env apiErrorBody
			if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			if env.Error.Code != "INVALID_BODY" {
				t.Errorf("error code = %q, want INVALID_BODY", env.Error.Code)
			}
			if !strings.Contains(env.Error.Message, "pinned") {
				t.Errorf("error message = %q, want it to name the pinned field", env.Error.Message)
			}
			if len(svc.updates) != 0 {
				t.Errorf("service Update was called %d times for a rejected body", len(svc.updates))
			}
		})
	}
}

// TestGAP080P1_HandlerPinnedIsNotAWholesaleMetadataReplace proves the handler
// passes an explicit metadata field AND the pin through together, so the
// service can apply them in the documented order.
func TestGAP080P1_HandlerPinnedWithMetadata(t *testing.T) {
	treeID, nodeID := uuid.New(), uuid.New()
	svc := &gap080NodeService{}
	h := NewNodeHandler(svc, nil)

	rr := gap080Patch(t, gap072Router(h),
		"/trees/"+treeID.String()+"/nodes/"+nodeID.String(),
		`{"metadata":{"multi_reference":{"v":1}},"pinned":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	if len(svc.updates) != 1 {
		t.Fatalf("Update calls = %d, want 1", len(svc.updates))
	}
	in := svc.updates[0]
	if in.Metadata == nil || !strings.Contains(string(*in.Metadata), "multi_reference") {
		t.Errorf("Metadata = %v, want the body's metadata object", in.Metadata)
	}
	if in.Pinned == nil || !*in.Pinned {
		t.Errorf("Pinned = %v, want true", in.Pinned)
	}
}
