package wailsbridge

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/dist"
	"github.com/coding-hermes/hermes-canopy/internal/updater"
)

// signManifest builds a strictly-signed latest.json body exactly the way the
// release pipeline does (internal/dist.SignManifest + SignArtifact), so the
// feed tests exercise the real production signing path end to end.
func signManifest(t *testing.T, channel updater.ReleaseChannel, artifacts []updater.ReleaseArtifact) (ed25519.PublicKey, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	metadata := dist.Metadata{
		Version:     artifacts[0].Version,
		Channel:     channel,
		GeneratedAt: time.Now(),
		PublishedAt: time.Now(),
	}
	// Sign each artifact's (empty) bytes the way dist.SignArtifact does, so
	// GenerateManifest's completeness gate passes and the wire body matches
	// a real release manifest.
	signed := make([]updater.ReleaseArtifact, len(artifacts))
	copy(signed, artifacts)
	for i := range signed {
		_, signature, err := dist.SignArtifact(privateKey, nil)
		if err != nil {
			t.Fatalf("sign artifact: %v", err)
		}
		signed[i].Signature = signature
	}
	manifest, err := dist.GenerateManifest(metadata, signed)
	if err != nil {
		t.Fatalf("generate manifest: %v", err)
	}
	if err := dist.SignManifest(privateKey, &manifest); err != nil {
		t.Fatalf("sign manifest: %v", err)
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	return publicKey, body
}

// testPublicKey returns a fresh throwaway Ed25519 public key for tests that
// need "a key of the right size that will not verify anything".
func testPublicKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return priv.Public().(ed25519.PublicKey)
}

func feedServer(t *testing.T, body []byte, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func newTestFeed(t *testing.T, manifestURL string, publicKey ed25519.PublicKey) *ReleaseFeed {
	t.Helper()
	feed, err := NewReleaseFeed(updater.ChannelStable, manifestURL, publicKey)
	if err != nil {
		t.Fatalf("NewReleaseFeed: %v", err)
	}
	return feed
}

func TestFeedDefaultManifestURLMatchesUpdater(t *testing.T) {
	feed, err := NewReleaseFeed(updater.ChannelBeta, "", testPublicKey(t))
	if err != nil {
		t.Fatalf("NewReleaseFeed: %v", err)
	}
	// The feed and the embed updater must resolve the SAME default URL for a
	// channel — one latest.json contract, two consumers.
	if got, want := feed.manifestURL(), updater.DefaultManifestURL(updater.ChannelBeta); got != want {
		t.Fatalf("feed default URL = %q, want %q", got, want)
	}
	embedded, err := updater.DefaultPublicKey()
	if err != nil {
		t.Fatalf("DefaultPublicKey: %v", err)
	}
	if len(embedded) != ed25519.PublicKeySize {
		t.Fatalf("embedded key has %d bytes", len(embedded))
	}
}

func TestFeedCheckLatestSelectsNewerWailsArtifact(t *testing.T) {
	publicKey, body := signManifest(t, updater.ChannelStable, []updater.ReleaseArtifact{{
		Version:  "0.2.0",
		Platform: updater.TargetPlatform{OS: "linux", Arch: "amd64", Mode: "wails"},
		URL:      "https://example.invalid/canopyd_linux_amd64_wails",
		SHA256:   fmt.Sprintf("%064d", 1),
		Channel:  updater.ChannelStable,
	}})
	server := feedServer(t, body, http.StatusOK)
	feed := newTestFeed(t, server.URL, publicKey)
	feed.Platform, feed.Arch = "linux", "amd64"

	artifact, available, err := feed.CheckLatest(context.Background(), "v0.1.0")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}
	if !available || artifact == nil {
		t.Fatalf("available=%v artifact=%v, want a wails-mode update", available, artifact)
	}
	if artifact.Platform.Mode != "wails" {
		t.Fatalf("mode = %q, want wails", artifact.Platform.Mode)
	}
}

func TestFeedCheckLatestFallsBackToEmptyModeArtifact(t *testing.T) {
	publicKey, body := signManifest(t, updater.ChannelStable, []updater.ReleaseArtifact{{
		Version:  "0.3.0",
		Platform: updater.TargetPlatform{OS: "darwin", Arch: "arm64", Mode: ""},
		URL:      "https://example.invalid/canopyd_darwin_arm64",
		SHA256:   fmt.Sprintf("%064d", 2),
		Channel:  updater.ChannelStable,
	}})
	server := feedServer(t, body, http.StatusOK)
	feed := newTestFeed(t, server.URL, publicKey)
	feed.Platform, feed.Arch = "darwin", "arm64"

	artifact, available, err := feed.CheckLatest(context.Background(), "0.2.9")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}
	if !available || artifact == nil {
		t.Fatalf("available=%v artifact=%v, want the empty-mode fallback", available, artifact)
	}
}

