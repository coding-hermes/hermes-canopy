package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- routing: an unknown first argument is refused, never booted ------------

// TestClassifyArgsRouting pins main()'s routing decision. Anything flag-shaped
// keeps server mode (so `canopyd -version`, `canopyd -relay-mode=saas` and bare
// `canopyd` are untouched); only a recognised subcommand starts CLI mode; a
// non-flag argument that matches nothing is refused (DF-HERMES-CANOPY-10).
func TestClassifyArgsRouting(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want argRoute
	}{
		{"bare invocation", nil, argRouteServer},
		{"version flag", []string{"-version"}, argRouteServer},
		{"long help flag", []string{"--help"}, argRouteServer},
		{"relay flag with value", []string{"-relay-mode=saas"}, argRouteServer},
		{"serve alias", []string{"serve"}, argRouteServe},
		{"serve with help", []string{"serve", "--help"}, argRouteServe},
		{"tree subcommand", []string{"tree", "list"}, argRouteSubcommand},
		{"session subcommand", []string{"session", "import"}, argRouteSubcommand},
		{"topic subcommand", []string{"topic", "proposals"}, argRouteSubcommand},
		{"unknown word", []string{"bogussubxyz"}, argRouteUnknown},
		{"unknown word with args", []string{"bogussubxyz", "--x"}, argRouteUnknown},
		{"case-sensitive mismatch", []string{"Tree"}, argRouteUnknown},
		{"prefix mismatch", []string{"treex"}, argRouteUnknown},
		{"empty argument", []string{""}, argRouteUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyArgs(tt.args); got != tt.want {
				t.Errorf("classifyArgs(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// TestUnknownSubcommandNeverEntersServerMode is AC4: the refusal prints the
// error plus the top-level usage, returns the CLI-misuse exit code, and does
// not print a server boot line. main() exits on argRouteUnknown before
// flag.Parse(), so no server code can run behind this decision.
func TestUnknownSubcommandNeverEntersServerMode(t *testing.T) {
	for _, arg := range []string{"bogussubxyz", "serve2", "TREE", ""} {
		t.Run(arg, func(t *testing.T) {
			if got := classifyArgs([]string{arg}); got != argRouteUnknown {
				t.Fatalf("classifyArgs([%q]) = %v, want argRouteUnknown (a typo must not reach server mode)", arg, got)
			}

			code, out := captureStderr(t, func() int { return refuseUnknownSubcommand(arg) })

			if code != exitUnknownSubcommand {
				t.Errorf("exit = %d, want %d", code, exitUnknownSubcommand)
			}
			if code == 0 {
				t.Error("unknown subcommand exited 0 — a script cannot detect the typo")
			}
			if !strings.Contains(out, "unknown subcommand: "+arg) {
				t.Errorf("stderr missing the refusal naming %q: %q", arg, out)
			}
			if !strings.Contains(out, "Usage: canopyd <subcommand> [args...]") {
				t.Errorf("stderr missing the top-level usage: %q", out)
			}
			for _, sub := range []string{"tree", "session", "topic", "serve"} {
				if !strings.Contains(out, sub) {
					t.Errorf("usage does not name the %q subcommand: %q", sub, out)
				}
			}
			if strings.Contains(out, "canopyd starting") || strings.Contains(out, "HTTP server listening") {
				t.Errorf("the refusal reached server mode: %q", out)
			}
		})
	}
}

// TestServerUsageListsSubcommands is AC4: `canopyd --help` (printServerUsage)
// teaches the reader that the CLI exists, while keeping the flag list.
func TestServerUsageListsSubcommands(t *testing.T) {
	code, out := captureStderr(t, func() int {
		printServerUsage()
		return 0
	})

	if code != 0 {
		t.Fatalf("printServerUsage returned %d, want 0", code)
	}
	for _, want := range []string{
		"Usage: canopyd [serve] [flags]",
		"Subcommands:",
		"  serve [flags]",
		"  tree <subcmd>",
		"  session <subcmd>",
		"  topic <subcmd>",
		"Flags:",
		"-version",
		"-relay-mode",
		"HTTP_ADDR",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("server usage is missing %q:\n%s", want, out)
		}
	}
}

// TestVersionOutputPrintsBuildVersionAndExitsZero is AC4 for -version: the
// value printed is the injected build version and the exit code is 0. The flag
// itself staying accepted (rather than being read as a subcommand) is covered
// by TestClassifyArgsRouting, and proven end to end by running the built binary.
func TestVersionOutputPrintsBuildVersionAndExitsZero(t *testing.T) {
	if version == "" {
		t.Fatal("build version is empty")
	}

	var buf bytes.Buffer
	if code := versionOutput(&buf); code != 0 {
		t.Errorf("-version exit code = %d, want 0", code)
	}
	if got, want := buf.String(), version+"\n"; got != want {
		t.Errorf("-version output = %q, want %q", got, want)
	}
}

// TestSessionCommandHelpExitsZero is AC4: `session --help` / `-h` prints the
// session usage and exits 0, while a genuinely unknown session subcommand
// still fails.
func TestSessionCommandHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, out := captureStderr(t, func() int { return runSessionCmdE(args) })

			if code != 0 {
				t.Errorf("exit = %d, want 0 (stderr: %q)", code, out)
			}
			if !strings.Contains(out, "Usage: canopyd session") {
				t.Errorf("stderr missing session usage: %q", out)
			}
			if strings.Contains(out, "unknown session subcommand") {
				t.Errorf("help was reported as an unknown subcommand: %q", out)
			}
		})
	}

	t.Run("bare session exits 1 with usage", func(t *testing.T) {
		code, out := captureStderr(t, func() int { return runSessionCmdE(nil) })
		if code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
		if !strings.Contains(out, "Usage: canopyd session") {
			t.Errorf("stderr missing session usage: %q", out)
		}
	})

	t.Run("unknown session subcommand exits 1", func(t *testing.T) {
		code, out := captureStderr(t, func() int { return runSessionCmdE([]string{"bogus"}) })
		if code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
		if !strings.Contains(out, "unknown session subcommand: bogus") {
			t.Errorf("stderr missing the unknown-subcommand error: %q", out)
		}
	})
}

