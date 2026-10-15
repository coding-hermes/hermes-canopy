package updater

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckForUpdateSelectsNewerArtifactForPlatform(t *testing.T) {
	manifest := ReleaseManifest{
		Channel: ChannelStable,
		Artifacts: []ReleaseArtifact{
			{Version: "9.0.0", Platform: TargetPlatform{OS: "other", Arch: runtime.GOARCH, Mode: "embed"}, Channel: ChannelStable},
			{Version: "0.2.0", Platform: TargetPlatform{OS: runtime.GOOS, Arch: runtime.GOARCH, Mode: "embed"}, Channel: ChannelStable, URL: "https://example.invalid/canopyd"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(manifest); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	u := &Updater{CurrentVersion: "v0.1.0", Channel: ChannelStable, ManifestURL: server.URL}
	artifact, available, err := u.CheckForUpdate(context.Background())
	if err != nil {
		t.Fatalf("CheckForUpdate: %v", err)
	}
	if !available || artifact == nil {
		t.Fatalf("available=%v artifact=%v, want an update", available, artifact)
	}
	if artifact.Version != "0.2.0" {
		t.Fatalf("version=%q, want 0.2.0", artifact.Version)
	}
}

func TestCheckForUpdateTreatsOlderAndEqualVersionsAsCurrent(t *testing.T) {
	for _, remote := range []string{"0.1.0", "0.0.9"} {
		t.Run(remote, func(t *testing.T) {
			manifest := ReleaseManifest{Channel: ChannelStable, Artifacts: []ReleaseArtifact{{
				Version: remote, Platform: TargetPlatform{OS: runtime.GOOS, Arch: runtime.GOARCH, Mode: "embed"}, Channel: ChannelStable,
			}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(manifest) }))
			defer server.Close()
			u := &Updater{CurrentVersion: "0.1.0", Channel: ChannelStable, ManifestURL: server.URL}
			artifact, available, err := u.CheckForUpdate(context.Background())
			if err != nil {
				t.Fatalf("CheckForUpdate: %v", err)
			}
			if available || artifact != nil {
				t.Fatalf("got artifact=%v available=%v for remote %s", artifact, available, remote)
			}
		})
	}
}

func TestDownloadApplyAndRollbackVerifiesChecksumAndSignature(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("new canopy binary")
	digest := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(payload) }))
	defer server.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "canopyd"), []byte("old canopy binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := &Updater{StateDir: dir, BinaryName: "canopyd", PublicKey: publicKey, HTTPClient: server.Client()}
	artifact := &ReleaseArtifact{
		Version: "0.2.0", URL: server.URL, SHA256: hex.EncodeToString(digest[:]),
		Signature: ed25519.Sign(privateKey, digest[:]),
	}
	if err := u.DownloadUpdate(context.Background(), artifact); err != nil {
		t.Fatalf("DownloadUpdate: %v", err)
	}
	staged, err := os.ReadFile(filepath.Join(dir, "canopyd.new"))
	if err != nil {
		t.Fatal(err)
	}
	if string(staged) != string(payload) {
		t.Fatalf("staged=%q, want %q", staged, payload)
	}
	path, err := u.ApplyUpdate()
	if err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	if path != filepath.Join(dir, "canopyd") {
		t.Fatalf("path=%q", path)
	}
	current, _ := os.ReadFile(path)
	if string(current) != string(payload) {
		t.Fatalf("current=%q, want new payload", current)
	}
	if err := u.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	rolledBack, _ := os.ReadFile(path)
	if string(rolledBack) != "old canopy binary" {
		t.Fatalf("rollback=%q, want old payload", rolledBack)
	}
}

func TestDownloadUpdateRemovesInvalidStagedFiles(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("corrupt")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(payload) }))
	defer server.Close()
	dir := t.TempDir()
	u := &Updater{StateDir: dir, BinaryName: "canopyd", PublicKey: publicKey, HTTPClient: server.Client()}
	err = u.DownloadUpdate(context.Background(), &ReleaseArtifact{URL: server.URL, SHA256: stringsRepeat("0", 64), Signature: make([]byte, ed25519.SignatureSize)})
	if err == nil {
		t.Fatal("DownloadUpdate succeeded for checksum mismatch")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "canopyd.new")); !os.IsNotExist(statErr) {
		t.Fatalf("staged file exists after failure: %v", statErr)
	}
}

func TestRunPeriodicChecksStopsOnContextCancellation(t *testing.T) {
	var checks atomic.Int32
	u := &Updater{CurrentVersion: "0.1.0", Channel: ChannelStable, OnStartupCheck: true, ManifestURL: "://invalid"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		u.RunPeriodicChecks(ctx, time.Hour, func(UpdateCheckResult) { checks.Add(1) })
		close(done)
	}()
	select {
	case <-time.After(time.Second):
		t.Fatal("startup check did not complete")
	case <-func() <-chan struct{} {
		ch := make(chan struct{})
		go func() {
			for checks.Load() == 0 {
				time.Sleep(time.Millisecond)
			}
			close(ch)
		}()
		return ch
	}():
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("periodic checks did not stop")
	}
}