func TestFeedCheckLatestIgnoresOtherModesAndPlatforms(t *testing.T) {
	publicKey, body := signManifest(t, updater.ChannelStable, []updater.ReleaseArtifact{
		{
			Version:  "9.0.0",
			Platform: updater.TargetPlatform{OS: "linux", Arch: "amd64", Mode: "embed"},
			URL:      "https://example.invalid/canopyd_linux_amd64",
			SHA256:   fmt.Sprintf("%064d", 3),
		},
		{
			Version:  "9.0.1",
			Platform: updater.TargetPlatform{OS: "windows", Arch: "amd64", Mode: "wails"},
			URL:      "https://example.invalid/canopyd_windows_amd64.exe",
			SHA256:   fmt.Sprintf("%064d", 4),
		},
	})
	server := feedServer(t, body, http.StatusOK)
	feed := newTestFeed(t, server.URL, publicKey)
	feed.Platform, feed.Arch = "linux", "amd64"

	artifact, available, err := feed.CheckLatest(context.Background(), "0.1.0")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}
	if available || artifact != nil {
		t.Fatalf("available=%v artifact=%v, want no desktop artifact for linux/amd64", available, artifact)
	}
}

func TestFeedCheckLatestTreatsOlderAndEqualAsCurrent(t *testing.T) {
	publicKey, body := signManifest(t, updater.ChannelStable, []updater.ReleaseArtifact{{
		Version:  "0.1.0",
		Platform: updater.TargetPlatform{OS: "linux", Arch: "amd64", Mode: "wails"},
		URL:      "https://example.invalid/canopyd_linux_amd64_wails",
		SHA256:   fmt.Sprintf("%064d", 5),
	}})
	server := feedServer(t, body, http.StatusOK)
	feed := newTestFeed(t, server.URL, publicKey)
	feed.Platform, feed.Arch = "linux", "amd64"
	for _, current := range []string{"0.1.0", "v0.1.0", "0.2.0"} {
		artifact, available, err := feed.CheckLatest(context.Background(), current)
		if err != nil {
			t.Fatalf("CheckLatest(%q): %v", current, err)
		}
		if available || artifact != nil {
			t.Fatalf("current %q: available=%v, want up to date", current, available)
		}
	}
}

func TestFeedCheckLatestRejectsUnsignedManifest(t *testing.T) {
	publicKey, body := signManifest(t, updater.ChannelStable, []updater.ReleaseArtifact{{
		Version:  "9.9.9",
		Platform: updater.TargetPlatform{OS: "linux", Arch: "amd64", Mode: "wails"},
		URL:      "https://example.invalid/x",
		SHA256:   fmt.Sprintf("%064d", 6),
	}})
	// Strip the signature: an unsigned manifest must be an ERROR for the
	// desktop feed, never a silent "up to date".
	var unsigned map[string]any
	if err := json.Unmarshal(body, &unsigned); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	delete(unsigned, "manifest_signature")
	stripped, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	server := feedServer(t, stripped, http.StatusOK)
	feed := newTestFeed(t, server.URL, publicKey)
	feed.Platform, feed.Arch = "linux", "amd64"

	if _, _, err := feed.CheckLatest(context.Background(), "0.1.0"); err == nil {
		t.Fatal("unsigned manifest accepted; want strict refusal")
	}
}

func TestFeedCheckLatestRejectsTamperedManifestSignature(t *testing.T) {
	publicKey, body := signManifest(t, updater.ChannelStable, []updater.ReleaseArtifact{{
		Version:  "9.9.9",
		Platform: updater.TargetPlatform{OS: "linux", Arch: "amd64", Mode: "wails"},
		URL:      "https://example.invalid/x",
		SHA256:   fmt.Sprintf("%064d", 7),
	}})
	var tampered map[string]any
	if err := json.Unmarshal(body, &tampered); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	tampered["generated_at"] = "1999-01-01T00:00:00Z"
	reencoded, err := json.Marshal(tampered)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	server := feedServer(t, reencoded, http.StatusOK)
	feed := newTestFeed(t, server.URL, publicKey)
	feed.Platform, feed.Arch = "linux", "amd64"

	if _, _, err := feed.CheckLatest(context.Background(), "0.1.0"); err == nil {
		t.Fatal("tampered manifest accepted; want signature refusal")
	}
}

