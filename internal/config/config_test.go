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
