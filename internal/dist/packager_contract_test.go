package dist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/updater"
)

// TestPlatformForFilenameGlobContract pins the canopyd_<os>_<arch>[.exe]
// naming contract (SPEC-FTR-06): the five historically hardcoded names must
// keep mapping to the same platforms, the generic pattern must fill in the
// remaining combinations, and every rejection class must stay rejected.
func TestPlatformForFilenameGlobContract(t *testing.T) {
	cases := []struct {
		name string
		want updater.TargetPlatform
		ok   bool
	}{
		// The five previously hardcoded names are unchanged.
		{"canopyd_linux_amd64", updater.TargetPlatform{OS: "linux", Arch: "amd64", Mode: "embed"}, true},
		{"canopyd_linux_arm64", updater.TargetPlatform{OS: "linux", Arch: "arm64", Mode: "embed"}, true},
		{"canopyd_darwin_amd64", updater.TargetPlatform{OS: "darwin", Arch: "amd64", Mode: "embed"}, true},
		{"canopyd_darwin_arm64", updater.TargetPlatform{OS: "darwin", Arch: "arm64", Mode: "embed"}, true},
		{"canopyd_windows_amd64.exe", updater.TargetPlatform{OS: "windows", Arch: "amd64", Mode: "embed"}, true},
		// Generic pattern fills the remaining combination.
		{"canopyd_windows_arm64.exe", updater.TargetPlatform{OS: "windows", Arch: "arm64", Mode: "embed"}, true},
		// Rejection classes.
		{"canopyd_freebsd_amd64", updater.TargetPlatform{}, false},      // unsupported OS
		{"canopyd_linux_arm", updater.TargetPlatform{}, false},          // unsupported arch
		{"canopyd_windows_amd64", updater.TargetPlatform{}, false},      // windows requires .exe
		{"canopyd_linux_amd64.exe", updater.TargetPlatform{}, false},    // .exe only for windows
		{"canopyd_linux_amd64_embed", updater.TargetPlatform{}, false},  // 4 segments
		{"canopyd_linux", updater.TargetPlatform{}, false},              // 2 segments
		{"notes.txt", updater.TargetPlatform{}, false},                  // unrelated file
		{"canopyd_linux_amd64.sha256", updater.TargetPlatform{}, false}, // checksum sidecar
		{"", updater.TargetPlatform{}, false},                           // empty name
		// Installer filenames keep their separate contract.
		{"canopy_0.1.0_amd64.deb", updater.TargetPlatform{OS: "linux", Arch: "amd64", Mode: "installer"}, true},
		{"canopy-0.1.0-1.aarch64.rpm", updater.TargetPlatform{OS: "linux", Arch: "arm64", Mode: "installer"}, true},
		{"Canopy-0.1.0-amd64.msi", updater.TargetPlatform{OS: "windows", Arch: "amd64", Mode: "installer"}, true},
	}
	for _, tc := range cases {
		got, ok := platformForFilename(tc.name)
		if ok != tc.ok {
			t.Errorf("platformForFilename(%q) ok=%v, want %v (got %+v)", tc.name, ok, tc.ok, got)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("platformForFilename(%q) = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// TestBuildManifestFromDirIgnoresUnmappedFiles reproduces the reported
// scenario: a directory containing one recognized binary plus unrelated files
// still builds a manifest for the recognized binary, while a directory with
// no recognized names fails with the accepted-name-pattern error.
func TestBuildManifestFromDirIgnoresUnmappedFiles(t *testing.T) {
	publicKey, privateKey := testKey(t)

	dir := t.TempDir()
	files := map[string][]byte{
		"canopyd_linux_amd64": []byte("linux"),
		"notes.txt":           []byte("hello"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	metadata := Metadata{
		Version:     "1.2.3",
		Channel:     updater.ChannelStable,
		GeneratedAt: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC),
		PublishedAt: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC),
	}
	manifest, err := BuildManifestFromDir(metadata, dir, "https://example.invalid/download/v1.2.3", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Artifacts) != 1 {
		t.Fatalf("artifact count=%d, want 1 (notes.txt must be ignored)", len(manifest.Artifacts))
	}
	if err := VerifyManifest(publicKey, manifest); err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}

	// No recognized artifacts: the error must name the accepted pattern.
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildManifestFromDir(metadata, empty, "https://example.invalid/download/v1.2.3", privateKey); err == nil {
		t.Fatal("BuildManifestFromDir accepted a directory with no release artifacts")
	} else if !strings.Contains(err.Error(), "canopyd_<os>_<arch>[.exe]") {
		t.Fatalf("error %q does not list the accepted name pattern", err)
	} else if !strings.Contains(err.Error(), empty) {
		t.Fatalf("error %q does not name the artifact directory", err)
	}
}
