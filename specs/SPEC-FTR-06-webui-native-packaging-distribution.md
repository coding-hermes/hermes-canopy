# SPEC-FTR-06 — WebUI Native Packaging & Distribution

> **Status:** Spec | **Phase:** Post-MVP (FTR-06) | **Blocks:** BE-01 (Project Scaffold — build pipeline), DEPLOY-01 (Docker + Compose + WebUI Binary), DEPLOY-03 (CI/CD), DEPLOY-04 (Documentation)
> **References:** T1.6-WebUI-Native-App-Evaluation (full research), ARCHITECTURE.md §9 (Native App Strategy), ARCHITECTURE.md §10.1-10.3 (Cost Estimates), SPEC-FTR-05 (§3 Go Interface pattern), AGENTS.md (§Architecture, §Deployment)
> **Commit:** 1a2a8f6

---

## 1. Purpose

Define the exact implementation contract for Canopy's **native packaging and distribution** layer — the automated build pipeline, cross-platform binary packaging, update delivery mechanism, and distribution channel strategy that transforms a Go binary into a delivered desktop application.

A Go worker reading this document can implement:

- `PackageManager` — coordinates the build, sign, package, and upload pipeline
- `Updater` — handles version checking, download, and atomic swap for self-update
- `BinarySigner` — platform-specific code signing (macOS, Windows, Linux)
- `InstallerBuilder` — produces platform-native installers (.app, .deb, .msi, .AppImage)
- `ReleaseArtifact` — structured metadata for each built artifact
- All automation scripts, GitHub Actions workflows, Docker multi-stage builds, and the `--update` / `--version` CLI flags

without making packaging-tool design decisions. The packaging layer is the **bridge between development and delivery** — it takes a verified Go build output and produces platform-native artifacts that users can install, update, and trust.

**Key insight:** Canopy targets three distribution modes (Go embed PWA, Wails native, Docker) across three OS platforms (Linux, macOS, Windows) with code signing, cross-compilation, and automated updates. The packaging system must be **mode-aware** (different build paths per mode) while maintaining a single codebase and a single CI pipeline. The same `canopyd` binary is wrapped differently for each mode.

---

