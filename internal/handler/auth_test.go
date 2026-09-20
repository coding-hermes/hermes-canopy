package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

func TestAuthMiddlewareRejectsMissingToken(t *testing.T) {
	h := AuthMiddleware("secret")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler called without authentication")
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/approvals/pending", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddlewareRejectsWrongSigningMethod(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"sub": uuid.New().String()})
	raw, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}

	h := AuthMiddleware("secret")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler called with unsigned token")
	}))
	req := httptest.NewRequest(http.MethodGet, "/approvals/pending", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddlewareStoresUserIDFromSubject(t *testing.T) {
	want := uuid.New()
	raw := signedToken(t, "secret", jwt.MapClaims{
		"sub": want.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	var got uuid.UUID
	h := AuthMiddleware("secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = UserIDFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/approvals/pending", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusNoContent, rr.Body.String())
	}
	if got != want {
		t.Fatalf("UserIDFromContext() = %s, want %s", got, want)
	}
}

func TestAuthMiddlewareAllowsPublicEndpoints(t *testing.T) {
	for _, path := range []string{"/health", "/healthz", "/version"} {
		t.Run(path, func(t *testing.T) {
			h := AuthMiddleware("secret")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
			if rr.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
			}
		})
	}
}

func TestUserIDFromContextWithoutIdentity(t *testing.T) {
	if got := UserIDFromContext(context.Background()); got != uuid.Nil {
		t.Fatalf("UserIDFromContext() = %s, want nil UUID", got)
	}
}

func TestAuthMiddlewareDevSecretProvisionsBeforeHandler(t *testing.T) {
	userID := uuid.New()
	calls := 0
	h := authMiddleware(db.DevJWTSecretDefault,
		func(context.Context, uuid.UUID) error { return nil },
		func(_ context.Context, got uuid.UUID) error {
			calls++
			if got != userID {
				t.Fatalf("provisioned user %s, want %s", got, userID)
			}
			return nil
		})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/collab/", nil)
		req.Header.Set("Authorization", "Bearer "+signedToken(t, db.DevJWTSecretDefault, jwt.MapClaims{
			"sub": userID.String(),
			"exp": time.Now().Add(time.Hour).Unix(),
		}))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
		}
	}
	if calls != 2 {
		t.Fatalf("provision calls = %d, want 2 (one idempotent ensure per request)", calls)
	}
}

func TestAuthMiddlewareProductionSecretRejectsUnknownUser(t *testing.T) {
	userID := uuid.New()
	h := authMiddleware("production-secret",
		func(context.Context, uuid.UUID) error { return db.ErrNotFound }, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler called for an unknown production user")
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/collab/", nil)
	req.Header.Set("Authorization", "Bearer "+signedToken(t, "production-secret", jwt.MapClaims{
		"sub": userID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusForbidden, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "USER_NOT_PROVISIONED") || !strings.Contains(rr.Body.String(), "ask the operator") {
		t.Fatalf("response = %s, want actionable USER_NOT_PROVISIONED error", rr.Body.String())
	}
}

func TestAuthMiddlewareDevProvisioningFailureIsVisible(t *testing.T) {
	h := authMiddleware(db.DevJWTSecretDefault,
		func(context.Context, uuid.UUID) error { return nil },
		func(context.Context, uuid.UUID) error { return errors.New("database unavailable") })(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler called after provisioning failure")
	}))
	userID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/collab/", nil)
	req.Header.Set("Authorization", "Bearer "+signedToken(t, db.DevJWTSecretDefault, jwt.MapClaims{
		"sub": userID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "USER_PROVISIONING_FAILED") {
		t.Fatalf("response = %s, want provisioning failure code", rr.Body.String())
	}
}

func signedToken(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	raw, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
