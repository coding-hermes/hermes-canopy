// DF-HERMES-CANOPY-92: unit contract for FlatNodeMethodNotAllowed and its
// wiring on NodeHandler.FlatRoutes — the same wiring production uses in
// internal/server/server.go. The wrapper must answer method mismatches with
// the documented error envelope (docs/API.md) plus the RFC 9110 §15.5.5
// Allow header, while every other flat error shape (malformed id → 400
// INVALID_NODE_ID from parseNodeID, unknown subpath → 404 ROUTE_NOT_FOUND)
// stays intact.
//
// This handler-level router has no auth/membership middleware (those live on
// the production mounts); the full middleware-first ladder — including the
// NodeAccessMiddleware 400 INVALID_NODE_ID that preempts a 405 on a
// malformed uuid — is proven against the real router in
// internal/server/flat_nodes_error_envelope_test.go.
package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/service"
)

// flat92TreeID is the tree the stub node lives in (value opaque to the
// wrapper contract; GetByID never touches storage here).
var flat92TreeID = uuid.MustParse("df920000-0000-0000-0000-0000000000a1")

// flat92NodeService embeds the interface so every unused method stays
// unimplemented; GetByID resolves any id so NodeAccess-style handlers (and
// handleGetByID, were it mounted) always find the node.
type flat92NodeService struct {
	service.NodeService
}

func (s *flat92NodeService) GetByID(_ context.Context, id uuid.UUID) (*service.NodeDetail, error) {
	return &service.NodeDetail{ID: id, TreeID: flat92TreeID}, nil
}

// flat92Router mirrors production wiring: FlatRoutes mounted at
// /api/v1/nodes with the enveloped 405/404 handlers attached exactly as the
// server mounts them (internal/server/server.go).
func flat92Router() http.Handler {
	node := NewNodeHandler(&flat92NodeService{}, nil)
	sub := chi.NewRouter()
	sub.MethodNotAllowed(FlatNodeMethodNotAllowed)
	// Production inherits the root router's enveloped NotFound via chi's
	// Mount-time propagation; pin the same behaviour explicitly here.
	sub.NotFound(NotFoundHandler)
	sub.Mount("/", node.FlatRoutes())
	r := chi.NewRouter()
	r.Mount("/api/v1/nodes", sub)
	return r
}

// flat92ErrorShape mirrors the documented error envelope
// ({"error":{"code","message"}}) without importing other test helpers.
type flat92ErrorShape struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// TestFlatRoutesMethodNotAllowedEnvelope sweeps wrong-method probes of the
// flat surface and asserts the 405 identity: envelope body, JSON content
// type, non-empty body, and the Allow header listing every method the flat
// mount serves.
func TestFlatRoutesMethodNotAllowedEnvelope(t *testing.T) {
	srv := httptest.NewServer(flat92Router())
	defer srv.Close()

	validID := uuid.MustParse("df920000-0000-0000-0000-0000000000c1")
	nodePath := "/" + validID.String()

	probes := []struct {
		name   string
		method string
		path   string
	}{
		{"GET bare node path", http.MethodGet, nodePath},
		{"PUT bare node path", http.MethodPut, nodePath},
		{"GET reply subpath", http.MethodGet, nodePath + "/reply"},
		{"GET fork subpath", http.MethodGet, nodePath + "/fork"},
		{"PATCH reply subpath", http.MethodPatch, nodePath + "/reply"},
	}

	client := srv.Client()
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			req, err := http.NewRequest(probe.method, srv.URL+"/api/v1/nodes"+probe.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}

			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", resp.StatusCode)
			}
			if got := resp.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if len(body) == 0 {
				t.Fatalf("body is EMPTY — chi's bare 405 leaked through the wrapper")
			}
			var shape flat92ErrorShape
			if err := json.Unmarshal(body, &shape); err != nil {
				t.Fatalf("body does not parse as the error envelope: %v (body %q)", err, string(body))
			}
			if shape.Error.Code != "METHOD_NOT_ALLOWED" {
				t.Errorf("error.code = %q, want METHOD_NOT_ALLOWED", shape.Error.Code)
			}
			if shape.Error.Message == "" {
				t.Errorf("error.message is empty")
			}
			// The flat surface serves PATCH and DELETE on /{node_id} and
			// POST on /{node_id}/reply and /{node_id}/fork — the Allow
			// header must name exactly that set.
			if got, want := resp.Header.Get("Allow"), "PATCH, DELETE, POST"; got != want {
				t.Errorf("Allow = %q, want %q", got, want)
			}
		})
	}
}

// TestFlatRoutesOtherErrorShapesUnchangedByDF92 pins the neighbouring flat
// error shapes so the 405 wrapper cannot disturb them.
func TestFlatRoutesOtherErrorShapesUnchangedByDF92(t *testing.T) {
	srv := httptest.NewServer(flat92Router())
	defer srv.Close()

	client := srv.Client()

	t.Run("malformed uuid on PATCH stays 400 INVALID_NODE_ID", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPatch, srv.URL+"/api/v1/nodes/not-a-uuid", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		var shape flat92ErrorShape
		if err := json.NewDecoder(resp.Body).Decode(&shape); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if shape.Error.Code != "INVALID_NODE_ID" {
			t.Errorf("error.code = %q, want INVALID_NODE_ID", shape.Error.Code)
		}
	})

	t.Run("unknown subpath stays 404 ROUTE_NOT_FOUND", func(t *testing.T) {
		validID := uuid.MustParse("df920000-0000-0000-0000-0000000000d1")
		resp, err := client.Post(srv.URL+"/api/v1/nodes/"+validID.String()+"/nope",
			"application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		var shape flat92ErrorShape
		if err := json.NewDecoder(resp.Body).Decode(&shape); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if shape.Error.Code != "ROUTE_NOT_FOUND" {
			t.Errorf("error.code = %q, want ROUTE_NOT_FOUND", shape.Error.Code)
		}
	})
}
