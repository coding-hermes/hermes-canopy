package config

import "testing"

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
