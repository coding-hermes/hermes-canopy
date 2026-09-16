package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

// TestMCPAdvertisedPathServesTheHandshakeBehindAuth probes the REAL production
// router (the same newRouter seam server.New uses) for the ADVERTISED MCP
// endpoint: POST /api/v1/mcp must complete an MCP handshake, work with a
// trailing slash, and — because the mount lives inside the authenticated
// /api/v1 group — require the same JWT as every other REST route
// (DF-HERMES-CANOPY-10). DB-free: services are nil and none of initialize /
// notifications / ping / tools/list dereference them.
func TestMCPAdvertisedPathServesTheHandshakeBehindAuth(t *testing.T) {
	// Hermetic gateway wiring: DefaultStateFile() derives from $HOME (see
	// TestRouteParityDocumentedNodeRoutes).
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".hermes", "canopy", "gateway"), 0o755); err != nil {
		t.Fatal(err)
	}

	const secret = "mcp-route-test-secret"
	router := newRouter(&routeDeps{
		jwtSecret: secret,
		connMgr:   transport.NewConnectionManager(nil),
		cfg:       &config.Config{},
	})

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": uuid.NewString(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign test token: %v", err)
	}

	post := func(path, auth, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"probe","version":"1"}}}`

	t.Run("no token is refused like every other REST route", func(t *testing.T) {
		rr := post("/api/v1/mcp", "", initBody)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (body=%s)", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "TOKEN_MISSING") {
			t.Errorf("body is not the TOKEN_MISSING envelope: %s", rr.Body.String())
		}
	})

	t.Run("initialize succeeds with the JWT", func(t *testing.T) {
		rr := post("/api/v1/mcp", token, initBody)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
		}
		for _, want := range []string{`"protocolVersion":"2025-06-18"`, `"name":"canopyd-canopy"`, `"tools":{"listChanged":false}`} {
			if !strings.Contains(rr.Body.String(), want) {
				t.Errorf("initialize response missing %s: %s", want, rr.Body.String())
			}
		}
	})

	t.Run("trailing slash is the same endpoint", func(t *testing.T) {
		rr := post("/api/v1/mcp/", token, initBody)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `"name":"canopyd-canopy"`) {
			t.Errorf("trailing-slash response is not the MCP result: %s", rr.Body.String())
		}
	})

	t.Run("notifications/initialized is an empty 202", func(t *testing.T) {
		rr := post("/api/v1/mcp", token, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
		if rr.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202 (body=%q)", rr.Code, rr.Body.String())
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("body = %q, want empty", rr.Body.String())
		}
	})

	t.Run("tools/list works without a preceding initialize", func(t *testing.T) {
		rr := post("/api/v1/mcp", token, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `"list_trees"`) {
			t.Errorf("tools/list did not advertise the tools: %s", rr.Body.String())
		}
	})

	t.Run("GET is not routed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET status = %d, want 405", rr.Code)
		}
	})
}