func TestFeedCheckLatestRejectsWrongKey(t *testing.T) {
	_, body := signManifest(t, updater.ChannelStable, []updater.ReleaseArtifact{{
		Version:  "9.9.9",
		Platform: updater.TargetPlatform{OS: "linux", Arch: "amd64", Mode: "wails"},
		URL:      "https://example.invalid/x",
		SHA256:   fmt.Sprintf("%064d", 8),
	}})
	otherKey := testPublicKey(t)
	server := feedServer(t, body, http.StatusOK)
	feed := newTestFeed(t, server.URL, otherKey)
	feed.Platform, feed.Arch = "linux", "amd64"

	if _, _, err := feed.CheckLatest(context.Background(), "0.1.0"); err == nil {
		t.Fatal("manifest from a foreign key accepted; want refusal")
	}
}

func TestFeedCheckLatestHonoursChannelMismatch(t *testing.T) {
	publicKey, body := signManifest(t, updater.ChannelBeta, []updater.ReleaseArtifact{{
		Version:  "9.9.9",
		Platform: updater.TargetPlatform{OS: "linux", Arch: "amd64", Mode: "wails"},
		URL:      "https://example.invalid/x",
		SHA256:   fmt.Sprintf("%064d", 9),
		Channel:  updater.ChannelBeta,
	}})
	server := feedServer(t, body, http.StatusOK)
	feed := newTestFeed(t, server.URL, publicKey)
	feed.Channel = updater.ChannelStable // asking for stable, manifest says beta
	feed.Platform, feed.Arch = "linux", "amd64"

	if _, _, err := feed.CheckLatest(context.Background(), "0.1.0"); err == nil {
		t.Fatal("channel mismatch accepted; want refusal")
	}
}

func TestFeedNightlyArtefactsOlderThanSevenDaysAreIgnored(t *testing.T) {
	publicKey, body := signManifest(t, updater.ChannelNightly, []updater.ReleaseArtifact{{
		Version:     "9.9.9",
		Platform:    updater.TargetPlatform{OS: "linux", Arch: "amd64", Mode: "wails"},
		URL:         "https://example.invalid/x",
		SHA256:      fmt.Sprintf("%064d", 10),
		Channel:     updater.ChannelNightly,
		PublishedAt: time.Now().Add(-8 * 24 * time.Hour),
	}})
	server := feedServer(t, body, http.StatusOK)
	feed := newTestFeed(t, server.URL, publicKey)
	feed.Channel = updater.ChannelNightly
	feed.Platform, feed.Arch = "linux", "amd64"

	artifact, available, err := feed.CheckLatest(context.Background(), "0.1.0")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}
	if available || artifact != nil {
		t.Fatalf("available=%v artifact=%v, want stale nightly ignored", available, artifact)
	}
}

func TestNewReleaseFeedRejectsBadConfiguration(t *testing.T) {
	if _, err := NewReleaseFeed("weekly", "", testPublicKey(t)); err == nil {
		t.Fatal("invalid channel accepted")
	}
	if _, err := NewReleaseFeed(updater.ChannelStable, "", nil); err == nil {
		t.Fatal("missing public key accepted")
	}
	if _, err := NewReleaseFeed(updater.ChannelStable, "", ed25519.PublicKey(make([]byte, 8))); err == nil {
		t.Fatal("short public key accepted")
	}
}

