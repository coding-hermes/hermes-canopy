// Package db provides the PostgreSQL data layer for Canopy.
//
// This file implements persistence for the transport adapter layer
// (transport_connections, transport_configs, transport_events tables).
// SPEC-FTR-04 §4 — DDL.

package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TransportConnection maps to the transport_connections table.
type TransportConnection struct {
	ID            uuid.UUID              `db:"id"              json:"id"`
	PeerID        string                 `db:"peer_id"         json:"peerId"`
	TransportType string                 `db:"transport_type"  json:"transportType"`
	State         string                 `db:"state"           json:"state"`
	Target        string                 `db:"target"          json:"target"`
	EstablishedAt *time.Time             `db:"established_at"  json:"establishedAt,omitempty"`
	LastActivity  time.Time              `db:"last_activity"   json:"lastActivity"`
	SequenceHigh  int64                  `db:"sequence_high"   json:"sequenceHigh"`
	Metadata      map[string]interface{} `db:"metadata"        json:"metadata"`
	CreatedAt     time.Time              `db:"created_at"      json:"createdAt"`
	UpdatedAt     time.Time              `db:"updated_at"      json:"updatedAt"`
}

// TransportConfig maps to the transport_configs table.
type TransportConfig struct {
	TransportType  string                 `db:"transport_type"   json:"transportType"`
	Enabled        bool                   `db:"enabled"          json:"enabled"`
	MaxMessageSize int64                  `db:"max_message_size" json:"maxMessageSize"`
	HeartbeatSecs  int                    `db:"heartbeat_secs"   json:"heartbeatSecs"`
	ConnectTimeout int                    `db:"connect_timeout"  json:"connectTimeout"`
	RetryMax       int                    `db:"retry_max"        json:"retryMax"`
	ConfigJSON     map[string]interface{} `db:"config_json"       json:"configJson"`
	CreatedAt      time.Time              `db:"created_at"       json:"createdAt"`
	UpdatedAt      time.Time              `db:"updated_at"       json:"updatedAt"`
}

// TransportEvent maps to the transport_events (append-only audit log).
type TransportEvent struct {
	ID            uuid.UUID              `db:"id"             json:"id"`
	ConnectionID  *uuid.UUID             `db:"connection_id"  json:"connectionId,omitempty"`
	TransportType string                 `db:"transport_type" json:"transportType"`
	EventType     string                 `db:"event_type"     json:"eventType"`
	PeerID        *string                `db:"peer_id"        json:"peerId,omitempty"`
	Details       map[string]interface{} `db:"details"        json:"details"`
	CreatedAt     time.Time              `db:"created_at"     json:"createdAt"`
}

// TransportConnectionRepo persists transport connections.
type TransportConnectionRepo interface {
	Create(ctx context.Context, conn *TransportConnection) (*TransportConnection, error)
	GetByPeer(ctx context.Context, peerID, transportType string) ([]TransportConnection, error)
	UpdateState(ctx context.Context, id uuid.UUID, state string, seqHigh int64) error
	ListActive(ctx context.Context) ([]TransportConnection, error)
}

// TransportConfigRepo persists per-transport configuration.
type TransportConfigRepo interface {
	Get(ctx context.Context, transportType string) (*TransportConfig, error)
	Upsert(ctx context.Context, cfg *TransportConfig) (*TransportConfig, error)
	List(ctx context.Context) ([]TransportConfig, error)
	Disable(ctx context.Context, transportType string) error
}

// TransportEventRepo persists transport-level audit events.
type TransportEventRepo interface {
	Insert(ctx context.Context, event *TransportEvent) (*TransportEvent, error)
	ListByConnection(ctx context.Context, connectionID uuid.UUID, limit, offset int) ([]TransportEvent, error)
	PruneOld(ctx context.Context, before time.Time) (int64, error)
}

// PGTransportConnectionRepo is the pgx implementation.
type PGTransportConnectionRepo struct {
	pool *pgxpool.Pool
}

func NewPGTransportConnectionRepo(pool *pgxpool.Pool) *PGTransportConnectionRepo {
	return &PGTransportConnectionRepo{pool: pool}
}

func (r *PGTransportConnectionRepo) Create(ctx context.Context, conn *TransportConnection) (*TransportConnection, error) {
	if conn.ID == uuid.Nil {
		conn.ID = uuid.New()
	}
	now := time.Now().UTC()
	conn.CreatedAt = now
	conn.UpdatedAt = now
	if conn.LastActivity.IsZero() {
		conn.LastActivity = now
	}
	metaJSON, err := json.Marshal(conn.Metadata)
	if err != nil {
		return nil, fmt.Errorf("transport_connection: marshal metadata: %w", err)
	}
	row := r.pool.QueryRow(ctx,
		`INSERT INTO transport_connections
		 (id, peer_id, transport_type, state, target, established_at, last_activity, sequence_high, metadata, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		 RETURNING id, peer_id, transport_type, state, target, established_at, last_activity, sequence_high, metadata, created_at, updated_at`,
		conn.ID, conn.PeerID, conn.TransportType, conn.State, conn.Target,
		conn.EstablishedAt, conn.LastActivity, conn.SequenceHigh, metaJSON,
		conn.CreatedAt, conn.UpdatedAt,
	)
	return scanTransportConnection(row)
}

