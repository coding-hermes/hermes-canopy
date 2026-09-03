package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

var (
	ErrTenantNotFound = errors.New("relay: tenant not found")
	ErrInvalidTier    = errors.New("relay: invalid tenant tier")
)

type Tenant struct {
	TenantID  uuid.UUID `json:"tenant_id"`
	Name      string    `json:"name"`
	Tier      string    `json:"tier"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type TenantRepository struct {
	db     *pgxpool.Pool
	events sse.SSEHub
}

func NewTenantRepository(db *pgxpool.Pool) *TenantRepository { return &TenantRepository{db: db} }

func (r *TenantRepository) SetEventHub(hub sse.SSEHub) { r.events = hub }

func (r *TenantRepository) publish(t *Tenant, eventType string) {
	if r.events == nil {
		return
	}
	b, _ := json.Marshal(map[string]any{"event_type": eventType, "tenant_id": t.TenantID, "tier": t.Tier})
	r.events.Broadcast(uuid.Nil, sse.SSEEvent{Type: "relay_tenant_event", Data: b})
}

func validTenantTier(tier string) bool {
	return tier == "free" || tier == "pro" || tier == "enterprise"
}

func (r *TenantRepository) CreateTenant(ctx context.Context, name, tier string) (*Tenant, error) {
	if !validTenantTier(tier) {
		return nil, ErrInvalidTier
	}
	var t Tenant
	err := r.db.QueryRow(ctx, `INSERT INTO tenants (name,tier) VALUES ($1,$2)
		RETURNING tenant_id,name,tier,created_at,updated_at`, name, tier).Scan(&t.TenantID, &t.Name, &t.Tier, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("relay: create tenant: %w", err)
	}
	r.publish(&t, "created")
	return &t, nil
}

func (r *TenantRepository) GetTenant(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	var t Tenant
	err := r.db.QueryRow(ctx, `SELECT tenant_id,name,tier,created_at,updated_at FROM tenants WHERE tenant_id=$1`, id).Scan(&t.TenantID, &t.Name, &t.Tier, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("relay: get tenant: %w", err)
	}
	return &t, nil
}

func (r *TenantRepository) UpdateTier(ctx context.Context, id uuid.UUID, tier string) (*Tenant, error) {
	if !validTenantTier(tier) {
		return nil, ErrInvalidTier
	}
	var t Tenant
	err := r.db.QueryRow(ctx, `UPDATE tenants SET tier=$2,updated_at=now() WHERE tenant_id=$1
		RETURNING tenant_id,name,tier,created_at,updated_at`, id, tier).Scan(&t.TenantID, &t.Name, &t.Tier, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("relay: update tenant tier: %w", err)
	}
	r.publish(&t, "tier_changed")
	return &t, nil
}

func (r *TenantRepository) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := r.db.Query(ctx, `SELECT tenant_id,name,tier,created_at,updated_at FROM tenants ORDER BY created_at,tenant_id`)
	if err != nil {
		return nil, fmt.Errorf("relay: list tenants: %w", err)
	}
	defer rows.Close()
	out := make([]Tenant, 0)
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.TenantID, &t.Name, &t.Tier, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
