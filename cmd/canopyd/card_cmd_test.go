package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
)

// ── helpers ───────────────────────────────────────────────────────────────

// cardFixture builds cards through the REAL writer and closes the manager, which
// leaves a quiescent store (`<type>.db` alone) — the shape `card export` is
// pointed at.
func cardFixture(t *testing.T, dir string, ctype card.CardType, n int) []uuid.UUID {
	t.Helper()
	mgr := card.NewCardDBManager(dir)
	repo, err := mgr.Repository(ctype)
	if err != nil {
		t.Fatalf("fixture: Repository(%s): %v", ctype, err)
	}
	ids := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		id := uuid.New()
		if _, err := repo.Create(context.Background(), card.CreateCardInput{
			ID:          id,
			TreeID:      uuid.New(),
			NodeID:      uuid.New(),
			AppID:       "app-cli",
			CardType:    ctype,
			Data:        json.RawMessage(fmt.Sprintf(`{"title":"cli-%d"}`, i)),
			Actions:     []card.CardAction{{Label: "Open", Handler: "open"}},
			ContextHash: fmt.Sprintf("hash-%d", i),
		}); err != nil {
			t.Fatalf("fixture: Create: %v", err)
		}
		if _, err := repo.AppendEvent(context.Background(), id, card.AppendEventInput{
			EventID:   uuid.New(),
			EventType: card.EventAgentProgress,
			ActorKind: card.ActorAgent,
			ActorID:   "worker",
			Payload:   json.RawMessage(`{"step":1}`),
		}); err != nil {
			t.Fatalf("fixture: AppendEvent: %v", err)
		}
		ids = append(ids, id)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("fixture: Close: %v", err)
	}
	return ids
}

// captureOutput runs f with os.Stdout and os.Stderr redirected to pipes and
// returns its exit code plus everything it wrote to each stream.
func captureOutput(t *testing.T, f func() int) (code int, stdout, stderr string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	code = f()
	_ = outW.Close()
	_ = errW.Close()
	outBytes, _ := io.ReadAll(outR)
	errBytes, _ := io.ReadAll(errR)
	return code, string(outBytes), string(errBytes)
}

// parseJSONLines decodes every line of s, failing the test on a blank line, a
// missing trailing newline, or a line that does not parse on its own.
func parseJSONLines(t *testing.T, s string) []map[string]json.RawMessage {
	t.Helper()
	if !strings.HasSuffix(s, "\n") {
		t.Fatalf("export does not end with a newline: %q", s)
	}
	var out []map[string]json.RawMessage
	for i, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			t.Fatalf("line %d is blank", i+1)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %d does not parse on its own: %v\n%s", i+1, err, line)
		}
		out = append(out, m)
	}
	return out
}

// tmpFiles lists the `*.tmp` files a snapshot directory must never leave behind.
func tmpFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	sort.Strings(matches)
	return matches
}

// ── tests ─────────────────────────────────────────────────────────────────

