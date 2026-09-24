package server

import (
	"encoding/json"
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

func TestProductionRouterUnknownRoutesUseJSONNotFoundEnvelope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".hermes", "canopy", "gateway"), 0o755); err != nil {
		t.Fatal(err)
	}

	const secret = "route-not-found-test-secret"
	router := newRouter(&routeDeps{
		jwtSecret: secret,
		connMgr:   transport.NewConnectionManager(nil),
		cfg:       &config.Config{},
	})
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": uuid.New().String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	rawToken, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/api/v1/cards/" + uuid.New().String() + "/nosuchroute",
		"/api/v1/trees/" + uuid.New().String() + "/topics/nosuchroute/unknown",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+rawToken)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			if strings.Contains(rec.Body.String(), "404 page not found") {
				t.Fatalf("response contains raw not-found text: %s", rec.Body.String())
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error envelope: %v; body=%s", err, rec.Body.String())
			}
			if body.Error.Code != "ROUTE_NOT_FOUND" {
				t.Fatalf("error code = %q, want ROUTE_NOT_FOUND", body.Error.Code)
			}
			if body.Error.Message == "" {
				t.Fatal("error message is empty")
			}
		})
	}

	// Auth remains ahead of routing for an unknown API path.
	unauthenticated := httptest.NewRequest(http.MethodGet, "/api/v1/cards/"+uuid.New().String()+"/nosuchroute", nil)
	unauthenticatedRec := httptest.NewRecorder()
	router.ServeHTTP(unauthenticatedRec, unauthenticated)
	if unauthenticatedRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated unknown API route = status %d, want 401; body=%s", unauthenticatedRec.Code, unauthenticatedRec.Body.String())
	}

	// The public surface also uses the canonical envelope, without auth.
	req := httptest.NewRequest(http.MethodGet, "/not-a-real-public-route", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("public unknown route = status %d, Content-Type %q; want 404 application/json", rec.Code, rec.Header().Get("Content-Type"))
	}
}
