package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
)

type approvalRepoStub struct {
	created  *db.Approval
	approval *db.Approval
	pending  []db.Approval
	total    int
	approved bool
	denied   bool
}

func (r *approvalRepoStub) Create(_ context.Context, a *db.Approval) (*db.Approval, error) {
	copy := *a
	copy.ID = uuid.New()
	copy.Status = db.ApprovalStatusPending
	r.created = &copy
	r.approval = &copy
	return &copy, nil
}
func (r *approvalRepoStub) GetByID(_ context.Context, _ uuid.UUID) (*db.Approval, error) {
	if r.approval == nil {
		return nil, db.ErrNotFound
	}
	copy := *r.approval
	return &copy, nil
}
func (r *approvalRepoStub) GetByNodeID(_ context.Context, _ uuid.UUID) (*db.Approval, error) {
	return r.GetByID(context.Background(), uuid.Nil)
}
func (r *approvalRepoStub) ListPending(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _, _ int) ([]db.Approval, int, error) {
	return r.pending, r.total, nil
}
func (r *approvalRepoStub) ListByTree(_ context.Context, _ uuid.UUID, _ string, _, _ int) ([]db.Approval, error) {
	return nil, nil
}
func (r *approvalRepoStub) Approve(_ context.Context, _ uuid.UUID, actorID uuid.UUID, _ *uuid.UUID) (*db.Approval, error) {
	copy := *r.approval
	copy.Status = db.ApprovalStatusApproved
	copy.DecidedBy = &actorID
	now := time.Now()
	copy.DecidedAt = &now
	r.approval = &copy
	r.approved = true
	return &copy, nil
}
func (r *approvalRepoStub) Deny(_ context.Context, _ uuid.UUID, actorID uuid.UUID, reason string) (*db.Approval, error) {
	copy := *r.approval
	copy.Status = db.ApprovalStatusDenied
	copy.DecidedBy = &actorID
	copy.DeniedReason = &reason
	now := time.Now()
	copy.DecidedAt = &now
	r.approval = &copy
	r.denied = true
	return &copy, nil
}
func (r *approvalRepoStub) ExpirePending(context.Context) ([]uuid.UUID, error) { return nil, nil }

type auditRepoStub struct {
	created []*db.AuditEntry
	entries []db.AuditEntry
}

func (r *auditRepoStub) Create(_ context.Context, e *db.AuditEntry) (*db.AuditEntry, error) {
	copy := *e
	copy.ID = uuid.New()
	r.created = append(r.created, &copy)
	return &copy, nil
}
func (r *auditRepoStub) ListByApproval(_ context.Context, _ uuid.UUID, _, _ int) ([]db.AuditEntry, error) {
	return r.entries, nil
}
func (r *auditRepoStub) ListByTree(_ context.Context, _ uuid.UUID, _, _ int) ([]db.AuditEntry, error) {
	return r.entries, nil
}

type sseHubStub struct {
	treeID uuid.UUID
	event  sse.SSEEvent
	count  int
}

func (*sseHubStub) Subscribe(context.Context, uuid.UUID, sse.SSEClient) error { return nil }
func (*sseHubStub) Unsubscribe(uuid.UUID, string)                            {}
func (h *sseHubStub) Broadcast(treeID uuid.UUID, event sse.SSEEvent) sse.SSEEvent {
	h.treeID, h.event = treeID, event
	h.count++
	return event
}
func (*sseHubStub) ReplaySince(context.Context, uuid.UUID, string, string) error { return nil }
func (*sseHubStub) SubscriberCount(uuid.UUID) int                               { return 0 }
func (*sseHubStub) TotalConnections() int                                        { return 0 }
func (*sseHubStub) Shutdown(context.Context) error                               { return nil }

func TestRequestApprovalCreatesPendingApprovalAndAudit(t *testing.T) {
	repo := &approvalRepoStub{}
	audit := &auditRepoStub{}
	svc := NewApprovalService(repo, audit, nil, nil)
	treeID, nodeID, requester := uuid.New(), uuid.New(), uuid.New()

	got, err := svc.RequestApproval(context.Background(), treeID, nodeID, requester)
	if err != nil {
		t.Fatalf("RequestApproval() error = %v", err)
	}
	if got.Status != db.ApprovalStatusPending || got.TreeID != treeID || got.NodeID != nodeID {
		t.Fatalf("RequestApproval() = %#v", got)
	}
	if len(audit.created) != 1 || audit.created[0].Action != db.AuditActionApprovalRequested {
		t.Fatalf("audit entries = %#v, want one approval_requested", audit.created)
	}
	if audit.created[0].NewStatus == nil || *audit.created[0].NewStatus != db.ApprovalStatusPending {
		t.Fatalf("new status = %#v, want pending", audit.created[0].NewStatus)
	}
}

func TestApproveRequiresOwnerAndBroadcastsChange(t *testing.T) {
	owner, treeID := uuid.New(), uuid.New()
	repo := &approvalRepoStub{approval: &db.Approval{
		ID: uuid.New(), TreeID: treeID, NodeID: uuid.New(), OwnerID: owner,
		Status: db.ApprovalStatusPending, ExpiresAt: time.Now().Add(time.Hour),
	}}
	hub := &sseHubStub{}
	svc := NewApprovalService(repo, &auditRepoStub{}, nil, hub)

	if _, err := svc.Approve(context.Background(), repo.approval.ID, uuid.New()); !errors.Is(err, ErrNotApprovalOwner) {
		t.Fatalf("Approve(non-owner) error = %v, want ErrNotApprovalOwner", err)
	}
	got, err := svc.Approve(context.Background(), repo.approval.ID, owner)
	if err != nil {
		t.Fatalf("Approve(owner) error = %v", err)
	}
	if !repo.approved || got.Status != db.ApprovalStatusApproved {
		t.Fatalf("approval not transitioned: %#v", got)
	}
	if hub.count != 1 || hub.treeID != treeID || hub.event.Type != "approval_changed" {
		t.Fatalf("broadcast = count %d tree %s event %#v", hub.count, hub.treeID, hub.event)
	}
	var payload map[string]any
	if err := json.Unmarshal(hub.event.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["new_status"] != db.ApprovalStatusApproved {
		t.Fatalf("new_status = %#v, want approved", payload["new_status"])
	}
}

func TestDenyValidatesReason(t *testing.T) {
	owner := uuid.New()
	repo := &approvalRepoStub{approval: &db.Approval{
		ID: uuid.New(), TreeID: uuid.New(), NodeID: uuid.New(), OwnerID: owner,
		Status: db.ApprovalStatusPending, ExpiresAt: time.Now().Add(time.Hour),
	}}
	svc := NewApprovalService(repo, &auditRepoStub{}, nil, &sseHubStub{})

	if _, err := svc.Deny(context.Background(), repo.approval.ID, owner, "   "); !errors.Is(err, ErrDenyReasonRequired) {
		t.Fatalf("Deny(blank) error = %v, want ErrDenyReasonRequired", err)
	}
	if _, err := svc.Deny(context.Background(), repo.approval.ID, owner, string(make([]byte, 1001))); !errors.Is(err, ErrDenyReasonTooLong) {
		t.Fatalf("Deny(long) error = %v, want ErrDenyReasonTooLong", err)
	}
}
