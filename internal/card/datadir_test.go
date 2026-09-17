package card

import (
	"os"
	"path/filepath"
	"testing"
)

// unsetEnv removes name for the duration of the test and restores the prior
// state (including absent) afterwards. t.Setenv cannot express "absent": an
// empty value and a missing variable both read as "" through os.Getenv — which
// is exactly the fallback contract for CANOPY_CARD_DATA_DIR — but only a real
// unset exercises the branch an un-configured process takes.
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

// TestDataDirOverride pins the DF-HERMES-CANOPY-9 contract: an explicit
// CANOPY_CARD_DATA_DIR is returned verbatim — never joined onto $HOME and
// never cleaned — while an unset or empty value keeps the historical
// $HOME/.hermes/canopy/cards path.
func TestDataDirOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wantDefault := filepath.Join(home, ".hermes", "canopy", "cards")

	tests := []struct {
		name   string
		setVar bool
		value  string
		want   string
	}{
		{
			name:   "override wins over HOME",
			setVar: true,
			value:  "/tmp/canopy-scratch-1234/cards",
			want:   "/tmp/canopy-scratch-1234/cards",
		},
		{
			// A relative value is the sharpest verbatim probe: any
			// filepath.Join(home, ...) would return <home>/relative/... instead.
			name:   "override returned verbatim, relative path not joined to HOME",
			setVar: true,
			value:  "relative/cards-dir",
			want:   "relative/cards-dir",
		},
		{
			// filepath.Clean would strip the trailing separator, so this row
			// fails if the override is ever routed through a path normaliser.
			name:   "override returned verbatim, trailing separator preserved",
			setVar: true,
			value:  "/tmp/canopy-scratch-1234/cards/",
			want:   "/tmp/canopy-scratch-1234/cards/",
		},
		{
			name:   "explicitly empty override falls back to the default",
			setVar: true,
			value:  "",
			want:   wantDefault,
		},
		{
			name:   "unset override falls back to HOME card store",
			setVar: false,
			want:   wantDefault,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setVar {
				t.Setenv("CANOPY_CARD_DATA_DIR", tc.value)
			} else {
				unsetEnv(t, "CANOPY_CARD_DATA_DIR")
			}
			if got := DataDir(); got != tc.want {
				t.Fatalf("DataDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDataDirDefaultTracksHomeAndFallback proves the un-overridden resolution
// is byte-identical to the pre-override behaviour: it follows $HOME, and with
// no $HOME at all (os.UserHomeDir errors on Unix) it lands on /tmp.
func TestDataDirDefaultTracksHomeAndFallback(t *testing.T) {
	unsetEnv(t, "CANOPY_CARD_DATA_DIR")

	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := DataDir(), filepath.Join(home, ".hermes", "canopy", "cards"); got != want {
		t.Fatalf("DataDir() with HOME=%q = %q, want %q", home, got, want)
	}

	unsetEnv(t, "HOME")
	if got, want := DataDir(), filepath.Join("/tmp", ".hermes", "canopy", "cards"); got != want {
		t.Fatalf("DataDir() with HOME unset = %q, want %q", got, want)
	}
}

// TestDataDirReadsEnvPerCall pins the documented "resolved on every call,
// never cached" property: tooling can redirect the card store after the
// process (and any earlier DataDir call) has already happened.
func TestDataDirReadsEnvPerCall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	unsetEnv(t, "CANOPY_CARD_DATA_DIR")

	if got, want := DataDir(), filepath.Join(home, ".hermes", "canopy", "cards"); got != want {
		t.Fatalf("DataDir() before override = %q, want %q", got, want)
	}

	t.Setenv("CANOPY_CARD_DATA_DIR", "/tmp/canopy-scratch-late/cards")
	if got, want := DataDir(), "/tmp/canopy-scratch-late/cards"; got != want {
		t.Fatalf("DataDir() after override = %q, want %q", got, want)
	}
}

// TestCardStoreActuallyLandsInTheOverride is the isolation claim at the STORE
// level rather than the string level: with CANOPY_CARD_DATA_DIR set, opening a
// repository must create the SQLite database inside the override directory and
// leave the shared HOME store untouched. This is the only test here that would
// catch a future refactor that keeps DataDir() correct but stops the manager
// from being built on it.
func TestCardStoreActuallyLandsInTheOverride(t *testing.T) {
	home := t.TempDir()
	scratch := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CANOPY_CARD_DATA_DIR", scratch)

	mgr := NewCardDBManager(DataDir())
	defer func() { _ = mgr.Close() }()

	repo, err := mgr.Repository(CardTypeCompact)
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	if repo == nil {
		t.Fatal("Repository returned a nil repo")
	}

	wantDB := filepath.Join(scratch, string(CardTypeCompact)+".db")
	if _, err := os.Stat(wantDB); err != nil {
		t.Fatalf("card DB was not created under the override dir: stat %s: %v", wantDB, err)
	}
	shared := filepath.Join(home, ".hermes")
	if _, err := os.Stat(shared); !os.IsNotExist(err) {
		t.Fatalf("shared HOME store was touched (%s): err=%v", shared, err)
	}
}
