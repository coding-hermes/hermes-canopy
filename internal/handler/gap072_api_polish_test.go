// Package handler — GAP-072 API polish tests (items 1 and 3), sourced from a
// dogfood session (2026-09-14):
//
//  1. POST /trees/{tree_id}/nodes with a PRESENT-BUT-EMPTY parent_id must 400
//     naming parent_id instead of silently creating a root node; an OMITTED
//     parent_id still creates a root node (201, unchanged).
//  3. POST /trees/{tree_id}/nodes/{node_id}/fork with an empty body (or a body
//     that omits content) must 400 naming content, not "request body must be
//     valid JSON"; malformed JSON is still INVALID_BODY.
//
// These tests need no database: the node service is a recording stub, so the
// assertions are about the handler's request contract, not persistence.
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

// gap072NodeService is a recording NodeService stub. It embeds the interface
// so every unused method stays unimplemented, and captures the inputs the
// handler hands to Create/Fork.
type gap072NodeService struct {
	service.NodeService

	created []service.CreateNodeInput
	forked  []service.ForkInput
}

func (s *gap072NodeService) Create(_ context.Context, treeID uuid.UUID, in service.CreateNodeInput) (*service.CreateNodeResult, error) {
	s.created = append(s.created, in)
	return gap072Result(treeID, in.Content), nil
}

func (s *gap072NodeService) Fork(_ context.Context, parentID uuid.UUID, in service.ForkInput) (*service.CreateNodeResult, error) {
	s.forked = append(s.forked, in)
	return gap072Result(uuid.New(), in.Content), nil
}

// gap072Result builds the node+edge envelope the real service returns.
func gap072Result(treeID uuid.UUID, content string) *service.CreateNodeResult {
	nodeID := uuid.New()
	return &service.CreateNodeResult{
		Node: &service.NodeDetail{
			ID:            nodeID,
			TreeID:        treeID,
			Content:       content,
			ContentFormat: "markdown",
			NodeType:      "message",
			SequenceNum:   1,
		},
		Edge: &service.EdgeDetail{
			ID:           uuid.New(),
			TreeID:       treeID,
			TargetNodeID: nodeID,
			EdgeType:     "reply",
		},
	}
}

// gap072Router mounts the tree-scoped node routes the way the production
// server does (server.go: r.Mount("/trees/{tree_id}/nodes", treeNodes) with
// treeNodes.Mount("/", nodeHandler.TreeRoutes())), so chi URL params
// (tree_id, node_id) resolve exactly as in production.
func gap072Router(h *NodeHandler) chi.Router {
	outer := chi.NewRouter()
	inner := chi.NewRouter()
	inner.Mount("/", h.TreeRoutes())
	outer.Mount("/trees/{tree_id}/nodes", inner)
	return outer
}

// gap072Post drives one POST through the router with an authenticated context
// (the handlers read the user id from the request context).
func gap072Post(t *testing.T, r chi.Router, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), userIDContextKey{}, uuid.New()))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// gap072Error decodes the {error:{code,message}} envelope.
func gap072Error(t *testing.T, rr *httptest.ResponseRecorder) apiError {
	t.Helper()
	var env apiErrorBody
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v (body=%s)", err, rr.Body.String())
	}
	return env.Error
}

func gap072CreatePath(treeID uuid.UUID) string {
	return "/trees/" + treeID.String() + "/nodes/"
}

// --- Item 1: parent_id present-but-empty -----------------------------------

