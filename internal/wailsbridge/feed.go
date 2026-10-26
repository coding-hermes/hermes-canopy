// Package wailsbridge holds the repository-owned parts of the Wails v3
// desktop packaging surface (SPEC-FTR-06 Phase 5) that must compile and test
// on hosts without the Wails framework: a release-feed adapter that exposes
// the existing latest.json contract to the Wails updater, and the desktop
// shell's canopyd backend supervisor contract.
//
// The Wails entry point itself lives in cmd/canopy-desktop as a nested Go
// module so the canopy module never depends on Wails. See
// docs/DESKTOP-WAILS.md for the boundary.
package wailsbridge

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/updater"
)

// feedManifest is the manifest-level wire shape this package parses. It has
// the same fields as updater.ReleaseManifest so the desktop shell reads the
// exact same latest.json documents canopyd consumes; decoding goes through a
// local struct so a future non-strict change to the feed path cannot silently
// alter desktop behaviour. Artifact entries reuse the canonical
// updater.ReleaseArtifact type — the same struct the embed updater matches
// against, so manifest producers cannot publish artifacts that only one of
// the two updaters understands.
type feedManifest struct {
	Channel           updater.ReleaseChannel    `json:"channel"`
	Artifacts         []updater.ReleaseArtifact `json:"artifacts"`
	GeneratedAt       time.Time                 `json:"generated_at"`
	ManifestSignature []byte                    `json:"manifest_signature"`
}

// releaseModeWails is the artifact Platform.Mode the desktop feed prefers
// (SPEC-FTR-06 binary naming: canopyd_<os>_<arch>_wails-style Mode B
// artifacts).
const releaseModeWails = "wails"

// ReleaseFeed reads the existing latest.json release-manifest contract for
// the desktop shell. OS/arch semantics, the channel match rule, and Ed25519
// manifest-signature verification mirror updater.Updater.CheckForUpdate; the
// artifact selection differs in one documented way: a desktop feed prefers
// Mode B artifacts (platform.mode == "wails") and falls back to artifacts
// with an empty mode, because that is what the shared release manifest
// carries for the desktop shell (SPEC-FTR-06 risk 14: only ONE updater runs
// per binary, and in desktop mode it is this one).
//
// Manifest verification is strict: an unsigned manifest is an error, never an
// "up to date" answer. This mirrors internal/dist.VerifyManifest (the
// release-pipeline contract) rather than the updater package's forgiving
// local-testing mode, because a desktop auto-updater has no operator watching
// it.
//
// Build one through NewReleaseFeed so the required fields are validated at
// configuration time; the zero value always errors.
type ReleaseFeed struct {
	// Channel selects which channel's manifest to fetch (stable, beta,
	// nightly). Required.
	Channel updater.ReleaseChannel

	// ManifestURL overrides the per-channel default
	// https://releases.canopy.app/<channel>/latest.json. Empty uses the
	// default, exactly like updater.Updater.ManifestURL.
	ManifestURL string

	// PublicKey is the Ed25519 release key. Required.
	PublicKey ed25519.PublicKey

	// Platform and Arch select the artifact. Defaults: runtime.GOOS /
	// runtime.GOARCH (runtime spellings: "darwin", not "macos").
	Platform string
	Arch     string

	// HTTPClient is optional. The default has a bounded timeout.
	HTTPClient *http.Client
}

// NewReleaseFeed validates the configuration and returns a ready feed.
func NewReleaseFeed(channel updater.ReleaseChannel, manifestURL string, publicKey ed25519.PublicKey) (*ReleaseFeed, error) {
	if err := updater.ValidateChannel(string(channel)); err != nil {
		return nil, err
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("wailsbridge: release feed requires the Ed25519 public key")
	}
	return &ReleaseFeed{
		Channel:     channel,
		ManifestURL: manifestURL,
		PublicKey:   append(ed25519.PublicKey(nil), publicKey...),
	}, nil
}

