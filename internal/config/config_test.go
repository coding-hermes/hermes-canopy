package config

import (
	"strings"
	"testing"
)

func TestDefaultJWTSecret(t *testing.T) {
	if got := Default().JWTSecret; got != "dev-secret-change-me" {
		t.Fatalf("Default().JWTSecret = %q, want development default", got)
	}
}

func TestFromEnvJWTSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	if got := FromEnv().JWTSecret; got != "test-secret" {
		t.Fatalf("FromEnv().JWTSecret = %q, want %q", got, "test-secret")
	}
}

func TestFromEnvNATSOptionalWiring(t *testing.T) {
	t.Setenv("CANOPY_NATS_URL", "")
	t.Setenv("CANOPY_NATS_CREDS", "")
	c := FromEnv()
	if c.NATSURL != "" || c.NATSCreds != "" {
		t.Fatalf("unset NATS environment enabled wiring: %#v", c)
	}

	t.Setenv("CANOPY_NATS_URL", "nats://example:4222")
	t.Setenv("CANOPY_NATS_CREDS", "/run/secrets/nats.creds")
	c = FromEnv()
	if c.NATSURL != "nats://example:4222" || c.NATSCreds != "/run/secrets/nats.creds" {
		t.Fatalf("NATS environment was not mapped: URL=%q creds=%q", c.NATSURL, c.NATSCreds)
	}
}

func TestFromEnv_CANOPY_DB_URL(t *testing.T) {
	t.Setenv("CANOPY_DB_URL", "postgres://myuser:mypass@myhost:5433/mydb?sslmode=require")
	c := FromEnv()
	if c.DBHost != "myhost" {
		t.Fatalf("DBHost = %q, want %q", c.DBHost, "myhost")
	}
	if c.DBPort != 5433 {
		t.Fatalf("DBPort = %d, want %d", c.DBPort, 5433)
	}
	if c.DBUser != "myuser" {
		t.Fatalf("DBUser = %q, want %q", c.DBUser, "myuser")
	}
	if c.DBPassword != "mypass" {
		t.Fatalf("DBPassword = %q, want %q", c.DBPassword, "mypass")
	}
	if c.DBName != "mydb" {
		t.Fatalf("DBName = %q, want %q", c.DBName, "mydb")
	}
	if c.DBSSLMode != "require" {
		t.Fatalf("DBSSLMode = %q, want %q", c.DBSSLMode, "require")
	}
}

func TestFromEnv_CANOPY_DB_URL_Empty(t *testing.T) {
	// When CANOPY_DB_URL is not set, individual env vars still work.
	t.Setenv("DB_HOST", "otherhost")
	t.Setenv("DB_PORT", "5444")
	c := FromEnv()
	if c.DBHost != "otherhost" {
		t.Fatalf("DBHost = %q, want %q", c.DBHost, "otherhost")
	}
	if c.DBPort != 5444 {
		t.Fatalf("DBPort = %d, want %d", c.DBPort, 5444)
	}
}

func TestDefaultDBSchema(t *testing.T) {
	if got := Default().DBSchema; got != "public" {
		t.Fatalf("Default().DBSchema = %q, want %q", got, "public")
	}
}

func TestFromEnvDBSchema(t *testing.T) {
	t.Setenv("DB_SCHEMA", "myschema")
	if got := FromEnv().DBSchema; got != "myschema" {
		t.Fatalf("FromEnv().DBSchema = %q, want %q", got, "myschema")
	}
}

func TestDSNIncludesSearchPath(t *testing.T) {
	c := Default()
	c.DBSchema = "myschema"
	dsn := c.DSN()
	if !strings.Contains(dsn, "search_path=myschema") {
		t.Fatalf("DSN() = %q, want search_path=myschema", dsn)
	}
	// Default schema should also appear in the DSN.
	c2 := Default()
	dsn2 := c2.DSN()
	if !strings.Contains(dsn2, "search_path=public") {
		t.Fatalf("DSN() = %q, want search_path=public", dsn2)
	}
}

func TestDefaultLogFormat(t *testing.T) {
	if got := Default().LogFormat; got != "text" {
		t.Fatalf("Default().LogFormat = %q, want %q", got, "text")
	}
}

func TestFromEnvLogFormat(t *testing.T) {
	t.Setenv("LOG_FORMAT", "json")
	if got := FromEnv().LogFormat; got != "json" {
		t.Fatalf("FromEnv().LogFormat = %q, want %q", got, "json")
	}
}

func TestDefaultCORSOrigin(t *testing.T) {
	if got := Default().CORSOrigin; got != "*" {
		t.Fatalf("Default().CORSOrigin = %q, want %q", got, "*")
	}
}

func TestFromEnvCORSOrigin(t *testing.T) {
	t.Setenv("CORS_ORIGIN", "http://example.com")
	if got := FromEnv().CORSOrigin; got != "http://example.com" {
		t.Fatalf("FromEnv().CORSOrigin = %q, want %q", got, "http://example.com")
	}
}

func TestDefaultGatewayConfig(t *testing.T) {
	c := Default()
	if c.GatewayBaseURL != "http://127.0.0.1:8642" {
		t.Fatalf("Default().GatewayBaseURL = %q, want http://127.0.0.1:8642", c.GatewayBaseURL)
	}
	if c.GatewayAPIKey != "" {
		t.Fatalf("Default().GatewayAPIKey = %q, want empty", c.GatewayAPIKey)
	}
}

func TestFromEnvGatewayConfig(t *testing.T) {
	t.Setenv("HERMES_WEBUI_GATEWAY_BASE_URL", "http://example.com:9999")
	t.Setenv("HERMES_WEBUI_GATEWAY_API_KEY", "webui-key")
	c := FromEnv()
	if c.GatewayBaseURL != "http://example.com:9999" {
		t.Fatalf("GatewayBaseURL = %q, want env override", c.GatewayBaseURL)
	}
	if c.GatewayAPIKey != "webui-key" {
		t.Fatalf("GatewayAPIKey = %q, want webui-key", c.GatewayAPIKey)
	}
}

func TestFromEnvGatewayAPIKeyFallsBackToAPIServerKey(t *testing.T) {
	t.Setenv("HERMES_WEBUI_GATEWAY_BASE_URL", "")
	t.Setenv("HERMES_WEBUI_GATEWAY_API_KEY", "")
	t.Setenv("API_SERVER_KEY", "api-server-key")
	c := FromEnv()
	if c.GatewayAPIKey != "api-server-key" {
		t.Fatalf("GatewayAPIKey = %q, want API_SERVER_KEY fallback", c.GatewayAPIKey)
	}
}
