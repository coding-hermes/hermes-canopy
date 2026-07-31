package plugin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Manifest extraction (SPEC-PL-01 §5.3): the manifest lives as a comment
// block at the TOP of the plugin JS file:
//
//	/**
//	 * @canopy-manifest
//	 * {
//	 *   "name": "csv-viewer",
//	 *   ...
//	 * }
//	 * @end-canopy-manifest
//	 */
const (
	manifestStartMarker = "@canopy-manifest"
	manifestEndMarker   = "@end-canopy-manifest"
)

// semverPattern mirrors the chk_plugin_version CHECK constraint:
// MAJOR.MINOR.PATCH with an optional -prerelease suffix.
var semverPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$`)

// slugStripPattern matches characters removed during slug derivation
// (anything that is not [a-z0-9-] after lowercasing).
var slugStripPattern = regexp.MustCompile(`[^a-z0-9-]`)

// ParseManifest extracts and validates the @canopy-manifest comment block
// from the top of a plugin source file. It returns ErrInvalidManifest when
// the block is missing or its JSON is malformed, and
// ErrManifestValidationFailed when required fields are missing or invalid
// (name, semver version, description, permissions ⊆ AllPermissions,
// render_type ∈ {card,embed,background}, non-empty entry_point).
func ParseManifest(source string) (*PluginManifest, error) {
	block, ok := extractManifestBlock(source)
	if !ok {
		return nil, ErrInvalidManifest
	}

	var m PluginManifest
	if err := json.Unmarshal([]byte(block), &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if err := validateManifest(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// extractManifestBlock returns the raw JSON between the @canopy-manifest and
// @end-canopy-manifest markers. Comment decorations (//, /*, *, */) are
// stripped line-by-line so the block is valid JSON.
func extractManifestBlock(source string) (string, bool) {
	startIdx := strings.Index(source, manifestStartMarker)
	if startIdx < 0 {
		return "", false
	}
	endIdx := strings.Index(source[startIdx:], manifestEndMarker)
	if endIdx < 0 {
		return "", false
	}
	raw := source[startIdx+len(manifestStartMarker) : startIdx+endIdx]

	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		// Strip JS comment decorations.
		trimmed = strings.TrimPrefix(trimmed, "//")
		trimmed = strings.TrimPrefix(trimmed, "/*")
		trimmed = strings.TrimPrefix(trimmed, "*")
		trimmed = strings.TrimPrefix(trimmed, "*/")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == "" {
			continue
		}
		lines = append(lines, trimmed)
	}
	if len(lines) == 0 {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}

// validateManifest checks required fields per SPEC-PL-01 §5.3 / GAP-002 §2.
func validateManifest(m *PluginManifest) error {
	switch {
	case strings.TrimSpace(m.Name) == "":
		return fmt.Errorf("%w: name is required", ErrManifestValidationFailed)
	case !semverPattern.MatchString(m.Version):
		return fmt.Errorf("%w: version %q is not valid semver (want MAJOR.MINOR.PATCH)", ErrManifestValidationFailed, m.Version)
	case !ValidRenderType(m.RenderType):
		return fmt.Errorf("%w: render_type %q must be one of card, embed, background", ErrManifestValidationFailed, m.RenderType)
	case strings.TrimSpace(m.EntryPoint) == "":
		return fmt.Errorf("%w: entry_point is required", ErrManifestValidationFailed)
	}
	// Permissions ⊆ AllPermissions (GAP-002 §2). Unknown permission strings
	// map to ErrInvalidPermission (422), not to a 400 validation failure.
	// The service re-checks the passed manifest (register algorithm step 3)
	// so non-ParseManifest callers cannot smuggle unknown permissions in.
	for _, p := range m.Permissions {
		if !ValidPermission(p) {
			return fmt.Errorf("%w: %q", ErrInvalidPermission, p)
		}
	}
	return nil
}

// DeriveSlug lowercases the plugin name, converts whitespace runs to '-',
// and strips every character outside [a-z0-9-] (SPEC-PL-01 §3.1 chk_plugin_slug).
func DeriveSlug(name string) string {
	var b strings.Builder
	lastWasDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastWasDash = false
		case unicode.IsSpace(r), r == '-', r == '_':
			if !lastWasDash && b.Len() > 0 {
				b.WriteRune('-')
				lastWasDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return ""
	}
	return slug
}
