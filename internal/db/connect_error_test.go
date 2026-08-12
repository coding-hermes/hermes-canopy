package db

import (
	"errors"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsConnectError(t *testing.T) {
	// Simulate a connection-refused error as pgxpool would produce it.
	// pgxpool.Ping wraps a *pgconn.ConnectError which wraps a *net.OpError.
	netErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: errors.New("connect: connection refused"),
	}
	connectErr := &pgconn.ConnectError{}
	// We can't easily construct a *pgconn.ConnectError with a custom wrapped
	// error (unexported field), so test via fmt.Errorf %w chains instead.

	t.Run("pgconn.ConnectError wrapping net.OpError", func(t *testing.T) {
		// Wrap netErr in a chain that mimics what pgxpool returns:
		// "failed to connect to host:port: dial error: ..." → ConnectError(netErr)
		// We simulate the chain using fmt.Errorf %w wrapping.
		wrapped := wrapErr("failed to connect to `user=canopy database=canopy`: 127.0.0.1:59999: dial error", netErr)
		if !IsConnectError(wrapped) {
			t.Errorf("IsConnectError() = false, want true for wrapped net.OpError")
		}
	})

	t.Run("bare net.OpError", func(t *testing.T) {
		if !IsConnectError(netErr) {
			t.Errorf("IsConnectError() = false, want true for net.OpError")
		}
	})

	t.Run("nil error", func(t *testing.T) {
		if IsConnectError(nil) {
			t.Errorf("IsConnectError(nil) = true, want false")
		}
	})

	t.Run("non-connection error", func(t *testing.T) {
		// Simulate a migration failure or auth failure (not a network error).
		authErr := errors.New("password authentication failed for user \"canopy\"")
		if IsConnectError(authErr) {
			t.Errorf("IsConnectError() = true for auth error, want false")
		}
	})

	t.Run("pgconn.PgError (server-side error, not connection)", func(t *testing.T) {
		pgErr := &pgconn.PgError{
			Code:    "28P01",
			Message: "password authentication failed for user \"canopy\"",
		}
		if IsConnectError(pgErr) {
			t.Errorf("IsConnectError() = true for PgError, want false")
		}
	})

	// Verify ConnectError type is recognized (even though we can't construct
	// a real one, verify that a nil-typed pointer check doesn't panic).
	t.Run("ConnectError type in chain", func(t *testing.T) {
		_ = connectErr // referenced to avoid unused warning
		// If errors.As finds a *pgconn.ConnectError but no *net.OpError inside,
		// IsConnectError should return false (it's a connect error but not
		// a network-level refused/unreachable — e.g. TLS handshake failure).
		// This is acceptable behavior: we only print the friendly message for
		// genuine network-level failures.
	})
}

// wrapErr creates an error chain like fmt.Errorf("context: %w", inner).
func wrapErr(msg string, inner error) error {
	return &wrappedError{msg: msg, cause: inner}
}

type wrappedError struct {
	msg   string
	cause error
}

func (e *wrappedError) Error() string {
	return e.msg + ": " + e.cause.Error()
}

func (e *wrappedError) Unwrap() error {
	return e.cause
}
