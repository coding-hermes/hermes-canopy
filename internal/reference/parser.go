// Package reference — reference parser.
// Extracts #topic-slug references from message content using the canonical
// regex from SPEC-TM-01 §5.3. A valid slug:
//   - Starts with a lowercase letter [a-z]
//   - Contains only lowercase alphanumeric [a-z0-9] and hyphens [-]
//   - Does not end with a hyphen
//   - Single letters are allowed
//
// The '#' must be at the start of text or preceded by a non-word, non-'#'
// character so that '#foo' inside a URL or markdown heading is not matched.
package reference

import "regexp"

// ReferenceRegex is the canonical regex for #topic-slug references.
// Exported so the frontend and other tools can verify the same pattern.
var ReferenceRegex = referenceRe

// slugRe is the bare-slug validator (no '#').
var slugRe = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

// ValidSlug returns true if s is a valid reference slug per the canonical rules.
func ValidSlug(s string) bool {
	return slugRe.MatchString(s)
}

// ParseReferences extracts all #topic-slug references from content.
// Returns a slice of ParsedReference, each with the raw match, extracted slug,
// character offset of the '#', and length of the full match.
// Duplicate slugs are preserved (each occurrence is returned); the service
// layer handles deduplication for persistence.
//
// The regex uses a non-capturing prefix '(?:^|[^a-zA-Z0-9#])' that consumes
// the boundary character. We track the offset of the '#' within the full match
// so callers get the true position of the reference start.
func ParseReferences(content string) []ParsedReference {
	if content == "" {
		return nil
	}

	matches := referenceRe.FindAllStringSubmatchIndex(content, -1)
	if matches == nil {
		return nil
	}

	refs := make([]ParsedReference, 0, len(matches))
	for _, m := range matches {
		// m[0]: start of full match (includes boundary char), m[1]: end
		// m[2]: start of capture group (slug), m[3]: end
		slugStart := m[2]
		slugEnd := m[3]

		// The '#' is at slugStart-1 within the content.
		hashIdx := slugStart - 1
		raw := content[hashIdx:slugEnd]
		slug := content[slugStart:slugEnd]

		refs = append(refs, ParsedReference{
			Raw:    raw,
			Slug:   slug,
			Offset: hashIdx,
			Length: len(raw),
		})
	}

	return refs
}

// DedupeBySlug returns the first ParsedReference for each unique slug,
// preserving order of first appearance.
func DedupeBySlug(refs []ParsedReference) []ParsedReference {
	seen := make(map[string]bool, len(refs))
	out := make([]ParsedReference, 0, len(refs))
	for _, r := range refs {
		if seen[r.Slug] {
			continue
		}
		seen[r.Slug] = true
		out = append(out, r)
	}
	return out
}
