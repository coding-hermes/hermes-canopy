// Package federation implements the Phase P1 federation link and handshake
// lifecycle from SPEC-FTR-02. Transport encryption and event relay are deferred
// to later phases.
package federation

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type FederationRole int

const (
	RoleInitiator FederationRole = iota
	RoleAcceptor
)

type PeerState int

const (
	PeerDisconnected PeerState = iota
	PeerConnecting
	PeerConnected
	PeerReconnecting
	PeerRevoked
	PeerQuarantined
)

func (s PeerState) String() string {
	switch s {
	case PeerDisconnected:
		return "disconnected"
	case PeerConnecting:
		return "connecting"
	case PeerConnected:
		return "connected"
	case PeerReconnecting:
		return "reconnecting"
	case PeerRevoked:
		return "revoked"
	case PeerQuarantined:
		return "quarantined"
	default:
		return "unknown"
	}
}

type FederationPeer struct {
	ID             uuid.UUID      `json:"id"`
	ServerURL      string         `json:"server_url"`
	SigningKeyFP   string         `json:"signing_key_fp"`
	ECDHEPublicKey []byte         `json:"ecdhe_public_key,omitempty"`
	Role           FederationRole `json:"role"`
	State          PeerState      `json:"state"`
	TreeID         uuid.UUID      `json:"tree_id"`
	CreatedBy      uuid.UUID      `json:"created_by"`
	CreatedAt      time.Time      `json:"created_at"`
	ConnectedAt    *time.Time     `json:"connected_at,omitempty"`
	LastHeartbeat  *time.Time     `json:"last_heartbeat,omitempty"`
	RevokedAt      *time.Time     `json:"revoked_at,omitempty"`
	RevokeReason   *string        `json:"revoke_reason,omitempty"`
}

var (
	ErrFederationNotFound = errors.New("federation: peer not found")
	ErrTokenInvalid       = errors.New("federation: federation token is invalid or expired")
	ErrTokenExpired       = errors.New("federation: federation token has expired")
	ErrLinkAlreadyExists  = errors.New("federation: link to this server+tree already exists")
	ErrLinkRevoked        = errors.New("federation: federation link has been revoked")
	ErrInvalidInput       = errors.New("federation: invalid input")
)

type Repository interface {
	Create(context.Context, *FederationPeer) (*FederationPeer, error)
	UpsertAccepted(context.Context, *FederationPeer) (*FederationPeer, error)
	Get(context.Context, uuid.UUID) (*FederationPeer, error)
	FindByServerTree(context.Context, string, uuid.UUID) (*FederationPeer, error)
	List(context.Context, *uuid.UUID, bool) ([]*FederationPeer, error)
	SetState(context.Context, uuid.UUID, PeerState, *time.Time, *string) error
}

type FederationService interface {
	CreateFederationLink(context.Context, string, uuid.UUID, uuid.UUID) (*FederationPeer, string, error)
	AcceptFederationLink(context.Context, string, string, []byte) (*FederationPeer, error)
	RevokeFederationLink(context.Context, uuid.UUID) error
	GetPeer(context.Context, uuid.UUID) (*FederationPeer, error)
	ListPeers(context.Context, uuid.UUID) ([]*FederationPeer, error)
	ListAllPeers(context.Context) ([]*FederationPeer, error)
	GenerateToken(uuid.UUID, uuid.UUID) (string, error)
	VerifyToken(string) (*TokenClaims, error)
}
