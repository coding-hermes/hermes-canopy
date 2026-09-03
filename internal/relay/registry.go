package relay

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

var (
	ErrProvisioningTokenInvalid = errors.New("relay: invalid or expired provisioning token")
	ErrProvisioningTokenScope   = errors.New("relay: provisioning token tenant scope mismatch")
	ErrProvisioningTokenUsed    = errors.New("relay: provisioning token already used")
	ErrInstanceNotFound         = errors.New("relay: instance not found")
)

// RelayRegistry manages instance registration, tenant assignment,
// and discovery of available relay nodes.
type RelayRegistry struct {
	db                 *pgxpool.Pool
	mode               transport.DeploymentMode
	provisioningSecret []byte
	mu                 sync.Mutex
	usedTokens         map[string]struct{}
}

// Instance represents a registered relay instance (SaaS) or
// a self-hosted relay node (self-hosted mode).
type Instance struct {
	InstanceID  uuid.UUID  `json:"instance_id"`
	TenantID    uuid.UUID  `json:"tenant_id,omitempty"` // set in SaaS mode
	PublicKey   []byte     `json:"public_key"`          // Ed25519 public key
	ListenAddr  string     `json:"listen_addr"`
	Tier        string     `json:"tier"` // "free", "pro", "enterprise" (SaaS)
	Enabled     bool       `json:"enabled"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// RegisterRequest is the payload for registering a new relay instance.
type RegisterRequest struct {
	TenantID          uuid.UUID `json:"tenant_id"`          // SaaS: assigned by admin
	PublicKey         []byte    `json:"public_key"`         // Ed25519 public key
	ListenAddr        string    `json:"listen_addr"`        // public address of this relay node
	Tier              string    `json:"tier"`               // SaaS: "free", "pro", "enterprise"
	ProvisioningToken string    `json:"provisioning_token"` // SaaS: one-time JWT
}

// RegisterResponse is returned after successful registration.
type RegisterResponse struct {
	InstanceID  uuid.UUID `json:"instance_id"`
	RelaySecret string    `json:"relay_secret"` // pre-shared HMAC base key
	CreatedAt   time.Time `json:"created_at"`
}

// RelayNodeInfo is returned by the discovery endpoint.
type RelayNodeInfo struct {
	InstanceID uuid.UUID `json:"instance_id"`
	ListenAddr string    `json:"listen_addr"`
	Load       float64   `json:"load"` // 0.0 (idle) to 1.0 (full)
	Region     string    `json:"region,omitempty"`
}

func NewRelayRegistry(db *pgxpool.Pool, mode, provisioningSecret string) *RelayRegistry {
	deploymentMode := transport.ModeAirGapped
	switch mode {
	case ModeSelfHosted:
		deploymentMode = transport.ModeSelfHosted
	case ModeSaaS:
		deploymentMode = transport.ModeSaaS
	}
	return &RelayRegistry{db: db, mode: deploymentMode, provisioningSecret: []byte(provisioningSecret), usedTokens: make(map[string]struct{})}
}

func (reg *RelayRegistry) DiscoveryAPIEnabled() bool {
	return reg != nil && reg.mode == transport.ModeSaaS
}

// RegisterInstance registers a relay instance.
// In SaaS mode, validates the provisioning token and assigns the tenant.
// In self-hosted mode, generates a new instance identity.
func (reg *RelayRegistry) RegisterInstance(ctx context.Context, req *RegisterRequest) (*RegisterResponse, error) {
	instanceID := uuid.New()
	createdAt := time.Now().UTC()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("relay: generate secret: %w", err)
	}
	if reg.mode == transport.ModeSelfHosted {
		return &RegisterResponse{InstanceID: instanceID, RelaySecret: base64.StdEncoding.EncodeToString(secret), CreatedAt: createdAt}, nil
	}
	if reg.mode != transport.ModeSaaS {
		return nil, ErrProvisioningTokenInvalid
	}

	tenantID, tokenID, err := reg.validateProvisioningToken(req.ProvisioningToken)
	if err != nil {
		return nil, err
	}
	if req.TenantID != uuid.Nil && req.TenantID != tenantID {
		return nil, ErrProvisioningTokenScope
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if _, used := reg.usedTokens[tokenID]; used {
		return nil, ErrProvisioningTokenUsed
	}
	_, err = reg.db.Exec(ctx, `INSERT INTO relay_instances
		(instance_id, tenant_id, public_key, listen_addr, tier, connected_at, last_heartbeat_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$6,$6,$6)`, instanceID, tenantID, req.PublicKey, req.ListenAddr, req.Tier, createdAt)
	if err != nil {
		return nil, fmt.Errorf("relay: register instance: %w", err)
	}
	reg.usedTokens[tokenID] = struct{}{}
	return &RegisterResponse{InstanceID: instanceID, RelaySecret: base64.StdEncoding.EncodeToString(secret), CreatedAt: createdAt}, nil
}

func (reg *RelayRegistry) validateProvisioningToken(raw string) (uuid.UUID, string, error) {
	token, err := jwt.Parse(raw, func(token *jwt.Token) (any, error) {
		return reg.provisioningSecret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !token.Valid {
		return uuid.Nil, "", ErrProvisioningTokenInvalid
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return uuid.Nil, "", ErrProvisioningTokenInvalid
	}
	tenantText, _ := claims["tenant_id"].(string)
	tenantID, err := uuid.Parse(tenantText)
	if err != nil || tenantID == uuid.Nil {
		return uuid.Nil, "", ErrProvisioningTokenScope
	}
	tokenID, _ := claims["jti"].(string)
	if tokenID == "" {
		return uuid.Nil, "", ErrProvisioningTokenInvalid
	}
	return tenantID, tokenID, nil
}

// GetAvailableRelays returns relay nodes for a tenant, ordered by load.
// Excludes disabled and unhealthy nodes. Only used in SaaS mode.
func (reg *RelayRegistry) GetAvailableRelays(ctx context.Context, tenantID uuid.UUID) ([]RelayNodeInfo, error) {
	rows, err := reg.db.Query(ctx, `SELECT instance_id, listen_addr, load_factor, COALESCE(region, '')
		FROM relay_instances WHERE tenant_id=$1 AND enabled=true AND load_factor < 0.95
		AND last_heartbeat_at > now() - interval '90 seconds' ORDER BY load_factor ASC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("relay: discover instances: %w", err)
	}
	defer rows.Close()
	relays := make([]RelayNodeInfo, 0)
	for rows.Next() {
		var node RelayNodeInfo
		if err := rows.Scan(&node.InstanceID, &node.ListenAddr, &node.Load, &node.Region); err != nil {
			return nil, err
		}
		relays = append(relays, node)
	}
	return relays, rows.Err()
}

