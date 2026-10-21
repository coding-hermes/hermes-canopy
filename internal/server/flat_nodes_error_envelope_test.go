// DF-HERMES-CANOPY-92: the flat /api/v1/nodes mount answered mux-level 405s
// with chi's bare default — an EMPTY body with no Content-Type — while
// docs/API.md promises every error body as {"error":{"code","message"}}. The
// DF-86 normalisation (fba87e31) wrapped the not-found path but the 405
// still leaked bare.
//
// This file sweeps the flat mount's error ladder on the REAL production
// router (newRouter seam, stub services, database-free auth — the same rig
// as flat_nodes_viewer_gate_test.go):
//
//   - wrong method on valid node paths → 405, Content-Type application/json,
//     non-empty parseable METHOD_NOT_ALLOWED envelope, Allow header listing
//     the flat surface's methods (PATCH, DELETE, POST);
//   - unknown subpaths under /nodes/{uuid}/... → 404 ROUTE_NOT_FOUND;
//   - malformed node UUID under any method/subpath → 400 INVALID_NODE_ID
//     from NodeAccessMiddleware, which runs before routing resolves.
//
// The tree-scoped mount is probed only as a control arm: its error shapes
// must stay what they were before this fix (AC: tree-scoped routes are
// unaffected). The control asserts status and Allow presence only — pinning
// chi's bare 405 body there would enshrine a leak this row deliberately
// does not touch.
package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// df92Member is a plain member of df84TreeID: enough for
// NodeAccessMiddleware to let a request through to routing (a viewer would
// be stopped earlier with 403 VIEWER_READ_ONLY, which is the DF-84 contract,
// not the error-ladder contract under test here).
var df92Member = uuid.MustParse("df920000-0000-0000-0000-0000000000b1")

// TestFlatNodesErrorLadderIsEnvelopedOnProductionRouter sweeps the flat
// mount's error ladder over live HTTP against the real router and asserts
// every error answers the documented JSON envelope (docs/API.md) — no bare
// bodies, no missing Content-Type.
func TestFlatNodesErrorLadderIsEnvelopedOnProductionRouter(t *testing.T) {
	members := &df84MemberStub{roles: map[df84MembershipKey]string{
		{df84TreeID, df92Member}: db.TreeRoleMember,
	}}
	srv := httptest.NewServer(df84Router(t, &df84NodeStub{}, members))
	defer srv.Close()

	validNode := "/api/v1/nodes/" + df84NodeID.String()

	probes := []struct {
		name      string
		method    string
		path      string
		wantCode  string
		wantAllow string // "" → Allow header not asserted
	}{
		{
			name:      "GET on a valid bare node path is 405 METHOD_NOT_ALLOWED",
			method:    http.MethodGet,
			path:      validNode,
			wantCode:  "METHOD_NOT_ALLOWED",
			wantAllow: "PATCH, DELETE, POST",
		},
		{
			name:      "GET on a valid reply subpath is 405 METHOD_NOT_ALLOWED",
			method:    http.MethodGet,
			path:      validNode + "/reply",
			wantCode:  "METHOD_NOT_ALLOWED",
			wantAllow: "PATCH, DELETE, POST",
		},
		{
			name:      "PUT on a valid bare node path is 405 METHOD_NOT_ALLOWED",
			method:    http.MethodPut,
			path:      validNode,
			wantCode:  "METHOD_NOT_ALLOWED",
			wantAllow: "PATCH, DELETE, POST",
		},
		{
			name:     "POST on an unknown subpath is 404 ROUTE_NOT_FOUND",
			method:   http.MethodPost,
			path:     validNode + "/nope",
			wantCode: "ROUTE_NOT_FOUND",
		},
		{
			name:     "GET on an unknown deep subpath is 404 ROUTE_NOT_FOUND",
			method:   http.MethodGet,
			path:     validNode + "/nope/deeper",
			wantCode: "ROUTE_NOT_FOUND",
		},
		{
			name:     "PATCH with a malformed node uuid is 400 INVALID_NODE_ID",
			method:   http.MethodPatch,
			path:     "/api/v1/nodes/not-a-uuid",
			wantCode: "INVALID_NODE_ID",
		},
		{
			name:     "GET with a malformed node uuid answers the middleware 400 before the 405",
			method:   http.MethodGet,
			path:     "/api/v1/nodes/not-a-uuid",
			wantCode: "INVALID_NODE_ID",
		},
		{
			name:     "GET on a malformed uuid unknown subpath is 400 INVALID_NODE_ID",
			method:   http.MethodGet,
			path:     "/api/v1/nodes/not-a-uuid/nope",
			wantCode: "INVALID_NODE_ID",
		},
	}

	client := srv.Client()
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			req, err := http.NewRequest(probe.method, srv.URL+probe.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+df84Token(t, df92Member))
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}

			if resp.StatusCode != wantStatusFor(probe.wantCode) {
				t.Errorf("status = %d, want %d", resp.StatusCode, wantStatusFor(probe.wantCode))
			}
			if got := resp.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json (bare bodies must not leak)", got)
			}
			if len(body) == 0 {
				t.Fatalf("body is EMPTY — the documented error envelope is missing")
			}
			var shape apiErrorShape
			if err := json.Unmarshal(body, &shape); err != nil {
				t.Fatalf("body does not parse as the error envelope: %v (body %q)", err, string(body))
			}
			if shape.Error.Code != probe.wantCode {
				t.Errorf("error.code = %q, want %q", shape.Error.Code, probe.wantCode)
			}
			if probe.wantAllow != "" {
				if got := resp.Header.Get("Allow"); got != probe.wantAllow {
					t.Errorf("Allow = %q, want %q", got, probe.wantAllow)
				}
			}
		})
	}
}

