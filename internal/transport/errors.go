package transport

import "errors"

// --- Sentinel errors (SPEC-FTR-04 §3.3) ------------------------------------
//
// All errors are defined with package prefix "transport:" so callers
// can use errors.Is to check for specific conditions.

var (
	// ErrConnectionRefused is returned when the peer actively rejects the connection.
	ErrConnectionRefused = errors.New("transport: connection refused by peer")
	// ErrAuthFailed is returned when authentication credentials are invalid.
	ErrAuthFailed = errors.New("transport: authentication failed")
	// ErrAuthExpired is returned when credentials have expired and need rotation.
	ErrAuthExpired = errors.New("transport: credentials expired, rotation required")
	// ErrTransportUnreachable is returned when the peer cannot be reached (DNS, network, timeout).
	ErrTransportUnreachable = errors.New("transport: peer unreachable (DNS, network, timeout)")
	// ErrTransportMismatch is returned when the adapter type does not match the requested transport.
	ErrTransportMismatch = errors.New("transport: adapter type does not match requested transport")
	// ErrConnectionClosed is returned when an operation is attempted on a closed connection.
	ErrConnectionClosed = errors.New("transport: operation on closed connection")
	// ErrSendTimeout is returned when a send operation exceeds the deadline.
	ErrSendTimeout = errors.New("transport: send timed out")
	// ErrPayloadTooLarge is returned when the payload exceeds the transport's max message size.
	ErrPayloadTooLarge = errors.New("transport: payload exceeds max message size")
	// ErrSequenceGap is returned when a gap is detected in the message sequence.
	ErrSequenceGap = errors.New("transport: gap detected in message sequence")
	// ErrRateLimited is returned when the rate limit is exceeded.
	ErrRateLimited = errors.New("transport: rate limit exceeded")
	// ErrNoTransportAvailable is returned when all transports in the fallback
	// chain are exhausted or unavailable, including an offline queue overflow.
	ErrNoTransportAvailable = errors.New("transport: no transport available in fallback chain")
)
