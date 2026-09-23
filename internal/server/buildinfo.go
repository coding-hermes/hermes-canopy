package server

import (
	"encoding/json"
	"net/http"
)

// Build identity, stamped at build time via -ldflags -X (Makefile
// build-embed / scripts/deploy-canopyd.sh). The defaults describe an
// unstamped dev build honestly (`go build ./cmd/canopyd`) instead of
// pretending: commit/build_time fall back to "unknown" at the surface.
//
// These are package-level vars — not constructor parameters — so the /version
// and /health surfaces and every test constructor (newRouter with a zero
// routeDeps) report the identity of the binary they are compiled into, which
// is exactly the thing a deploy has to prove (R14-03 / GAP-100).
var (
	buildVersion = "dev"
	buildCommit  = ""
	buildTime    = ""
)

// BuildVersion/BuildCommit/BuildTime are the ldflags injection points used by
// cmd/canopyd at startup: -X main.version/commit/buildTime flow into these so
// the HTTP surfaces report the binary's real build, not the defaults.
var (
	BuildVersion = &buildVersion
	BuildCommit  = &buildCommit
	BuildTime    = &buildTime
)

// buildInfo is the identity payload served by GET /version and embedded in
// GET /health. Both surfaces render the SAME values from the SAME vars, so a
// running daemon can be identified without touching the binary on disk and
// the two answers can never disagree.
type buildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
}

func currentBuildInfo() buildInfo {
	bi := buildInfo{Version: buildVersion, Commit: buildCommit, BuildTime: buildTime}
	if bi.Version == "" {
		bi.Version = "dev"
	}
	if bi.Commit == "" {
		bi.Commit = "unknown"
	}
	if bi.BuildTime == "" {
		bi.BuildTime = "unknown"
	}
	return bi
}

// versionHandler responds with the exact build identity of this binary.
func versionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(currentBuildInfo())
}
