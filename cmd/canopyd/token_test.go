package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// The token verbs are pinned here rather than only in the shell because the
// acceptance contract is a *composition* one: exactly one line on stdout,
// three segments, and a payload whose `sub` decodes to the dev user id. A
// regression that adds a log line to stdout breaks `$(canopyd token mint)`
// for every user, so it is a test, not a convention.

// splitToken returns the three JWT segments or fails the test.
func splitToken(t *testing.T, raw string) []string {
	t.Helper()
	parts := strings.Split(strings.TrimSpace(raw), ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3: %q", len(parts), raw)
	}
	return parts
}

// decodeClaims decodes the payload segment (RawURL, no padding) into a map.
func decodeClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := splitToken(t, token)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("payload segment is not RawURL base64: %v", err)
	}
	claims := map[string]any{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("payload is not JSON: %v (payload %q)", err, payload)
	}
	return claims
}

func TestTokenMintPrintsExactlyOneThreeSegmentLine(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	var stdout, stderr bytes.Buffer
	code := mintTokenE(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("token mint exit = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("token mint wrote %q to stderr, want nothing", stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("token mint printed %d lines, want exactly 1: %q", len(lines), stdout.String())
	}
	splitToken(t, lines[0])
}

func TestTokenMintDefaultSubIsDevUser(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	var stdout, stderr bytes.Buffer
	if code := mintTokenE(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("token mint exit = %d (stderr %q)", code, stderr.String())
	}
	claims := decodeClaims(t, stdout.String())
	if got := claims["sub"]; got != db.DevJWTUserID {
		t.Fatalf("sub claim = %v, want %s", got, db.DevJWTUserID)
	}
	// The dev user every fresh dev database provisions (GAP-064) must be the
	// default subject, otherwise the minted token 401s/404s out of the box.
	if db.DevJWTUserID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("db.DevJWTUserID = %q, want the documented dev user", db.DevJWTUserID)
	}
}

func TestTokenMintSignsWithDevSecretByDefault(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	var stdout, stderr bytes.Buffer
	if code := mintTokenE(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("token mint exit = %d (stderr %q)", code, stderr.String())
	}
	token := strings.TrimSpace(stdout.String())

	claims, err := verifyDevToken(db.DevJWTSecretDefault, token)
	if err != nil {
		t.Fatalf("token does not verify with the dev secret: %v", err)
	}
	if claims["sub"] != db.DevJWTUserID {
		t.Fatalf("verified sub = %v, want %s", claims["sub"], db.DevJWTUserID)
	}
	if _, err := verifyDevToken("some-other-secret", token); err == nil {
		t.Fatal("token verified with a secret it was not signed with")
	}
}

func TestTokenMintHonoursJWTSecretEnv(t *testing.T) {
	t.Setenv("JWT_SECRET", "operator-secret")
	var stdout, stderr bytes.Buffer
	if code := mintTokenE(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("token mint exit = %d (stderr %q)", code, stderr.String())
	}
	token := strings.TrimSpace(stdout.String())
	if _, err := verifyDevToken("operator-secret", token); err != nil {
		t.Fatalf("token does not verify with JWT_SECRET: %v", err)
	}
	if _, err := verifyDevToken(db.DevJWTSecretDefault, token); err == nil {
		t.Fatal("token verifies with the dev default even though JWT_SECRET was set")
	}
}

func TestTokenMintDefaultExpiryIs24h(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	var stdout, stderr bytes.Buffer
	if code := mintTokenE(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("token mint exit = %d (stderr %q)", code, stderr.String())
	}
	claims := decodeClaims(t, stdout.String())
	iat, okIat := claims["iat"].(float64)
	exp, okExp := claims["exp"].(float64)
	if !okIat || !okExp {
		t.Fatalf("iat/exp missing or not numeric: %v", claims)
	}
	if got := time.Duration(exp-iat) * time.Second; got != 24*time.Hour {
		t.Fatalf("token lifetime = %v, want 24h", got)
	}
}

func TestTokenMintTTLFlagAndSubFlag(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	sub := "11111111-2222-3333-4444-555555555555"
	var stdout, stderr bytes.Buffer
	if code := mintTokenE([]string{"--ttl", "90m", "--sub", sub}, &stdout, &stderr); code != 0 {
		t.Fatalf("token mint --ttl --sub exit = %d (stderr %q)", code, stderr.String())
	}
	claims := decodeClaims(t, stdout.String())
	if claims["sub"] != sub {
		t.Fatalf("sub claim = %v, want %s", claims["sub"], sub)
	}
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if got := time.Duration(exp-iat) * time.Second; got != 90*time.Minute {
		t.Fatalf("token lifetime = %v, want 90m", got)
	}
}

