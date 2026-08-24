package collaboration

import "errors"

// Error catalogue for the collaboration service (SPEC-FTR-01 §3.5).
// Handlers map these to HTTP status codes; the service layer returns
// them wrapped with additional context where useful.
var (
	ErrNotFound           = errors.New("collaboration: not found")
	ErrLockHeld           = errors.New("collaboration: node is locked by another operation")
	ErrNotLockOwner       = errors.New("collaboration: caller does not own this lock")
	ErrPermissionDenied   = errors.New("collaboration: permission denied")
	ErrNotWorkspaceMember = errors.New("collaboration: user is not a member of this workspace")
	ErrInvalidToken       = errors.New("collaboration: invitation token is invalid or expired")
	ErrGateExpired        = errors.New("collaboration: approval gate has expired")
	ErrGateClosed         = errors.New("collaboration: approval gate is not pending")
	ErrDuplicated         = errors.New("collaboration: resource already exists")
	ErrRateLimited        = errors.New("collaboration: rate limit exceeded. Try again later")
)
