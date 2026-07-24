// Package service contains the business logic layer for the Canopy
// approval system. ApprovalService orchestrates the approval lifecycle
// (pending → approved / denied / expired) with audit logging and SSE
// broadcast on every state change.
//
// SPEC-FTR-01 §3.4, SPEC-API-05 §3–8, SPEC-DM-03 §4.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
)

// --- Error sentinels -------------------------------------------------------

var (
	ErrApprovalNotFound   = errors.New("approval service: approval not found")
	ErrAlreadyDecided     = errors.New("approval service: approval already decided")
	ErrApprovalExpired    = errors.New("approval service: approval has expired")
	ErrInvalidDenyReason  = errors.New("approval service: deny reason is required (1-1000 chars)")
	ErrPermissionDenied   = errors.New("approval service: permission denied")
	ErrNotApprovalOwner   = errors.New("approval service: not the approval owner")
	ErrDenyReasonRequired = errors.New("approval service: deny reason is required")
	ErrDenyReasonTooLong  = errors.New("approval service: deny reason exceeds 1000 characters")
)

// ApprovalService defines the approval lifecycle operations.
type ApprovalService interface {
	// RequestApproval creates a pending approval for an agent action.
	RequestApproval(ctx context.Context, treeID, nodeID, ownerID, requestedBy uuid.UUID) (*db.Approval, error)

	// GetPending returns pending approvals for an owner, paginated.
	// treeID may be nil to span all trees.
	GetPending(ctx context.Context, ownerID uuid.UUID, treeID *uuid.UUID, limit, offset int) ([]db.Approval, int, error)

	// GetApproval returns a single approval by ID.
	GetApproval(ctx context.Context, id uuid.UUID) (*db.Approval, error)

	// Approve transitions a pending approval to approved.
	// Broadcasts SSE event on success.
	Approve(ctx context.Context, approvalID, actorID uuid.UUID) (*db.Approval, error)

	// Deny transitions a pending approval to denied with mandatory reason.
	// Broadcasts SSE event on success.
	Deny(ctx context.Context, approvalID, actorID uuid.UUID, reason string) (*db.Approval, error)

	// ExpireStale expires all pending approvals past their expires_at.
	ExpireStale(ctx context.Context) ([]uuid.UUID, error)

	// ListHistory returns audit log entries, filtered by approval or tree.
	ListHistory(ctx context.Context, approvalID *uuid.UUID, treeID *uuid.UUID, limit, offset int) ([]db.AuditEntry, error)
}

// approvalService is the concrete implementation of ApprovalService.
type approvalService struct {
	approvals db.ApprovalRepo
	audit     db.AuditRepo
	users     db.UserRepo
	profiles  db.ProfileRepo
	members   db.TreeMemberRepo
	sseHub    sse.SSEHub
}

// NewApprovalService wires the service to its dependencies.
func NewApprovalService(
	approvals db.ApprovalRepo,
	audit db.AuditRepo,
	users db.UserRepo,
	profiles db.ProfileRepo,
	members db.TreeMemberRepo,
	sseHub sse.SSEHub,
) ApprovalService {
	return &approvalService{
		approvals: approvals,
		audit:     audit,
		users:     users,
		profiles:  profiles,
		members:   members,
		sseHub:    sseHub,
	}
}

// --- RequestApproval -------------------------------------------------------

func (s *approvalService) RequestApproval(ctx context.Context, treeID, nodeID, ownerID, requestedBy uuid.UUID) (*db.Approval, error) {
	appr := &db.Approval{
		TreeID:      treeID,
		NodeID:      nodeID,
		OwnerID:     ownerID,
		RequestedBy: requestedBy,
		Status:      db.ApprovalStatusPending,
		ExpiresAt:   time.Now().UTC().Add(7 * 24 * time.Hour),
	}
	created, err := s.approvals.Create(ctx, appr)
	if err != nil {
		return nil, fmt.Errorf("approval service: create: %w", err)
	}

	// Audit trail
	prev := db.ApprovalStatusPending
	_, aErr := s.audit.Create(ctx, &db.AuditEntry{
		ApprovalID: created.ID,
		Action:     db.AuditActionApprovalRequested,
		Actor:      &requestedBy,
		NewStatus:  &prev,
		Details:    mustMarshalAuditDetail(treeID, nodeID, requestedBy, ownerID),
	})
	if aErr != nil {
		log.Warn().Err(aErr).Str("approval_id", created.ID.String()).Msg("approval service: audit log write failed")
	}

	// SSE broadcast (best-effort)
	s.broadcastApprovalEvent(ctx, created, "pending", nil)
	return created, nil
}

