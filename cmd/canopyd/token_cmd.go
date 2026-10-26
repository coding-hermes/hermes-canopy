package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// Dev-token minting (REVIEW-CANOPY-2026-10-13-DX part 1). Before this verb
// every first-contact user hand-crafted a JWT to call the API: the README
// documents a sub claim and a secret, but nothing produced a token. `canopyd
// token mint` prints exactly one line (the token) so it composes:
//
//	TOKEN=$(./bin/canopyd token mint)
//	curl -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/trees
//
// The minted token is a plain HS256 JWT signed with JWT_SECRET (default: the
// well-known dev secret, matching cfg.JWTSecret and db.DevJWTSecretDefault)
// for the dev user db.DevJWTUserID, which `serve` provisions on a fresh dev
// database (GAP-064). Minting performs NO I/O and no config validation: it
// must work before a server or database exists, which is the whole point.

const (
	// tokenDefaultTTL is how long a freshly minted dev token stays valid.
	tokenDefaultTTL = 24 * time.Hour
	// tokenIssuer is stamped into the iss claim so a token found in a log is
	// identifiable as a canopyd dev token rather than a real credential.
	tokenIssuer = "canopyd-dev-token"
)

// tokenSecret resolves the HS256 signing key exactly like the server does:
// JWT_SECRET when set, otherwise the documented dev default.
func tokenSecret(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv("JWT_SECRET")); v != "" {
		return v
	}
	return db.DevJWTSecretDefault
}

// mintDevToken builds and signs a dev JWT. `now` is injected so expiry
// arithmetic is testable without sleeping.
func mintDevToken(secret, sub string, ttl time.Duration, now time.Time) (string, error) {
	if strings.TrimSpace(sub) == "" {
		return "", errors.New("token mint: subject must not be empty")
	}
	if ttl <= 0 {
		return "", errors.New("token mint: ttl must be positive")
	}
	claims := jwt.MapClaims{
		"sub": sub,
		"iss": tokenIssuer,
		"iat": now.Unix(),
		"exp": now.Add(ttl).Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("token mint: sign: %w", err)
	}
	return signed, nil
}

// verifyDevToken parses and validates a token against secret. The accepted
// algorithm set is pinned to HS256 so an `alg: none` or RS256 token can never
// be accepted by mistake (BUG-014 class).
func verifyDevToken(secret, raw string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return nil, fmt.Errorf("token verify: %w", err)
	}
	if !token.Valid {
		return nil, errors.New("token verify: token is not valid")
	}
	return claims, nil
}

// runTokenCmd dispatches `canopyd token <mint|verify>`.
func runTokenCmd(args []string) {
	os.Exit(runTokenCmdE(args))
}

// runTokenCmdE returns 0 on success, 1 when verification rejects a token, and
// 2 for invalid command-line usage.
func runTokenCmdE(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printTokenUsage(os.Stderr)
		if len(args) == 0 {
			return 1
		}
		return 0
	}
	switch args[0] {
	case "mint":
		return mintTokenE(args[1:], os.Stdout, os.Stderr)
	case "verify":
		return verifyTokenE(args[1:], os.Stdout, os.Stderr)
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown token subcommand: %s\n", args[0])
		printTokenUsage(os.Stderr)
		return 2
	}
}

// printTokenUsage documents the token verbs. It names JWT_SECRET because that
// is the one thing a user needs to make the token work against a server that
// is not running the default dev secret.
var tokenUsageText = `Usage: canopyd token <mint|verify> [flags]

Mint and verify dev HS256 JWTs for the local API.

  token mint [--sub <uuid>] [--ttl <duration>]
      Print exactly one line: a three-segment JWT signed with
      JWT_SECRET (default "` + db.DevJWTSecretDefault + `") for the dev user
      ` + db.DevJWTUserID + `. Default expiry: 24h (--ttl, e.g. 30m, 72h).

  token verify [--token <jwt>]
      Check a token's signature and expiry against JWT_SECRET.
      Reads the token from --token, a positional argument, or stdin.

Environment:
  JWT_SECRET   HS256 signing key (default: the well-known dev secret;
               must match the running server's JWT_SECRET)
`

func printTokenUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, tokenUsageText)
}

// mintTokenE implements `canopyd token mint`. Stdout carries the token and
// nothing else, so `canopyd token mint | cut -d. -f2` composes.
func mintTokenE(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("token mint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printTokenUsage(stderr) }
	sub := fs.String("sub", db.DevJWTUserID, "subject (sub) claim — the dev user id by default")
	ttl := fs.Duration("ttl", tokenDefaultTTL, "token lifetime (e.g. 30m, 72h)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "Error: unexpected argument %q\n", fs.Arg(0))
		printTokenUsage(stderr)
		return 2
	}
	token, err := mintDevToken(tokenSecret(os.Getenv), *sub, *ttl, time.Now())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, token)
	return 0
}

// verifyTokenE implements `canopyd token verify`: 0 when the token verifies,
// 1 when it does not (with the reason on stderr), 2 on misuse.
func verifyTokenE(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("token verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printTokenUsage(stderr) }
	raw := fs.String("token", "", "JWT to verify (defaults to a positional argument, then stdin)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	token := strings.TrimSpace(*raw)
	if token == "" && fs.NArg() > 0 {
		if fs.NArg() > 1 {
			_, _ = fmt.Fprintf(stderr, "Error: unexpected argument %q\n", fs.Arg(1))
			printTokenUsage(stderr)
			return 2
		}
		token = strings.TrimSpace(fs.Arg(0))
	}
	if token == "" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: read token from stdin: %v\n", err)
			return 1
		}
		token = strings.TrimSpace(string(data))
	}
	if token == "" {
		_, _ = fmt.Fprintln(stderr, "Error: no token given (--token, positional argument, or stdin)")
		printTokenUsage(stderr)
		return 2
	}

	claims, err := verifyDevToken(tokenSecret(os.Getenv), token)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid: %v\n", err)
		return 1
	}
	sub, _ := claims["sub"].(string)
	exp, _ := claims["exp"].(float64)
	expiry := time.Unix(int64(exp), 0).UTC()
	_, _ = fmt.Fprintf(stdout, "valid sub=%s exp=%s expires_in=%s\n",
		sub, expiry.Format(time.RFC3339), time.Until(expiry).Round(time.Minute))
	return 0
}
