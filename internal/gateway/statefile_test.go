package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unsetEnv removes name for the duration of the test and restores the prior
// state (including absent) afterwards. t.Setenv cannot express "absent": an
// empty value and a missing variable both read as "" through os.Getenv — which
// is exactly the fallback contract for CANOPY_GATEWAY_STATE_FILE — but only a
// real unset exercises the branch an un-configured process takes.
func unsetEnv(t *testing.T, name string) {
	t.Helper()
	prev, had := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("os.Unsetenv(%s): %v", name, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(name, prev)
			return
		}
		_ = os.Unsetenv(name)
	})
}

// TestDefaultStateFileOverride pins the DF-HERMES-CANOPY-9 contract: an
// explicit CANOPY_GATEWAY_STATE_FILE is returned verbatim — never joined onto
// $HOME and never cleaned — while an unset or empty value keeps the historical
// $HOME/.hermes/canopy/gateway/runs.jsonl path.
//
// The override names the JSONL FILE, not a directory: a test row with no
// .jsonl suffix is deliberate, because joining "runs.jsonl" (or any filename)
// onto the override would change both rows.
func TestDefaultStateFileOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wantDefault := filepath.Join(home, ".hermes", "canopy", "gateway", "runs.jsonl")

	tests := []struct {
		name   string
		setVar bool
		value  string
		want   string
	}{
		{
			name:   "override wins over HOME",
			setVar: true,
			value:  "/tmp/canopy-scratch-1234/gateway/runs.jsonl",
			want:   "/tmp/canopy-scratch-1234/gateway/runs.jsonl",
		},
		{
			// A relative value is the sharpest verbatim probe: any
			// filepath.Join(home, ...) would return <home>/relative/... instead.
			name:   "override returned verbatim, relative path not joined to HOME",
			setVar: true,
			value:  "relative/runs.jsonl",
			want:   "relative/runs.jsonl",
		},
		{
			// If the implementation treated the override as a DIRECTORY and
			// appended the basename, this row would come back with a doubled
			// runs.jsonl.
			name:   "override is the file itself, no basename appended",
			setVar: true,
			value:  "/tmp/canopy-scratch-1234/registry",
			want:   "/tmp/canopy-scratch-1234/registry",
		},
		{
			name:   "explicitly empty override falls back to the default",
			setVar: true,
			value:  "",
			want:   wantDefault,
		},
		{
			name:   "unset override falls back to the HOME registry",
			setVar: false,
			want:   wantDefault,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setVar {
				t.Setenv("CANOPY_GATEWAY_STATE_FILE", tc.value)
			} else {
				unsetEnv(t, "CANOPY_GATEWAY_STATE_FILE")
			}
			if got := DefaultStateFile(); got != tc.want {
				t.Fatalf("DefaultStateFile() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDefaultStateFileTracksHomeAndFallback proves the un-overridden
// resolution is byte-identical to the pre-override behaviour: it follows
// $HOME, and with no $HOME at all (os.UserHomeDir errors on Unix) it lands on
// /tmp.
func TestDefaultStateFileTracksHomeAndFallback(t *testing.T) {
	unsetEnv(t, "CANOPY_GATEWAY_STATE_FILE")

	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := DefaultStateFile(), filepath.Join(home, ".hermes", "canopy", "gateway", "runs.jsonl"); got != want {
		t.Fatalf("DefaultStateFile() with HOME=%q = %q, want %q", home, got, want)
	}

	unsetEnv(t, "HOME")
	if got, want := DefaultStateFile(), filepath.Join("/tmp", ".hermes", "canopy", "gateway", "runs.jsonl"); got != want {
		t.Fatalf("DefaultStateFile() with HOME unset = %q, want %q", got, want)
	}
}

// TestDefaultStateFileReadsEnvPerCall pins the documented "resolved on every
// call, never cached" property.
func TestDefaultStateFileReadsEnvPerCall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	unsetEnv(t, "CANOPY_GATEWAY_STATE_FILE")

	if got, want := DefaultStateFile(), filepath.Join(home, ".hermes", "canopy", "gateway", "runs.jsonl"); got != want {
		t.Fatalf("DefaultStateFile() before override = %q, want %q", got, want)
	}

	t.Setenv("CANOPY_GATEWAY_STATE_FILE", "/tmp/canopy-scratch-late/runs.jsonl")
	if got, want := DefaultStateFile(), "/tmp/canopy-scratch-late/runs.jsonl"; got != want {
		t.Fatalf("DefaultStateFile() after override = %q, want %q", got, want)
	}
}

// TestDefaultStateFileOverrideKeepsTheStoreOffHome is the isolation claim
// itself, stated as a test: with the override set, the resolved path must not
// live under a shared $HOME at all. This is the property the scratch recipe
// depends on, and the one that a future Join(home, ...) refactor would break
// while both narrower tests stayed green.
func TestDefaultStateFileOverrideKeepsTheStoreOffHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CANOPY_GATEWAY_STATE_FILE", "/tmp/canopy-scratch-9999/gateway/runs.jsonl")

	got := DefaultStateFile()
	if got == filepath.Join(home, ".hermes", "canopy", "gateway", "runs.jsonl") {
		t.Fatalf("override ignored: DefaultStateFile() = %q (shared HOME store)", got)
	}
	// A path inside `home` relativises without a ".." prefix; an isolating
	// override always walks out of it.
	rel, err := filepath.Rel(home, got)
	if err != nil {
		t.Fatalf("filepath.Rel(%q, %q): %v", home, got, err)
	}
	if !strings.HasPrefix(rel, "..") {
		t.Fatalf("override resolved under $HOME: %q is inside %q (rel=%q)", got, home, rel)
	}
}

// TestGatewayRegistryActuallyReadFromTheOverride is the isolation claim at the
// STORE level rather than the string level: with CANOPY_GATEWAY_STATE_FILE set,
// a service built on DefaultStateFile() must restore its registry from the
// override path — proving the override reaches the persistence layer, not just
// the path helper. This is the only test here that would catch a refactor that
// keeps DefaultStateFile() correct while the service is handed a different path.
func TestGatewayRegistryActuallyReadFromTheOverride(t *testing.T) {
	home := t.TempDir()
	scratch := t.TempDir()
	t.Setenv("HOME", home)
	override := filepath.Join(scratch, "runs.jsonl")
	t.Setenv("CANOPY_GATEWAY_STATE_FILE", override)

	// Terminal record: restore does not refresh it against the gateway, so the
	// assertion is about WHERE the registry was read from, nothing else.
	seedStateFile(t, override, RunRecord{
		RunID:     "run_scratch",
		Status:    "completed",
		CreatedAt: time.Now().UTC(),
	})

	stub := newGatewayStub(nil)
	defer stub.Close()
	c, err := NewClient(stub.URL, "k")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	svc := NewServiceWithState(c, DefaultStateFile())
	t.Cleanup(svc.Close)

	list := svc.ListRuns(context.Background())
	if len(list) != 1 || list[0].RunID != "run_scratch" {
		t.Fatalf("registry was not restored from the override path: %+v", list)
	}
	if _, err := os.Stat(filepath.Join(home, ".hermes")); !os.IsNotExist(err) {
		t.Fatalf("shared HOME store was touched: err=%v", err)
	}
}
