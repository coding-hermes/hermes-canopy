package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDBCommandDispatchAndUsage(t *testing.T) {
	if !isSubcommand("db") {
		t.Fatal("db is not a registered top-level subcommand")
	}
	if got := classifyArgs([]string{"db", "export-sqlite"}); got != argRouteSubcommand {
		t.Fatalf("classifyArgs(db export-sqlite) = %v, want subcommand", got)
	}
	code, stderr := captureStderr(t, func() int { return runDBCmdE([]string{"--help"}) })
	if code != 0 || !strings.Contains(stderr, "db export-sqlite") {
		t.Fatalf("db help exit=%d stderr=%q", code, stderr)
	}
}

func TestExportSQLiteRequiresDestination(t *testing.T) {
	code, stderr := captureStderr(t, func() int { return runDBCmdE([]string{"export-sqlite"}) })
	if code != exitUnknownSubcommand || !strings.Contains(stderr, "--sqlite-path is required") {
		t.Fatalf("missing destination exit=%d stderr=%q", code, stderr)
	}
}

// captureCombinedOutput preserves stdout/stderr ordering by redirecting both
// streams to the same pipe.
func captureCombinedOutput(t *testing.T, f func() int) (int, string) {
	t.Helper()
	oldStdout, oldStderr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout, os.Stderr = w, w
	defer func() {
		os.Stdout, os.Stderr = oldStdout, oldStderr
	}()
	code := f()
	_ = w.Close()
	data, _ := io.ReadAll(r)
	return code, string(data)
}

func TestExportSQLitePrintsTargetBeforeFailure(t *testing.T) {
	t.Setenv("CANOPY_DB_URL", "")
	t.Setenv("DB_HOST", "127.0.0.1")
	t.Setenv("DB_PORT", "5499")
	t.Setenv("DB_USER", "canopy")
	t.Setenv("DB_PASSWORD", "canopy")
	t.Setenv("DB_NAME", "canopy")
	t.Setenv("DB_SSLMODE", "disable")

	sqlitePath := filepath.Join(t.TempDir(), "export.sqlite")
	code, output := captureCombinedOutput(t, func() int {
		return exportSQLiteE([]string{"--sqlite-path", sqlitePath})
	})
	if code != 1 {
		t.Fatalf("exportSQLiteE exit = %d, want 1; output=%q", code, output)
	}
	targetIndex := strings.Index(output, "DB target:")
	errorIndex := strings.Index(output, "Error:")
	if targetIndex < 0 || errorIndex < 0 {
		t.Fatalf("output must contain target and error lines: %q", output)
	}
	if targetIndex >= errorIndex {
		t.Fatalf("target line must precede error line: %q", output)
	}
	if count := strings.Count(output, "DB target:"); count != 1 {
		t.Fatalf("target line printed %d times, want once: %q", count, output)
	}
}

func TestExportSQLiteRejectsConflictingDestinationFlags(t *testing.T) {
	code, stderr := captureStderr(t, func() int {
		return runDBCmdE([]string{"export-sqlite", "--sqlite-path", "/tmp/a", "--dsn", "/tmp/b"})
	})
	if code != exitUnknownSubcommand || !strings.Contains(stderr, "disagree") {
		t.Fatalf("conflicting destinations exit=%d stderr=%q", code, stderr)
	}
}
