package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memoryRepo struct{ peers map[uuid.UUID]*FederationPeer }

func newMemoryRepo() *memoryRepo { return &memoryRepo{peers: make(map[uuid.UUID]*FederationPeer)} }

func (r *memoryRepo) Create(_ context.Context, peer *FederationPeer) (*FederationPeer, error) {
	copy := *peer
	copy.ID = uuid.New()
	copy.CreatedAt = time.Now().UTC()
	r.peers[copy.ID] = &copy
	return &copy, nil
}
func (r *memoryRepo) UpsertAccepted(ctx context.Context, peer *FederationPeer) (*FederationPeer, error) {
	if existing, err := r.FindByServerTree(ctx, peer.ServerURL, peer.TreeID); err == nil {
		if existing.State == PeerRevoked {
			return nil, ErrLinkRevoked
		}
		copy := *peer
		copy.ID, copy.CreatedAt = existing.ID, existing.CreatedAt
		r.peers[copy.ID] = &copy
		return &copy, nil
	}
	return r.Create(ctx, peer)
}
func (r *memoryRepo) Get(_ context.Context, id uuid.UUID) (*FederationPeer, error) {
	p, ok := r.peers[id]
	if !ok {
		return nil, ErrFederationNotFound
	}
	copy := *p
	return &copy, nil
}
func (r *memoryRepo) FindByServerTree(_ context.Context, serverURL string, treeID uuid.UUID) (*FederationPeer, error) {
	for _, p := range r.peers {
		if p.ServerURL == serverURL && p.TreeID == treeID {
			copy := *p
			return &copy, nil
		}
	}
	return nil, ErrFederationNotFound
}
func (r *memoryRepo) List(_ context.Context, treeID *uuid.UUID, activeOnly bool) ([]*FederationPeer, error) {
	out := make([]*FederationPeer, 0)
	for _, p := range r.peers {
		if treeID != nil && p.TreeID != *treeID || activeOnly && p.State == PeerRevoked {
			continue
		}
		copy := *p
		out = append(out, &copy)
	}
	return out, nil
}
func (r *memoryRepo) SetState(_ context.Context, id uuid.UUID, state PeerState, revokedAt *time.Time, reason *string) error {
	p, ok := r.peers[id]
	if !ok {
		return ErrFederationNotFound
	}
	p.State, p.RevokedAt, p.RevokeReason = state, revokedAt, reason
	return nil
}

func newTestService(repo Repository) *Service {
	return NewService(repo, []byte("federation-test-secret"), uuid.MustParse("10000000-0000-0000-0000-000000000001"), "https://local.example")
}

func TestServiceLinkLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := newTestService(newMemoryRepo())
	profileID, treeID := uuid.New(), uuid.New()

	peer, token, err := svc.CreateFederationLink(ctx, "https://remote.example/", profileID, treeID)
	if err != nil {
		t.Fatalf("CreateFederationLink: %v", err)
	}
	if token == "" || peer.State != PeerConnected || peer.ServerURL != "https://remote.example" {
		t.Fatalf("created peer = %+v, token empty=%v", peer, token == "")
	}
	peers, err := svc.ListPeers(ctx, treeID)
	if err != nil || len(peers) != 1 {
		t.Fatalf("ListPeers = %d, %v", len(peers), err)
	}
	if err := svc.RevokeFederationLink(ctx, peer.ID); err != nil {
		t.Fatalf("RevokeFederationLink: %v", err)
	}
	peers, err = svc.ListAllPeers(ctx)
	if err != nil || len(peers) != 0 {
		t.Fatalf("active peers after revoke = %d, %v", len(peers), err)
	}
}

func TestServiceHandshake(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newMemoryRepo()
	svc := newTestService(repo)
	profileID, treeID := uuid.New(), uuid.New()
	token, err := svc.GenerateToken(profileID, treeID)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := svc.AcceptFederationLink(ctx, token, "https://local.example", []byte("peer-key"))
	if err != nil {
		t.Fatalf("AcceptFederationLink: %v", err)
	}
	if peer.Role != RoleAcceptor || peer.State != PeerConnected || string(peer.ECDHEPublicKey) != "peer-key" {
		t.Fatalf("accepted peer = %+v", peer)
	}

	if _, err := svc.AcceptFederationLink(ctx, token+"bad", "https://local.example", nil); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("bad token error = %v, want ErrTokenInvalid", err)
	}
	if err := svc.RevokeFederationLink(ctx, peer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptFederationLink(ctx, token, "https://local.example", nil); !errors.Is(err, ErrLinkRevoked) {
		t.Fatalf("revoked handshake error = %v, want ErrLinkRevoked", err)
	}
}