// GetInstanceByID retrieves a single instance record.
func (reg *RelayRegistry) GetInstanceByID(ctx context.Context, instanceID uuid.UUID) (*Instance, error) {
	var instance Instance
	err := reg.db.QueryRow(ctx, `SELECT instance_id, tenant_id, public_key, listen_addr, tier, enabled,
		connected_at, created_at, updated_at FROM relay_instances WHERE instance_id=$1`, instanceID).Scan(
		&instance.InstanceID, &instance.TenantID, &instance.PublicKey, &instance.ListenAddr, &instance.Tier,
		&instance.Enabled, &instance.ConnectedAt, &instance.CreatedAt, &instance.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInstanceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("relay: get instance: %w", err)
	}
	return &instance, nil
}

// UpdateInstanceHeartbeat updates the last-seen time for an instance.
// Called on every successful HELLO handshake.
func (reg *RelayRegistry) UpdateInstanceHeartbeat(ctx context.Context, instanceID uuid.UUID) error {
	tag, err := reg.db.Exec(ctx, `UPDATE relay_instances SET connected_at=COALESCE(connected_at, now()),
		last_heartbeat_at=now(), updated_at=now() WHERE instance_id=$1`, instanceID)
	if err != nil {
		return fmt.Errorf("relay: update heartbeat: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInstanceNotFound
	}
	return nil
}

func (reg *RelayRegistry) ResolveInstanceTenant(ctx context.Context, instanceID uuid.UUID) (uuid.UUID, string, error) {
	var tenantID uuid.UUID
	var tier string
	err := reg.db.QueryRow(ctx, `SELECT i.tenant_id,t.tier FROM relay_instances i
		JOIN tenants t ON t.tenant_id=i.tenant_id WHERE i.instance_id=$1 AND i.enabled=true`, instanceID).Scan(&tenantID, &tier)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", ErrInstanceNotFound
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("relay: resolve instance tenant: %w", err)
	}
	return tenantID, tier, nil
}

func (reg *RelayRegistry) OpenSession(ctx context.Context, s *RelaySession) error {
	_, err := reg.db.Exec(ctx, `INSERT INTO relay_sessions (session_id,instance_id,tenant_id,remote_addr,established_at,last_activity_at)
		VALUES ($1,$2,$3,$4,$5,$5)`, s.ID, s.InstanceID, s.TenantID, s.Conn.RemoteAddr().String(), s.Established)
	if err != nil {
		return fmt.Errorf("relay: record session: %w", err)
	}
	return nil
}

func (reg *RelayRegistry) DeleteInstance(ctx context.Context, instanceID uuid.UUID) error {
	tag, err := reg.db.Exec(ctx, `DELETE FROM relay_instances WHERE instance_id=$1`, instanceID)
	if err != nil {
		return fmt.Errorf("relay: delete instance: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInstanceNotFound
	}
	return nil
}
