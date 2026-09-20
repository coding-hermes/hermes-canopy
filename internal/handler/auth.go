package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

type userIDContextKey struct{}
type tenantIDContextKey struct{}
type roleContextKey struct{}

// AuthMiddleware validates HS256 Bearer tokens and stores the authenticated
// user's UUID in the request context. Health and version routes are public.
//
// This constructor deliberately has no database dependency: it is also used
// by federation's dual-auth path, which must not provision local users.
func AuthMiddleware(secret string) func(http.Handler) http.Handler {
	return authMiddleware(secret, nil, nil)
}

// AuthMiddlewareWithUserRepo adds the local user-account gate to authenticated
// API routes. Dev-secret subjects are provisioned on demand; production-secret
// subjects must already have a users row. Federation continues to use the
// database-free AuthMiddleware constructor above.
func AuthMiddlewareWithUserRepo(secret string, users db.UserRepo) func(http.Handler) http.Handler {
	if users == nil {
		return authMiddleware(secret, nil, nil)
	}

	var ensure func(context.Context, uuid.UUID) error
	if provisioner, ok := users.(db.UserProvisioner); ok {
		ensure = func(ctx context.Context, id uuid.UUID) error {
			_, err := provisioner.EnsureByID(ctx, id)
			return err
		}
	}
	lookup := func(ctx context.Context, id uuid.UUID) error {
		_, err := users.GetByID(ctx, id)
		return err
	}
	return authMiddleware(secret, lookup, ensure)
}

// authMiddleware is split from the public constructors so authentication
// tests can exercise the account gate without a live PostgreSQL pool.
func authMiddleware(secret string, lookup func(context.Context, uuid.UUID) error, ensure func(context.Context, uuid.UUID) error) func(http.Handler) http.Handler {
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
				// Explicitly reject unsigned tokens (BUG-014 fix).
				if token.Method.Alg() == "none" {
					return nil, jwt.ErrSignatureInvalid
				}
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
			if raw, ok := claims["tenant_id"].(string); ok {
				if tenantID, parseErr := uuid.Parse(raw); parseErr == nil {
					ctx = context.WithValue(ctx, tenantIDContextKey{}, tenantID)
				}
			}
			if role, ok := claims["role"].(string); ok {
				ctx = context.WithValue(ctx, roleContextKey{}, role)
			}

			if lookup != nil {
				if db.IsDevJWTSecret(secret) {
					if ensure == nil {
						log.Error().Str("user_id", userID.String()).Msg("dev JWT user provisioning is not configured")
						writeError(w, http.StatusServiceUnavailable, "USER_PROVISIONING_UNAVAILABLE", "server cannot provision dev token subjects")
						return
					}
					if err := ensure(r.Context(), userID); err != nil {
						log.Error().Err(err).Str("user_id", userID.String()).Msg("failed to provision dev JWT user")
						writeError(w, http.StatusServiceUnavailable, "USER_PROVISIONING_FAILED", "server could not provision the token subject")
						return
					}
				} else {
					err := lookup(r.Context(), userID)
					switch {
					case errors.Is(err, db.ErrNotFound):
						writeError(w, http.StatusForbidden, "USER_NOT_PROVISIONED", "token subject has no user account on this server; ask the operator to provision it")
						return
					case err != nil:
						log.Error().Err(err).Str("user_id", userID.String()).Msg("failed to look up authenticated user")
						writeError(w, http.StatusServiceUnavailable, "USER_LOOKUP_FAILED", "server could not verify the token subject account")
						return
					}
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if role, _ := r.Context().Value(roleContextKey{}).(string); role != "admin" {
			writeError(w, http.StatusForbidden, "PERMISSION_DENIED", "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// TenantIDFromContext returns the tenant scope carried by the authenticated JWT.
func TenantIDFromContext(ctx context.Context) uuid.UUID {
	tenantID, _ := ctx.Value(tenantIDContextKey{}).(uuid.UUID)
	return tenantID
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
