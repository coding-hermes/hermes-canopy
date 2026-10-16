// Package dist contains deterministic release-manifest and artifact-signing
// primitives used by the release workflow.
package dist

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/updater"
)

// Metadata is the release information shared by every artifact in a manifest.
// Timestamps are supplied by the caller so tests and repeatable release jobs
// can produce stable output instead of reading the wall clock implicitly.
type Metadata struct {
	Version         string
	Channel         updater.ReleaseChannel
	GeneratedAt     time.Time
	PublishedAt     time.Time
	MinVersion      string
	ReleaseNotesURL string
}

// GenerateManifest normalizes release metadata, sorts artifacts by their
// stable identity, and returns a manifest compatible with internal/updater.
// It does not sign the result; call SignManifest after all artifact signatures
// have been populated.
func GenerateManifest(metadata Metadata, artifacts []updater.ReleaseArtifact) (updater.ReleaseManifest, error) {
	if strings.TrimSpace(metadata.Version) == "" {
		return updater.ReleaseManifest{}, errors.New("dist: release version is required")
	}
	if err := updater.ValidateChannel(string(metadata.Channel)); err != nil {
		return updater.ReleaseManifest{}, err
	}
	if metadata.GeneratedAt.IsZero() {
		return updater.ReleaseManifest{}, errors.New("dist: generated timestamp is required")
	}
	if metadata.PublishedAt.IsZero() {
		return updater.ReleaseManifest{}, errors.New("dist: published timestamp is required")
	}

	result := make([]updater.ReleaseArtifact, len(artifacts))
	copy(result, artifacts)
	for i := range result {
		if result[i].Version == "" {
			result[i].Version = metadata.Version
		}
		if result[i].Channel == "" {
			result[i].Channel = metadata.Channel
		}
		if result[i].PublishedAt.IsZero() {
			result[i].PublishedAt = metadata.PublishedAt
		}
		if result[i].MinVersion == "" {
			result[i].MinVersion = metadata.MinVersion
		}
		if result[i].ReleaseNotesURL == "" {
			result[i].ReleaseNotesURL = metadata.ReleaseNotesURL
		}
		if result[i].URL == "" {
			return updater.ReleaseManifest{}, fmt.Errorf("dist: artifact at index %d has no URL", i)
		}
		if result[i].SHA256 == "" {
			return updater.ReleaseManifest{}, fmt.Errorf("dist: artifact %q has no SHA-256", result[i].URL)
		}
		if len(result[i].Signature) != ed25519.SignatureSize {
			return updater.ReleaseManifest{}, fmt.Errorf("dist: artifact %q has no Ed25519 signature", result[i].URL)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		return artifactKey(result[i]) < artifactKey(result[j])
	})
	return updater.ReleaseManifest{
		Channel:     metadata.Channel,
		Artifacts:   result,
		GeneratedAt: metadata.GeneratedAt,
	}, nil
}

func artifactKey(artifact updater.ReleaseArtifact) string {
	return strings.Join([]string{
		artifact.Platform.OS,
		artifact.Platform.Arch,
		artifact.Platform.Mode,
		artifact.URL,
		artifact.SHA256,
	}, "\x00")
}

// SignArtifact hashes data with SHA-256 and signs the raw digest. The returned
// signature is the same signature format consumed by updater.DownloadUpdate.
func SignArtifact(privateKey ed25519.PrivateKey, data []byte) (sha256Hex string, signature []byte, err error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", nil, errors.New("dist: an Ed25519 private key is required")
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), ed25519.Sign(privateKey, digest[:]), nil
}

// VerifyArtifact verifies both the declared SHA-256 and the Ed25519 signature
// for an artifact's exact bytes.
func VerifyArtifact(publicKey ed25519.PublicKey, artifact updater.ReleaseArtifact, data []byte) error {
	digest := sha256.Sum256(data)
	if !strings.EqualFold(artifact.SHA256, hex.EncodeToString(digest[:])) {
		return fmt.Errorf("dist: artifact %q has a SHA-256 mismatch", artifact.URL)
	}
	if !updater.VerifySignature(publicKey, digest[:], artifact.Signature) {
		return fmt.Errorf("dist: artifact %q has an invalid Ed25519 signature", artifact.URL)
	}
	return nil
}

// SignManifest signs the SHA-256 of the canonical manifest JSON with its
// manifest_signature field omitted. This is the exact representation verified
// by updater.VerifyManifestSignature.
func SignManifest(privateKey ed25519.PrivateKey, manifest *updater.ReleaseManifest) error {
	if manifest == nil {
		return errors.New("dist: nil manifest")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("dist: an Ed25519 private key is required")
	}
	manifest.ManifestSignature = nil
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("dist: canonicalize manifest: %w", err)
	}
	digest := sha256.Sum256(canonical)
	manifest.ManifestSignature = ed25519.Sign(privateKey, digest[:])
	return nil
}

// VerifyManifest requires a non-empty, valid manifest signature. The updater
// package intentionally permits unsigned local manifests; release output does
// not, so CI cannot accidentally publish an unsigned latest.json.
func VerifyManifest(publicKey ed25519.PublicKey, manifest updater.ReleaseManifest) error {
	if len(manifest.ManifestSignature) != ed25519.SignatureSize {
		return errors.New("dist: manifest signature is required")
	}
	return updater.VerifyManifestSignature(publicKey, manifest)
}