// --- GetPending ------------------------------------------------------------

func (s *approvalService) GetPending(ctx context.Context, ownerID uuid.UUID, treeID *uuid.UUID, limit, offset int) ([]db.Approval, int, error) {
	out, total, err := s.approvals.ListPending(ctx, ownerID, treeID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("approval service: list pending: %w", err)
	}
	return out, total, nil
}

// --- GetApproval -----------------------------------------------------------

func (s *approvalService) GetApproval(ctx context.Context, id uuid.UUID) (*db.Approval, error) {
	appr, err := s.approvals.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrApprovalNotFound, id.String())
		}
		return nil, fmt.Errorf("approval service: get by id: %w", err)
	}
	return appr, nil
}

// --- Approve ---------------------------------------------------------------

func (s *approvalService) Approve(ctx context.Context, approvalID, actorID uuid.UUID) (*db.Approval, error) {
	// Validate state
	appr, err := s.approvals.GetByID(ctx, approvalID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrApprovalNotFound, approvalID.String())
		}
		return nil, fmt.Errorf("approval service: get approval: %w", err)
	}

	// Owner check — only the approval owner can approve
	if appr.OwnerID != actorID {
		return nil, fmt.Errorf("%w: approval %s is owned by %s",
			ErrNotApprovalOwner, approvalID.String(), appr.OwnerID.String())
	}

	if appr.Status != db.ApprovalStatusPending {
		return nil, fmt.Errorf("%w: current status is %s", ErrAlreadyDecided, appr.Status)
	}
	if time.Now().UTC().After(appr.ExpiresAt) {
		return nil, fmt.Errorf("%w: expired at %s", ErrApprovalExpired, appr.ExpiresAt.Format(time.RFC3339))
	}

	// Execute state transition
	updated, err := s.approvals.Approve(ctx, approvalID, actorID, nil)
	if err != nil {
		return nil, fmt.Errorf("approval service: approve: %w", err)
	}

	// Audit trail
	prev := db.ApprovalStatusPending
	_, aErr := s.audit.Create(ctx, &db.AuditEntry{
		ApprovalID:     approvalID,
		Action:         db.AuditActionApprovalGranted,
		Actor:          &actorID,
		PreviousStatus: &prev,
		NewStatus:      &updated.Status,
		Details:        mustMarshalSimpleDetail(updated.TreeID, "approval_granted"),
	})
	if aErr != nil {
		log.Warn().Err(aErr).Str("approval_id", approvalID.String()).Msg("approval service: audit log write failed")
	}

	s.broadcastApprovalEvent(ctx, updated, "approved", &actorID)
	return updated, nil
}

// --- Deny ------------------------------------------------------------------

