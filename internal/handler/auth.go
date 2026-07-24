package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type userIDContextKey struct{}

// AuthMiddleware validates HS256 Bearer tokens and stores the authenticated
// user's UUID in the request context. Health and version routes are public.
func AuthMiddleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublicPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			auth := r.Header.Get("Authorization")
			parts := strings.Fields(auth)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				writeError(w, http.StatusUnauthorized, "TOKEN_MISSING", "Authorization Bearer token required")
				return
			}

			token, err := jwt.Parse(parts[1], func(token *jwt.Token) (any, error) {
				return []byte(secret), nil
			}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
			if err != nil || !token.Valid {
				writeError(w, http.StatusUnauthorized, "TOKEN_INVALID", "invalid or expired token")
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				writeError(w, http.StatusUnauthorized, "TOKEN_INVALID", "invalid token claims")
				return
			}
			subject, err := claims.GetSubject()
			if err != nil || subject == "" {
				if raw, exists := claims["user_id"].(string); exists {
					subject = raw
				}
			}
			userID, err := uuid.Parse(subject)
			if err != nil || userID == uuid.Nil {
				writeError(w, http.StatusUnauthorized, "TOKEN_INVALID", "token subject must be a valid user UUID")
				return
			}

			ctx := context.WithValue(r.Context(), userIDContextKey{}, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserIDFromContext returns the authenticated user UUID or uuid.Nil when the
// request has no authenticated identity.
func UserIDFromContext(ctx context.Context) uuid.UUID {
	userID, _ := ctx.Value(userIDContextKey{}).(uuid.UUID)
	return userID
}

func isPublicPath(path string) bool {
	switch path {
	case "/health", "/healthz", "/version":
		return true
	default:
		return false
	}
}