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
// $HOME and never cleaned — while an unset or empty value keeps the per-port
// default (~/.hermes/canopy/gateway/runs-<port>.jsonl) for the canonical port.
//
// The override names the JSONL FILE, not a directory: a test row with no
// .jsonl suffix is deliberate, because joining "runs.jsonl" (or any filename)
// onto the override would change both rows.
func TestDefaultStateFileOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wantDefault := filepath.Join(home, ".hermes", "canopy", "gateway", "runs-8091.jsonl")

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
			name:   "unset override falls back to the per-port HOME registry",
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
			if got := DefaultStateFile(":8091"); got != tc.want {
				t.Fatalf("DefaultStateFile(:8091) = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDefaultStateFileTracksHomeAndFallback proves the un-overridden
// resolution is byte-identical to the per-port default: it follows $HOME, and
// with no $HOME at all (os.UserHomeDir errors on Unix) it lands on /tmp.
func TestDefaultStateFileTracksHomeAndFallback(t *testing.T) {
	unsetEnv(t, "CANOPY_GATEWAY_STATE_FILE")

	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := DefaultStateFile(":8091"), filepath.Join(home, ".hermes", "canopy", "gateway", "runs-8091.jsonl"); got != want {
		t.Fatalf("DefaultStateFile(:8091) with HOME=%q = %q, want %q", home, got, want)
	}

	unsetEnv(t, "HOME")
	if got, want := DefaultStateFile(":8091"), filepath.Join("/tmp", ".hermes", "canopy", "gateway", "runs-8091.jsonl"); got != want {
		t.Fatalf("DefaultStateFile(:8091) with HOME unset = %q, want %q", got, want)
	}
}

// TestDefaultStateFileReadsEnvPerCall pins the documented "resolved on every
// call, never cached" property.
func TestDefaultStateFileReadsEnvPerCall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	unsetEnv(t, "CANOPY_GATEWAY_STATE_FILE")

	if got, want := DefaultStateFile(":8091"), filepath.Join(home, ".hermes", "canopy", "gateway", "runs-8091.jsonl"); got != want {
		t.Fatalf("DefaultStateFile(:8091) before override = %q, want %q", got, want)
	}

	t.Setenv("CANOPY_GATEWAY_STATE_FILE", "/tmp/canopy-scratch-late/runs.jsonl")
	if got, want := DefaultStateFile(":8091"), "/tmp/canopy-scratch-late/runs.jsonl"; got != want {
		t.Fatalf("DefaultStateFile(:8091) after override = %q, want %q", got, want)
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

	got := DefaultStateFile(":8091")
	if got == filepath.Join(home, ".hermes", "canopy", "gateway", "runs.jsonl") {
		t.Fatalf("override ignored: DefaultStateFile(:8091) = %q (shared HOME store)", got)
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
// a service built on DefaultStateFile(:8091) must restore its registry from the
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
	svc := NewServiceWithState(c, DefaultStateFile(":8091"))
	t.Cleanup(svc.Close)

	list := svc.ListRuns(context.Background())
	if len(list) != 1 || list[0].RunID != "run_scratch" {
		t.Fatalf("registry was not restored from the override path: %+v", list)
	}
	if _, err := os.Stat(filepath.Join(home, ".hermes")); !os.IsNotExist(err) {
		t.Fatalf("shared HOME store was touched: err=%v", err)
	}
}

// TestDefaultStateFileIsPerPort pins the per-instance default: two distinct
// ports yield two distinct paths, both under ~/.hermes/canopy/gateway/, and the
// canonical port's path is per-port (runs-8091.jsonl), never the legacy
// runs.jsonl. A host:port address extracts the same port as the bare :port
// form.
func TestDefaultStateFileIsPerPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	unsetEnv(t, "CANOPY_GATEWAY_STATE_FILE")

	gatewayDir := filepath.Join(home, ".hermes", "canopy", "gateway")

	a := DefaultStateFile(":8091")
	b := DefaultStateFile(":8092")
	if a == b {
		t.Fatalf("DefaultStateFile(:8091) == DefaultStateFile(:8092) == %q", a)
	}
	if want := filepath.Join(gatewayDir, "runs-8091.jsonl"); a != want {
		t.Fatalf("DefaultStateFile(:8091) = %q, want %q", a, want)
	}
	if want := filepath.Join(gatewayDir, "runs-8092.jsonl"); b != want {
		t.Fatalf("DefaultStateFile(:8092) = %q, want %q", b, want)
	}
	if got := DefaultStateFile("127.0.0.1:8092"); got != b {
		t.Fatalf("DefaultStateFile(127.0.0.1:8092) = %q, want %q", got, b)
	}
	// The canonical default is per-port, not the legacy shared file.
	if want := filepath.Join(gatewayDir, "runs.jsonl"); a == want {
		t.Fatalf("DefaultStateFile(:8091) = %q (legacy shared path, not per-port)", a)
	}
}

// TestMigrateLegacyStateFile pins the one-time migration contract: the legacy
// runs.jsonl is renamed forward to the per-port target ONLY when the address is
// the canonical port and the target is absent; a non-default port leaves the
// legacy file untouched, and the override disables migration entirely.
func TestMigrateLegacyStateFile(t *testing.T) {
	unsetEnv(t, "CANOPY_GATEWAY_STATE_FILE")

	// fresh isolates each subtest with its own HOME and a seeded legacy file,
	// returning the legacy and canonical-target paths.
	fresh := func(t *testing.T) (legacy, target string) {
		t.Helper()
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, ".hermes", "canopy", "gateway")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		legacy = filepath.Join(dir, "runs.jsonl")
		target = filepath.Join(dir, "runs-8091.jsonl")
		if err := os.WriteFile(legacy, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return legacy, target
	}

	t.Run("canonical port migrates when target absent", func(t *testing.T) {
		legacy, target := fresh(t)
		if err := MigrateLegacyStateFile(":8091"); err != nil {
			t.Fatalf("MigrateLegacyStateFile(:8091): %v", err)
		}
		if _, err := os.Stat(legacy); !os.IsNotExist(err) {
			t.Fatalf("legacy file still present after migration: %v", err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("per-port target missing after migration: %v", err)
		}
	})

	t.Run("non-default port leaves legacy alone", func(t *testing.T) {
		legacy, _ := fresh(t)
		if err := MigrateLegacyStateFile(":8092"); err != nil {
			t.Fatalf("MigrateLegacyStateFile(:8092): %v", err)
		}
		if _, err := os.Stat(legacy); err != nil {
			t.Fatalf("legacy file was touched on non-default port: %v", err)
		}
	})

	t.Run("canonical port leaves legacy when target already present", func(t *testing.T) {
		legacy, target := fresh(t)
		if err := os.WriteFile(target, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := MigrateLegacyStateFile(":8091"); err != nil {
			t.Fatalf("MigrateLegacyStateFile(:8091): %v", err)
		}
		if _, err := os.Stat(legacy); err != nil {
			t.Fatalf("legacy file was renamed despite an existing target: %v", err)
		}
	})

	t.Run("override disables migration", func(t *testing.T) {
		legacy, _ := fresh(t)
		t.Setenv("CANOPY_GATEWAY_STATE_FILE", filepath.Join(t.TempDir(), "override.jsonl"))
		if err := MigrateLegacyStateFile(":8091"); err != nil {
			t.Fatalf("MigrateLegacyStateFile(:8091) with override: %v", err)
		}
		if _, err := os.Stat(legacy); err != nil {
			t.Fatalf("legacy file was touched despite the override: %v", err)
		}
	})
}