## 2. Design Decisions

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| 1 | Distribution modes | Three modes: (A) Go embed + PWA, (B) Wails v3 native, (C) Docker container. All three produced from the same repo. | Each mode serves a different use case. Go embed is the zero-effort MVP. Wails is the native desktop upgrade. Docker is the self-hosted/server deployment. All three must be buildable from a single CI run. |
| 2 | Mode A packaging | Single `canopyd` binary with embedded frontend. User runs `canopyd serve` and opens `http://localhost:8080` in browser. | Zero dependencies beyond the binary itself. The user's browser serves as the UI runtime. This is the fastest path to delivery and the documented MVP approach. |
| 3 | Mode B build tool | Wails v3 CLI (`wails build`) invoked from the project's Makefile. The Go backend code is 100% shared with Mode A. | Wails wraps the Go binary in a native WebView window. All backend logic, API handlers, and SSE events are identical. Only the entry point and build flags differ. |
| 4 | Frontend asset handling | Single web build output (`web/dist/`) served by both Mode A (Go embed) and Mode B (Wails embed). No separate frontend variants. | The React PWA is identical in both modes. Wails simply provides a native window container. The same `npm run build` output is embedded by both Go embed and Wails. |
| 5 | Cross-compilation strategy | Go toolchain cross-compilation for Mode A (`GOOS`/`GOARCH`). Wails cross-compilation via platform-specific Docker build containers. | Go cross-compiles trivially. Wails requires platform WebView SDKs (WebView2 on Windows, WKWebView on macOS, WebKitGTK on Linux) which are best provided by Docker build containers or CI runner OS images. |
| 6 | Code signing | macOS: `codesign` + `gon` for notarization. Windows: `signtool` + Azure Key Vault or hardware token. Linux: no signing (community trust model). | macOS and Windows require code signing for gatekeeper and SmartScreen. Linux distribution stores (Snap, Flatpak) handle signing at the store level. Signing keys stored in GitHub Actions secrets with hardware-backed key management where possible. |
| 7 | Auto-update mechanism | **Mode A (Go embed):** Built-in HTTP updater: `GET https://releases.canopy.app/latest.json` → version check → download binary → verify checksum → atomic swap (rename+replace on Unix, copy+delete on Windows). **Mode B (Wails):** Wails v3 embedded updater: same JSON endpoint, Wails handles download + verified swap + restart. | Both modes use the same release metadata endpoint. The Go binary updater is a ~300-line package under `internal/updater/`. Wails uses its own updater but points at the same JSON. A single release workflow publishes artifacts + metadata for both modes. |
| 8 | Update channel strategy | Three channels: `stable` (production), `beta` (pre-release testing), `nightly` (automatic daily build). User opts in via `--channel` CLI flag or config file. | Channels enable staged rollout and early access. A config file toggle (`update_channel = "beta"`) persists the preference across updates. Default: `stable`. Nightly builds expire after 7 days. |
| 9 | Update safety | Atomic swap with rollback: new binary downloaded to `canopyd.new`, verified (SHA-256 + Ed25519 signature), then `rename(2)` on Linux/macOS or `MoveFileEx` on Windows. On failure, `canopyd.old` is restored. | Atomic rename is POSIX-native and crash-safe. On Windows, `MoveFileEx` with `MOVEFILE_REPLACE_EXISTING` provides the same guarantee. The old binary is retained for exactly one update cycle. |
| 10 | Release metadata format | `latest.json`: `{version, sha256, ed25519_signature, url, channel, min_version, release_notes_url, published_at}` JSON at a well-known URL per channel: `https://releases.canopy.app/stable/latest.json`. | Simple JSON is parseable by both the Go updater and Wails. Ed25519 signatures use the project's release signing key (generated once, stored in GitHub secrets). `min_version` enables forced-update scenarios for security patches. |
| 11 | Version scheme | Semantic versioning: `MAJOR.MINOR.PATCH` (e.g., `0.1.0`, `1.0.0`, `1.2.3`). Pre-release suffix for channels: `-beta.1`, `-nightly.20260722`. | Matches Go module versioning and semantic import versioning. Pre-release suffixes are semver-compatible and sort correctly for update checks. |
| 12 | Binary naming convention | `canopyd_{os}_{arch}_{mode}` (e.g., `canopyd_linux_amd64_embed`, `canopyd_darwin_arm64_wails`, `canopyd_windows_amd64_embed.exe`). | Explicit naming prevents confusion across platforms and modes. The release page clearly shows which artifact to download. |
| 13 | Installer formats | Linux: `.deb` (Debian/Ubuntu) + `.rpm` (Fedora/RHEL) + `.AppImage` (universal). macOS: `.dmg` + `.app` bundle. Windows: `.msi` installer. | Platform-standard installers provide the expected user experience: double-click to install, app menu entry, file association registration. `.AppImage` covers the long tail of Linux distributions. |
| 14 | Installer build toolchain | Linux: `nfpm` (Go) for deb/rpm + `appimagetool` for AppImage. macOS: `create-dmg` for .dmg + manual .app bundle structure. Windows: `wix` (Windows Installer XML) for .msi. | `nfpm` is Go-native, cross-platform, and declarative (YAML config). `create-dmg` is a simple shell script. WiX is the standard for Windows MSI packaging. All three can run in CI. |
| 15 | Docker distribution | Multi-stage Dockerfile: `node:22` frontend build → `golang:1.24` backend build → `distroless/static-debian12` runtime image. Image published to `ghcr.io/coding-hermes/canopy` and optionally `docker.io/coding-hermes/canopy`. | Distroless base minimizes attack surface (~5MB image). Multi-stage build avoids bundling build tools in the runtime image. GitHub Container Registry is free for public images. Docker Hub for broader discoverability. |
| 16 | CI pipeline structure | Single GitHub Actions workflow with 4 jobs: (1) **test** (lint, vet, unit tests), (2) **build-embed** (cross-compile Mode A for linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64), (3) **build-wails** (Mode B for each platform — runs on platform-specific runners or Docker), (4) **package** (code sign + create installers + upload to release). | Job dependency: test → build-embed/build-wails (parallel) → package (sequential). This keeps CI fast by parallelizing build targets while ensuring tests pass before packaging. |
| 17 | Release artifact storage | GitHub Releases for binary artifacts. S3 bucket (or Hetzner Storage Box) for nightly builds. `latest.json` served from GitHub Pages (`https://releases.canopy.app/`) via a `gh-pages` branch. | GitHub Releases are free and public for open-source projects. Nightly builds are too numerous for Releases (PR retention limits) — S3/Storage Box is cheaper for transient artifacts. GitHub Pages provides a reliable static file server for the update metadata. |
| 18 | Release signing key | Ed25519 keypair generated once via `openssl genpkey -algorithm ed25519 -out release-key.pem`. Private key stored as GitHub Actions secret `RELEASE_SIGNING_KEY`. Public key embedded in the canopyd binary at compile time via `//go:embed`. | Ed25519 signatures are small (64 bytes) and fast to verify. Embedding the public key means update verification requires no network fetch — the binary already knows what key to trust. Key rotation: include both old and new public keys in the binary for a transition period. |
| 19 | Graceful degradation | If update server is unreachable (network down, DNS failure, server down), the updater silently skips the check and retries on the next interval. No blocking, no error dialogs. | Canopy must never block startup waiting for update checks. Updates are advisory, not mandatory (except security patches via `min_version`). Failed checks are logged to the audit trail (SPEC-DM-03) and retried on the configured interval. |
| 20 | Update check frequency | On startup + every 6 hours while running. Configurable via `--update-interval` flag. | Startup check catches updates after the app was closed. Periodic check catches updates released during a long-running session. 6 hours balances prompt delivery with network pollution. |

---

## 3. Go Interface Definitions

The following package is syntactically compilable Go. All types are defined in `package dist`. The `PackageManager` depends on `internal/updater` for update logic and `internal/build` for build metadata.

### 3.1 Core Types

