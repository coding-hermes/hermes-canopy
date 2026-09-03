package relay

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

func TestDeploymentConfigValidation(t *testing.T) {
	valid := DefaultConfig()
	tests := []struct {
		name string
		edit func(*DeploymentConfig)
		want string
	}{
		{"valid default", func(*DeploymentConfig) {}, ""},
		{"valid tcp", func(c *DeploymentConfig) { c.ListenAddr = "tcp://127.0.0.1:9443" }, ""},
		{"invalid mode", func(c *DeploymentConfig) { c.Mode = "local" }, "invalid mode"},
		{"zero sessions", func(c *DeploymentConfig) { c.MaxSessions = 0 }, "max sessions"},
		{"bad port", func(c *DeploymentConfig) { c.ListenAddr = "tcp://localhost:70000" }, "port"},
		{"missing scheme", func(c *DeploymentConfig) { c.ConnectAddr = "localhost:9443" }, "scheme"},
		{"zero heartbeat", func(c *DeploymentConfig) { c.HeartbeatSecs = 0 }, "heartbeat"},
		{"zero drain", func(c *DeploymentConfig) { c.DrainTimeoutSecs = 0 }, "drain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.edit(&cfg)
			err := cfg.Validate()
			if tt.want == "" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDeploymentConfigLoadSaveReload(t *testing.T) {
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	mgr := NewDeploymentConfigManager()

	got, err := mgr.Load(ctx, pool)
	if err != nil || !reflect.DeepEqual(got, DefaultConfig()) {
		t.Fatalf("Load(empty) = %+v, %v", got, err)
	}
	certFile, keyFile, caFile := "relay.crt", "relay.key", "ca.crt"
	rotatedAt := time.Now().UTC().Truncate(time.Microsecond)
	want := DeploymentConfig{
		Mode: ModeSaaS, MaxSessions: 500, HeartbeatSecs: 15, DrainTimeoutSecs: 60,
		TLSEnabled: true, TLSCertFile: &certFile, TLSKeyFile: &keyFile, TLSCAFile: &caFile,
		TLSMutual: true, HMACKeyRotatedAt: &rotatedAt, HMACKeyID: 7, Enabled: true,
	}
	if err := mgr.Save(ctx, pool, want); err != nil {
		t.Fatal(err)
	}
	got, err = mgr.Reload(ctx, pool)
	want.InstanceID = got.InstanceID
	// Pointer fields (*string/*time.Time) must compare by value, not identity.
	// time.Time must use .Equal (DeepEqual compares location pointers).
	if err != nil || !configsEqual(got, want) || !configsEqual(mgr.Current(), want) {
		t.Fatalf("Reload() = %+v, %v; current = %+v", got, err, mgr.Current())
	}
}

func configsEqual(a, b DeploymentConfig) bool {
	aTime, bTime := a.HMACKeyRotatedAt, b.HMACKeyRotatedAt
	a.HMACKeyRotatedAt, b.HMACKeyRotatedAt = nil, nil
	if !reflect.DeepEqual(a, b) {
		return false
	}
	switch {
	case aTime == nil && bTime == nil:
		return true
	case aTime != nil && bTime != nil:
		return aTime.Equal(*bTime)
	default:
		return false
	}
}

func TestDefaultConfigIsAirGappedAndDisabled(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Mode != ModeAirGapped || cfg.Enabled {
		t.Fatalf("DefaultConfig() = %+v", cfg)
	}
}