// TestGAP072_CreateNode_EmptyParentIDRejected proves a PRESENT-BUT-EMPTY
// parent_id is a 400 that names the field, on both casings, and that no node
// is created (the pre-fix behavior silently created a ROOT node).
func TestGAP072_CreateNode_EmptyParentIDRejected(t *testing.T) {
	treeID := uuid.New()
	cases := map[string]string{
		"snake_case with content":    `{"content":"hello","parent_id":""}`,
		"snake_case only":            `{"parent_id":""}`,
		"camelCase alias":            `{"content":"hello","parentId":""}`,
		"whitespace only":            `{"content":"hello","parent_id":"   "}`,
		"empty string + extra field": `{"content":"hello","parent_id":"","edge_type":"reply"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &gap072NodeService{}
			rr := gap072Post(t, gap072Router(NewNodeHandler(svc, nil)), gap072CreatePath(treeID), body)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
			}
			e := gap072Error(t, rr)
			if e.Code != "INVALID_PARENT_ID" {
				t.Fatalf("error code = %q, want INVALID_PARENT_ID", e.Code)
			}
			if !strings.Contains(e.Message, "parent_id") {
				t.Fatalf("error message %q does not name parent_id", e.Message)
			}
			if len(svc.created) != 0 {
				t.Fatalf("service Create was called %d time(s) for an empty parent_id; want 0 (no node must be created)", len(svc.created))
			}
		})
	}
}

// TestGAP072_CreateNode_OmittedParentIDCreatesRoot is the no-regression half:
// an ABSENT parent_id (key omitted or explicitly null) still creates a root
// node with a zero ParentID (201).
func TestGAP072_CreateNode_OmittedParentIDCreatesRoot(t *testing.T) {
	treeID := uuid.New()
	cases := map[string]string{
		"key omitted":   `{"content":"root node"}`,
		"explicit null": `{"content":"root node","parent_id":null}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &gap072NodeService{}
			rr := gap072Post(t, gap072Router(NewNodeHandler(svc, nil)), gap072CreatePath(treeID), body)

			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
			}
			if len(svc.created) != 1 {
				t.Fatalf("service Create calls = %d, want 1", len(svc.created))
			}
			if svc.created[0].ParentID != uuid.Nil {
				t.Fatalf("CreateNodeInput.ParentID = %s, want uuid.Nil (root node)", svc.created[0].ParentID)
			}
			if svc.created[0].Content != "root node" {
				t.Fatalf("CreateNodeInput.Content = %q, want %q", svc.created[0].Content, "root node")
			}
			// Node create answers with the {node,edge} envelope quoted by
			// docs/API.md — assert it so an envelope change cannot slip by.
			var env map[string]json.RawMessage
			if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode create response: %v", err)
			}
			if _, ok := env["node"]; !ok {
				t.Fatalf("create response has no \"node\" key: %s", rr.Body.String())
			}
			if _, ok := env["edge"]; !ok {
				t.Fatalf("create response has no \"edge\" key: %s", rr.Body.String())
			}
		})
	}
}

// TestGAP072_CreateNode_ValidAndInvalidParentID is the no-regression guard for
// the adjacent behaviors: a present, non-empty, non-UUID value still 400s, and
// a present valid UUID still reaches the service as that UUID.
func TestGAP072_CreateNode_ValidAndInvalidParentID(t *testing.T) {
	treeID := uuid.New()

	t.Run("invalid uuid rejected", func(t *testing.T) {
		svc := &gap072NodeService{}
		rr := gap072Post(t, gap072Router(NewNodeHandler(svc, nil)), gap072CreatePath(treeID),
			`{"content":"hello","parent_id":"not-a-uuid"}`)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
		}
		e := gap072Error(t, rr)
		if e.Code != "INVALID_PARENT_ID" || !strings.Contains(e.Message, "parent_id") {
			t.Fatalf("error = %+v, want INVALID_PARENT_ID naming parent_id", e)
		}
		if len(svc.created) != 0 {
			t.Fatalf("service Create called for an invalid parent_id")
		}
	})

	t.Run("valid uuid forwarded", func(t *testing.T) {
		parentID := uuid.New()
		svc := &gap072NodeService{}
		rr := gap072Post(t, gap072Router(NewNodeHandler(svc, nil)), gap072CreatePath(treeID),
			`{"content":"child","parent_id":"`+parentID.String()+`"}`)
		if rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
		}
		if len(svc.created) != 1 || svc.created[0].ParentID != parentID {
			t.Fatalf("CreateNodeInput.ParentID = %+v, want %s", svc.created, parentID)
		}
	})
}

// --- Item 3: fork error naming the missing field ---------------------------

