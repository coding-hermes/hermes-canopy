//go:build embedui

package webui

import (
	"embed"
	"io/fs"
)

// The built UI is compiled into the binary only under `-tags embedui`.
//
// go:embed cannot reference paths outside the embedding file's own directory,
// so `make build-ui` stages a copy of frontend/dist at internal/webui/dist and
// removes it again after the build. The pattern deliberately includes dotfiles
// (all:) — the Vite output is plain, but a stray dotfile must not silently
// vanish from the shipped bundle.
//
//go:embed all:dist
var embeddedDist embed.FS

func init() {
	// Strip the staging directory so the handler addresses "index.html" and
	// "assets/..." exactly as they appear in frontend/dist.
	sub, err := fs.Sub(embeddedDist, "dist")
	if err != nil {
		// Leave distFS nil: Embedded() reports false and NotFound degrades to
		// the API 404 handler instead of serving a broken shell.
		return
	}
	distFS = sub
}