```go
package dist

import (
    "context"
    "crypto/ed25519"
    "time"

    "github.com/google/uuid"
)

// DeploymentMode represents the three distribution modes.
type DeploymentMode int

const (
    ModeEmbed   DeploymentMode = iota // Go embed + PWA (MVP)
    ModeWails                         // Wails v3 native desktop
    ModeDocker                        // Docker container
)

// String returns the mode identifier for artifact naming.
func (m DeploymentMode) String() string {
    switch m {
    case ModeEmbed:
        return "embed"
    case ModeWails:
        return "wails"
    case ModeDocker:
        return "docker"
    default:
        return "unknown"
    }
}

// ReleaseChannel represents the update channel.
type ReleaseChannel string

const (
    ChannelStable  ReleaseChannel = "stable"
    ChannelBeta    ReleaseChannel = "beta"
    ChannelNightly ReleaseChannel = "nightly"
)

// TargetPlatform represents an OS+arch combination.
type TargetPlatform struct {
    OS       string // "linux", "darwin", "windows"
    Arch     string // "amd64", "arm64"
    Mode     DeploymentMode
}

// ReleaseArtifact represents a single build artifact.
type ReleaseArtifact struct {
    // Version is the semantic version of this release.
    Version string `json:"version"`

    // Platform describes the target OS/arch/mode.
    Platform TargetPlatform `json:"platform"`

    // URL is the download URL for this artifact.
    URL string `json:"url"`

    // SHA256 is the checksum of the artifact.
    SHA256 string `json:"sha256"`

    // Signature is the Ed25519 signature of the SHA-256 hash.
    Signature []byte `json:"signature"`

    // Channel is the release channel this artifact belongs to.
    Channel ReleaseChannel `json:"channel"`

    // PublishedAt is when this artifact was published.
    PublishedAt time.Time `json:"published_at"`

    // MinVersion, if non-empty, requires the user to upgrade from at least
    // this version. Used for forced security updates.
    MinVersion string `json:"min_version,omitempty"`

    // ReleaseNotesURL links to the changelog for this version.
    ReleaseNotesURL string `json:"release_notes_url,omitempty"`
}

// ReleaseManifest is the JSON served at latest.json.
// One manifest per channel.
type ReleaseManifest struct {
    // Channel identifies which channel this manifest represents.
    Channel ReleaseChannel `json:"channel"`

    // Artifacts is the list of available artifacts for this channel.
    Artifacts []ReleaseArtifact `json:"artifacts"`

    // GeneratedAt is when this manifest was generated.
    GeneratedAt time.Time `json:"generated_at"`

    // ManifestSignature is the Ed25519 signature of the JSON content
    // (SHA-256 of the canonical JSON encoding).
    ManifestSignature []byte `json:"manifest_signature"`
}
```

### 3.2 PackageManager — Build + Package + Release Orchestrator

```go
// PackageManager orchestrates the build, sign, package, and upload pipeline.
// It is the single entry point for CI workflows producing release artifacts.
type PackageManager struct {
    // SigningKey is the Ed25519 private key for signing artifacts.
    SigningKey ed25519.PrivateKey

    // Version is the release version being built.
    Version string

    // Channel is the release channel.
    Channel ReleaseChannel

    // OutputDir is where build artifacts are written.
    OutputDir string

    // Platforms is the list of target platforms to build for.
    Platforms []TargetPlatform

    // FrontendDir is the path to the built frontend (web/dist/).
    FrontendDir string

    // GitHubToken is used to upload artifacts to GitHub Releases.
    GitHubToken string
}

// BuildAll builds all artifacts for all configured platforms.
// Returns the list of generated artifacts.
func (pm *PackageManager) BuildAll(ctx context.Context) ([]ReleaseArtifact, error)

// BuildForPlatform builds artifacts for a single platform.
func (pm *PackageManager) BuildForPlatform(ctx context.Context, platform TargetPlatform) (*ReleaseArtifact, error)

// SignArtifact signs a build artifact with the Ed25519 key.
// The signature is over the SHA-256 hash of the artifact content.
func (pm *PackageManager) SignArtifact(artifact *ReleaseArtifact, data []byte) error

// CreateInstaller wraps a binary in platform-native installer format.
// Returned artifact points to the .deb/.dmg/.msi file.
func (pm *PackageManager) CreateInstaller(ctx context.Context, artifact *ReleaseArtifact) (*ReleaseArtifact, error)

// GenerateManifest produces the ReleaseManifest for this release.
func (pm *PackageManager) GenerateManifest(artifacts []ReleaseArtifact) *ReleaseManifest

// UploadRelease uploads artifacts and manifest to GitHub Releases.
// Also updates the gh-pages branch for latest.json.
func (pm *PackageManager) UploadRelease(ctx context.Context, manifest *ReleaseManifest, artifacts []ReleaseArtifact) error
```

### 3.3 Updater — Self-Update for Mode A (Go embed)

```go
// Updater handles self-update for the embedded-mode binary.
// For Wails mode, Wails provides its own updater — use the JSON
// manifest from ReleaseManifest as the data source.
type Updater struct {
    // CurrentVersion is the running binary's version.
    CurrentVersion string

    // Channel is the update channel to check.
    Channel ReleaseChannel

    // ManifestURL is the URL to fetch latest.json from.
    // Default: https://releases.canopy.app/<channel>/latest.json
    ManifestURL string

    // PublicKey is the embedded Ed25519 public key for signature verification.
    PublicKey ed25519.PublicKey

    // CheckInterval is how often to poll for updates.
    // Default: 6 hours.
    CheckInterval time.Duration

    // OnStartupCheck controls whether to check on startup.
    // Default: true.
    OnStartupCheck bool

    // StateDir is where update state is stored (canopyd.new, canopyd.old).
    // Default: the binary's directory.
    StateDir string
}

// CheckForUpdate fetches the release manifest and checks if a newer
// version is available for the current platform.
func (u *Updater) CheckForUpdate(ctx context.Context) (*ReleaseArtifact, bool, error)
// Returns (artifact, true, nil) if update available; (nil, false, nil) if up to date.

// DownloadUpdate downloads the new binary, verifies checksum + signature,
// and stages it as canopyd.new in the StateDir.
func (u *Updater) DownloadUpdate(ctx context.Context, artifact *ReleaseArtifact) error

// ApplyUpdate performs the atomic swap: renames canopyd → canopyd.old,
// renames canopyd.new → canopyd, and returns the path to the new binary.
// On POSIX: os.Rename (atomic if same filesystem).
// On Windows: MoveFileEx with MOVEFILE_REPLACE_EXISTING.
func (u *Updater) ApplyUpdate() (string, error)

// Rollback restores canopyd.old → canopyd.
func (u *Updater) Rollback() error

// Cleanup removes the retained old binary after a successful update.
func (u *Updater) Cleanup() error

// UpdateCheckResult is returned by the periodic check goroutine.
type UpdateCheckResult struct {
    Available bool
    Artifact  *ReleaseArtifact
    CheckedAt time.Time
    Error     error
}
```