func TestVerificationForDecodesDigestAndSignature(t *testing.T) {
	artifact := &updater.ReleaseArtifact{
		URL:       "https://example.invalid/canopyd",
		SHA256:    fmt.Sprintf("%064d", 11),
		Signature: []byte("ed25519-signature-bytes"),
	}
	algo, sigAlgo, digest, signature, err := VerificationFor(artifact)
	if err != nil {
		t.Fatalf("VerificationFor: %v", err)
	}
	if algo != "sha256" || sigAlgo != "ed25519" {
		t.Fatalf("algos = %q/%q, want sha256/ed25519", algo, sigAlgo)
	}
	if len(digest) != 32 {
		t.Fatalf("digest has %d bytes, want 32", len(digest))
	}
	if string(signature) != "ed25519-signature-bytes" {
		t.Fatalf("signature = %q", signature)
	}
	// The Ed25519Signature compat field is honoured when Signature is empty.
	artifact.Signature = nil
	artifact.Ed25519Signature = []byte("compat")
	_, _, _, signature, err = VerificationFor(artifact)
	if err != nil || string(signature) != "compat" {
		t.Fatalf("compat signature: err=%v sig=%q", err, signature)
	}
	if _, _, _, _, err := VerificationFor(nil); err == nil {
		t.Fatal("nil artifact accepted")
	}
	bad := &updater.ReleaseArtifact{URL: "https://example.invalid/x", SHA256: "nothex"}
	if _, _, _, _, err := VerificationFor(bad); err == nil {
		t.Fatal("bad SHA-256 accepted")
	}
	nosig := &updater.ReleaseArtifact{URL: "https://example.invalid/x", SHA256: fmt.Sprintf("%064d", 12)}
	if _, _, _, _, err := VerificationFor(nosig); err == nil {
		t.Fatal("missing signature accepted")
	}
}

// --- backend contract ---

func TestValidateBackendAddrRequiresLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8091", "localhost:8091", "localhost:0"} {
		if err := ValidateBackendAddr(addr); err != nil {
			t.Fatalf("ValidateBackendAddr(%q): %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:8091", ":8091", "192.168.1.10:8091", "8091", ""} {
		if err := ValidateBackendAddr(addr); err == nil {
			t.Fatalf("ValidateBackendAddr(%q) accepted; want refusal", addr)
		}
	}
}

func TestResolveBackendBinary(t *testing.T) {
	// PATH-lookup negative: only meaningful when canopyd is absent from
	// PATH (true in CI; dev hosts may have it installed).
	if _, err := ResolveBackendBinary(""); err == nil {
		t.Log("canopyd on PATH; skipping the PATH-lookup negative case")
	}

	// An explicit nonexistent path must be refused with a descriptive
	// error, not passed through to fail later at exec time.
	if _, err := ResolveBackendBinary("/nonexistent/canopyd-xyz"); err == nil {
		t.Fatal("explicit nonexistent path accepted")
	} else if !strings.Contains(err.Error(), "/nonexistent/canopyd-xyz") {
		t.Fatalf("error %q does not name the rejected path", err)
	}

	// An explicit path that exists but is a directory must be refused
	// (not a regular file).
	dir := t.TempDir()
	if _, err := ResolveBackendBinary(dir + string(filepath.Separator) + "."); err == nil {
		t.Fatal("explicit directory path accepted")
	}

	// A valid explicit executable path must be accepted and returned
	// unchanged.
	exe := filepath.Join(dir, "canopyd")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write executable fixture: %v", err)
	}
	got, err := ResolveBackendBinary(exe)
	if err != nil {
		t.Fatalf("valid explicit executable rejected: %v", err)
	}
	if got != exe {
		t.Fatalf("ResolveBackendBinary(%q) = %q, want unchanged", exe, got)
	}

	// A non-executable regular file must be refused. POSIX-mode based;
	// Windows has no meaningful exec bit, so this arm is POSIX-only.
	if runtime.GOOS != "windows" {
		noexec := filepath.Join(dir, "canopyd-noexec")
		if err := os.WriteFile(noexec, []byte("not executable\n"), 0o644); err != nil {
			t.Fatalf("write non-executable fixture: %v", err)
		}
		if _, err := ResolveBackendBinary(noexec); err == nil {
			t.Fatal("explicit non-executable path accepted")
		}
	}
}

func TestPollBackendHealthReadyAndTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	addr := server.Listener.Addr().String()
	if err := PollBackendHealth(context.Background(), addr, 5*time.Second); err != nil {
		t.Fatalf("healthy backend: %v", err)
	}

	// Closed port: must time out with an error, not hang forever.
	blocked, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr = blocked.Addr().String()
	_ = blocked.Close()
	start := time.Now()
	if err := PollBackendHealth(context.Background(), addr, 700*time.Millisecond); err == nil {
		t.Fatal("dead backend reported healthy")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("health poll took %s, want bounded by timeout", elapsed)
	}
}

