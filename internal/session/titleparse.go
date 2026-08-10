package session

import (
	"regexp"
	"strings"
)

// TitleInfo holds the best-effort metadata extracted from a Hermes
// session title. Every field is optional; all are empty when the title
// does not match any known pattern. The parser is intentionally
// permissive — it never fails, it just extracts less.
type TitleInfo struct {
	Project    string // e.g. "hermes-canopy", "9router", "consensus"
	BoardTask  string // e.g. "WIRE-006", "BUG-034", "GAP-001"
	CommitHash string // 7-40 hex chars, e.g. "a1b2c3d"
}

// --- Compiled regexes -------------------------------------------------------

// boardTaskRe matches the Coding Hermes board-task id format:
// 2-6 letter prefix, hyphen, 2-4 digits. Case-insensitive so lowercase
// variants ("bug-034") match too. Examples: WIRE-006, BUG-034, GAP-001.
var boardTaskRe = regexp.MustCompile(`(?i)\b([A-Z]{2,6})-(\d{2,4})\b`)

// commitHashRe matches a git short/long hash: 7-40 hexadecimal chars as
// a standalone token.
var commitHashRe = regexp.MustCompile(`\b([0-9a-f]{7,40})\b`)

// projectFromSyncRe captures the project slug before "-duckbrain-sync"
// in cron sync titles. Requires at least one char before the suffix.
// Examples:
//
//	"hermes-canopy-duckbrain-sync · Aug 09" → "hermes-canopy"
//	"9router-duckbrain-sync · Aug 09"       → "9router"
//	"consensus-duckbrain-sync · Aug 08"     → "consensus"
var projectFromSyncRe = regexp.MustCompile(`^([a-z0-9][a-z0-9-]*?)-duckbrain-sync\b`)

// projectStandaloneRe captures a kebab-case slug (with at least one
// hyphen) that appears before " · " in a non-sync title. Requires at
// least one hyphen to avoid matching plain words like "alpha".
// Example: "totalstack-shape-validator · Aug 09" → "totalstack-shape-validator"
var projectStandaloneRe = regexp.MustCompile(`^([a-z0-9][a-z0-9]*(?:-[a-z0-9]+)+)\s+·`)

// projectColonRe captures a project slug before a colon delimiter.
// Example: "hermes-canopy: Fix BUG-034" → "hermes-canopy"
var projectColonRe = regexp.MustCompile(`^([a-z0-9][a-z0-9]*(?:-[a-z0-9]+)+):\s`)

// ParseTitle extracts best-effort metadata from a session title. It is
// a pure function — no I/O, no side effects. Every field in the result
// is empty when the title does not contain a recognizable pattern.
//
// Recognized patterns:
//
//   - Board task:   "BUG-034", "WIRE-006", "GAP-001", "TASK-123" (case-insensitive)
//   - Commit hash:  7-40 hex chars as a standalone token ("a1b2c3d")
//   - Project slug: extracted from sync/cron titles and colon-prefixed titles:
//       "hermes-canopy-duckbrain-sync · Aug 09" → "hermes-canopy"
//       "9router-duckbrain-sync · Aug 09"       → "9router"
//       "totalstack-shape-validator · Aug 09"   → "totalstack-shape-validator"
//       "hermes-canopy: Fix BUG-034"            → "hermes-canopy"
//
// Unmatched titles ("Documenting the Kelly leverage cycle", "alpha",
// "H4F Hourly Health Check · Aug 09 19:12") produce an empty TitleInfo
// — the parser degrades gracefully rather than guessing.
func ParseTitle(title string) TitleInfo {
	var info TitleInfo
	if title == "" {
		return info
	}

	// Board task — first match wins (case-insensitive).
	if m := boardTaskRe.FindString(title); m != "" {
		info.BoardTask = strings.ToUpper(m)
	}

	// Commit hash — first match. Filter out pure numbers (no hex letters).
	if m := commitHashRe.FindString(title); m != "" {
		if hasHexLetter(m) {
			info.CommitHash = m
		}
	}

	// Project slug — try sync suffix, then standalone kebab-case, then colon.
	if m := projectFromSyncRe.FindStringSubmatch(title); len(m) > 1 {
		info.Project = m[1]
	} else if m := projectStandaloneRe.FindStringSubmatch(title); len(m) > 1 {
		info.Project = m[1]
	} else if m := projectColonRe.FindStringSubmatch(title); len(m) > 1 {
		info.Project = m[1]
	}

	return info
}

// hasHexLetter reports whether s contains at least one of [a-fA-F],
// distinguishing a git hash from a pure number.
func hasHexLetter(s string) bool {
	for _, c := range s {
		if (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			return true
		}
	}
	return false
}
