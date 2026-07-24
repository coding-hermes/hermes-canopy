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