// TestTopicCommandHelpExitsZero covers the same defect for `topic --help`
// (it answered "unknown topic subcommand: --help" before).
func TestTopicCommandHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, out := captureStderr(t, func() int { return runTopicCmdE(args) })

			if code != 0 {
				t.Errorf("exit = %d, want 0 (stderr: %q)", code, out)
			}
			if !strings.Contains(out, "Usage: canopyd topic") {
				t.Errorf("stderr missing topic usage: %q", out)
			}
			if strings.Contains(out, "unknown topic subcommand") {
				t.Errorf("help was reported as an unknown subcommand: %q", out)
			}
		})
	}

	t.Run("bare topic exits 1 with usage", func(t *testing.T) {
		code, out := captureStderr(t, func() int { return runTopicCmdE(nil) })
		if code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
		if !strings.Contains(out, "Usage: canopyd topic") {
			t.Errorf("stderr missing topic usage: %q", out)
		}
	})

	t.Run("unknown topic subcommand exits 1", func(t *testing.T) {
		code, out := captureStderr(t, func() int { return runTopicCmdE([]string{"bogus"}) })
		if code != 1 {
			t.Errorf("exit = %d, want 1", code)
		}
		if !strings.Contains(out, "unknown topic subcommand: bogus") {
			t.Errorf("stderr missing the unknown-subcommand error: %q", out)
		}
	})
}

// --- API failures: the error envelope must produce a nonzero exit -----------