func TestTokenMintRejectsBadInput(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	var stdout, stderr bytes.Buffer
	if code := mintTokenE([]string{"--ttl", "0s"}, &stdout, &stderr); code != 1 {
		t.Fatalf("token mint --ttl 0s exit = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("failed mint still wrote a token to stdout: %q", stdout.String())
	}
	if code := mintTokenE([]string{"--sub", "  "}, &stdout, &stderr); code != 1 {
		t.Fatalf("token mint --sub blank exit = %d, want 1", code)
	}
	if code := mintTokenE([]string{"extra"}, &stdout, &stderr); code != exitUnknownSubcommand {
		t.Fatalf("token mint with a positional argument exit = %d, want %d", code, exitUnknownSubcommand)
	}
}

func TestTokenVerifyRoundTrip(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	var mintOut, mintErr bytes.Buffer
	if code := mintTokenE(nil, &mintOut, &mintErr); code != 0 {
		t.Fatalf("token mint exit = %d (stderr %q)", code, mintErr.String())
	}
	token := strings.TrimSpace(mintOut.String())

	// Positional argument form.
	var stdout, stderr bytes.Buffer
	if code := verifyTokenE([]string{token}, &stdout, &stderr); code != 0 {
		t.Fatalf("token verify exit = %d (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "valid sub="+db.DevJWTUserID) {
		t.Fatalf("token verify output = %q, want the dev sub", stdout.String())
	}

	// --token flag form.
	stdout.Reset()
	stderr.Reset()
	if code := verifyTokenE([]string{"--token", token}, &stdout, &stderr); code != 0 {
		t.Fatalf("token verify --token exit = %d (stderr %q)", code, stderr.String())
	}
}

func TestTokenVerifyRejectsWrongSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "one-secret")
	var mintOut, mintErr bytes.Buffer
	if code := mintTokenE(nil, &mintOut, &mintErr); code != 0 {
		t.Fatalf("token mint exit = %d", code)
	}
	token := strings.TrimSpace(mintOut.String())

	t.Setenv("JWT_SECRET", "another-secret")
	var stdout, stderr bytes.Buffer
	if code := verifyTokenE([]string{token}, &stdout, &stderr); code != 1 {
		t.Fatalf("verify under a different secret exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "invalid") {
		t.Fatalf("verify rejection stderr = %q, want it to say the token is invalid", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("rejected token still printed %q to stdout", stdout.String())
	}
}

func TestTokenVerifyRejectsUnsignedAlgorithm(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": db.DevJWTUserID,
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("build alg=none token: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := verifyTokenE([]string{unsigned}, &stdout, &stderr); code != 1 {
		t.Fatalf("verify alg=none exit = %d, want 1", code)
	}
}

func TestTokenVerifyRejectsExpiredToken(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": db.DevJWTUserID,
		"iat": time.Now().Add(-2 * time.Hour).Unix(),
		"exp": time.Now().Add(-time.Hour).Unix(),
	}).SignedString([]byte(db.DevJWTSecretDefault))
	if err != nil {
		t.Fatalf("build expired token: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := verifyTokenE([]string{expired}, &stdout, &stderr); code != 1 {
		t.Fatalf("verify expired token exit = %d, want 1", code)
	}
}

func TestTokenUsageMentionsJWTSecret(t *testing.T) {
	code, usage := captureStderr(t, func() int { return runTokenCmdE([]string{"--help"}) })
	if code != 0 {
		t.Fatalf("token --help exit = %d, want 0", code)
	}
	if !strings.Contains(usage, "JWT_SECRET") {
		t.Fatalf("token help does not mention JWT_SECRET: %q", usage)
	}
	var stdout, stderr bytes.Buffer
	if code := mintTokenE([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("token mint --help exit = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "JWT_SECRET") {
		t.Fatalf("token mint --help does not mention JWT_SECRET: %q", stderr.String())
	}
}

func TestTokenCommandRouting(t *testing.T) {
	if !isSubcommand("token") {
		t.Fatal("token is not a registered top-level subcommand")
	}
	if got := classifyArgs([]string{"token", "mint"}); got != argRouteSubcommand {
		t.Fatalf("classifyArgs(token mint) = %v, want subcommand", got)
	}
	code, stderr := captureStderr(t, func() int { return runTokenCmdE([]string{"bogus"}) })
	if code != exitUnknownSubcommand {
		t.Fatalf("unknown token subcommand exit = %d, want %d", code, exitUnknownSubcommand)
	}
	if !strings.Contains(stderr, "unknown token subcommand: bogus") {
		t.Fatalf("unknown token subcommand stderr = %q", stderr)
	}
}