func (r *PGTransportConnectionRepo) GetByPeer(ctx context.Context, peerID, transportType string) ([]TransportConnection, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, peer_id, transport_type, state, target, established_at, last_activity, sequence_high, metadata, created_at, updated_at
		 FROM transport_connections WHERE peer_id = $1 AND transport_type = $2
		 ORDER BY created_at DESC`,
		peerID, transportType,
	)
	if err != nil {
		return nil, fmt.Errorf("transport_connection: get by peer: %w", err)
	}
	defer rows.Close()
	return scanTransportConnections(rows)
}

func (r *PGTransportConnectionRepo) UpdateState(ctx context.Context, id uuid.UUID, state string, seqHigh int64) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE transport_connections SET state = $1, sequence_high = GREATEST(sequence_high, $2), last_activity = now(), updated_at = now() WHERE id = $3`,
		state, seqHigh, id,
	)
	if err != nil {
		return fmt.Errorf("transport_connection: update state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("transport_connection: not found")
	}
	return nil
}

func (r *PGTransportConnectionRepo) ListActive(ctx context.Context) ([]TransportConnection, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, peer_id, transport_type, state, target, established_at, last_activity, sequence_high, metadata, created_at, updated_at
		 FROM transport_connections WHERE state IN ('active','degraded')
		 ORDER BY last_activity DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("transport_connection: list active: %w", err)
	}
	defer rows.Close()
	return scanTransportConnections(rows)
}

// PGTransportConfigRepo is the pgx implementation.
type PGTransportConfigRepo struct {
	pool *pgxpool.Pool
}

func NewPGTransportConfigRepo(pool *pgxpool.Pool) *PGTransportConfigRepo {
	return &PGTransportConfigRepo{pool: pool}
}

func (r *PGTransportConfigRepo) Get(ctx context.Context, transportType string) (*TransportConfig, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT transport_type, enabled, max_message_size, heartbeat_secs, connect_timeout, retry_max, config_json, created_at, updated_at
		 FROM transport_configs WHERE transport_type = $1`,
		transportType,
	)
	return scanTransportConfig(row)
}

func (r *PGTransportConfigRepo) Upsert(ctx context.Context, cfg *TransportConfig) (*TransportConfig, error) {
	now := time.Now().UTC()
	cfgJSON, err := json.Marshal(cfg.ConfigJSON)
	if err != nil {
		return nil, fmt.Errorf("transport_config: marshal config_json: %w", err)
	}
	row := r.pool.QueryRow(ctx,
		`INSERT INTO transport_configs (transport_type, enabled, max_message_size, heartbeat_secs, connect_timeout, retry_max, config_json, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (transport_type) DO UPDATE SET
		   enabled = EXCLUDED.enabled,
		   max_message_size = EXCLUDED.max_message_size,
		   heartbeat_secs = EXCLUDED.heartbeat_secs,
		   connect_timeout = EXCLUDED.connect_timeout,
		   retry_max = EXCLUDED.retry_max,
		   config_json = EXCLUDED.config_json,
		   updated_at = now()
		 RETURNING transport_type, enabled, max_message_size, heartbeat_secs, connect_timeout, retry_max, config_json, created_at, updated_at`,
		cfg.TransportType, cfg.Enabled, cfg.MaxMessageSize, cfg.HeartbeatSecs,
		cfg.ConnectTimeout, cfg.RetryMax, cfgJSON, now, now,
	)
	return scanTransportConfig(row)
}

func (r *PGTransportConfigRepo) List(ctx context.Context) ([]TransportConfig, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT transport_type, enabled, max_message_size, heartbeat_secs, connect_timeout, retry_max, config_json, created_at, updated_at
		 FROM transport_configs ORDER BY transport_type`,
	)
	if err != nil {
		return nil, fmt.Errorf("transport_config: list: %w", err)
	}
	defer rows.Close()
	return scanTransportConfigs(rows)
}