// TestCardExportStdoutIsJSONL is AC1: one JSON object per card on stdout, each
// line parsing on its own, exit 0 — with the human summary kept on stderr so
// stdout is pure export bytes.
func TestCardExportStdoutIsJSONL(t *testing.T) {
	dir := t.TempDir()
	cardFixture(t, dir, card.CardTypeCompact, 3)

	code, out, stderr := captureOutput(t, func() int {
		return cardExportE([]string{"--data-dir", dir})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}

	lines := parseJSONLines(t, out)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, m := range lines {
		if string(m["record"]) != `"card"` {
			t.Errorf("line %d record = %s, want \"card\"", i+1, m["record"])
		}
		if string(m["card_type"]) != `"compact"` {
			t.Errorf("line %d card_type = %s, want \"compact\"", i+1, m["card_type"])
		}
		var cardObj map[string]json.RawMessage
		if err := json.Unmarshal(m["card"], &cardObj); err != nil {
			t.Fatalf("line %d card is not an object: %v", i+1, err)
		}
		for _, key := range []string{"id", "tree_id", "node_id", "app_id", "card_type", "data", "actions", "status", "context_hash", "revision", "created_at", "updated_at"} {
			if _, ok := cardObj[key]; !ok {
				t.Errorf("line %d card is missing %q", i+1, key)
			}
		}
		var events []map[string]json.RawMessage
		if err := json.Unmarshal(m["events"], &events); err != nil {
			t.Fatalf("line %d events is not an array: %v", i+1, err)
		}
		if len(events) != 1 {
			t.Errorf("line %d has %d events, want 1", i+1, len(events))
		}
	}

	if !strings.Contains(stderr, "exported 3 card(s)") {
		t.Errorf("stderr summary missing: %q", stderr)
	}
	if strings.Contains(out, "exported") {
		t.Errorf("the human summary leaked into stdout: %q", out)
	}
}

// TestCardExportOutBytesEqualStdout is AC6 (first half): --out writes exactly the
// bytes stdout would have carried, and stdout stays empty in that mode.
func TestCardExportOutBytesEqualStdout(t *testing.T) {
	dir := t.TempDir()
	cardFixture(t, dir, card.CardTypeCompact, 2)
	outPath := filepath.Join(t.TempDir(), "cards.jsonl")

	code, stdout, stderr := captureOutput(t, func() int {
		return cardExportE([]string{"--data-dir", dir, "--out", outPath})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if stdout != "" {
		t.Errorf("--out wrote %q to stdout, want nothing", stdout)
	}

	fileBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read --out file: %v", err)
	}

	code, stdoutBytes, stderr := captureOutput(t, func() int {
		return cardExportE([]string{"--data-dir", dir})
	})
	if code != 0 {
		t.Fatalf("stdout run exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if !bytes.Equal(fileBytes, []byte(stdoutBytes)) {
		t.Fatalf("--out bytes differ from stdout bytes:\n--out:  %q\nstdout: %q", fileBytes, stdoutBytes)
	}
	if len(fileBytes) == 0 {
		t.Fatal("both exports are empty — the fixture did not reach the exporter")
	}
}

// TestCardExportSnapshot is AC6 (second half): --snapshot-dir writes
// <dir>/cards-<UTC date>.jsonl atomically with the same bytes, prints the path
// and leaves no *.tmp behind.
func TestCardExportSnapshot(t *testing.T) {
	dir := t.TempDir()
	cardFixture(t, dir, card.CardTypeCompact, 2)
	snapDir := t.TempDir()

	// A local time whose UTC date is the NEXT day: the filename must follow UTC.
	cardExportNow = func() time.Time {
		return time.Date(2026, 9, 17, 23, 30, 0, 0, time.FixedZone("UTC-5", -5*3600))
	}
	t.Cleanup(func() { cardExportNow = func() time.Time { return time.Now() } })

	code, stdout, stderr := captureOutput(t, func() int {
		return cardExportE([]string{"--data-dir", dir, "--snapshot-dir", snapDir})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}

	wantPath := filepath.Join(snapDir, "cards-2026-09-18.jsonl")
	if strings.TrimSpace(stdout) != wantPath {
		t.Fatalf("stdout = %q, want the written path %q", stdout, wantPath)
	}
	entries, err := os.ReadDir(snapDir)
	if err != nil {
		t.Fatalf("read snapshot dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "cards-2026-09-18.jsonl" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("snapshot dir holds %v, want exactly cards-2026-09-18.jsonl", names)
	}
	if left := tmpFiles(t, snapDir); len(left) != 0 {
		t.Errorf("snapshot left tmp files behind: %v", left)
	}

	snapBytes, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}

	// Same bytes as a stdout export of the same store.
	var stdoutRun strings.Builder
	code, out, stderr := captureOutput(t, func() int {
		return cardExportE([]string{"--data-dir", dir})
	})
	if code != 0 {
		t.Fatalf("stdout run exit = %d (stderr: %s)", code, stderr)
	}
	stdoutRun.WriteString(out)
	if !bytes.Equal(snapBytes, []byte(stdoutRun.String())) {
		t.Fatalf("snapshot bytes differ from stdout bytes:\nsnapshot: %q\nstdout:   %q", snapBytes, stdoutRun.String())
	}

	// Re-running replaces the file and still leaves no tmp behind.
	code, _, stderr = captureOutput(t, func() int {
		return cardExportE([]string{"--data-dir", dir, "--snapshot-dir", snapDir})
	})
	if code != 0 {
		t.Fatalf("second snapshot run exit = %d (stderr: %s)", code, stderr)
	}
	again, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("re-read snapshot: %v", err)
	}
	if !bytes.Equal(again, snapBytes) {
		t.Error("re-running the snapshot changed its bytes for an unchanged store")
	}
	if left := tmpFiles(t, snapDir); len(left) != 0 {
		t.Errorf("second snapshot left tmp files behind: %v", left)
	}
}

// TestCardExportSnapshotIsAtomic proves the tmp+rename half of AC6 the only way
// that bites: a FAILING export pointed at a snapshot directory must leave the
// previous snapshot untouched and no tmp file behind.
func TestCardExportSnapshotIsAtomic(t *testing.T) {
	snapDir := t.TempDir()
	target := filepath.Join(snapDir, "cards-2026-09-18.jsonl")
	const sentinel = "PREVIOUS SNAPSHOT\n"
	if err := os.WriteFile(target, []byte(sentinel), 0o644); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	cardExportNow = func() time.Time { return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { cardExportNow = func() time.Time { return time.Now() } })

	// A store that makes the export fail: a `*.db` that is not a database.
	brokenDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(brokenDir, "compact.db"), []byte("not sqlite"), 0o600); err != nil {
		t.Fatalf("write broken store: %v", err)
	}

	code, stdout, stderr := captureOutput(t, func() int {
		return cardExportE([]string{"--data-dir", brokenDir, "--snapshot-dir", snapDir})
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for a broken store (stderr: %s)", code, stderr)
	}
	if stdout != "" {
		t.Errorf("a failed snapshot printed %q to stdout, want nothing (the path must only be printed on success)", stdout)
	}
	if !strings.Contains(stderr, "no snapshot written") {
		t.Errorf("stderr does not say the snapshot was not written: %q", stderr)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the previous snapshot was destroyed: %v", err)
	}
	if string(got) != sentinel {
		t.Errorf("the previous snapshot was modified: %q, want %q", got, sentinel)
	}
	if left := tmpFiles(t, snapDir); len(left) != 0 {
		t.Errorf("a failed snapshot left tmp files behind: %v", left)
	}
}

// TestCardExportExitCodes is AC7 plus the usage surface.
func TestCardExportExitCodes(t *testing.T) {
	dir := t.TempDir()
	cardFixture(t, dir, card.CardTypeCompact, 1)

	missing := filepath.Join(t.TempDir(), "no-such-dir")
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, "compact.db"), []byte("not sqlite"), 0o600); err != nil {
		t.Fatalf("write broken store: %v", err)
	}
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tests := []struct {
		name     string
		args     []string
		want     int
		wantErr  string // substring expected on stderr
		wantHelp bool   // usage text expected on stderr
	}{
		{"ok", []string{"--data-dir", dir}, 0, "", false},
		{"unknown flag", []string{"--data-dir", dir, "--nope"}, exitUnknownSubcommand, "flag provided but not defined", true},
		{"out and snapshot", []string{"--data-dir", dir, "--out", filepath.Join(t.TempDir(), "o.jsonl"), "--snapshot-dir", t.TempDir()}, exitUnknownSubcommand, "mutually exclusive", true},
		{"stray positional", []string{"--data-dir", dir, "extra"}, exitUnknownSubcommand, "unexpected argument", true},
		{"missing data dir", []string{"--data-dir", missing}, 1, "does not exist", false},
		{"data dir is a file", []string{"--data-dir", notADir}, 1, "is not a directory", false},
		{"broken store", []string{"--data-dir", broken}, 1, "compact.db", false},
		{"missing snapshot dir", []string{"--data-dir", dir, "--snapshot-dir", missing}, 1, "does the snapshot directory exist", false},
		{"out into a missing dir", []string{"--data-dir", dir, "--out", filepath.Join(missing, "o.jsonl")}, 1, "create", false},
		{"help", []string{"--help"}, 0, "", true},
		{"short help", []string{"-h"}, 0, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := captureOutput(t, func() int { return cardExportE(tt.args) })
			if code != tt.want {
				t.Errorf("exit = %d, want %d (stderr: %s)", code, tt.want, stderr)
			}
			if tt.wantErr != "" && !strings.Contains(stderr, tt.wantErr) {
				t.Errorf("stderr %q does not mention %q", stderr, tt.wantErr)
			}
			if tt.want == exitUnknownSubcommand && !strings.Contains(stderr, "Usage: canopyd card export") {
				t.Errorf("CLI misuse did not print usage: %q", stderr)
			}
			if tt.want != 0 && stdout != "" {
				t.Errorf("a failing run wrote %q to stdout, want nothing", stdout)
			}
		})
	}
}

