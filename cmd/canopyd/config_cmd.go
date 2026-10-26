package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// `canopyd config check` (REVIEW-CANOPY-2026-10-13-DX part 2).
//
// Configuration is environment-only, so before this verb the only way to learn
// what canopyd resolved was to boot it — and with CANOPY_DB_DRIVER=sqlite that
// boot ends in a deliberate FTL refusal AFTER opening the database, applying
// the embedded migrations and running the parity self-check. A user could not
// even confirm the sqlite path was the one they meant without creating a file.
//
// `config check` validates the same Config `serve` validates and prints the
// resolved values, then exits. It does NOT open a database, create a file, run
// migrations or bind a port — the only side effect is stdout. The sqlite
// refusal on the serve path is untouched: it lives in main()'s boot sequence
// and still fires (non-zero exit) after the core-graph self-check passes.
//
// Exit codes: 0 config is valid, 1 config is invalid (reason on stderr),
// 2 command-line misuse.

// runConfigCmd dispatches `canopyd config <check>`.
func runConfigCmd(args []string) {
	os.Exit(runConfigCmdE(args))
}

// runConfigCmdE returns the process exit code for the config verbs.
func runConfigCmdE(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printConfigUsage(os.Stderr)
		if len(args) == 0 {
			return 1
		}
		return 0
	}
	switch args[0] {
	case "check":
		return configCheckE(args[1:], os.Stdout, os.Stderr)
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown config subcommand: %s\n", args[0])
		printConfigUsage(os.Stderr)
		return 2
	}
}

var configUsageText = `Usage: canopyd config check

Validates the environment-derived configuration and prints the resolved
values without booting: no database is opened, no migration runs and no
port is bound. Exit 0 means ` + "`canopyd serve`" + ` would accept this
configuration; 1 means it would refuse (reason on stderr).

With CANOPY_DB_DRIVER=sqlite this reports the resolved SQLite path and
still exits 0 — ` + "`serve`" + ` remains the surface that refuses a full boot on
the sqlite backend (PostgreSQL-only repositories remain).
`

func printConfigUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, configUsageText)
}

// configCheckE implements `canopyd config check`.
func configCheckE(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printConfigUsage(stderr) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "Error: unexpected argument %q\n", fs.Arg(0))
		printConfigUsage(stderr)
		return 2
	}

	cfg := config.FromEnv()
	if err := cfg.Validate(); err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid: %v\n", err)
		return 1
	}
	writeResolvedConfig(stdout, cfg)
	return 0
}

// writeResolvedConfig prints the resolved configuration. Secrets are never
// printed — only whether a non-default value is in effect — because this
// output lands in terminals, logs and bug reports.
func writeResolvedConfig(stdout io.Writer, cfg *config.Config) {
	_, _ = fmt.Fprintln(stdout, "config: resolved (no server boot, no database opened)")

	// One line carries the driver AND, for sqlite, the path boot would use:
	// driver=sqlite sqlite_path=<path>. Keeping them together means a reader
	// (or a script) never has to correlate two lines to answer "which file?".
	if cfg.DBDriver == "sqlite" {
		_, _ = fmt.Fprintf(stdout, "driver=sqlite sqlite_path=%s\n", cfg.SQLitePath)
		_, _ = fmt.Fprintf(stdout, "sqlite_path_parent=%s\n", describePathParent(cfg.SQLitePath))
	} else {
		_, _ = fmt.Fprintf(stdout, "driver=%s\n", cfg.DBDriver)
		_, _ = fmt.Fprintln(stdout, config.DBTargetOf(cfg, os.Getenv).Describe())
	}

	_, _ = fmt.Fprintf(stdout, "env=%s production=%t http_addr=%s\n", cfg.Env, cfg.IsProduction(), cfg.HTTPAddr)
	_, _ = fmt.Fprintf(stdout, "log_level=%s log_format=%s metrics_enabled=%t\n",
		cfg.LogLevel, cfg.LogFormat, cfg.MetricsEnabled)
	_, _ = fmt.Fprintf(stdout, "cors_origin=%s\n", describeCORS(cfg.CORSOrigin))
	_, _ = fmt.Fprintf(stdout, "jwt_secret=%s\n", describeJWTSecret(cfg.JWTSecret))

	// State the serve outcome so the two verbs cannot be confused: `check`
	// validating sqlite is not a promise that `serve` boots on it.
	if cfg.DBDriver == "sqlite" {
		_, _ = fmt.Fprintln(stdout, "serve=refused (sqlite backend: core-graph self-check only; PostgreSQL-only repository surfaces remain — see SQLITE-PIVOT.md)")
	} else {
		_, _ = fmt.Fprintln(stdout, "serve=allowed (configuration accepted)")
	}
}

// describeCORS renders the fail-closed CORS default readably.
func describeCORS(origin string) string {
	if strings.TrimSpace(origin) == "" {
		return "(disabled — no cross-origin requests admitted; set CORS_ORIGIN to opt in)"
	}
	return origin
}

// describeJWTSecret reports whether the well-known dev secret is in effect
// without ever echoing the secret itself.
func describeJWTSecret(secret string) string {
	if db.IsDevJWTSecret(secret) {
		return "dev-default (INSECURE outside local dev — set JWT_SECRET; `canopyd token mint` signs with it)"
	}
	if strings.TrimSpace(secret) == "" {
		return "empty (no token can verify)"
	}
	return "set (value not printed)"
}

// describePathParent reports whether the sqlite file's directory already
// exists. Purely informational: `check` never creates it.
func describePathParent(path string) string {
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	switch {
	case err != nil:
		return dir + " (missing — created on first sqlite boot)"
	case !info.IsDir():
		return dir + " (NOT a directory — sqlite boot would fail)"
	default:
		return dir + " (exists)"
	}
}