### 3.4 InstallerBuilder — Platform-Native Installers

```go
// InstallerBuilder produces platform-native installers from a binary.
// Each platform has its own implementation behind this interface.
type InstallerBuilder interface {
    // Build creates an installer from the given binary path.
    // Returns the path to the generated installer file.
    Build(ctx context.Context, binaryPath string, metadata *InstallerMetadata) (string, error)

    // SupportedExtensions returns the file extensions this builder produces.
    SupportedExtensions() []string
}

// InstallerMetadata contains metadata for installer generation.
type InstallerMetadata struct {
    AppName       string   // "Canopy" or "Hermes Canopy"
    AppVersion    string   // e.g., "0.1.0"
    Description   string   // "Graph-native collaboration surface for human-agent work"
    Homepage      string   // "https://github.com/coding-hermes/hermes-canopy"
    Maintainer    string   // "coding-hermes team"
    Vendor        string   // "coding-hermes"
    License       string   // "MIT"
    IconPath      string   // path to .png or .icns file
    Categories    []string // e.g., ["Utility", "Productivity"]

    // Platform-specific
    BundleID      string   // "app.canopy.desktop" (macOS bundle ID)
    SigningID     string   // "Developer ID Application: ..." (macOS)
    MSIGUID       string   // UUID for Windows MSI
}

// DebInstaller builds .deb packages using nfpm.
type DebInstaller struct {
    ConfigPath string // path to nfpm.yaml template
}

func (d *DebInstaller) Build(ctx context.Context, binaryPath string, meta *InstallerMetadata) (string, error)

// DmgInstaller builds macOS .dmg files.
type DmgInstaller struct {
    AppDirPath string // path to the .app bundle
}

func (d *DmgInstaller) Build(ctx context.Context, binaryPath string, meta *InstallerMetadata) (string, error)

// MsiInstaller builds Windows .msi files using WiX.
type MsiInstaller struct {
    WxsTemplate string // path to .wxs template
}

func (m *MsiInstaller) Build(ctx context.Context, binaryPath string, meta *InstallerMetadata) (string, error)
```

---

## 4. JSON Schema: latest.json Manifest

The release metadata manifest served at `https://releases.canopy.app/{channel}/latest.json` follows this schema:

```json
{
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "title": "Canopy Release Manifest",
    "type": "object",
    "required": ["channel", "artifacts", "generated_at", "manifest_signature"],
    "properties": {
        "channel": {
            "type": "string",
            "enum": ["stable", "beta", "nightly"],
            "description": "Release channel identifier"
        },
        "artifacts": {
            "type": "array",
            "items": {
                "type": "object",
                "required": ["version", "platform", "url", "sha256", "signature", "channel", "published_at"],
                "properties": {
                    "version": {"type": "string", "pattern": "^\\d+\\.\\d+\\.\\d+(-[a-zA-Z0-9.]+)?$"},
                    "platform": {
                        "type": "object",
                        "required": ["os", "arch", "mode"],
                        "properties": {
                            "os": {"type": "string", "enum": ["linux", "darwin", "windows"]},
                            "arch": {"type": "string", "enum": ["amd64", "arm64"]},
                            "mode": {"type": "string", "enum": ["embed", "wails", "docker"]}
                        }
                    },
                    "url": {"type": "string", "format": "uri"},
                    "sha256": {"type": "string", "pattern": "^[a-f0-9]{64}$"},
                    "signature": {"type": "string"},
                    "channel": {"type": "string", "enum": ["stable", "beta", "nightly"]},
                    "published_at": {"type": "string", "format": "date-time"},
                    "min_version": {"type": "string"},
                    "release_notes_url": {"type": "string", "format": "uri"}
                }
            }
        },
        "generated_at": {"type": "string", "format": "date-time"},
        "manifest_signature": {"type": "string"}
    }
}
```

### Example `latest.json` (stable channel):

```json
{
    "channel": "stable",
    "artifacts": [
        {
            "version": "0.1.0",
            "platform": {"os": "linux", "arch": "amd64", "mode": "embed"},
            "url": "https://github.com/coding-hermes/hermes-canopy/releases/download/v0.1.0/canopyd_linux_amd64_embed",
            "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
            "signature": "base64-encoded-ed25519-signature",
            "channel": "stable",
            "published_at": "2026-07-22T00:00:00Z",
            "release_notes_url": "https://github.com/coding-hermes/hermes-canopy/releases/tag/v0.1.0"
        },
        {
            "version": "0.1.0",
            "platform": {"os": "darwin", "arch": "arm64", "mode": "wails"},
            "url": "https://github.com/coding-hermes/hermes-canopy/releases/download/v0.1.0/Canopy-0.1.0-arm64.dmg",
            "sha256": "abc123...",
            "signature": "base64-ed25519-sig",
            "channel": "stable",
            "published_at": "2026-07-22T00:00:00Z"
        }
    ],
    "generated_at": "2026-07-22T12:00:00Z",
    "manifest_signature": "base64-ed25519-manifest-signature"
}
```

