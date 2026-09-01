package federation

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Service struct {
	repo   Repository
	signer *tokenSigner
}

func NewService(repo Repository, signingKey []byte, serverID uuid.UUID, serverURL string) *Service {
	return &Service{repo: repo, signer: newTokenSigner(signingKey, serverID, strings.TrimRight(serverURL, "/"))}
}

func (s *Service) GenerateToken(profileID, treeID uuid.UUID) (string, error) {
	if profileID == uuid.Nil || treeID == uuid.Nil {
		return "", ErrInvalidInput
	}
	return s.signer.generate(profileID, treeID)
}

func (s *Service) VerifyToken(token string) (*TokenClaims, error) { return s.signer.verify(token) }

func (s *Service) CreateFederationLink(ctx context.Context, remoteURL string, profileID, treeID uuid.UUID) (*FederationPeer, string, error) {
	remoteURL = strings.TrimRight(strings.TrimSpace(remoteURL), "/")
	parsed, err := url.ParseRequestURI(remoteURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || profileID == uuid.Nil || treeID == uuid.Nil {
		return nil, "", ErrInvalidInput
	}
	if existing, findErr := s.repo.FindByServerTree(ctx, remoteURL, treeID); findErr == nil {
		if existing.State == PeerRevoked {
			return nil, "", ErrLinkRevoked
		}
		return nil, "", ErrLinkAlreadyExists
	} else if !errors.Is(findErr, ErrFederationNotFound) {
		return nil, "", fmt.Errorf("federation: find existing link: %w", findErr)
	}
	now := time.Now().UTC()
	peer, err := s.repo.Create(ctx, &FederationPeer{ServerURL: remoteURL, SigningKeyFP: s.signer.fingerprint(), Role: RoleInitiator, State: PeerConnected, TreeID: treeID, CreatedBy: profileID, ConnectedAt: &now})
	if err != nil {
		return nil, "", fmt.Errorf("federation: create link: %w", err)
	}
	token, err := s.GenerateToken(profileID, treeID)
	if err != nil {
		return nil, "", err
	}
	return peer, token, nil
}

func (s *Service) AcceptFederationLink(ctx context.Context, token, requestServerURL string, ecdhePublicKey []byte) (*FederationPeer, error) {
	claims, err := s.VerifyToken(token)
	if err != nil {
		return nil, err
	}
	requestServerURL = strings.TrimRight(strings.TrimSpace(requestServerURL), "/")
	if requestServerURL != claims.ServerURL {
		return nil, ErrTokenInvalid
	}
	if existing, findErr := s.repo.FindByServerTree(ctx, requestServerURL, claims.TreeID); findErr == nil && existing.State == PeerRevoked {
		return nil, ErrLinkRevoked
	} else if findErr != nil && !errors.Is(findErr, ErrFederationNotFound) {
		return nil, fmt.Errorf("federation: find handshake peer: %w", findErr)
	}
	now := time.Now().UTC()
	peer, err := s.repo.UpsertAccepted(ctx, &FederationPeer{ServerURL: requestServerURL, SigningKeyFP: claims.SigningKeyFP, ECDHEPublicKey: ecdhePublicKey, Role: RoleAcceptor, State: PeerConnected, TreeID: claims.TreeID, CreatedBy: claims.ProfileID, ConnectedAt: &now})
	if err != nil {
		return nil, fmt.Errorf("federation: accept link: %w", err)
	}
	return peer, nil
}

func (s *Service) RevokeFederationLink(ctx context.Context, peerID uuid.UUID) error {
	now := time.Now().UTC()
	reason := "revoked by user"
	if err := s.repo.SetState(ctx, peerID, PeerRevoked, &now, &reason); err != nil {
		return fmt.Errorf("federation: revoke link: %w", err)
	}
	return nil
}

func (s *Service) GetPeer(ctx context.Context, peerID uuid.UUID) (*FederationPeer, error) {
	return s.repo.Get(ctx, peerID)
}

func (s *Service) ListPeers(ctx context.Context, treeID uuid.UUID) ([]*FederationPeer, error) {
	return s.repo.List(ctx, &treeID, true)
}

func (s *Service) ListAllPeers(ctx context.Context) ([]*FederationPeer, error) {
	return s.repo.List(ctx, nil, true)
}
