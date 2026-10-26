// FTR-06 Phase 5 — Wails v3 native desktop entry point (SPEC-FTR-06,
// "Phase 5: Wails Native Packaging").
//
// This directory is a SEPARATE Go module on purpose. Wails v3 needs go >= 1.26
// (the canopy module pins 1.25), CGO, and platform WebView SDKs (GTK4 +
// WebKitGTK 6.0 on Linux, WKWebView on macOS, WebView2 on Windows). Keeping
// the desktop shell in its own module guarantees the canopy module — and with
// it `make build`, `make test`, and every existing CI job — never gains a
// Wails dependency: Go excludes nested modules from `./...` package patterns,
// so ordinary builds stay independent of Wails availability. The module still
// reaches into the canopy module's internal/ packages through a `replace`
// directive; internal/ visibility is per module and this directory lives
// inside the canopy module's tree, so the import is legal. The full boundary
// is documented in docs/DESKTOP-WAILS.md.
//
// Architecture (SPEC-FTR-06 decision 3: "Wails wraps the Go binary in a
// native WebView window. All backend logic, API handlers, and SSE events are
// identical. Only the entry point and build flags differ."):
//
//	canopy-desktop  →  supervises a canopyd child process  →  native window
//	                   (same canopyd binary as Mode A)        loads the child's
//	                                                          http:// URL
//
// The web build output stays single: canopyd (built with -tags embedui) serves
// the same SPA the PWA mode serves; the desktop shell only wraps it in a
// native window and owns the Wails-side updater.
//
// Build modes:
//
//   - Desktop (needs the platform WebView SDKs, CGO_ENABLED=1):
//       go build -tags production ./cmd/canopy-desktop
//     Wrapped by `make build-wails`, which checks the prerequisites first and
//     fails with an actionable message when they are missing.
//
//   - Server/CI mode (builds anywhere, no SDKs, CGO_ENABLED=0):
//       CGO_ENABLED=0 go build -tags server,production ./cmd/canopy-desktop
//     The Wails application surface compiles with the pure-Go server platform
//     app instead of a native window. This is the host-independent build the
//     tests and CI prove; it is NOT a shippable desktop app.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	wailsupdater "github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/coding-hermes/hermes-canopy/internal/updater"
	"github.com/coding-hermes/hermes-canopy/internal/wailsbridge"
)

// Build identity, injected via -ldflags exactly like cmd/canopyd:
//
//	go build -ldflags="-X main.version=... -X main.commit=... -X main.buildTime=..." ./cmd/canopy-desktop
var (
	version   = "dev"
	commit    = ""
	buildTime = ""
)

// defaultBackendAddr is the loopback address the desktop shell assigns to the
// supervised canopyd child. The shell owns the port so the window URL is
// predictable regardless of the operator environment; canopyd still binds
// loopback only, matching the local-server deployment posture.
const defaultBackendAddr = "127.0.0.1:8091"

func main() {
	backendAddr := flag.String("addr", defaultBackendAddr, "loopback address for the supervised canopyd backend")
	canopydBin := flag.String("canopyd", "", "path to the canopyd backend binary (default: next to this binary, then PATH)")
	updateInterval := flag.Duration("update-interval", 0, "signed update check interval (0 disables update checks; the canopyd child never checks)")
	updateChannel := flag.String("channel", string(updater.ChannelStable), "update channel: stable, beta, or nightly")
	updateManifestURL := flag.String("update-manifest-url", "", "signed update manifest URL (empty uses the built-in per-channel default)")
	updatePublicKeyFile := flag.String("update-public-key-file", "", "path to a hex-encoded Ed25519 public key (empty uses the embedded release key)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("canopy-desktop %s (commit %s, built %s)\n", version, commit, buildTime)
		return
	}

	if err := updater.ValidateChannel(*updateChannel); err != nil {
		fmt.Fprintln(os.Stderr, "canopy-desktop:", err)
		os.Exit(2)
	}

	publicKey, err := updater.ResolvePublicKey(*updatePublicKeyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "canopy-desktop:", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The canopyd backend is a supervised child. Its lifecycle is owned here:
	// resolve, start, health-poll, graceful stop. Update checks stay disabled
	// in the child (they are opt-in via -update-interval in canopyd, which we
	// do not pass) — exactly one updater runs per distribution (SPEC-FTR-06
	// risk 14), and in desktop mode it is the Wails-side one below.
	backend, err := wailsbridge.StartBackend(ctx, wailsbridge.BackendOptions{
		Binary: *canopydBin,
		Addr:   *backendAddr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "canopy-desktop: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = backend.Stop() }()

	app := application.New(application.Options{
		Name:        "Hermes Canopy",
		Description: "Graph-native collaboration surface for human-agent work",
		Services: []application.Service{
			// IPC bindings exposed to the frontend via the Wails runtime.
			// The service itself is a plain Go type in internal/wailsbridge;
			// only this generic instantiation touches the Wails API.
			application.NewService(wailsbridge.NewDesktopService(backend, version)),
		},
	})

	// Wails-mode updater: same latest.json release contract as canopyd, read
	// through the repository-owned feed adapter and exposed to the Wails
	// updater as a Provider. The feed verifies the manifest Ed25519 signature
	// with the same release key; the Wails updater additionally verifies the
	// downloaded artifact bytes (SHA-256 digest + Ed25519 signature) before
	// staging the swap.
	if *updateInterval > 0 {
		feed := &wailsbridge.ReleaseFeed{
			Channel:     updater.ReleaseChannel(*updateChannel),
			ManifestURL: *updateManifestURL,
			PublicKey:   publicKey,
			Platform:    runtime.GOOS,
			Arch:        runtime.GOARCH,
		}
		provider, err := newReleaseFeedProvider(feed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "canopy-desktop: configure updater: %v\n", err)
			os.Exit(2)
		}
		var _ wailsupdater.Provider = provider // compile-time interface proof
		if err := app.Updater.Init(wailsupdater.Config{
			CurrentVersion: version,
			Providers:      []wailsupdater.Provider{provider},
			PublicKey:      publicKey,
			CheckInterval:  *updateInterval,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "canopy-desktop: init updater: %v\n", err)
			os.Exit(2)
		}
	}

	// Native window around the supervised backend. The child serves the same
	// embedded SPA as PWA mode (SPEC-FTR-06 decision 4: single web build
	// output), so the shell does not embed or re-serve a second asset tree.
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Hermes Canopy",
		Width:  1280,
		Height: 800,
		URL:    "http://" + backend.Addr(),
	})

	// If the backend dies the window is dead too; shut the shell down instead
	// of showing a blank frame.
	go func() {
		select {
		case <-ctx.Done():
			app.Quit()
		case <-backend.Exited():
			app.Quit()
		}
	}()

	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "canopy-desktop: %v\n", err)
		_ = backend.Stop()
		os.Exit(1)
	}
	if err := backend.Stop(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		fmt.Fprintf(os.Stderr, "canopy-desktop: backend stop: %v\n", err)
		os.Exit(1)
	}
}