---

## 5. CI/CD Workflow

### 5.1 GitHub Actions Workflow Structure

```yaml
name: Release
on:
  push:
    tags: ['v*']               # v0.1.0, v1.0.0-beta.1, etc.
  schedule:
    - cron: '0 6 * * *'        # Daily 06:00 UTC — nightly builds

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.24' }
      - run: go vet ./...
      - run: go test -short ./...

  build-embed:
    needs: test
    strategy:
      matrix:
        os: [linux, darwin, windows]
        arch: [amd64, arm64]
        exclude:
          - os: windows
            arch: arm64            # Win ARM64 deferred until demand
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.24' }
      - uses: actions/setup-node@v4
        with: { node-version: '22' }
      - run: npm ci && npm run build   # Build frontend
        working-directory: web/
      - run: GOOS=${{ matrix.os }} GOARCH=${{ matrix.arch }} go build \
              -ldflags "-X main.version=${{ github.ref_name }}" \
              -o dist/canopyd_${{ matrix.os }}_${{ matrix.arch }}_embed \
              ./cmd/canopyd/
      - uses: actions/upload-artifact@v4
        with:
          name: embed-${{ matrix.os }}-${{ matrix.arch }}
          path: dist/canopyd_${{ matrix.os }}_${{ matrix.arch }}_embed

  build-wails:
    needs: test
    strategy:
      matrix:
        include:
          - os: ubuntu-latest
            goos: linux
            goarch: amd64
            wails_target: linux/amd64
          - os: macos-latest
            goos: darwin
            goarch: arm64
            wails_target: darwin/arm64
          - os: windows-latest
            goos: windows
            goarch: amd64
            wails_target: windows/amd64
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.24' }
      - uses: actions/setup-node@v4
        with: { node-version: '22' }
      - run: go install github.com/wailsapp/wails/v3/cmd/wails@latest
      - run: npm ci && npm run build
        working-directory: web/
      - run: wails build -platform ${{ matrix.wails_target }} \
              -ldflags "-X main.version=${{ github.ref_name }}" \
              -o canopy-${{ matrix.goos }}-${{ matrix.goarch }}
      - uses: actions/upload-artifact@v4
        with:
          name: wails-${{ matrix.goos }}-${{ matrix.goarch }}
          path: build/bin/

  package:
    needs: [build-embed, build-wails]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: |                        # Install packaging tools
          go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest
          sudo apt-get install -y wixl  # for WiX (MSI)
          pip install create-dmg
      - uses: actions/download-artifact@v4  # Download all build outputs
      - run: |                        # Code sign (macOS)
          echo "$MACOS_SIGNING_KEY" > signing.p12
          security import signing.p12 -k ~/Library/Keychains/login.keychain
          for dmg in *.dmg; do
            codesign --sign "Developer ID Application: ..." "$dmg"
            gon gon-config.json
          done
      - run: |                        # Sign artifacts & generate manifest
          go run ./cmd/release-tool/ \
            --signing-key <(echo "$RELEASE_SIGNING_KEY") \
            --version ${{ github.ref_name }} \
            --channel stable \
            --output ./dist/
      - uses: softprops/action-gh-release@v2
        with:
          files: dist/*
          generate_release_notes: true
      - run: |                        # Update gh-pages branch for latest.json
          git checkout gh-pages
          cp dist/latest.json stable/latest.json
          git add stable/latest.json
          git commit -m "chore: update stable latest.json for v${{ github.ref_name }}"
          git push
```

### 5.2 Build Flags

The `canopyd` binary is compiled with build-time version injection:

```go
// cmd/canopyd/main.go
package main

var version = "dev" // set via -ldflags=-X main.version=...

func main() {
    // version is printed by --version and sent to the updater
}
```

Build-time variables:

| Flag | Value | Purpose |
|------|-------|---------|
| `main.version` | `git tag` or `git describe` | Displayed by `canopyd --version`. Used by Updater to check against `latest.json`. |
| `main.commit` | `git rev-parse HEAD` | Embedded for debug builds. Not used by updater. |
| `main.date` | `date -u +%Y-%m-%dT%H:%M:%SZ` | Build timestamp for debug info. |

---

## 6. Directory Layout: Distribution Artifacts

