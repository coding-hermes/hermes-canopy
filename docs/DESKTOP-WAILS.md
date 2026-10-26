# Desktop shell (Wails v3) — build surface and host boundary

SPEC-FTR-06 Phase 5 adds **Mode B** distribution (Wails v3 native desktop) as
an *additive* surface. The existing embed/PWA pipeline (`make build`,
`make build-ui`, `make test`, `release.yml`) does not change and does not
depend on Wails in any way. This page documents what is wired in-repo, what
each `make` target proves, and — just as important — what is NOT produced
here and why.

## Files

| Path | Role |
|------|------|
| `cmd/canopy-desktop/` | Wails v3 entry point, **nested Go module** (own `go.mod`) |
| `cmd/canopy-desktop/main.go` | Shell: supervisor + native window + updater wiring |
| `cmd/canopy-desktop/provider.go` | `updater.Provider` shim over the shared `latest.json` feed |
| `cmd/canopy-desktop/download.go` | Artifact streaming helper for the provider |
| `cmd/canopy-desktop/provider_test.go` | Tests against the pinned Wails interface |
| `cmd/canopy-desktop/go.mod` / `go.sum` | Nested module: pins `wailsapp/wails/v3 v3.0.0-beta.34`, `replace ../..` |
| `wails.json` | Wails project config (name, product identity, dev-mode tasks) |
| `internal/wailsbridge/` | Repository-owned contract code (canopy module, no Wails imports): release-feed adapter, backend supervisor, IPC service |
| `scripts/validate-wails-desktop.py` | Offline source-level contract validation |

## Why a nested module

Wails v3 needs Go >= 1.26 (the canopy module pins 1.25), CGO, and the
platform WebView SDKs. Making the desktop shell a nested module means Go's
`./...` package patterns skip it entirely, so:

- the canopy module's `go.mod` carries **no Wails dependency** (checked by
  `scripts/validate-wails-desktop.py`),
- `make build` / `make test` / `go vet ./...` behave exactly as before,
- the desktop module still imports the canopy module's `internal/` packages
  via `replace ../..` (legal because the directory sits inside the canopy
  module's tree).

## Architecture

The shell supervises a regular `canopyd serve` child (loopback-only address,
`-update-interval` deliberately NOT passed — canopyd's updater is flag
opt-in, so exactly ONE updater runs per desktop binary, SPEC-FTR-06 risk 14)
and opens a native Wails window on the child's URL:

    canopy-desktop  ──supervises──▶  canopyd serve (same binary as Mode A,
    (Wails window + updater)         `-tags embedui`, serves the same SPA)

The Wails-side updater reuses the **same** `latest.json` contract as the
embed updater: `internal/wailsbridge.ReleaseFeed` parses the identical
`updater.ReleaseManifest` wire shape, verifies the identical Ed25519
manifest signature, applies the identical channel/semver/nightly rules, and
prefers artifacts with `platform.mode == "wails"` (falling back to empty
mode). The nested `releaseFeedProvider` shim adapts that feed to the Wails
`updater.Provider` interface; the Wails updater then verifies the downloaded
bytes (SHA-256 digest + Ed25519 signature over the digest — exactly what
`internal/dist.SignArtifact` produces) before staging the swap.

## Make targets

| Target | What it does | Host requirements |
|--------|--------------|-------------------|
| `make validate-wails-desktop` | Offline source-level contract check (module boundary, wails.json, updater wiring, docs honesty) | python3 only |
| `make test-wails-desktop-contract` | Builds + tests the desktop module in **server mode** (`-tags server,production`, CGO off): proves the Wails app surface, the provider shim, and the shared feed compile and behave | go only |
| `make build-wails` | Validates the contract, checks prerequisites **fail-loud** (wails3 CLI on PATH, Linux WebView SDKs via pkg-config), then compiles the server-mode cell and prints the exact next command for a native build | see below |

`make build` / `make build-ui` do not touch any of this.

## What is verified on this repository's Linux hosts

- The nested-module boundary and `wails.json` consistency
  (`make validate-wails-desktop`).
- The complete Wails application surface **compiles** in server mode for
  linux/amd64, windows/amd64, and darwin/arm64 with `CGO_ENABLED=0`
  (`CGO_ENABLED=0 go build -tags server,production .` inside
  `cmd/canopy-desktop`).
- The updater provider shim satisfies the pinned `wailsupdater.Provider`
  interface and resolves/downloads releases from a signed feed
  (`make test-wails-desktop-contract`).
- The shared feed passes the production signing path end-to-end in tests
  (`internal/wailsbridge` uses `internal/dist.GenerateManifest` +
  `SignManifest` + `SignArtifact` to build its fixtures).

## What is NOT produced here (host/CI-gated)

- **No native windowed binary.** The Linux WebView SDKs (GTK4 +
  WebKitGTK 6.0 dev packages, i.e. `pkg-config gtk4 webkitgtk-6.0`) are not
  installed on the fleet's Linux hosts, and Wails' Linux backend is CGO —
  the native build cannot even compile without them. The native window build
  is therefore not verified on this host; `make build-wails` detects the
  missing SDKs and prints the exact install commands instead of failing
  cryptically or, worse, silently falling back.
- **No signed `.dmg`.** Producing one requires macOS + Xcode +
  `codesign`/`notarytool` with Apple Developer credentials. The wails3
  packaging task exists upstream (`wails3 package` on a darwin runner), but
  this repository neither runs it nor claims its output.
- **No signed `.msi`/NSIS installer.** Requires Windows + WiX/NSIS + a code
  signing certificate. Same rule: the tooling is listed, the artifact is not
  claimed.
- **No Wails CI matrix in `release.yml`.** A matrix job would need
  macos/windows runners with WebView SDKs and signing secrets, and a broken
  or unverifiable matrix job must not gate the existing embed release
  pipeline. `scripts/validate-wails-desktop.py` asserts `release.yml` stays
  embed-only; when the owner provisions signing-capable runners, the matrix
  can be added behind that same guard.
- **No updater end-to-end restart.** The Wails updater's download → verify →
  stage path is exercised in tests; the final restart-swap needs a packaged
  app layout (the helper binary expects an installed app bundle), so it is
  verified upstream-on-host, not in this repo's CI.

## Provisioning a host for the native build (Debian/Ubuntu)

```bash
sudo apt install libgtk-4-dev libwebkitgtk-6.0-dev libsoup-3.0-dev
CGO_ENABLED=0 go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.34
make build-wails                     # now passes its prerequisite checks
cd cmd/canopy-desktop
go build -tags production -o ../../bin/canopy-desktop .
```

macOS: Xcode + `xcode-select --install`; Windows: WebView2 runtime (preinstalled
on Windows 11) + WiX/NSIS for packaging. Signing credentials are owner secrets
and are deliberately absent from this repository.

## Traceability

ch:trace row=FTR-06 spec=specs/SPEC-FTR-06-webui-native-packaging-distribution.md#phase-5 wave=none:serial-tick-hermes-canopy-2026-10-26-16-13-57 test=make validate-wails-desktop && make test-wails-desktop-contract doc=docs/DESKTOP-WAILS.md prompt=/tmp/hermes-canopy-2026-10-26-16-13-57-FTR-06-brief.txt evidence=go test ./internal/wailsbridge/ -count=1 (18 tests PASS) + cmd/canopy-desktop CGO_ENABLED=0 go test -tags server,production ./... (4 tests PASS) witness=python3 scripts/validate-wails-desktop.py verdict=gitreins:none:go_lint-lane-cannot-load-nested-module-engine-limitation-attributed-in-commit-body commit=this-commit