func (f *ReleaseFeed) httpClient() *http.Client {
	if f.HTTPClient != nil {
		return f.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (f *ReleaseFeed) manifestURL() string {
	if f.ManifestURL != "" {
		return f.ManifestURL
	}
	// Keep the default in one place: the updater package's constant.
	return updater.DefaultManifestURL(f.Channel)
}

func (f *ReleaseFeed) platform() string {
	if f.Platform != "" {
		return f.Platform
	}
	return updater.DefaultPlatform()
}

func (f *ReleaseFeed) arch() string {
	if f.Arch != "" {
		return f.Arch
	}
	return updater.DefaultArch()
}

// fetch downloads and verifies the channel manifest and returns the artifact
// selected for this platform, or (nil, nil) when the manifest carries none.
func (f *ReleaseFeed) fetch(ctx context.Context) (*updater.ReleaseArtifact, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.manifestURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("wailsbridge: create manifest request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("wailsbridge: fetch manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wailsbridge: manifest returned HTTP %s", resp.Status)
	}
	var manifest feedManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("wailsbridge: decode manifest: %w", err)
	}
	if manifest.Channel != "" && manifest.Channel != f.Channel {
		return nil, fmt.Errorf("wailsbridge: manifest channel %q does not match requested channel %q", manifest.Channel, f.Channel)
	}
	if len(manifest.ManifestSignature) == 0 {
		return nil, errors.New("wailsbridge: manifest is not signed (manifest_signature missing)")
	}
	if err := updater.VerifyManifestSignature(f.PublicKey, updater.ReleaseManifest{
		Channel:           manifest.Channel,
		Artifacts:         manifest.Artifacts,
		GeneratedAt:       manifest.GeneratedAt,
		ManifestSignature: manifest.ManifestSignature,
	}); err != nil {
		return nil, fmt.Errorf("wailsbridge: %w", err)
	}
	return f.selectArtifact(&manifest), nil
}

// selectArtifact picks the desktop artifact for the feed's platform/arch:
// the first artifact with mode "wails", else the first with an empty mode.
// All other modes (embed, installer) belong to other updaters.
func (f *ReleaseFeed) selectArtifact(m *feedManifest) *updater.ReleaseArtifact {
	platform := strings.ToLower(f.platform())
	arch := strings.ToLower(f.arch())
	var fallback *updater.ReleaseArtifact
	for i := range m.Artifacts {
		a := &m.Artifacts[i]
		if !strings.EqualFold(a.Platform.OS, platform) || !strings.EqualFold(a.Platform.Arch, arch) {
			continue
		}
		switch a.Platform.Mode {
		case releaseModeWails:
			return a
		case "":
			if fallback == nil {
				fallback = a
			}
		}
	}
	return fallback
}

// CheckLatest reports whether a newer signed release exists for the feed's
// platform, mirroring updater.Updater.CheckForUpdate semantics: semver
// comparison with an optional leading "v", and the nightly channel ignores
// artifacts published more than 7 days ago. Errors are returned, never
// swallowed.
func (f *ReleaseFeed) CheckLatest(ctx context.Context, currentVersion string) (*updater.ReleaseArtifact, bool, error) {
	if currentVersion == "" {
		return nil, false, errors.New("wailsbridge: current version is required")
	}
	artifact, err := f.fetch(ctx)
	if err != nil {
		return nil, false, err
	}
	if artifact == nil {
		return nil, false, nil
	}
	cmp, err := updater.CompareVersions(artifact.Version, currentVersion)
	if err != nil {
		return nil, false, fmt.Errorf("wailsbridge: artifact version: %w", err)
	}
	if f.Channel == updater.ChannelNightly && !artifact.PublishedAt.IsZero() && time.Since(artifact.PublishedAt) > 7*24*time.Hour {
		return nil, false, nil
	}
	if cmp <= 0 {
		return nil, false, nil
	}
	return artifact, true, nil
}

// VerificationFor maps a release artifact to the digest/signature pair an
// artifact-bytes verifier must check, decoded into raw bytes. Our release
// pipeline (internal/dist.SignArtifact) signs the SHA-256 digest of the file
// bytes with Ed25519, so the pair is: SHA-256 digest + Ed25519-over-digest.
// The Wails v3 updater's verifier registry consumes exactly this combination
// (signature algorithm "ed25519" signs the computed digest); the nested
// cmd/canopy-desktop provider shim feeds it these fields.
func VerificationFor(artifact *updater.ReleaseArtifact) (digestAlgo, sigAlgo string, digest, signature []byte, err error) {
	if artifact == nil {
		return "", "", nil, nil, errors.New("wailsbridge: nil release artifact")
	}
	digest, err = hex.DecodeString(strings.TrimSpace(artifact.SHA256))
	if err != nil || len(digest) != 32 {
		return "", "", nil, nil, fmt.Errorf("wailsbridge: artifact %q has no valid SHA-256", artifact.URL)
	}
	if len(artifact.Signature) == 0 && len(artifact.Ed25519Signature) == 0 {
		return "", "", nil, nil, fmt.Errorf("wailsbridge: artifact %q has no Ed25519 signature", artifact.URL)
	}
	if len(artifact.Signature) != 0 {
		signature = artifact.Signature
	} else {
		signature = artifact.Ed25519Signature
	}
	return "sha256", "ed25519", digest, signature, nil
}