```text
canopy/
├── dist/                              # CI build output
│   ├── canopyd_linux_amd64_embed      # Mode A: Linux x86_64
│   ├── canopyd_linux_arm64_embed      # Mode A: Linux ARM64
│   ├── canopyd_darwin_amd64_embed     # Mode A: macOS Intel
│   ├── canopyd_darwin_arm64_embed     # Mode A: macOS Apple Silicon
│   ├── canopyd_windows_amd64_embed.exe # Mode A: Windows x86_64
│   ├── Canopy-0.1.0-amd64.dmg         # Mode B: macOS Intel
│   ├── Canopy-0.1.0-arm64.dmg         # Mode B: macOS Apple Silicon
│   ├── canopy_0.1.0_amd64.deb         # Mode B: Linux Debian
│   ├── canopy-0.1.0-1.x86_64.rpm     # Mode B: Linux Fedora/RHEL
│   ├── Canopy-0.1.0-x86_64.AppImage   # Mode B: Linux universal
│   ├── Canopy-0.1.0-amd64.msi        # Mode B: Windows
│   ├── latest.json                    # Release manifest (per channel)
│   └── SHA256SUMS                    # Checksum file for all artifacts
├── deploy/
│   ├── nfpm.yaml                      # nfpm config for .deb/.rpm
│   ├── nfpm-linux-arm64.yaml          # ARM64 variant
│   ├── canopy.wxs                     # WiX template for Windows MSI
│   ├── canopy.app.icns               # macOS app icon
│   ├── canopy.png                     # Linux app icon (256x256)
│   ├── canopy.desktop                 # Linux desktop entry
│   ├── canopy.service                 # systemd service unit (server mode)
│   └── Dockerfile                     # Multi-stage Docker build
├── internal/
│   ├── updater/
│   │   ├── updater.go                 # Updater struct + CheckForUpdate
│   │   ├── download.go                # DownloadUpdate + verify
│   │   ├── swap.go                    # ApplyUpdate + Rollback (platform-specific)
│   │   └── updater_test.go
│   └── dist/
│       ├── packager.go                # PackageManager
│       ├── sign.go                    # Artifact signing
│       ├── manifest.go               # latest.json generation
│       └── packager_test.go
└── .github/
    └── workflows/
        └── release.yml               # Release + nightly workflow
```

---

## 7. Edge Cases

| # | Edge Case | Behavior |
|---|-----------|----------|
| 1 | Binary has no write permission in install dir (e.g., `/usr/local/bin/`) | Updater detects permission error, logs to audit trail, shows user message: "Cannot auto-update — run with sudo or install in user-writable directory." Fallback: user downloads and replaces manually. |
| 2 | Partial download (network drops after 60%) | `DownloadUpdate` writes to a `.partial` file first. On resume (next check), the `.partial` file is discarded and download restarts. No partial binary ever becomes `canopyd.new`. |
| 3 | New binary crashes on startup | The updater stores the old binary as `canopyd.old`. If the new binary exits with non-zero within 30 seconds of startup, the launcher script restores the old binary automatically. The user sees a single restart cycle. |
| 4 | Disk full during download | `os.Rename` (atomic swap) fails on full disk. The `.partial` file is cleaned up. The old binary remains intact. Update check retries next interval. Logged to audit trail. |
| 5 | Version downgrade (user on 1.2.0, latest.json shows 1.1.0) | `CheckForUpdate` compares semver: if remote ≤ current, returns `(nil, false)`. No downgrade path unless the user explicitly requests it via `--force-downgrade` flag. |
| 6 | Architecture mismatch (ARM binary downloaded on AMD64) | The updater fetches artifact by platform. `latest.json` contains separate entries per OS/arch/mode. `CheckForUpdate` filters to the current platform. CI must produce all expected platform artifacts before generating `latest.json`. |
| 7 | Wails runtime missing (WebView2 not installed on Windows) | The Wails binary checks for WebView2 runtime on first launch. If missing, displays a dialog linking to the WebView2 installer and exits gracefully. The `.msi` installer can optionally bundle the WebView2 runtime (Evergreen WebView2 bootstrapper). |
| 8 | Nightly build expired (older than 7 days) | Nightly `latest.json` entries include a `published_at` field. The updater checks age: if >7 days, it skips that entry and reports "No nightly update available." The user should switch to stable or beta if they need a recent build. |
| 9 | Concurrent update check while another is in progress | `Updater` uses a mutex: `CheckForUpdate` returns immediately if a check is already running. No concurrent downloads. The periodic goroutine skips skipped checks — the interval counter is based on wall clock, not completion time. |
| 10 | macOS quarantine attribute on downloaded binary | macOS applies the `com.apple.quarantine` xattr to files downloaded via browser/curl. The notarization step (via `gon`) removes this. For direct-download updates, the updater calls `xattr -dr com.apple.quarantine` on the new binary before the atomic swap. |
| 11 | Symlinked install path | `Updater` resolves symlinks before attempting the swap. `os.Readlink` resolves the target, then the swap operates on the resolved path. If the symlink target is read-only, falls to Edge Case 1. |
| 12 | Homebrew (brew) managed installation | `canopyd` installed via Homebrew should NOT use its built-in updater — Homebrew manages versions independently. Detection: check if the binary is inside a Homebrew prefix (`/usr/local/Cellar/`, `/opt/homebrew/Cellar/`). If yes, the updater silently disables itself and logs "Managed by Homebrew — update via `brew upgrade canopy`." |
| 13 | Two canopyd instances running (same binary) | The updater checks for other running instances of the same binary via `pidfile` or `lockfile` before applying the swap. If another instance holds the `canopyd.lock`, the update is deferred to the next check interval. The lock is advisory (flock on the state directory). |
| 14 | Wails updater vs Go embed updater conflict | Only ONE updater runs per binary. Mode A uses the Go `internal/updater`. Mode B uses Wails' built-in updater pointing at the same `latest.json`. They never both exist in the same binary. Controlled by build tags: `//go:build !wails` on Go updater, `//go:build wails` on Wails stub. |

---

## 8. Test Scenarios