func stringsRepeat(value string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += value
	}
	return result
}

// TestEmbeddedPublicKey pins the go:embed invariant (DF-HERMES-CANOPY-71):
// the binary embeds a valid, non-zero, deterministic Ed25519 public key, so
// the shipped updater can verify signatures without any network fetch.
func TestEmbeddedPublicKey(t *testing.T) {
	key, err := DefaultPublicKey()
	if err != nil {
		t.Fatalf("DefaultPublicKey: %v", err)
	}
	if len(key) != ed25519.PublicKeySize {
		t.Fatalf("embedded public key length = %d, want %d", len(key), ed25519.PublicKeySize)
	}
	if bytes.Equal(key, make([]byte, ed25519.PublicKeySize)) {
		t.Fatal("embedded public key is all zeros")
	}
	again, err := DefaultPublicKey()
	if err != nil {
		t.Fatalf("DefaultPublicKey (2nd call): %v", err)
	}
	if !bytes.Equal(key, again) {
		t.Fatal("embedded public key is not deterministic")
	}
}

func TestParsePublicKeyHex(t *testing.T) {
	valid := "33fec462b7dffac70720f509ab5958481c515947ef9da8fded2cc5a1f28c83c1"
	key, err := ParsePublicKeyHex(valid)
	if err != nil {
		t.Fatalf("ParsePublicKeyHex(valid): %v", err)
	}
	if len(key) != ed25519.PublicKeySize {
		t.Fatalf("parsed key length = %d, want %d", len(key), ed25519.PublicKeySize)
	}
	// Whitespace tolerance: a file read from disk carries a trailing newline.
	key2, err := ParsePublicKeyHex("  " + valid + "\n")
	if err != nil {
		t.Fatalf("ParsePublicKeyHex(whitespace): %v", err)
	}
	if !bytes.Equal(key, key2) {
		t.Fatal("whitespace-tolerant parse changed the key bytes")
	}
	if _, err := ParsePublicKeyHex("zzzz"); err == nil {
		t.Fatal("ParsePublicKeyHex accepted non-hex input")
	}
	if _, err := ParsePublicKeyHex("deadbeef"); err == nil {
		t.Fatal("ParsePublicKeyHex accepted a short key")
	}
	if _, err := ParsePublicKeyHex(stringsRepeat("00", 64)); err == nil {
		t.Fatal("ParsePublicKeyHex accepted a 64-byte key (want 32)")
	}
}

func TestResolvePublicKey(t *testing.T) {
	// Empty path resolves to the embedded default.
	embedded, err := ResolvePublicKey("")
	if err != nil {
		t.Fatalf("ResolvePublicKey(\"\"): %v", err)
	}
	if len(embedded) != ed25519.PublicKeySize {
		t.Fatalf("embedded key length = %d, want %d", len(embedded), ed25519.PublicKeySize)
	}

	// A file override resolves to exactly the written key.
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(path, []byte(hex.EncodeToString(pub)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolvePublicKey(path)
	if err != nil {
		t.Fatalf("ResolvePublicKey(file): %v", err)
	}
	if !bytes.Equal(resolved, pub) {
		t.Fatal("resolved file key differs from the written key")
	}

	// A missing file fails loudly.
	if _, err := ResolvePublicKey(filepath.Join(dir, "nope.txt")); err == nil {
		t.Fatal("ResolvePublicKey accepted a missing file")
	}
	// A malformed file fails loudly.
	bad := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(bad, []byte("not-hex"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePublicKey(bad); err == nil {
		t.Fatal("ResolvePublicKey accepted a malformed key file")
	}
}

// TestUpdaterInitializesWithEmbeddedKeyAndManifestURL proves AC6: an Updater
// built with the embedded key and an explicit manifest URL initializes and
// completes a check against a served manifest. The manifest carries no
// manifest_signature, which VerifyManifestSignature allows for local/test
// manifests, so this exercises initialization and plumbing without needing the
// placeholder key's private half (which is deliberately not in the repo).
func TestUpdaterInitializesWithEmbeddedKeyAndManifestURL(t *testing.T) {
	manifest := ReleaseManifest{
		Channel: ChannelStable,
		Artifacts: []ReleaseArtifact{
			{Version: "0.2.0", Platform: TargetPlatform{OS: runtime.GOOS, Arch: runtime.GOARCH, Mode: "embed"}, Channel: ChannelStable, URL: "https://example.invalid/canopyd"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(manifest); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	publicKey, err := DefaultPublicKey()
	if err != nil {
		t.Fatalf("DefaultPublicKey: %v", err)
	}
	u := &Updater{
		CurrentVersion: "v0.1.0",
		Channel:        ChannelStable,
		ManifestURL:    server.URL,
		PublicKey:      publicKey,
	}
	artifact, available, err := u.CheckForUpdate(context.Background())
	if err != nil {
		t.Fatalf("CheckForUpdate with embedded key: %v", err)
	}
	if !available || artifact == nil {
		t.Fatalf("available=%v artifact=%v, want an update", available, artifact)
	}
	if artifact.Version != "0.2.0" {
		t.Fatalf("artifact version = %q, want 0.2.0", artifact.Version)
	}
}
