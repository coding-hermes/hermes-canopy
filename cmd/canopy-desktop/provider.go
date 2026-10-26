package main

import (
	"context"
	"errors"
	"io"

	wailsupdater "github.com/wailsapp/wails/v3/pkg/updater"

	"github.com/coding-hermes/hermes-canopy/internal/updater"
	"github.com/coding-hermes/hermes-canopy/internal/wailsbridge"
)

// releaseFeedProvider adapts the repository-owned ReleaseFeed (the shared
// latest.json contract) to the Wails v3 updater.Provider interface. It lives
// in the nested module because the Provider interface is defined by the
// Wails module; the canopy module must not import Wails. All actual policy —
// channel rules, manifest signature verification, artifact selection, semver
// comparison — lives in internal/wailsbridge and is tested there; this file
// is a thin, pinned shim.
type releaseFeedProvider struct {
	feed *wailsbridge.ReleaseFeed
}

// newReleaseFeedProvider validates the feed configuration.
func newReleaseFeedProvider(feed *wailsbridge.ReleaseFeed) (*releaseFeedProvider, error) {
	if feed == nil {
		return nil, errors.New("canopy-desktop: nil release feed")
	}
	return &releaseFeedProvider{feed: feed}, nil
}

// Name implements wailsupdater.Provider.
func (p *releaseFeedProvider) Name() string { return "canopy-latest-json" }

// release is the wailsupdater.Release shape built from the shared
// updater.ReleaseArtifact. The Verification block feeds the Wails updater's
// verifier registry: our pipeline (internal/dist.SignArtifact) signs the
// SHA-256 digest of the artifact bytes with Ed25519, which maps to
// digestAlgo=sha256 + signatureAlgo=ed25519 (ed25519 signs the digest).
// The artifact URL is stashed in Metadata for Download, mirroring the
// pattern the Wails endpoint provider uses.
func (p *releaseFeedProvider) release(artifact *updater.ReleaseArtifact) (*wailsupdater.Release, error) {
	algo, sigAlgo, digest, signature, err := wailsbridge.VerificationFor(artifact)
	if err != nil {
		return nil, err
	}
	filename := artifact.URL
	for i := len(filename) - 1; i >= 0; i-- {
		if filename[i] == '/' {
			filename = filename[i+1:]
			break
		}
	}
	return &wailsupdater.Release{
		Version:     artifact.Version,
		Channel:     string(artifact.Channel),
		PublishedAt: artifact.PublishedAt,
		Artifact: wailsupdater.Artifact{
			Filename: filename,
			Platform: artifact.Platform.OS,
			Arch:     artifact.Platform.Arch,
		},
		Verification: &wailsupdater.Verification{
			DigestAlgo:    algo,
			Digest:        digest,
			SignatureAlgo: sigAlgo,
			Signature:     signature,
		},
		Metadata: map[string]any{
			"canopy.artifact.url": artifact.URL,
		},
	}, nil
}

// Check implements wailsupdater.Provider: resolve the next release for the
// running platform, or (nil, nil) when up to date. Download delegates to the
// shared downloadTransport below so the artifact bytes flow through the
// same HTTP client policy as the manifest.
func (p *releaseFeedProvider) Check(ctx context.Context, req wailsupdater.CheckRequest) (*wailsupdater.Release, error) {
	artifact, available, err := p.feed.CheckLatest(ctx, req.CurrentVersion)
	if err != nil {
		return nil, err
	}
	if !available {
		return nil, nil
	}
	return p.release(artifact)
}

// Download implements wailsupdater.Provider: stream the artifact bytes for
// the release resolved by Check. The URL is carried in the release metadata
// by Check (the same pattern the wails endpoint provider uses).
func (p *releaseFeedProvider) Download(ctx context.Context, r *wailsupdater.Release, dst io.Writer, onProgress func(written, total int64)) error {
	if r == nil || r.Metadata == nil {
		return errors.New("canopy-desktop: release missing metadata")
	}
	urlStr, ok := r.Metadata["canopy.artifact.url"].(string)
	if !ok || urlStr == "" {
		return errors.New("canopy-desktop: release metadata missing artifact URL")
	}
	return downloadArtifact(ctx, p.feed.HTTPClient, urlStr, dst, onProgress)
}

// Compile-time proof that the shim satisfies the pinned Wails Provider
// interface (wailsupdater.Provider: Name/Check/Download).
var _ wailsupdater.Provider = (*releaseFeedProvider)(nil)