| # | Scenario | Setup | Expected |
|---|----------|-------|----------|
| 1 | Cold update check — network available | Start updater with valid manifest URL. Current version: 0.0.1, remote: 0.1.0. | `CheckForUpdate` → returns `(artifact, true, nil)`. Artifact matches current platform. |
| 2 | Cold update check — no update | Same setup, remote version: 0.0.1. | `CheckForUpdate` → returns `(nil, false, nil)`. |
| 3 | Cold update check — network unavailable | `ManifestURL` points to unavailable host. | `CheckForUpdate` returns `(nil, false, ConnectionError)`. Logged to audit trail. Updater resumes next interval. |
| 4 | Download — success | Valid URL, sufficient disk space. | `DownloadUpdate` → binary at `canopyd.new`. SHA-256 matches manifest. Ed25519 signature verifies. |
| 5 | Download — checksum mismatch | Corrupted download. SHA-256 does not match. | `DownloadUpdate` → error. `canopyd.new` deleted. Retry on next interval. |
| 6 | Download — signature verification fails | Valid download, manifest signature is for different key. | `DownloadUpdate` → `ErrInvalidSignature`. `canopyd.new` deleted. Logged as security incident to audit trail. |
| 7 | Atomic swap — success (POSIX) | `canopyd.new` staged. Current binary at `/usr/local/bin/canopyd`. | `ApplyUpdate` → `os.Rename(canopyd, canopyd.old)`, `os.Rename(canopyd.new, canopyd)`. Exit code 0. |
| 8 | Atomic swap — failure (Windows rename collision) | Another process holds a handle to the binary. | `MoveFileEx` returns error. `ApplyUpdate` returns error. `Rollback` restores original. |
| 9 | Rollback — old binary restored | After failed `ApplyUpdate`, `Rollback` called. | `os.Rename(canopyd.old, canopyd)`. Binary restored. Exit code 0. |
| 10 | New binary crash detection | Updated binary exits with non-zero within 30s. | Launcher detects crash, calls `Rollback`, restarts old binary. Logs to audit trail. |
| 11 | Installer — .deb packaging | Build `canopyd_linux_amd64_embed`. Run `nfpm package`. | Produces `canopy_0.1.0_amd64.deb`. `dpkg -I` shows correct metadata (name, version, maintainer). File installs to `/usr/bin/canopyd`. |
| 12 | Installer — .dmg packaging (macOS) | Build Wails app for darwin/arm64. | Produces `Canopy-0.1.0-arm64.dmg`. Mount + copy to `/Applications/` works. `codesign -dv` shows valid signature. |
| 13 | Installer — .msi packaging (Windows) | Build Wails app for windows/amd64. Run WiX. | Produces `Canopy-0.1.0-amd64.msi`. msiexec installs to `Program Files\Canopy\`. Start menu entry created. |
| 14 | Docker — multi-stage build | Run `docker build -t canopy .` from project root. | Build succeeds. Image size <50MB. `docker run -p 8080:8080 canopy` serves frontend at `localhost:8080`. |
| 15 | Version injection | Build with `-ldflags=-X main.version=0.1.0`. | `canopyd --version` outputs `canopyd 0.1.0`. |
| 16 | Cross-compile all platforms | Run `GOOS=linux GOARCH=amd64 go build`, repeat for all 5 embed targets. | All 5 binaries compile without errors. Each runs `--version` correctly on its target platform (test via Docker/QEMU for non-native). |
| 17 | Release manifest generation | CI produces 5 embed + 3 wails artifacts. Run manifest generation. | `latest.json` contains 8 artifact entries. All SHA-256 checksums correct. Manifest signature verifies. |
| 18 | GitHub Pages deployment | CI pushes updated `latest.json` to gh-pages branch. | `https://releases.canopy.app/stable/latest.json` returns valid JSON. Updater `CheckForUpdate` parses correctly. |
| 19 | Wails runtime missing (Windows) | Launch Wails binary on fresh Windows VM without WebView2. | Binary displays dialog: "WebView2 Runtime required — Download from microsoft.com." Graceful exit, no crash. |
| 20 | Homebrew detection | Install canopyd via `brew`. Run `CheckForUpdate`. | Updater silently returns `(nil, false)` with log: "Managed by Homebrew — skipping auto-update." |

---

## 9. Security Considerations

| # | Concern | Mitigation |
|---|---------|------------|
| 1 | Man-in-the-middle on update download | All downloads use HTTPS (TLS 1.3). The SHA-256 checksum is separately signed with Ed25519. The public key is embedded in the binary. Even with compromised TLS, the signature verification catches tampered binaries. |
| 2 | Ed25519 signing key compromise | Key rotation procedure: new keypair generated. Both old and new public keys embedded in the next binary release. Old key removed after all users on that version have upgraded. The release manifest is signed with the new key; `min_version` field forces upgrade from old-key versions. |
| 3 | Rollback to vulnerable version | Version comparison: `CheckForUpdate` only returns updates where remote version > current version. The `min_version` field in the manifest allows the release server to require upgrades. If the user manually installs an older version, the updater still respects the `min_version` from the manifest. |
| 4 | Update server DNS hijack | In addition to TLS, the manifest's Ed25519 signature is verified against the embedded public key. A hijacked server can serve arbitrary JSON but cannot forge a valid signature. The updater discards any manifest with an invalid signature. |
| 5 | Binary planted in StateDir | The `StateDir` is a subdirectory of the binary's directory (or configurable). The updater only touches files it created (`.new`, `.old`, `.partial`). It never executes arbitrary files from `StateDir`. The atomic swap renames only its own `.new` file. |
| 6 | Race condition: update while running | The updater uses advisory locking (flock on StateDir). Only one update operation can run at a time. The binary's critical operations (server, API, SSE) are not affected — the update happens to a separate `.new` file. Only the final `Rename` affects the running binary (and it won't be reflected until restart). |

---

## 10. Implementation Plan

