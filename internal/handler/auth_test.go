package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
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

func signedToken(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	raw, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