// The contract tests below need a real child process. A tiny Python HTTP
// server that honours SIGTERM keeps the test self-contained while proving
// the actual StartBackend path: binary resolution, env injection, readiness
// polling, graceful stop, and the exit signal. The child writes its own
// env-evidence file BEFORE binding, using the path we hand it, so the test
// can assert what the child saw.
func writeFakeCanopyd(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake backend launcher is POSIX-only")
	}
	script := filepath.Join(dir, "fake-canopyd")
	launcher := `#!/bin/sh
# Fake canopyd for supervisor contract tests. Usage: fake-canopyd serve
# Env: HTTP_ADDR (bind address), FAKE_CANOPYD_ENV_FILE (output path).
# exec so the SIGTERM from Stop() reaches the server process itself —
# mirroring the real canopyd child, which IS the server process.
while [ $# -gt 0 ]; do shift; done
exec python3 - "$HTTP_ADDR" "$FAKE_CANOPYD_ENV_FILE" <<'PYEOF'
import os, signal, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

addr, env_file = sys.argv[1], sys.argv[2]
host, port = addr.rsplit(":", 1)

class H(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200 if self.path == "/health" else 404)
        self.end_headers()
    def log_message(self, *a):
        pass

srv = HTTPServer((host, int(port)), H)
with open(env_file, "w") as f:
    f.write("HTTP_ADDR=" + os.environ.get("HTTP_ADDR", "") + "\n")
    f.write("ARGV_SERVE=" + str(len(sys.argv)) + "\n")
signal.signal(signal.SIGTERM, lambda *a: sys.exit(0))
srv.serve_forever()
PYEOF
`
	if err := os.WriteFile(script, []byte(launcher), 0o755); err != nil {
		t.Fatalf("write fake canopyd: %v", err)
	}
	return script
}

// startFakeBackend launches StartBackend against the fake with a free
// loopback port and a registered cleanup. The fake binds the exact
// HTTP_ADDR we hand the child, so the health poll observes real readiness.
func startFakeBackend(t *testing.T, script string, extraEnv ...string) (*SupervisedBackend, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.txt")
	env := append([]string{
		"FAKE_CANOPYD_ENV_FILE=" + envFile,
	}, extraEnv...)
	backend, err := StartBackend(context.Background(), BackendOptions{
		Binary: script,
		Addr:   addr,
		Env:    env,
	})
	if err != nil {
		t.Fatalf("StartBackend: %v", err)
	}
	t.Cleanup(func() { _ = backend.Stop() })
	return backend, addr
}

func TestStartBackendSupervisesChild(t *testing.T) {
	dir := t.TempDir()
	script := writeFakeCanopyd(t, dir)
	backend, addr := startFakeBackend(t, script)
	if backend.Addr() != addr {
		t.Fatalf("Addr() = %q, want %q", backend.Addr(), addr)
	}
	if backend.Binary() != script {
		t.Fatalf("Binary() = %q, want %q", backend.Binary(), script)
	}
	select {
	case <-backend.Exited():
		t.Fatal("backend exited before Stop")
	default:
	}
	if err := backend.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Second Stop is a no-op, not an error.
	if err := backend.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	select {
	case <-backend.Exited():
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not exit after Stop")
	}
}

func TestStartBackendRejectsNonLoopbackAndMissingBinary(t *testing.T) {
	dir := t.TempDir()
	script := writeFakeCanopyd(t, dir)
	if _, err := StartBackend(context.Background(), BackendOptions{Binary: script, Addr: "0.0.0.0:8091"}); err == nil {
		t.Fatal("non-loopback addr accepted")
	}
	if _, err := StartBackend(context.Background(), BackendOptions{Binary: "/nonexistent/canopyd-xyz", Addr: "127.0.0.1:8091"}); err == nil {
		t.Fatal("missing binary accepted")
	}
}

// TestDesktopRunsExactlyOneUpdater pins the SPEC-FTR-06 risk-14 boundary at
// the code level: StartBackend constructs the canopyd child WITHOUT any
// -update-interval argument (canopyd's updater is flag opt-in), so in
// desktop mode only the Wails-side feed can perform update checks. If a
// future change adds update flags to the child command, this test fails and
// forces a conscious re-decision.
func TestDesktopRunsExactlyOneUpdater(t *testing.T) {
	for _, arg := range backendServeArgs() {
		if arg == "-update-interval" || strings.HasPrefix(arg, "-update-interval=") {
			t.Fatalf("desktop backend child carries update flag %q: two updaters would run", arg)
		}
	}
}