// WriteManifest writes stable, human-readable JSON with a trailing newline.
func WriteManifest(path string, manifest updater.ReleaseManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("dist: encode manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("dist: create manifest directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("dist: write manifest: %w", err)
	}
	return nil
}

// BuildManifestFromDir discovers embed binaries (canopyd_<os>_<arch>[.exe])
// and native installer files in artifactDir, signs every discovered file, and
// signs the manifest. Unknown files (checksums, notes, and temporary files)
// are ignored.
func BuildManifestFromDir(metadata Metadata, artifactDir, baseURL string, privateKey ed25519.PrivateKey) (updater.ReleaseManifest, error) {
	if strings.TrimSpace(baseURL) == "" {
		return updater.ReleaseManifest{}, errors.New("dist: release asset base URL is required")
	}
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		return updater.ReleaseManifest{}, fmt.Errorf("dist: read artifact directory: %w", err)
	}
	baseURL = strings.TrimRight(baseURL, "/")
	artifacts := make([]updater.ReleaseArtifact, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		platform, ok := platformForFilename(entry.Name())
		if !ok {
			continue
		}
		path := filepath.Join(artifactDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return updater.ReleaseManifest{}, fmt.Errorf("dist: read %s: %w", entry.Name(), err)
		}
		sha256Hex, signature, err := SignArtifact(privateKey, data)
		if err != nil {
			return updater.ReleaseManifest{}, err
		}
		artifacts = append(artifacts, updater.ReleaseArtifact{
			Version:         metadata.Version,
			Platform:        platform,
			URL:             baseURL + "/" + url.PathEscape(entry.Name()),
			SHA256:          sha256Hex,
			Signature:       signature,
			Channel:         metadata.Channel,
			PublishedAt:     metadata.PublishedAt,
			MinVersion:      metadata.MinVersion,
			ReleaseNotesURL: metadata.ReleaseNotesURL,
		})
	}
	if len(artifacts) == 0 {
		return updater.ReleaseManifest{}, fmt.Errorf("dist: no release artifacts found in %s (accepted: %s)", artifactDir, acceptedArtifactNames)
	}
	manifest, err := GenerateManifest(metadata, artifacts)
	if err != nil {
		return updater.ReleaseManifest{}, err
	}
	if err := SignManifest(privateKey, &manifest); err != nil {
		return updater.ReleaseManifest{}, err
	}
	return manifest, nil
}

// acceptedArtifactNames describes the platform-filename contract for embed
// binaries (SPEC-FTR-06): "canopyd_<os>_<arch>" with an optional ".exe"
// suffix that is required on windows. os is one of linux, darwin, windows;
// arch is one of amd64, arm64.
const acceptedArtifactNames = "canopyd_<os>_<arch>[.exe]"

// platformForFilename maps an embed-binary filename to its target platform.
// The accepted pattern is canopyd_<os>_<arch> for linux/darwin binaries and
// canopyd_<os>_<arch>.exe for windows binaries. Rejecting a name here makes
// BuildManifestFromDir ignore the file (e.g. checksums, notes, temp files).
func platformForFilename(name string) (updater.TargetPlatform, bool) {
	base := name
	ext := strings.ToLower(filepath.Ext(name))
	hasExe := ext == ".exe"
	if hasExe {
		base = name[:len(name)-len(ext)]
	}
	parts := strings.Split(base, "_")
	// Expect exactly "canopyd" _ <os> _ <arch>; windows binaries carry ".exe".
	if len(parts) != 3 || parts[0] != "canopyd" {
		// Fall back to native installer package matching below.
		return installerForFilename(name, ext)
	}
	osName, arch := parts[1], parts[2]
	if !platformOSAllowed(osName) || !platformArchAllowed(arch) {
		return updater.TargetPlatform{}, false
	}
	if osName == "windows" && !hasExe {
		return updater.TargetPlatform{}, false
	}
	if osName != "windows" && hasExe {
		return updater.TargetPlatform{}, false
	}
	return updater.TargetPlatform{OS: osName, Arch: arch, Mode: "embed"}, true
}

func platformOSAllowed(osName string) bool {
	switch osName {
	case "linux", "darwin", "windows":
		return true
	}
	return false
}

func platformArchAllowed(arch string) bool {
	switch arch {
	case "amd64", "arm64":
		return true
	}
	return false
}

// installerForFilename maps native installer packages (.deb, .rpm, .msi) to
// their target platforms.
func installerForFilename(name, lowerExt string) (updater.TargetPlatform, bool) {
	if lowerExt == ".deb" || lowerExt == ".rpm" {
		arch := "amd64"
		lower := strings.ToLower(name)
		if strings.Contains(lower, "arm64") || strings.Contains(lower, "aarch64") {
			arch = "arm64"
		}
		return updater.TargetPlatform{OS: "linux", Arch: arch, Mode: "installer"}, true
	}
	if lowerExt == ".msi" {
		return updater.TargetPlatform{OS: "windows", Arch: "amd64", Mode: "installer"}, true
	}
	return updater.TargetPlatform{}, false
}
