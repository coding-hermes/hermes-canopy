// Package service implements the Canopy business logic layer.
//
// Tree-role request context (DF-HERMES-CANOPY-79).
//
// TreeMembershipMiddleware (and the flat-mount NodeAccessMiddleware) read
// the member's role alongside membership and store it on the request
// context here; the NodeService write paths consult it before mutating, so
// a viewer is rejected at the service boundary no matter which transport
// reached it. The helpers live in the service package — not handler —
// because the enforcement point is the service and the dependency
// direction (handler imports service) forbids the reverse.
package service

import (
	"context"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// treeRoleKey is the context key under which the authenticated requester's
// tree membership role is stored for the current request. A request
// touches exactly one tree, so a single unscoped key is unambiguous.
type treeRoleKey struct{}

// WithTreeRole stores the requester's tree membership role on ctx (one of
// the db.TreeRole* values; "" means no role was resolved).
func WithTreeRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, treeRoleKey{}, role)
}

// TreeRoleFromContext returns the role stored by WithTreeRole, or "" when
// no role was injected — the shape of a server wired without the tree
// membership middleware (single-user deployments, stub-backed tests).
// Callers treat "" as "no restriction" so those deployments keep working.
func TreeRoleFromContext(ctx context.Context) string {
	role, _ := ctx.Value(treeRoleKey{}).(string)
	return role
}

// isViewer reports whether ctx carries the viewer role injected by the
// membership middleware.
func isViewer(ctx context.Context) bool {
	return TreeRoleFromContext(ctx) == db.TreeRoleViewer
}