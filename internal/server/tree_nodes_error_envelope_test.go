// DF-HERMES-CANOPY-95: the tree-scoped /api/v1/trees/{tree_id}/nodes mount
// answered mux-level 405s with chi's bare default — an EMPTY body with no
// Content-Type — while the flat /api/v1/nodes mount was already enveloped
// (DF-HERMES-CANOPY-92) and docs/API.md promises every error body as
// {"error":{"code","message"}}.
//
// This file sweeps the tree-scoped mount's error ladder on the REAL
// production router (df84 rig — stub services, database-free auth, the same
// harness as flat_nodes_error_envelope_test.go):
//
//   - wrong method on valid tree-scoped node paths → 405, Content-Type
//     application/json, parseable METHOD_NOT_ALLOWED envelope, Allow header
//     listing the tree-scoped surface's methods (GET, POST, PATCH, DELETE —
//     wider than the flat mount's because TreeRoutes also serves GET);
//   - unknown subpaths under /trees/{tree}/nodes/{uuid}/... → 404
//     ROUTE_NOT_FOUND (unchanged by this fix);
//   - malformed tree UUID → 400 INVALID_TREE_ID from
//     TreeMembershipMiddleware, which runs inside the subrouter before
//     chi's method dispatch;
//   - malformed node UUID → 400 INVALID_NODE_ID from parseNodeID.
//
// ch:trace row=DF-HERMES-CANOPY-95 spec=docs/API.md#response-envelopes-per-route wave=hermes-canopy-foreman-2026-10-21-06-04-00#DF-HERMES-CANOPY-95 test=internal/server/tree_nodes_error_envelope_test.go::TestTreeNodeErrorLadderIsEnvelopedOnProductionRouter doc=docs/API.md prompt=/tmp/hermes-canopy-foreman-2026-10-21-06-04-00-DF-HERMES-CANOPY-95-brief.txt evidence=internal/server/tree_nodes_error_envelope_test.go witness=none:in-process-httptest-no-persistent-live-surface verdict=none:judge-verdict-lives-on-board-row-DF-HERMES-CANOPY-95 commit=f16fa535e85718ad9c5b9dc74715a36a0bf5a6b5 memory=none:duckbrain-write-not-verified-this-tick

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// TestTreeNodeErrorLadderIsEnvelopedOnProductionRouter sweeps the
// tree-scoped mount's error ladder over live HTTP against the real router
// and asserts every error answers the documented JSON envelope
// (docs/API.md) — no bare bodies, no missing Content-Type.
func TestTreeNodeErrorLadderIsEnvelopedOnProductionRouter(t *testing.T) {
	members := &df84MemberStub{roles: map[df84MembershipKey]string{
		{df84TreeID, df92Member}: db.TreeRoleMember,
	}}
	srv := httptest.NewServer(df84Router(t, &df84NodeStub{}, members))
	defer srv.Close()

	treeRoot := "/api/v1/trees/" + df84TreeID.String() + "/nodes"
	validNode := treeRoot + "/" + df84NodeID.String()
	// The tree-scoped surface's full method set (TreeRoutes): GET/POST on
	// the collection, GET/PATCH/DELETE on /{node_id}, POST on
	// /{node_id}/reply and /{node_id}/fork.
	treeAllow := "GET, POST, PATCH, DELETE"

	probes := []struct {
		name      string
		method    string
		path      string
		wantCode  string
		wantAllow string // "" → Allow header not asserted
	}{
		{
			name:      "PUT on a valid tree-scoped node path is 405 METHOD_NOT_ALLOWED",
			method:    http.MethodPut,
			path:      validNode,
			wantCode:  "METHOD_NOT_ALLOWED",
			wantAllow: treeAllow,
		},
		{
			name:      "DELETE on the collection root is 405 METHOD_NOT_ALLOWED",
			method:    http.MethodDelete,
			path:      treeRoot,
			wantCode:  "METHOD_NOT_ALLOWED",
			wantAllow: treeAllow,
		},
		{
			name:      "PUT on the collection root is 405 METHOD_NOT_ALLOWED",
			method:    http.MethodPut,
			path:      treeRoot,
			wantCode:  "METHOD_NOT_ALLOWED",
			wantAllow: treeAllow,
		},
		{
			name:      "DELETE on a reply subpath is 405 METHOD_NOT_ALLOWED",
			method:    http.MethodDelete,
			path:      validNode + "/reply",
			wantCode:  "METHOD_NOT_ALLOWED",
			wantAllow: treeAllow,
		},
		{
			name:      "GET on a fork subpath is 405 METHOD_NOT_ALLOWED",
			method:    http.MethodGet,
			path:      validNode + "/fork",
			wantCode:  "METHOD_NOT_ALLOWED",
			wantAllow: treeAllow,
		},
		{
			name:     "GET on an unknown subpath stays 404 ROUTE_NOT_FOUND",
			method:   http.MethodGet,
			path:     validNode + "/nope",
			wantCode: "ROUTE_NOT_FOUND",
		},
		{
			name:     "PATCH with a malformed tree uuid is 400 INVALID_TREE_ID",
			method:   http.MethodPatch,
			path:     "/api/v1/trees/not-a-uuid/nodes/" + df84NodeID.String(),
			wantCode: "INVALID_TREE_ID",
		},
		{
			name:     "GET with a malformed tree uuid is 400 INVALID_TREE_ID",
			method:   http.MethodGet,
			path:     "/api/v1/trees/not-a-uuid/nodes",
			wantCode: "INVALID_TREE_ID",
		},
		{
			name:     "GET with a malformed node uuid is 400 INVALID_NODE_ID",
			method:   http.MethodGet,
			path:     treeRoot + "/not-a-uuid",
			wantCode: "INVALID_NODE_ID",
		},
		{
			name:     "DELETE with a malformed node uuid is 400 INVALID_NODE_ID",
			method:   http.MethodDelete,
			path:     treeRoot + "/not-a-uuid",
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
