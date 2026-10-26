package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wailsupdater "github.com/wailsapp/wails/v3/pkg/updater"

	"github.com/coding-hermes/hermes-canopy/internal/dist"
	"github.com/coding-hermes/hermes-canopy/internal/updater"
	"github.com/coding-hermes/hermes-canopy/internal/wailsbridge"
)

// These tests run ONLY in the nested module (server,production tags, no CGO):
// they prove the actual wailsupdater.Provider shim against the pinned Wails
// interface — something the canopy module cannot do without importing Wails.

// signedFeed serves a strictly-signed latest.json body the way the release
// pipeline produces it (dist.GenerateManifest + SignManifest + SignArtifact).
func signedFeed(t *testing.T, version string, artifacts []updater.ReleaseArtifact) (*httptest.Server, ed25519.PublicKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signed := make([]updater.ReleaseArtifact, len(artifacts))
	copy(signed, artifacts)
	for i := range signed {
		_, signature, err := dist.SignArtifact(privateKey, []byte("artifact-bytes"))
		if err != nil {
			t.Fatalf("sign artifact: %v", err)
		}
		signed[i].Signature = signature
	}
	metadata := dist.Metadata{
		Version:     version,
		Channel:     updater.ChannelStable,
		GeneratedAt: time.Now(),
		PublishedAt: time.Now(),
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server, publicKey
}

// TestProviderCheckResolvesWailsRelease proves the shim implements the real
// wailsupdater.Provider interface against a signed feed: Check returns a
// Release carrying the shared-contract Verification block and the artifact
// URL in Metadata for Download.
func TestProviderCheckResolvesWailsRelease(t *testing.T) {
	server, publicKey := signedFeed(t, "9.9.9", []updater.ReleaseArtifact{{
		Version:  "9.9.9",
		Platform: updater.TargetPlatform{OS: "linux", Arch: "amd64", Mode: "wails"},
		URL:      "https://example.invalid/canopyd_linux_amd64_wails",
		SHA256:   sha256Hex([]byte("artifact-bytes")),
		Channel:  updater.ChannelStable,
	}})
	feed, err := wailsbridge.NewReleaseFeed(updater.ChannelStable, server.URL, publicKey)
	if err != nil {
		t.Fatalf("NewReleaseFeed: %v", err)
	}
	feed.Platform, feed.Arch = "linux", "amd64"
	provider, err := newReleaseFeedProvider(feed)
	if err != nil {
		t.Fatalf("newReleaseFeedProvider: %v", err)
	}

	// The shim must satisfy the REAL pinned interface.
	var _ wailsupdater.Provider = provider

	release, err := provider.Check(context.Background(), wailsupdater.CheckRequest{
		CurrentVersion: "0.1.0",
		Platform:       "linux",
		Arch:           "amd64",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if release == nil {
		t.Fatal("Check returned nil release for a newer wails artifact")
	}
	if release.Version != "9.9.9" {
		t.Fatalf("version = %q, want 9.9.9", release.Version)
	}
	if release.Verification == nil || release.Verification.SignatureAlgo != "ed25519" {
		t.Fatalf("verification = %+v, want ed25519 signature block", release.Verification)
	}
	if release.Metadata["canopy.artifact.url"] != "https://example.invalid/canopyd_linux_amd64_wails" {
		t.Fatalf("metadata url = %v", release.Metadata["canopy.artifact.url"])
	}
}

// TestProviderCheckUpToDate proves the (nil, nil) contract for a current
// version — the Wails updater treats that as "no update".
func TestProviderCheckUpToDate(t *testing.T) {
	server, publicKey := signedFeed(t, "0.1.0", []updater.ReleaseArtifact{{
		Version:  "0.1.0",
		Platform: updater.TargetPlatform{OS: "linux", Arch: "amd64", Mode: "wails"},
		URL:      "https://example.invalid/x",
		SHA256:   sha256Hex([]byte("artifact-bytes")),
		Channel:  updater.ChannelStable,
	}})
	feed, err := wailsbridge.NewReleaseFeed(updater.ChannelStable, server.URL, publicKey)
	if err != nil {
		t.Fatalf("NewReleaseFeed: %v", err)
	}
	feed.Platform, feed.Arch = "linux", "amd64"
	provider, err := newReleaseFeedProvider(feed)
	if err != nil {
		t.Fatalf("newReleaseFeedProvider: %v", err)
	}
	release, err := provider.Check(context.Background(), wailsupdater.CheckRequest{
		CurrentVersion: "0.1.0",
		Platform:       "linux",
		Arch:           "amd64",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if release != nil {
		t.Fatalf("release = %+v, want nil (up to date)", release)
	}
}

// TestProviderDownloadStreamsBytes proves Download streams the artifact from
// the URL Check stashed in Metadata.
func TestProviderDownloadStreamsBytes(t *testing.T) {
	const payload = "fake-canopyd-binary-bytes"
	downloadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(downloadServer.Close)
	server, publicKey := signedFeed(t, "9.9.9", []updater.ReleaseArtifact{{
		Version:  "9.9.9",
		Platform: updater.TargetPlatform{OS: "linux", Arch: "amd64", Mode: "wails"},
		URL:      downloadServer.URL + "/bin",
		SHA256:   sha256Hex([]byte(payload)),
		Channel:  updater.ChannelStable,
	}})

	feed, err := wailsbridge.NewReleaseFeed(updater.ChannelStable, server.URL, publicKey)
	if err != nil {
		t.Fatalf("NewReleaseFeed: %v", err)
	}
	feed.Platform, feed.Arch = "linux", "amd64"
	provider, err := newReleaseFeedProvider(feed)
	if err != nil {
		t.Fatalf("newReleaseFeedProvider: %v", err)
	}
	release, err := provider.Check(context.Background(), wailsupdater.CheckRequest{
		CurrentVersion: "0.1.0",
		Platform:       "linux",
		Arch:           "amd64",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	var got strings.Builder
	var progressCalls int
	if err := provider.Download(context.Background(), release, &got, func(written, total int64) {
		progressCalls++
	}); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got.String() != payload {
		t.Fatalf("downloaded %q, want %q", got.String(), payload)
	}
	if progressCalls == 0 {
		t.Fatal("no progress callbacks fired")
	}
}

// TestProviderDownloadRejectsMissingMetadata pins the fail-loud contract for
// a Release that never went through Check.
func TestProviderDownloadRejectsMissingMetadata(t *testing.T) {
	dir := t.TempDir()
	_ = dir
	feed, err := wailsbridge.NewReleaseFeed(updater.ChannelStable, "http://127.0.0.1:1/latest.json", testKey(t))
	if err != nil {
		t.Fatalf("NewReleaseFeed: %v", err)
	}
	provider, err := newReleaseFeedProvider(feed)
	if err != nil {
		t.Fatalf("newReleaseFeedProvider: %v", err)
	}
	if err := provider.Download(context.Background(), &wailsupdater.Release{}, io.Discard, nil); err == nil {
		t.Fatal("Download accepted a release without metadata")
	}
	if err := provider.Download(context.Background(), nil, io.Discard, nil); err == nil {
		t.Fatal("Download accepted a nil release")
	}
}

func testKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return priv.Public().(ed25519.PublicKey)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
