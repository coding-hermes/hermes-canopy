package relay

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/testutil"
	"github.com/coding-hermes/hermes-canopy/migrations"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newRegistryPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	testutil.SkipIfNoDB(t)
	ctx := context.Background()
	dsn := os.Getenv("CANOPY_TEST_DB_URL")
	if dsn == "" {
		dsn = "postgres://canopy:canopy@localhost:5437/canopy?sslmode=disable"
	}
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "relay_registry_" + uuid.New().String()[:8]
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE tenants (tenant_id UUID PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	ddl, err := migrations.FS().ReadFile("000041_relay_registry.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(ddl)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return pool
}

func provisioningToken(t *testing.T, secret string, tenantID uuid.UUID, tokenID string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"tenant_id": tenantID.String(), "jti": tokenID, "exp": time.Now().Add(time.Hour).Unix(),
	})
	raw, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRelayRegistryLivePostgres(t *testing.T) {
	pool := newRegistryPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants (tenant_id) VALUES ($1)`, tenantID); err != nil {
		t.Fatal(err)
	}
	registry := NewRelayRegistry(pool, ModeSaaS, "provisioning-secret")
	req := &RegisterRequest{TenantID: tenantID, PublicKey: make([]byte, 32), ListenAddr: "tcp://relay-a:9443", Tier: "pro"}
	req.ProvisioningToken = provisioningToken(t, "provisioning-secret", tenantID, "once")

	registered, err := registry.RegisterInstance(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := registry.GetInstanceByID(ctx, registered.InstanceID)
	if err != nil || instance.TenantID != tenantID || instance.ListenAddr != req.ListenAddr {
		t.Fatalf("instance=%+v err=%v", instance, err)
	}
	if _, err := registry.RegisterInstance(ctx, req); err != ErrProvisioningTokenUsed {
		t.Fatalf("duplicate token err=%v", err)
	}
	if err := registry.UpdateInstanceHeartbeat(ctx, registered.InstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE relay_instances SET load_factor=.4 WHERE instance_id=$1`, registered.InstanceID); err != nil {
		t.Fatal(err)
	}

	insert := func(load float64, enabled bool, heartbeat time.Time) uuid.UUID {
		id := uuid.New()
		_, err := pool.Exec(ctx, `INSERT INTO relay_instances
			(instance_id,tenant_id,public_key,listen_addr,tier,enabled,load_factor,last_heartbeat_at)
			VALUES ($1,$2,$3,$4,'free',$5,$6,$7)`, id, tenantID, make([]byte, 32), fmt.Sprintf("tcp://%s:9443", id), enabled, load, heartbeat)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	low := insert(.1, true, time.Now())
	insert(.2, false, time.Now())
	insert(.3, true, time.Now().Add(-2*time.Minute))
	relays, err := registry.GetAvailableRelays(ctx, tenantID)
	if err != nil || len(relays) != 2 || relays[0].InstanceID != low || relays[0].Load > relays[1].Load {
		t.Fatalf("relays=%+v err=%v", relays, err)
	}
}
