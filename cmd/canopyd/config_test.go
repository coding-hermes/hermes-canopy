package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// `canopyd config check` exists because configuration is env-only and the
// sqlite backend only ever answered questions by booting into an FTL refusal.
// These tests pin the two halves of that contract: check exits 0 and reports
// the resolved values (creating nothing), while the serve refusal stays wired
// and named.

// runConfigCheck runs the verb with captured streams.
func runConfigCheck(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = configCheckE(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestConfigCheckAcceptsSQLiteAndPrintsDriverAndPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canopy-check.sqlite")
	t.Setenv("CANOPY_DB_DRIVER", "sqlite")
	t.Setenv("CANOPY_SQLITE_PATH", path)

	code, stdout, stderr := runConfigCheck(t)
	if code != 0 {
		t.Fatalf("config check exit = %d, want 0 (stderr %q)", code, stderr)
	}
	if stderr != "" {
		t.Errorf("config check wrote %q to stderr, want nothing", stderr)
	}

	// The driver and the path must appear on ONE line: a reader asking "which
	// sqlite file would boot use" should not have to correlate two lines.
	var driverLine string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "driver=sqlite") {
			driverLine = line
			break
		}
	}
	if driverLine == "" {
		t.Fatalf("no driver=sqlite line in output:\n%s", stdout)
	}
	if !strings.Contains(driverLine, path) {
		t.Fatalf("driver line %q does not carry the resolved sqlite path %q", driverLine, path)
	}
}

func TestConfigCheckCreatesNoSQLiteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "canopy.sqlite")
	t.Setenv("CANOPY_DB_DRIVER", "sqlite")
	t.Setenv("CANOPY_SQLITE_PATH", path)

	if code, _, stderr := runConfigCheck(t); code != 0 {
		t.Fatalf("config check exit = %d (stderr %q)", code, stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config check created %q (err=%v); it must be read-only", path, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("config check created the parent dir %q; it must be read-only", filepath.Dir(path))
	}
	// An absent parent directory is reported, not created.
	_, stdout, _ := runConfigCheck(t)
	if !strings.Contains(stdout, "missing — created on first sqlite boot") {
		t.Fatalf("output does not note the missing parent dir:\n%s", stdout)
	}
}

func TestConfigCheckStatesServeStillRefusesSQLite(t *testing.T) {
	t.Setenv("CANOPY_DB_DRIVER", "sqlite")
	t.Setenv("CANOPY_SQLITE_PATH", filepath.Join(t.TempDir(), "canopy.sqlite"))

	code, stdout, stderr := runConfigCheck(t)
	if code != 0 {
		t.Fatalf("config check exit = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "serve=refused") {
		t.Fatalf("output does not state that serve refuses the sqlite backend:\n%s", stdout)
	}
	// The refusal reason is the exact text serve fails with, so the check
	// output and the boot failure cannot drift apart.
	if !strings.Contains(errSQLiteFullBootRefused.Error(), "PostgreSQL-only repository surfaces") {
		t.Fatalf("sqlite serve refusal text changed unexpectedly: %q", errSQLiteFullBootRefused.Error())
	}
	if !strings.Contains(stdout, "PostgreSQL-only repository surfaces") {
		t.Fatalf("check output does not name the serve refusal reason:\n%s", stdout)
	}
}

func TestConfigCheckDefaultsToPostgres(t *testing.T) {
	t.Setenv("CANOPY_DB_DRIVER", "")
	t.Setenv("CANOPY_SQLITE_PATH", "")
	code, stdout, stderr := runConfigCheck(t)
	if code != 0 {
		t.Fatalf("config check exit = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "driver=postgres") {
		t.Fatalf("default driver is not reported as postgres:\n%s", stdout)
	}
	if !strings.Contains(stdout, "DB target: postgres") {
		t.Fatalf("resolved postgres target missing from output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "serve=allowed") {
		t.Fatalf("postgres config should report serve=allowed:\n%s", stdout)
	}
}

func TestConfigCheckRejectsInvalidDriver(t *testing.T) {
	t.Setenv("CANOPY_DB_DRIVER", "mysql")
	code, stdout, stderr := runConfigCheck(t)
	if code != 1 {
		t.Fatalf("config check with an invalid driver exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "CANOPY_DB_DRIVER") {
		t.Fatalf("validation error does not name the variable: %q", stderr)
	}
	if stdout != "" {
		t.Errorf("invalid config still printed resolved values: %q", stdout)
	}
}

func TestConfigCheckNeverPrintsTheJWTSecret(t *testing.T) {
	const secret = "super-secret-value-do-not-print"
	t.Setenv("CANOPY_DB_DRIVER", "sqlite")
	t.Setenv("CANOPY_SQLITE_PATH", filepath.Join(t.TempDir(), "canopy.sqlite"))
	t.Setenv("JWT_SECRET", secret)

	code, stdout, stderr := runConfigCheck(t)
	if code != 0 {
		t.Fatalf("config check exit = %d (stderr %q)", code, stderr)
	}
	if strings.Contains(stdout, secret) {
		t.Fatalf("config check printed the JWT secret:\n%s", stdout)
	}
	if !strings.Contains(stdout, "jwt_secret=set (value not printed)") {
		t.Fatalf("output does not report the secret as set-but-hidden:\n%s", stdout)
	}
}

func TestConfigCheckFlagsDevJWTSecret(t *testing.T) {
	t.Setenv("CANOPY_DB_DRIVER", "sqlite")
	t.Setenv("CANOPY_SQLITE_PATH", filepath.Join(t.TempDir(), "canopy.sqlite"))
	t.Setenv("JWT_SECRET", "")
	code, stdout, _ := runConfigCheck(t)
	if code != 0 {
		t.Fatalf("config check exit = %d", code)
	}
	if !strings.Contains(stdout, "dev-default") || !strings.Contains(stdout, "INSECURE") {
		t.Fatalf("dev JWT secret is not flagged:\n%s", stdout)
	}
	if !db.IsDevJWTSecret(db.DevJWTSecretDefault) {
		t.Fatal("db.DevJWTSecretDefault is no longer recognised as the dev secret")
	}
}

func TestConfigUsageAndRouting(t *testing.T) {
	if !isSubcommand("config") {
		t.Fatal("config is not a registered top-level subcommand")
	}
	if got := classifyArgs([]string{"config", "check"}); got != argRouteSubcommand {
		t.Fatalf("classifyArgs(config check) = %v, want subcommand", got)
	}
	if code := runConfigCmdE([]string{"--help"}); code != 0 {
		t.Fatalf("config --help exit = %d, want 0", code)
	}
	if code := runConfigCmdE([]string{"bogus"}); code != exitUnknownSubcommand {
		t.Fatalf("unknown config subcommand exit = %d, want %d", code, exitUnknownSubcommand)
	}
	var out, errOut bytes.Buffer
	if code := configCheckE([]string{"extra"}, &out, &errOut); code != exitUnknownSubcommand {
		t.Fatalf("config check with a positional argument exit = %d, want %d", code, exitUnknownSubcommand)
	}
}
