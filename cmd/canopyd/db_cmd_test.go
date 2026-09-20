package main

import (
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

func TestExportSQLiteRejectsConflictingDestinationFlags(t *testing.T) {
	code, stderr := captureStderr(t, func() int {
		return runDBCmdE([]string{"export-sqlite", "--sqlite-path", "/tmp/a", "--dsn", "/tmp/b"})
	})
	if code != exitUnknownSubcommand || !strings.Contains(stderr, "disagree") {
		t.Fatalf("conflicting destinations exit=%d stderr=%q", code, stderr)
	}
}