// TestGAP072_Fork_MissingContentNamesField proves the empty-body and
// content-omitted fork cases report the missing field rather than the
// misleading "request body must be valid JSON".
func TestGAP072_Fork_MissingContentNamesField(t *testing.T) {
	treeID, nodeID := uuid.New(), uuid.New()
	forkPath := "/trees/" + treeID.String() + "/nodes/" + nodeID.String() + "/fork"
	cases := map[string]string{
		"empty body":       ``,
		"whitespace body":  `   `,
		"empty object":     `{}`,
		"content omitted":  `{"content_format":"plain"}`,
		"camelCase only":   `{"contentFormat":"plain"}`,
		"content empty":    `{"content":""}`,
		"node_type only":   `{"node_type":"message"}`,
		"metadata only":    `{"metadata":{}}`,
		"null content":     `{"content":null}`,
		"empty with extra": `{"metadata":{},"content":""}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &gap072NodeService{}
			rr := gap072Post(t, gap072Router(NewNodeHandler(svc, nil)), forkPath, body)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
			}
			e := gap072Error(t, rr)
			if e.Code != "EMPTY_CONTENT" {
				t.Fatalf("error code = %q, want EMPTY_CONTENT (body=%s)", e.Code, rr.Body.String())
			}
			if !strings.Contains(e.Message, "content") {
				t.Fatalf("error message %q does not name content", e.Message)
			}
			if strings.Contains(e.Message, "must be valid JSON") {
				t.Fatalf("error message %q is still the generic JSON message", e.Message)
			}
			if len(svc.forked) != 0 {
				t.Fatalf("service Fork called %d time(s) without content", len(svc.forked))
			}
		})
	}
}

// TestGAP072_Fork_MalformedJSONStaysInvalidBody is the no-regression half of
// item 3: only a body with no JSON at all (or a missing content) becomes
// EMPTY_CONTENT; malformed JSON and unknown fields keep INVALID_BODY.
func TestGAP072_Fork_MalformedJSONStaysInvalidBody(t *testing.T) {
	treeID, nodeID := uuid.New(), uuid.New()
	forkPath := "/trees/" + treeID.String() + "/nodes/" + nodeID.String() + "/fork"
	cases := map[string]struct {
		body  string
		parts []string
	}{
		"truncated json": {`{"content":`, []string{"request body must be valid JSON"}},
		"garbage":        {`not json at all`, []string{"request body must be valid JSON"}},
		"unknown field":  {`{"content":"hi","bogus":true}`, []string{"unknown field", "bogus"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &gap072NodeService{}
			rr := gap072Post(t, gap072Router(NewNodeHandler(svc, nil)), forkPath, tc.body)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
			}
			e := gap072Error(t, rr)
			if e.Code != "INVALID_BODY" {
				t.Fatalf("error code = %q, want INVALID_BODY", e.Code)
			}
			for _, part := range tc.parts {
				if !strings.Contains(e.Message, part) {
					t.Fatalf("message %q does not contain %q", e.Message, part)
				}
			}
		})
	}
}

// TestGAP072_Fork_ValidContentStillForks proves the new required-content check
// does not block a well-formed fork.
func TestGAP072_Fork_ValidContentStillForks(t *testing.T) {
	treeID, nodeID := uuid.New(), uuid.New()
	forkPath := "/trees/" + treeID.String() + "/nodes/" + nodeID.String() + "/fork"

	svc := &gap072NodeService{}
	rr := gap072Post(t, gap072Router(NewNodeHandler(svc, nil)), forkPath, `{"content":"branched"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if len(svc.forked) != 1 || svc.forked[0].Content != "branched" {
		t.Fatalf("ForkInput = %+v, want content %q", svc.forked, "branched")
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode fork response: %v", err)
	}
	if _, ok := env["node"]; !ok {
		t.Fatalf("fork response has no \"node\" key: %s", rr.Body.String())
	}
	if _, ok := env["edge"]; !ok {
		t.Fatalf("fork response has no \"edge\" key: %s", rr.Body.String())
	}
}