func (r *PGTransportConfigRepo) Disable(ctx context.Context, transportType string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE transport_configs SET enabled = false, updated_at = now() WHERE transport_type = $1`,
		transportType,
	)
	if err != nil {
		return fmt.Errorf("transport_config: disable: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("transport_config: not found")
	}
	return nil
}

// PGTransportEventRepo is the pgx implementation.
type PGTransportEventRepo struct {
	pool *pgxpool.Pool
}

func NewPGTransportEventRepo(pool *pgxpool.Pool) *PGTransportEventRepo {
	return &PGTransportEventRepo{pool: pool}
}

func (r *PGTransportEventRepo) Insert(ctx context.Context, event *TransportEvent) (*TransportEvent, error) {
	if event.ID == uuid.Nil {
		event.ID = uuid.New()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	detailsJSON, err := json.Marshal(event.Details)
	if err != nil {
		return nil, fmt.Errorf("transport_event: marshal details: %w", err)
	}
	row := r.pool.QueryRow(ctx,
		`INSERT INTO transport_events (id, connection_id, transport_type, event_type, peer_id, details, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 RETURNING id, connection_id, transport_type, event_type, peer_id, details, created_at`,
		event.ID, event.ConnectionID, event.TransportType, event.EventType,
		event.PeerID, detailsJSON, event.CreatedAt,
	)
	return scanTransportEvent(row)
}

func (r *PGTransportEventRepo) ListByConnection(ctx context.Context, connectionID uuid.UUID, limit, offset int) ([]TransportEvent, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, connection_id, transport_type, event_type, peer_id, details, created_at
		 FROM transport_events WHERE connection_id = $1
		 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		connectionID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("transport_event: list by connection: %w", err)
	}
	defer rows.Close()
	return scanTransportEvents(rows)
}

func (r *PGTransportEventRepo) PruneOld(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM transport_events WHERE created_at < $1`, before,
	)
	if err != nil {
		return 0, fmt.Errorf("transport_event: prune: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Scan helpers
func scanTransportConnection(row pgx.Row) (*TransportConnection, error) {
	var metaJSON []byte
	c := &TransportConnection{}
	err := row.Scan(&c.ID, &c.PeerID, &c.TransportType, &c.State, &c.Target,
		&c.EstablishedAt, &c.LastActivity, &c.SequenceHigh, &metaJSON,
		&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("transport_connection: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("transport_connection: scan: %w", err)
	}
	if len(metaJSON) > 0 {
		_ = json.Unmarshal(metaJSON, &c.Metadata)
	}
	return c, nil
}

func scanTransportConnections(rows pgx.Rows) ([]TransportConnection, error) {
	var results []TransportConnection
	for rows.Next() {
		var metaJSON []byte
		c := TransportConnection{}
		err := rows.Scan(&c.ID, &c.PeerID, &c.TransportType, &c.State, &c.Target,
			&c.EstablishedAt, &c.LastActivity, &c.SequenceHigh, &metaJSON,
			&c.CreatedAt, &c.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("transport_connection: scan row: %w", err)
		}
		if len(metaJSON) > 0 {
			_ = json.Unmarshal(metaJSON, &c.Metadata)
		}
		results = append(results, c)
	}
	return results, rows.Err()
}

func scanTransportConfig(row pgx.Row) (*TransportConfig, error) {
	var cfgJSON []byte
	c := &TransportConfig{}
	err := row.Scan(&c.TransportType, &c.Enabled, &c.MaxMessageSize, &c.HeartbeatSecs,
		&c.ConnectTimeout, &c.RetryMax, &cfgJSON, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("transport_config: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("transport_config: scan: %w", err)
	}
	if len(cfgJSON) > 0 {
		_ = json.Unmarshal(cfgJSON, &c.ConfigJSON)
	}
	return c, nil
}

func scanTransportConfigs(rows pgx.Rows) ([]TransportConfig, error) {
	var results []TransportConfig
	for rows.Next() {
		var cfgJSON []byte
		c := TransportConfig{}
		err := rows.Scan(&c.TransportType, &c.Enabled, &c.MaxMessageSize, &c.HeartbeatSecs,
			&c.ConnectTimeout, &c.RetryMax, &cfgJSON, &c.CreatedAt, &c.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("transport_config: scan row: %w", err)
		}
		if len(cfgJSON) > 0 {
			_ = json.Unmarshal(cfgJSON, &c.ConfigJSON)
		}
		results = append(results, c)
	}
	return results, rows.Err()
}

func scanTransportEvent(row pgx.Row) (*TransportEvent, error) {
	var detJSON []byte
	e := &TransportEvent{}
	err := row.Scan(&e.ID, &e.ConnectionID, &e.TransportType, &e.EventType,
		&e.PeerID, &detJSON, &e.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("transport_event: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("transport_event: scan: %w", err)
	}
	if len(detJSON) > 0 {
		_ = json.Unmarshal(detJSON, &e.Details)
	}
	return e, nil
}

func scanTransportEvents(rows pgx.Rows) ([]TransportEvent, error) {
	var results []TransportEvent
	for rows.Next() {
		var detJSON []byte
		e := TransportEvent{}
		err := rows.Scan(&e.ID, &e.ConnectionID, &e.TransportType, &e.EventType,
			&e.PeerID, &detJSON, &e.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("transport_event: scan row: %w", err)
		}
		if len(detJSON) > 0 {
			_ = json.Unmarshal(detJSON, &e.Details)
		}
		results = append(results, e)
	}
	return results, rows.Err()
}