### Phase 1: Build Pipeline & Version Injection (BE-01 Scaffold)

**Files:** `cmd/canopyd/main.go`, `Makefile`, `.github/workflows/release.yml`

1. Add `var version string` to `main.go` with `-ldflags` injection
2. Add `--version` flag to `main.go`
3. Create `Makefile` with `build-embed` targets for all 5 platforms
4. Create `.github/workflows/build.yml` — PR validation (test + build, no packaging)
5. Create `deploy/Dockerfile` — multi-stage Go embed build

**Evidence:** `make build-embed-linux-amd64` produces a working binary. `canopyd --version` outputs build version.

### Phase 2: Updater Package (internal/updater/)

**Files:** `internal/updater/*.go`

1. Implement `Updater.CheckForUpdate()` — HTTP client, JSON parsing, semver comparison
2. Implement `Updater.DownloadUpdate()` — streaming download, SHA-256 verification
3. Implement `Updater.ApplyUpdate()` — atomic rename (POSIX) + `MoveFileEx` (Windows)
4. Implement `Updater.Rollback()` — reverse rename
5. Implement Ed25519 signature verification in `internal/updater/verify.go`
6. Add `--update-interval`, `--channel` CLI flags
7. Wire periodic check goroutine into `canopyd serve`

**Evidence:** Unit test covers Scenarios 1-10. Manual: start canopyd, publish fake `latest.json`, observe download + swap.

### Phase 3: Release Pipeline & Installers

**Files:** `deploy/nfpm.yaml`, `deploy/canopy.wxs`, `.github/workflows/release.yml`

1. Configure `nfpm.yaml` for .deb and .rpm packaging
2. Create WiX `.wxs` template for Windows MSI
3. Create `deploy/canopy.desktop` and app icons
4. Add packaging step to release workflow
5. Add code signing for macOS and Windows
6. Implement `internal/dist/packager.go` — `GenerateManifest`, `SignArtifact`

**Evidence:** Full release workflow produces all artifacts. `.deb` installs and `canopyd --version` works on a clean Ubuntu VM.

### Phase 4: gh-pages Release Server

1. Create `gh-pages` branch with `stable/`, `beta/`, `nightly/` directories
2. Add CI step to copy `latest.json` to `gh-pages` after release
3. Configure GitHub Pages for `https://releases.canopy.app/`
4. Add `nightly` workflow trigger (daily cron + on push to main)
5. Add nightly cleanup (delete artifacts older than 7 days)

**Evidence:** `curl https://releases.canopy.app/stable/latest.json` returns valid manifest. `CheckForUpdate` against production URL succeeds.

### Phase 5: Wails Native Packaging (Post-MVP)

1. Install Wails v3 CLI
2. Create `cmd/canopy-desktop/` — Wails entry point with IPC bindings
3. Configure `wails.json` project file
4. Add `build-wails` targets to Makefile
5. Add Wails CI matrix to release workflow
6. Wire Wails updater to same `latest.json` endpoint
7. Add macOS `.dmg` + Windows `.msi` packaging for Wails builds

**Evidence:** `wails dev` opens native window with Canopy UI. `wails build` produces signed `.dmg`/`.msi`. Auto-update in Wails mode downloads and restarts.

---

## 11. Cost Breakdown

### Release Infrastructure

| Service | Monthly Cost | Free Tier? | Notes |
|---------|-------------|-----------|-------|
| GitHub Actions (public repo) | $0 | 2,000 min/mo | Public repo = free minutes. ~30 min per release. Monthly releases stay within free tier. |
| GitHub Pages | $0 | 1GB, 100GB/mo | Release manifest is <100KB. Well within free tier. |
| GitHub Container Registry | $0 | Public images free | Docker images for Canopy. |
| Hetzner Storage Box (nightlies) | ~$3 | No | 1TB, nightly builds at ~50MB each = ~1.5GB/mo. Allows 600+ nightlies retention. |
| **Total/mo** | **~$3** | | Only if nightly retention exceeds GitHub storage. |

### Build Times (Estimated, per CI run)

| Job | Runtime | Notes |
|-----|---------|-------|
| test | ~2 min | lint + vet + unit tests |
| build-embed (5 targets) | ~5 min | Parallel matrix (5 runners) |
| build-wails (3 targets) | ~8 min | Platform-specific runners |
| package + sign | ~3 min | OS-specific signing tools |
| **Total** | **~10-12 min** | Parallel builds, sequential packaging |

---

## 12. References

- **T1.6** — WebUI Native App Evaluation. Full research: Go embed (MVP) → Wails v3 (post-MVP). Primary source for all deployment-mode decisions.
- **ARCHITECTURE.md §9** — Native App Strategy. MVP vs Wails comparison table, Go embed architecture diagram, Wails upgrade path.
- **ARCHITECTURE.md §10** — Cost Estimates. Single-user ($0/mo), Self-Hosted (~$8/mo), SaaS (~$5/mo per tenant).
- **SPEC-FTR-05** — Self-Hosted & SaaS Relay Architecture. Go interface and Go struct patterns followed here.
- **Go embed documentation** — `//go:embed` directive for embedding frontend assets.
- **Wails v3 documentation** — `wails.io`, Wails CLI, IPC, multi-window, updater.
- **nfpm** — `goreleaser.com/nfpm/` — Go-native cross-platform package manager.
- **WiX Toolset** — `wixtoolset.org` — Windows Installer XML.
- **Ed25519** — `crypto/ed25519` in Go stdlib — signature generation and verification.