// stubEnvelopeServer serves the documented API error envelope on every request,
// so a command's exit code can be measured against a real HTTP peer without a
// live canopyd or PostgreSQL.
func stubEnvelopeServer(t *testing.T, status int, code, message string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`, code, message)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestCLIAPIErrorEnvelopesExitNonzero is AC5. Every CLI command path that
// talks to the API must exit nonzero when the server answers with the
// documented error envelope — a script must be able to detect TOKEN_MISSING
// and TREE_NOT_FOUND. Measured at HEAD 3175180 before this change: all six
// paths below already exited 1 (the E-form refactor in DF-HERMES-CANOPY-6);
// this test locks that in so it cannot regress.
func TestCLIAPIErrorEnvelopesExitNonzero(t *testing.T) {
	const treeID = "11111111-1111-4111-8111-111111111111"

	envelopes := []struct {
		name   string
		status int
		code   string
		msg    string
	}{
		{"TOKEN_MISSING", http.StatusUnauthorized, "TOKEN_MISSING", "authentication required"},
		{"TREE_NOT_FOUND", http.StatusNotFound, "TREE_NOT_FOUND", "tree not found"},
		{"CONNECTION_REFUSED", http.StatusBadGateway, "UPSTREAM_UNAVAILABLE", "upstream unavailable"},
	}

	commands := []struct {
		name string
		run  func() int
	}{
		{"tree list", func() int { return runTreeCmdE([]string{"list"}) }},
		{"tree create", func() int { return runTreeCmdE([]string{"create", "Scratch", "--content", "hello"}) }},
		{"tree delete", func() int { return runTreeCmdE([]string{"delete", treeID}) }},
		{"tree navigate", func() int { return runTreeCmdE([]string{"navigate", treeID}) }},
		{"topic proposals", func() int { return topicProposalsE([]string{"--tree", treeID}) }},
		{"topic config", func() int { return topicConfigE([]string{"--tree", treeID}) }},
		{"topic config put", func() int { return topicConfigE([]string{"--tree", treeID, "--level", "full"}) }},
	}

	for _, env := range envelopes {
		t.Run(env.code, func(t *testing.T) {
			srv := stubEnvelopeServer(t, env.status, env.code, env.msg)
			clearCLITargetEnv(t)
			t.Setenv("CANOPY_SERVER_URL", srv.URL)
			t.Setenv("CANOPY_TOKEN", "stub-token")

			for _, cmd := range commands {
				t.Run(cmd.name, func(t *testing.T) {
					code, out := captureStderr(t, cmd.run)

					if code == 0 {
						t.Errorf("exit = 0 for %s, want nonzero (stderr: %q)", env.code, out)
					}
					if !strings.Contains(out, env.code) {
						t.Errorf("stderr does not report %s: %q", env.code, out)
					}
					if !strings.Contains(out, "Error:") {
						t.Errorf("stderr is not shaped as a CLI error: %q", out)
					}
				})
			}
		})
	}
}

// TestCLIUnreachableServerExitsNonzero asserts a transport failure (the
// server is gone) also exits nonzero for every command path.
func TestCLIUnreachableServerExitsNonzero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening on url any more

	clearCLITargetEnv(t)
	t.Setenv("CANOPY_SERVER_URL", url)
	t.Setenv("CANOPY_TOKEN", "stub-token")

	const treeID = "11111111-1111-4111-8111-111111111111"
	for _, cmd := range []struct {
		name string
		run  func() int
	}{
		{"tree list", func() int { return runTreeCmdE([]string{"list"}) }},
		{"tree delete", func() int { return runTreeCmdE([]string{"delete", treeID}) }},
		{"topic config", func() int { return topicConfigE([]string{"--tree", treeID}) }},
	} {
		t.Run(cmd.name, func(t *testing.T) {
			code, out := captureStderr(t, cmd.run)
			if code == 0 {
				t.Errorf("exit = 0 against an unreachable server, want nonzero (stderr: %q)", out)
			}
			if !strings.Contains(out, "failed to reach server") {
				t.Errorf("stderr does not report the transport failure: %q", out)
			}
		})
	}
}

// TestCLIErrorEnvelopeDetectionIsNotTextMatching guards the mechanism behind
// AC5: the nonzero exit comes from apiRequestE returning the error, so a
// caller that ignores the error would still succeed. This test asserts the
// helper itself reports the envelope.
func TestCLIErrorEnvelopeDetectionIsNotTextMatching(t *testing.T) {
	srv := stubEnvelopeServer(t, http.StatusUnauthorized, "TOKEN_MISSING", "authentication required")
	clearCLITargetEnv(t)
	t.Setenv("CANOPY_SERVER_URL", srv.URL)
	t.Setenv("CANOPY_TOKEN", "stub-token")

	_, status, err := apiRequestE(http.MethodGet, "/api/v1/trees", nil)
	if err == nil {
		t.Fatal("apiRequestE returned nil error for a 401 envelope")
	}
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", status)
	}
	if !strings.Contains(err.Error(), "TOKEN_MISSING") {
		t.Errorf("error does not carry the envelope code: %v", err)
	}
	if errors.Is(err, ErrCLITargetUnresolved) {
		t.Errorf("a server error was misreported as a target refusal: %v", err)
	}
}
