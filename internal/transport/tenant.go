// Package transport — Multi-tenant isolation (SPEC-FTR-04 §7)
//
// Tenant isolation ensures data from one tenant cannot leak to another
// via transport channels. Each transport adapter MUST use tenant-scoped
// namespace prefixes derived from the tenant ID.
//
// TenantAuth validates that a client is authorised to subscribe to a
// given tenant's streams. The tenant ID is extracted from the JWT token
// or from ConnectOptions metadata.

package transport

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// --- Tenant namespace derivation --------------------------------------------

const (
	// TenantPrefix is the root namespace prefix for tenant-scoped channels.
	TenantPrefix = "canopy:tenant"

	// TenantHashKeyLength is the byte length of the truncated tenant hash used
	// in namespace derivation.
	TenantHashKeyLength = 16
)

// TenantNamespace returns the fully-qualified tenant namespace prefix
// for a given tenant ID. The returned string is safe for use as a
// channel / stream / subject prefix in any transport.
//
//	canopy:tenant:<hash-of-tenantID>
//
// The hash truncation at 16 hex bytes (64 bits) prevents collisions in
// practice while keeping channel names compact.
func TenantNamespace(tenantID string) string {
	if tenantID == "" {
		return TenantPrefix + ":public"
	}
	hash := tenantHash(tenantID)
	return fmt.Sprintf("%s:%s", TenantPrefix, hash)
}

// TenantChannel returns the transport-specific channel name for a tenant
// and sub-channel. The resulting key is safe for WebSocket rooms, NATS
// subjects, Redis streams, etc.
//
//	canopy:tenant:<hash>:<subChannel>
func TenantChannel(tenantID, subChannel string) string {
	return fmt.Sprintf("%s:%s", TenantNamespace(tenantID), subChannel)
}

// tenantHash returns a truncated hex-encoded HMAC-SHA256 of the tenant ID.
func tenantHash(tenantID string) string {
	mac := hmac.New(sha256.New, []byte("canopy.tenant.v1"))
	mac.Write([]byte(tenantID))
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:TenantHashKeyLength])
}

// --- Tenant-scoped connection helpers ---------------------------------------

// TenantIDFromConnection extracts the tenant ID from a Connection.
// Returns the empty string if no tenant is configured (public / single-tenant fallback).
func TenantIDFromConnection(conn *Connection) string {
	if conn == nil {
		return ""
	}
	return conn.TenantID
}

// TenantIDFromMetadata extracts the tenant ID from ConnectOptions metadata.
// The key "tenant_id" is the canonical metadata field.
func TenantIDFromMetadata(metadata map[string]string) string {
	if metadata == nil {
		return ""
	}
	return metadata["tenant_id"]
}

// ValidateTenantConnection checks that a connection is authorised to
// access the given tenant. Returns an error if the tenant does not match
// or if the connection is not properly scoped.
//
// When the connection's TenantID is empty, it is treated as public-scope
// and can only access the "public" namespace (empty tenantID).
func ValidateTenantConnection(conn *Connection, tenantID string) error {
	if conn == nil {
		return ErrConnectionClosed
	}
	connTenant := TenantIDFromConnection(conn)
	if connTenant == "" && tenantID == "" {
		return nil // public ↔ public
	}
	if connTenant == "" {
		return fmt.Errorf("%w: connection has no tenant scope but requested tenant %q", ErrAuthFailed, tenantID)
	}
	if tenantID == "" {
		return fmt.Errorf("%w: connection scoped to tenant %q but requested public namespace", ErrAuthFailed, connTenant)
	}
	if connTenant != tenantID {
		return fmt.Errorf("%w: connection tenant %q does not match requested tenant %q", ErrAuthFailed, connTenant, tenantID)
	}
	return nil
}

// --- Tenant token extraction ------------------------------------------------

// ErrTenantTokenInvalid is returned when the tenant token cannot be parsed.
var ErrTenantTokenInvalid = errors.New("transport: invalid tenant token")

// TenantTokenPrefix is the prefix for tenant-scoped auth tokens.
const TenantTokenPrefix = "tct_"

// ExtractTenantIDFromToken extracts the tenant ID from a transport auth token.
//
// Token format: "tct_<tenantID>" (simple prefix-based token for MVP).
// Post-MVP this will be a signed JWT or PASETO token.
func ExtractTenantIDFromToken(token string) (string, error) {
	if token == "" {
		return "", nil // public access
	}
	if !strings.HasPrefix(token, TenantTokenPrefix) {
		return "", ErrTenantTokenInvalid
	}
	tenantID := strings.TrimPrefix(token, TenantTokenPrefix)
	if tenantID == "" {
		return "", ErrTenantTokenInvalid
	}
	// Sanitise: tenant IDs must be alphanumeric with hyphens/underscores.
	for _, r := range tenantID {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_'
		if !valid {
			return "", fmt.Errorf("%w: invalid character in tenant ID", ErrTenantTokenInvalid)
		}
	}
	return tenantID, nil
}

// --- Isolated error messages ------------------------------------------------

// ErrTenantIsolation is returned when a cross-tenant operation is detected.
var ErrTenantIsolation = errors.New("transport: cross-tenant isolation violation")