// wantStatusFor maps the expected error code to its documented status.
func wantStatusFor(code string) int {
	switch code {
	case "METHOD_NOT_ALLOWED":
		return http.StatusMethodNotAllowed
	case "ROUTE_NOT_FOUND":
		return http.StatusNotFound
	case "INVALID_NODE_ID", "INVALID_TREE_ID":
		return http.StatusBadRequest
	default:
		return 0
	}
}

// TestTreeScopedNodeErrorShapesUnchangedByDF92 is the control arm: the
// tree-scoped mount's own error responses must be untouched by the flat
// mount's 405 wrapper. Status and Allow presence are pinned (both stable
// before and after this fix); the bare 405 body is deliberately NOT pinned —
// normalising it is not this row's scope.
func TestTreeScopedNodeErrorShapesUnchangedByDF92(t *testing.T) {
	members := &df84MemberStub{roles: map[df84MembershipKey]string{
		{df84TreeID, df92Member}: db.TreeRoleOwner,
	}}
	srv := httptest.NewServer(df84Router(t, &df84NodeStub{}, members))
	defer srv.Close()

	token := df84Token(t, df92Member)

	do := func(t *testing.T, method, path string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	t.Run("unknown subpath keeps the enveloped ROUTE_NOT_FOUND", func(t *testing.T) {
		resp := do(t, http.MethodPost,
			"/api/v1/trees/"+df84TreeID.String()+"/nodes/"+df84NodeID.String()+"/nope")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
		var shape apiErrorShape
		if err := json.NewDecoder(resp.Body).Decode(&shape); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if shape.Error.Code != "ROUTE_NOT_FOUND" {
			t.Errorf("error.code = %q, want ROUTE_NOT_FOUND", shape.Error.Code)
		}
	})

	t.Run("method mismatch keeps status 405 with an Allow header", func(t *testing.T) {
		resp := do(t, http.MethodPut,
			"/api/v1/trees/"+df84TreeID.String()+"/nodes/"+df84NodeID.String())
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", resp.StatusCode)
		}
		if resp.Header.Get("Allow") == "" {
			t.Errorf("Allow header missing on tree-scoped 405")
		}
	})
}