// TestCardExportHelpAndDispatch covers the subcommand surface: `card --help` and
// `card export --help` exit 0, a bare `card` exits 1 with usage, an unknown card
// subcommand exits 1, and `card` is a registered top-level subcommand.
func TestCardExportHelpAndDispatch(t *testing.T) {
	if !isSubcommand("card") {
		t.Error("card is not a registered top-level subcommand")
	}
	if got := classifyArgs([]string{"card", "export"}); got != argRouteSubcommand {
		t.Errorf("classifyArgs([card export]) = %v, want argRouteSubcommand", got)
	}

	tests := []struct {
		name    string
		args    []string
		run     func() int
		want    int
		wantOut string
	}{
		{"card --help", nil, func() int { return runCardCmdE([]string{"--help"}) }, 0, "Usage: canopyd card <subcommand>"},
		{"card -h", nil, func() int { return runCardCmdE([]string{"-h"}) }, 0, "Usage: canopyd card <subcommand>"},
		{"bare card", nil, func() int { return runCardCmdE(nil) }, 1, "Usage: canopyd card <subcommand>"},
		{"unknown card subcommand", nil, func() int { return runCardCmdE([]string{"bogus"}) }, 1, "unknown card subcommand: bogus"},
		{"card export --help", nil, func() int { return runCardCmdE([]string{"export", "--help"}) }, 0, "Usage: canopyd card export"},
		{"card export -h", nil, func() int { return runCardCmdE([]string{"export", "-h"}) }, 0, "Usage: canopyd card export"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := captureOutput(t, tt.run)
			if code != tt.want {
				t.Errorf("exit = %d, want %d (stderr: %s)", code, tt.want, stderr)
			}
			if !strings.Contains(stderr, tt.wantOut) {
				t.Errorf("stderr %q does not contain %q", stderr, tt.wantOut)
			}
			if stdout != "" {
				t.Errorf("usage went to stdout: %q", stdout)
			}
		})
	}

	// The top-level usage lists the new subcommand.
	_, _, stderr := captureOutput(t, func() int {
		printCLIUsage()
		return 0
	})
	if !strings.Contains(stderr, "card export") || !strings.Contains(stderr, "docs/CARD_EXPORT.md") {
		t.Errorf("top-level usage does not advertise card export: %q", stderr)
	}
}

