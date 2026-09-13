package fileviewer

import (
	"testing"
)

func TestIsValidSemver(t *testing.T) {
	for _, ok := range []string{"0.4.0", "1.2.3", "1.2.3-beta.1"} {
		if !IsValidSemver(ok) {
			t.Fatalf("%q should be valid semver", ok)
		}
	}
	for _, bad := range []string{"dev", "1.2", "v1.2.3", "1.2.3.4", ""} {
		if IsValidSemver(bad) {
			t.Fatalf("%q should be invalid semver", bad)
		}
	}
}

func TestSeededDescriptors_SemverSafe(t *testing.T) {
	// Dev builds carry a non-semver BuildVersion; seeded rows must still
	// satisfy the registry's semver CHECK.
	BuildVersion = "dev"
	descs := seededDescriptors()
	if len(descs) != 7 {
		t.Fatalf("expected 7 descriptors, got %d", len(descs))
	}
	for _, d := range descs {
		if !IsValidSemver(d.Version) || !IsValidSemver(d.CanopydVersion) {
			t.Fatalf("viewer %s got non-semver version %q", d.ViewerSlug, d.Version)
		}
	}
	// A real release version passes through unchanged.
	BuildVersion = "1.2.3"
	descs = seededDescriptors()
	if descs[0].Version != "1.2.3" {
		t.Fatalf("release version should pass through, got %q", descs[0].Version)
	}
	BuildVersion = "dev"
}

func TestExtensionDispatchDerived(t *testing.T) {
	if ExtensionDispatch["pdf"] != "pdf" {
		t.Fatalf("pdf extension should map to the pdf viewer")
	}
	if ExtensionDispatch["csv"] != "csv" {
		t.Fatalf("csv extension should map to the csv viewer")
	}
	if ExtensionDispatch["md"] != "markdown" {
		t.Fatalf("md extension should map to the markdown viewer (dedicated beats code)")
	}
	if ExtensionDispatch["json"] != "json" {
		t.Fatalf("json extension should map to the json viewer (dedicated beats code)")
	}
	if ExtensionDispatch["mp4"] != "audio_video" {
		t.Fatalf("mp4 extension should map to the audio_video viewer")
	}
	if ExtensionDispatch["go"] != "code" {
		t.Fatalf("go extension should map to the code viewer")
	}
}
