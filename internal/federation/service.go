package federation

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Service struct {
	repo      Repository
	router    ProfileRouter
	signer    *tokenSigner
	mu        sync.RWMutex
	sessions  map[uuid.UUID][]byte
	localECDH map[uuid.UUID][]byte
	relay     *RelayService
}

func NewService(repo Repository, signingKey []byte, serverID uuid.UUID, serverURL string) *Service {
	return newService(repo, newTokenSigner(signingKey, serverID, strings.TrimRight(serverURL, "/")))
}

func newService(repo Repository, signer *tokenSigner) *Service {
	service := &Service{repo: repo, signer: signer, sessions: make(map[uuid.UUID][]byte), localECDH: make(map[uuid.UUID][]byte)}
	if pgRepo, ok := repo.(*PGRepository); ok {
		service.router = NewPGProfileRouter(pgRepo.pool)
		service.relay = NewRelayService(NewPGRelayRepository(pgRepo.pool), repo, nil,
			func(_ context.Context, peer *FederationPeer) (string, error) {
				return service.GenerateToken(peer.CreatedBy, peer.TreeID)
			})
	}
	return service
}

func (s *Service) Relay() *RelayService { return s.relay }

func (s *Service) Create(ctx context.Context, route *Route) (*Route, error) {
	if s.router == nil {
		return nil, ErrRouteNotFound
	}
	return s.router.Create(ctx, route)
}

func (s *Service) List(ctx context.Context, profileID, treeID *uuid.UUID) ([]Route, error) {
	if s.router == nil {
		return nil, ErrRouteNotFound
	}
	return s.router.List(ctx, profileID, treeID)
}

func (s *Service) Update(ctx context.Context, id uuid.UUID, update RouteUpdate) (*Route, error) {
	if s.router == nil {
		return nil, ErrRouteNotFound
	}
	return s.router.Update(ctx, id, update)
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	if s.router == nil {
		return ErrRouteNotFound
	}
	return s.router.Delete(ctx, id)
}

func (s *Service) Resolve(ctx context.Context, profileID, treeID uuid.UUID) (*Route, error) {
	if s.router == nil {
		return nil, ErrRouteNotFound
	}
	return s.router.Resolve(ctx, profileID, treeID)
}

func (s *Service) SigningPublicKey() []byte { return append([]byte(nil), s.signer.publicKey...) }

func (s *Service) GenerateToken(profileID, treeID uuid.UUID) (string, error) {
	if profileID == uuid.Nil || treeID == uuid.Nil {
		return "", ErrInvalidInput
	}
	return s.signer.generate(profileID, treeID)
}

func (s *Service) VerifyToken(token string) (*TokenClaims, error) { return s.signer.verify(token) }

func (s *Service) AuthenticatePeerToken(ctx context.Context, token string, peerID uuid.UUID) (*TokenClaims, error) {
	peer, err := s.repo.Get(ctx, peerID)
	if err != nil {
		return nil, err
	}
	claims, err := s.signer.verifyWithKey(token, ed25519.PublicKey(peer.SigningPublicKey))
	if err != nil || claims.TreeID != peer.TreeID {
		return nil, ErrTokenInvalid
	}
	return claims, nil
}

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

func (s *Service) AcceptFederationLink(ctx context.Context, token, requestServerURL string, ecdhePublicKey []byte, signingKeys ...[]byte) (*FederationPeer, error) {
	var signingKey ed25519.PublicKey
	if len(signingKeys) > 0 {
		signingKey = signingKeys[0]
	}
	claims, err := s.signer.verifyWithKey(token, signingKey)
	if len(signingKey) == 0 {
		claims, err = s.VerifyToken(token)
	}
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
	curve := ecdh.X25519()
	remoteKey, keyErr := curve.NewPublicKey(ecdhePublicKey)
	if keyErr != nil {
		return nil, ErrInvalidInput
	}
	localPrivate, keyErr := curve.GenerateKey(rand.Reader)
	if keyErr != nil {
		return nil, fmt.Errorf("federation: generate ECDH key: %w", keyErr)
	}
	shared, keyErr := localPrivate.ECDH(remoteKey)
	if keyErr != nil {
		return nil, ErrInvalidInput
	}
	now := time.Now().UTC()
	peer, err := s.repo.UpsertAccepted(ctx, &FederationPeer{ServerURL: requestServerURL, SigningKeyFP: claims.SigningKeyFP, ECDHEPublicKey: ecdhePublicKey, SigningPublicKey: signingKey, Role: RoleAcceptor, State: PeerConnected, TreeID: claims.TreeID, CreatedBy: claims.ProfileID, ConnectedAt: &now})
	if err != nil {
		return nil, fmt.Errorf("federation: accept link: %w", err)
	}
	s.mu.Lock()
	s.sessions[peer.ID] = shared
	s.localECDH[peer.ID] = append([]byte(nil), localPrivate.PublicKey().Bytes()...)
	s.mu.Unlock()
	return peer, nil
}

func (s *Service) LocalECDHEPublicKey(peerID uuid.UUID) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key, ok := s.localECDH[peerID]
	if !ok {
		return nil, ErrNoSharedSecret
	}
	return append([]byte(nil), key...), nil
}

// EstablishSession derives and installs an ephemeral session key. It is used
// by the initiating side after receiving the handshake response.
func (s *Service) EstablishSession(peerID uuid.UUID, localPrivateKey, remotePublicKey []byte) error {
	shared, err := DeriveSharedSecret(localPrivateKey, remotePublicKey)
	if err != nil {
		return ErrInvalidInput
	}
	s.mu.Lock()
	s.sessions[peerID] = shared
	s.mu.Unlock()
	return nil
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
