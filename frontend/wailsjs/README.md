# frontend/wailsjs — Wails v3 bindings (GENERATED)

The files in this directory are **auto-generated** by the Wails v3 CLI — do not
edit them by hand; they will be overwritten on the next regeneration.

## Provenance

Generated with the pinned Wails v3 CLI:

    CGO_ENABLED=0 go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.34
    cd cmd/canopy-desktop
    CGO_ENABLED=0 wails3 generate bindings -d ../../frontend/wailsjs .

The scanned package is `cmd/canopy-desktop` (a nested Go module — see
`cmd/canopy-desktop/go.mod` and `docs/DESKTOP-WAILS.md`). The bound surface is
the repository-owned `internal/wailsbridge.DesktopService` (currently one
method: `Status()` — status reflection only, deliberately no control verbs,
see the service doc comment in `internal/wailsbridge`).

## Notes

- These files import `@wailsio/runtime`, which is resolved by the Wails
  runtime when the app runs inside the native window (desktop mode). They are
  NOT imported by the PWA/React source tree (`frontend/src`), so `tsc` (which
  includes only `src`) and the Vite build are unaffected by this directory.
- The Linux WebView SDKs (GTK4 + WebKitGTK 6.0) are NOT installed on the
  fleet's build hosts, so `wails3 generate bindings` emits warnings from the
  Wails library's own linux webview sources (undefined symbols in
  `webview_window_linux*.go`). This is expected — those warnings are about the
  *native window* code paths, not the bindings parser, and generation still
  completes and writes the bindings. See `docs/DESKTOP-WAILS.md` for the
  host-limitation boundary.
- The generated call IDs (e.g. `$Call.ByID(3031330334)`) are content-derived;
  regenerate after any change to `DesktopService`'s public methods so the IDs
  stay in sync with the binary.