func (s *approvalService) Deny(ctx context.Context, approvalID, actorID uuid.UUID, reason string) (*db.Approval, error) {
	// Validate inputs
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, fmt.Errorf("%w: got empty reason", ErrDenyReasonRequired)
	}
	if len(reason) > 1000 {
		return nil, fmt.Errorf("%w: got %d chars", ErrDenyReasonTooLong, len(reason))
	}

	// Validate state
	appr, err := s.approvals.GetByID(ctx, approvalID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrApprovalNotFound, approvalID.String())
		}
		return nil, fmt.Errorf("approval service: get approval: %w", err)
	}

	// Owner check — only the approval owner can deny
	if appr.OwnerID != actorID {
		return nil, fmt.Errorf("%w: approval %s is owned by %s",
			ErrNotApprovalOwner, approvalID.String(), appr.OwnerID.String())
	}

	if appr.Status != db.ApprovalStatusPending {
		return nil, fmt.Errorf("%w: current status is %s", ErrAlreadyDecided, appr.Status)
	}
	if time.Now().UTC().After(appr.ExpiresAt) {
		return nil, fmt.Errorf("%w: expired at %s", ErrApprovalExpired, appr.ExpiresAt.Format(time.RFC3339))
	}

	// Execute state transition
	updated, err := s.approvals.Deny(ctx, approvalID, actorID, reason)
	if err != nil {
		return nil, fmt.Errorf("approval service: deny: %w", err)
	}

	// Audit trail
	prev := db.ApprovalStatusPending
	_, aErr := s.audit.Create(ctx, &db.AuditEntry{
		ApprovalID:     approvalID,
		Action:         db.AuditActionApprovalDenied,
		Actor:          &actorID,
		PreviousStatus: &prev,
		NewStatus:      &updated.Status,
		Details:        mustMarshalSimpleDetail(updated.TreeID, "deny: "+reason),
	})
	if aErr != nil {
		log.Warn().Err(aErr).Str("approval_id", approvalID.String()).Msg("approval service: audit log write failed")
	}

	s.broadcastApprovalEvent(ctx, updated, "denied", &actorID)
	return updated, nil
}

// --- ExpireStale -----------------------------------------------------------

func (s *approvalService) ExpireStale(ctx context.Context) ([]uuid.UUID, error) {
	ids, err := s.approvals.ExpirePending(ctx)
	if err != nil {
		return nil, fmt.Errorf("approval service: expire pending: %w", err)
	}
	for _, id := range ids {
		log.Info().Str("approval_id", id.String()).Msg("approval service: expired stale approval")
	}
	return ids, nil
}

// --- ListHistory -----------------------------------------------------------

func (s *approvalService) ListHistory(ctx context.Context, approvalID *uuid.UUID, treeID *uuid.UUID, limit, offset int) ([]db.AuditEntry, error) {
	if approvalID != nil {
		return s.audit.ListByApproval(ctx, *approvalID, limit, offset)
	}
	if treeID != nil {
		return s.audit.ListByTree(ctx, *treeID, limit, offset)
	}
	return nil, errors.New("approval service: either approvalID or treeID must be set")
}

// --- Internal helpers ------------------------------------------------------

// broadcastApprovalEvent sends an SSE event for an approval state change.
// Errors are logged but not returned (best-effort).
func (s *approvalService) broadcastApprovalEvent(ctx context.Context, appr *db.Approval, status string, actorID *uuid.UUID) {
	if s.sseHub == nil {
		return
	}

	payload := map[string]interface{}{
		"approval_id": appr.ID.String(),
		"tree_id":     appr.TreeID.String(),
		"node_id":     appr.NodeID.String(),
		"new_status":  status,
	}
	if actorID != nil {
		payload["actor_id"] = actorID.String()
	}

	data, err := json.Marshal(payload)
	if err != nil {
		log.Warn().Err(err).Msg("approval service: marshal SSE payload")
		return
	}

	aid := appr.RequestedBy
	if actorID != nil {
		aid = *actorID
	}

	event := s.sseHub.Broadcast(appr.TreeID, sse.SSEEvent{
		TreeID:    appr.TreeID,
		Type:      "approval_changed",
		Data:      data,
		Timestamp: time.Now().UTC(),
		ActorID:   aid,
	})
	_ = event // event is the enriched copy (with SequenceNum populated)
}

// mustMarshalAuditDetail builds the jsonb details field for audit entries.
func mustMarshalAuditDetail(treeID, nodeID, requestedBy, ownerID uuid.UUID) json.RawMessage {
	m := map[string]interface{}{
		"tree_id":      treeID.String(),
		"node_id":      nodeID.String(),
		"requested_by": requestedBy.String(),
		"owner_id":     ownerID.String(),
	}
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// mustMarshalSimpleDetail builds a minimal details payload for post-decision entries.
func mustMarshalSimpleDetail(treeID uuid.UUID, context string) json.RawMessage {
	m := map[string]interface{}{
		"tree_id": treeID.String(),
		"context": context,
	}
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
