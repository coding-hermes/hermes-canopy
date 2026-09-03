package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ModeSelfHosted = "self_hosted"
	ModeSaaS       = "saas"
	ModeAirGapped  = "air_gapped"
)

// DeploymentConfig is the durable configuration for the relay coordinator.
type DeploymentConfig struct {
	InstanceID       uuid.UUID  `json:"instance_id"`
	Mode             string     `json:"deployment_mode"`
	ListenAddr       string     `json:"listen_addr"`
	ConnectAddr      string     `json:"connect_addr"`
	MaxSessions      int        `json:"max_sessions"`
	HeartbeatSecs    int        `json:"heartbeat_secs"`
	DrainTimeoutSecs int        `json:"drain_timeout_secs"`
	TLSEnabled       bool       `json:"tls_enabled"`
	TLSCertFile      *string    `json:"tls_cert_file"`
	TLSKeyFile       *string    `json:"tls_key_file"`
	TLSCAFile        *string    `json:"tls_ca_file"`
	TLSMutual        bool       `json:"tls_mutual"`
	HMACKeyRotatedAt *time.Time `json:"hmac_key_rotated_at"`
	HMACKeyID        int        `json:"hmac_key_id"`
	// HMACKeyRotateInterval defaults to 168 hours (7 days).
	HMACKeyRotateInterval time.Duration `json:"-"`
	// HMAC keys are runtime secrets and are deliberately not persisted in relay_config.
	HMACKey       []byte `json:"-"`
	HMACKeyPrev   []byte `json:"-"`
	HMACKeyPrevID int    `json:"-"`
	Enabled       bool   `json:"enabled"`
}

func DefaultConfig() DeploymentConfig {
	return DeploymentConfig{Mode: ModeAirGapped, MaxSessions: 500, HeartbeatSecs: 30, DrainTimeoutSecs: 30, HMACKeyRotateInterval: 168 * time.Hour}
}

// Validate rejects configuration that cannot safely start a relay.
func (c DeploymentConfig) Validate() error {
	switch c.Mode {
	case ModeSelfHosted, ModeSaaS, ModeAirGapped:
	default:
		return fmt.Errorf("relay: invalid mode %q", c.Mode)
	}
	if c.MaxSessions <= 0 {
		return errors.New("relay: max sessions must be greater than zero")
	}
	// The durable counter is uint32 by contract, but the shipped frame header is
	// uint16. Refuse values that would wrap and reuse an on-wire key ID.
	if c.HMACKeyID < 0 || c.HMACKeyID > 65535 {
		return errors.New("relay: HMAC key ID exceeds uint16 frame limit")
	}
	if c.HeartbeatSecs <= 0 || c.DrainTimeoutSecs <= 0 {
		return errors.New("relay: heartbeat and drain timeout must be greater than zero")
	}
	for name, addr := range map[string]string{"listen": c.ListenAddr, "connect": c.ConnectAddr} {
		if err := validateAddress(addr); err != nil {
			return fmt.Errorf("relay: invalid %s address: %w", name, err)
		}
	}
	return nil
}

func validateAddress(addr string) error {
	if addr == "" {
		return nil
	}
	u, err := url.Parse(addr)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("expected scheme://host:port")
	}
	if u.Scheme != "tcp" && u.Scheme != "quic" {
		return fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return errors.New("expected host:port")
	}
	if port == "" || strings.HasPrefix(port, "-") {
		return errors.New("port is required")
	}
	p, err := net.LookupPort("tcp", port)
	if err != nil || p < 1 || p > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

// DeploymentConfigManager loads, persists, and hot-reloads relay configuration.
type DeploymentConfigManager struct {
	mu      sync.RWMutex
	current DeploymentConfig
}

func NewDeploymentConfigManager() *DeploymentConfigManager {
	return &DeploymentConfigManager{current: DefaultConfig()}
}

func (m *DeploymentConfigManager) Current() DeploymentConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

func (m *DeploymentConfigManager) Load(ctx context.Context, db *pgxpool.Pool) (DeploymentConfig, error) {
	cfg := DefaultConfig()
	err := db.QueryRow(ctx, `SELECT instance_id, deployment_mode, heartbeat_secs, drain_timeout_secs,
		tls_enabled, tls_cert_file, tls_key_file, tls_ca_file, tls_mutual,
		hmac_key_rotated_at, hmac_key_id, enabled
		FROM relay_config LIMIT 1`).Scan(&cfg.InstanceID, &cfg.Mode,
		&cfg.HeartbeatSecs, &cfg.DrainTimeoutSecs,
		&cfg.TLSEnabled, &cfg.TLSCertFile, &cfg.TLSKeyFile, &cfg.TLSCAFile, &cfg.TLSMutual,
		&cfg.HMACKeyRotatedAt, &cfg.HMACKeyID, &cfg.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return DeploymentConfig{}, fmt.Errorf("relay: load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return DeploymentConfig{}, err
	}
	m.mu.Lock()
	m.current = cfg
	m.mu.Unlock()
	return cfg, nil
}

func (m *DeploymentConfigManager) Save(ctx context.Context, db *pgxpool.Pool, cfg DeploymentConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	row := db.QueryRow(ctx, `WITH updated AS (
		UPDATE relay_config SET deployment_mode=$1, heartbeat_secs=$2, drain_timeout_secs=$3,
			tls_enabled=$4, tls_cert_file=$5, tls_key_file=$6, tls_ca_file=$7, tls_mutual=$8,
			hmac_key_rotated_at=$9, hmac_key_id=$10, enabled=$11
		RETURNING instance_id
	), inserted AS (INSERT INTO relay_config (deployment_mode, heartbeat_secs, drain_timeout_secs,
		tls_enabled, tls_cert_file, tls_key_file, tls_ca_file, tls_mutual,
		hmac_key_rotated_at, hmac_key_id, enabled)
	SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11
	WHERE NOT EXISTS (SELECT 1 FROM updated)
	RETURNING instance_id)
	SELECT instance_id FROM updated
	UNION ALL
	SELECT instance_id FROM inserted`,
		cfg.Mode, cfg.HeartbeatSecs, cfg.DrainTimeoutSecs,
		cfg.TLSEnabled, cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSCAFile, cfg.TLSMutual,
		cfg.HMACKeyRotatedAt, cfg.HMACKeyID, cfg.Enabled)
	var instanceID uuid.UUID
	err := row.Scan(&instanceID)
	if err != nil {
		return fmt.Errorf("relay: save config: %w", err)
	}
	cfg.InstanceID = instanceID
	m.mu.Lock()
	m.current = cfg
	m.mu.Unlock()
	return nil
}

func (m *DeploymentConfigManager) Reload(ctx context.Context, db *pgxpool.Pool) (DeploymentConfig, error) {
	return m.Load(ctx, db)
}

// PersistHMACRotation atomically records the non-secret rotation metadata.
func (m *DeploymentConfigManager) PersistHMACRotation(ctx context.Context, db *pgxpool.Pool, rotatedAt time.Time, keyID uint32) error {
	result, err := db.Exec(ctx, `UPDATE relay_config SET hmac_key_rotated_at=$1, hmac_key_id=$2`, rotatedAt, keyID)
	if err != nil {
		return fmt.Errorf("relay: persist HMAC rotation: %w", err)
	}
	if result.RowsAffected() == 0 {
		return errors.New("relay: persist HMAC rotation: relay_config row not found")
	}
	m.mu.Lock()
	m.current.HMACKeyRotatedAt, m.current.HMACKeyID = &rotatedAt, int(keyID)
	m.mu.Unlock()
	return nil
}
