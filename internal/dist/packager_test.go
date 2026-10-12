package dist

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/updater"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return publicKey, privateKey
}

func signedArtifact(t *testing.T, privateKey ed25519.PrivateKey, name, osName, arch string, data []byte) updater.ReleaseArtifact {
	t.Helper()
	digest, signature, err := SignArtifact(privateKey, data)
	if err != nil {
		t.Fatal(err)
	}
	return updater.ReleaseArtifact{
		Platform:  updater.TargetPlatform{OS: osName, Arch: arch, Mode: "embed"},
		URL:       "https://example.invalid/" + name,
		SHA256:    digest,
		Signature: signature,
	}
}

func testMetadata() Metadata {
	timestamp := time.Date(2026, 10, 12, 15, 0, 0, 0, time.UTC)
	return Metadata{
		Version:     "1.2.3",
		Channel:     updater.ChannelStable,
		GeneratedAt: timestamp,
		PublishedAt: timestamp,
	}
}

func TestGenerateManifestIsStableAndSorted(t *testing.T) {
	publicKey, privateKey := testKey(t)
	linux := signedArtifact(t, privateKey, "canopyd_linux_amd64", "linux", "amd64", []byte("linux"))
	windows := signedArtifact(t, privateKey, "canopyd_windows_amd64.exe", "windows", "amd64", []byte("windows"))
	metadata := testMetadata()

	first, err := GenerateManifest(metadata, []updater.ReleaseArtifact{windows, linux})
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateManifest(metadata, []updater.ReleaseArtifact{linux, windows})
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("manifest changed with input order:\n%s\n%s", firstJSON, secondJSON)
	}
	if got := first.Artifacts[0].Platform.OS; got != "linux" {
		t.Fatalf("first artifact OS=%q, want linux", got)
	}
	if first.Artifacts[0].Version != metadata.Version || first.Artifacts[0].Channel != metadata.Channel {
		t.Fatalf("metadata was not applied: %+v", first.Artifacts[0])
	}
	if err := VerifyArtifact(publicKey, linux, []byte("linux")); err != nil {
		t.Fatalf("VerifyArtifact: %v", err)
	}
}

func TestSignAndVerifyManifestRejectsTampering(t *testing.T) {
	publicKey, privateKey := testKey(t)
	artifact := signedArtifact(t, privateKey, "canopyd_linux_amd64", "linux", "amd64", []byte("binary"))
	manifest, err := GenerateManifest(testMetadata(), []updater.ReleaseArtifact{artifact})
	if err != nil {
		t.Fatal(err)
	}
	if err := SignManifest(privateKey, &manifest); err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifest(publicKey, manifest); err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	manifest.Artifacts[0].SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := VerifyManifest(publicKey, manifest); err == nil {
		t.Fatal("VerifyManifest accepted a tampered artifact declaration")
	}
	if err := VerifyArtifact(publicKey, artifact, []byte("tampered")); err == nil {
		t.Fatal("VerifyArtifact accepted tampered bytes")
	}
}

func TestBuildManifestFromDirSignsRecognizedArtifactsOnly(t *testing.T) {
	publicKey, privateKey := testKey(t)
	dir := t.TempDir()
	files := map[string][]byte{
		"canopyd_windows_amd64.exe":  []byte("windows"),
		"canopyd_linux_amd64":        []byte("linux"),
		"canopyd_linux_amd64.sha256": []byte("checksum"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := BuildManifestFromDir(testMetadata(), dir, "https://github.com/example/canopy/releases/download/v1.2.3", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Artifacts) != 2 {
		t.Fatalf("artifact count=%d, want 2", len(manifest.Artifacts))
	}
	if err := VerifyManifest(publicKey, manifest); err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	for _, artifact := range manifest.Artifacts {
		name := filepath.Base(artifact.URL)
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyArtifact(publicKey, artifact, data); err != nil {
			t.Fatalf("VerifyArtifact(%s): %v", name, err)
		}
	}
}

func TestWriteManifestProducesStableJSON(t *testing.T) {
	_, privateKey := testKey(t)
	artifact := signedArtifact(t, privateKey, "canopyd_linux_amd64", "linux", "amd64", []byte("binary"))
	manifest, err := GenerateManifest(testMetadata(), []updater.ReleaseArtifact{artifact})
	if err != nil {
		t.Fatal(err)
	}
	if err := SignManifest(privateKey, &manifest); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "stable", "latest.json")
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("WriteManifest output changed between writes")
	}
}

func TestParsePrivateKeyRejectsMissingMaterial(t *testing.T) {
	if _, err := ParsePrivateKey(""); err == nil {
		t.Fatal("ParsePrivateKey accepted an empty key")
	}
}
