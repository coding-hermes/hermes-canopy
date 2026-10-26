module github.com/coding-hermes/hermes-canopy/cmd/canopy-desktop

go 1.26.0

require (
	github.com/coding-hermes/hermes-canopy v0.0.0
	github.com/wailsapp/wails/v3 v3.0.0-beta.34
)

require (
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	golang.org/x/sys v0.48.0 // indirect
)

// The desktop shell is a nested module: Go excludes it from the canopy
// module's ./... patterns, so the canopy module never gains a Wails
// dependency. internal/ visibility holds because this directory lives
// inside the canopy module's tree.
replace github.com/coding-hermes/hermes-canopy => ../..