// TestCardExportDefaultDataDir: with no --data-dir the command resolves
// card.DataDir() at run time, so CANOPY_CARD_DATA_DIR redirects it — and the
// usage text reports the same resolution.
func TestCardExportDefaultDataDir(t *testing.T) {
	dir := t.TempDir()
	cardFixture(t, dir, card.CardTypeCompact, 2)
	t.Setenv("CANOPY_CARD_DATA_DIR", dir)

	code, out, stderr := captureOutput(t, func() int { return cardExportE(nil) })
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if lines := parseJSONLines(t, out); len(lines) != 2 {
		t.Fatalf("got %d lines from the default data dir, want 2", len(lines))
	}

	_, _, usage := captureOutput(t, func() int {
		printCardExportUsage()
		return 0
	})
	if !strings.Contains(usage, dir) {
		t.Errorf("usage does not name the resolved default %q: %q", dir, usage)
	}
}

// TestCardExportOutRemovedOnFailure: a failed --out run leaves no partial export
// behind for a reader to mistake for a complete one.
func TestCardExportOutRemovedOnFailure(t *testing.T) {
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, "compact.db"), []byte("not sqlite"), 0o600); err != nil {
		t.Fatalf("write broken store: %v", err)
	}
	outPath := filepath.Join(t.TempDir(), "cards.jsonl")

	code, _, stderr := captureOutput(t, func() int {
		return cardExportE([]string{"--data-dir", broken, "--out", outPath})
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr: %s)", code, stderr)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Errorf("a partial export was left at %s (stat err: %v)", outPath, err)
	}
}
