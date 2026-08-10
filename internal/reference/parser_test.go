// Package reference — parser unit tests.
// Tests SPEC-TM-04 §11.1 scenarios 1-3, 28.
package reference

import (
	"testing"
)

// Scenario 1: Parse single reference.
func TestParseReferences_Single(t *testing.T) {
	refs := ParseReferences("See #database-schema")
	if len(refs) != 1 {
		t.Fatalf("expected 1 reference, got %d", len(refs))
	}
	if refs[0].Slug != "database-schema" {
		t.Errorf("slug: expected database-schema, got %s", refs[0].Slug)
	}
	if refs[0].Raw != "#database-schema" {
		t.Errorf("raw: expected #database-schema, got %s", refs[0].Raw)
	}
	if refs[0].Offset != 4 {
		t.Errorf("offset: expected 4, got %d", refs[0].Offset)
	}
	if refs[0].Length != 16 {
		t.Errorf("length: expected 16, got %d", refs[0].Length)
	}
}

// Scenario 2: Parse multiple references.
func TestParseReferences_Multiple(t *testing.T) {
	refs := ParseReferences("#schema and #data-flow")
	if len(refs) != 2 {
		t.Fatalf("expected 2 references, got %d", len(refs))
	}
	if refs[0].Slug != "schema" {
		t.Errorf("ref[0] slug: expected schema, got %s", refs[0].Slug)
	}
	if refs[0].Offset != 0 {
		t.Errorf("ref[0] offset: expected 0, got %d", refs[0].Offset)
	}
	if refs[1].Slug != "data-flow" {
		t.Errorf("ref[1] slug: expected data-flow, got %s", refs[1].Slug)
	}
}

// Scenario 3: Invalid slugs are not parsed.
func TestParseReferences_InvalidSlugs(t *testing.T) {
	// These must NOT produce any valid slug match:
	// #123-start — starts with a digit
	// #UPPER — starts with uppercase
	// #-leading — starts with a hyphen
	refs := ParseReferences("#123-start #UPPER #-leading")
	if len(refs) != 0 {
		t.Fatalf("expected 0 references for invalid slugs, got %d: %+v", len(refs), refs)
	}
}

// Scenario 28: SQL injection via slug is rejected by the regex.
func TestParseReferences_SQLInjectionRejected(t *testing.T) {
	refs := ParseReferences("#slug'; DROP TABLE topics;--")
	// Only the valid prefix '#slug' should be matched, not the injection.
	if len(refs) == 0 {
		// The regex won't match at all because the slug contains invalid chars
		// after 'slug' (the ' is not [a-z0-9-]).
		return
	}
	// If it did match, the slug must be just 'slug', no injection.
	for _, r := range refs {
		if r.Slug != "slug" {
			t.Fatalf("SQL injection leaked into slug: %s", r.Slug)
		}
	}
}

// Single-letter slugs are valid.
func TestParseReferences_SingleLetter(t *testing.T) {
	refs := ParseReferences("See #a here")
	if len(refs) != 1 {
		t.Fatalf("expected 1 reference, got %d", len(refs))
	}
	if refs[0].Slug != "a" {
		t.Errorf("expected slug 'a', got '%s'", refs[0].Slug)
	}
}

// No references in plain text.
func TestParseReferences_None(t *testing.T) {
	refs := ParseReferences("Just some text without references")
	if len(refs) != 0 {
		t.Fatalf("expected 0 references, got %d", len(refs))
	}
}

// Empty content returns nil.
func TestParseReferences_Empty(t *testing.T) {
	refs := ParseReferences("")
	if refs != nil {
		t.Fatalf("expected nil for empty content, got %v", refs)
	}
}

// Duplicate references are preserved (dedup is caller's job).
func TestParseReferences_Duplicates(t *testing.T) {
	refs := ParseReferences("#schema and #schema again")
	if len(refs) != 2 {
		t.Fatalf("expected 2 references (preserved), got %d", len(refs))
	}
}

// DedupeBySlug keeps first occurrence.
func TestDedupeBySlug(t *testing.T) {
	refs := []ParsedReference{
		{Slug: "a", Offset: 0},
		{Slug: "b", Offset: 5},
		{Slug: "a", Offset: 10},
	}
	deduped := DedupeBySlug(refs)
	if len(deduped) != 2 {
		t.Fatalf("expected 2 deduped refs, got %d", len(deduped))
	}
	if deduped[0].Slug != "a" || deduped[0].Offset != 0 {
		t.Errorf("first should keep offset 0, got %d", deduped[0].Offset)
	}
	if deduped[1].Slug != "b" {
		t.Errorf("second should be 'b', got '%s'", deduped[1].Slug)
	}
}

// ValidSlug tests.
func TestValidSlug(t *testing.T) {
	valid := []string{"a", "ab", "a-b", "database-schema", "topic123", "a1b2c3"}
	for _, s := range valid {
		if !ValidSlug(s) {
			t.Errorf("expected '%s' to be valid", s)
		}
	}
	invalid := []string{"", "A", "1abc", "-ab", "ab-", "a_b", "a.b", "#ab"}
	for _, s := range invalid {
		if ValidSlug(s) {
			t.Errorf("expected '%s' to be invalid", s)
		}
	}
}

// Hash inside a URL should NOT match.
func TestParseReferences_NoMatchInsideURL(t *testing.T) {
	refs := ParseReferences("https://example.com/page#section")
	// The '#section' is preceded by 'e' (a word char), so it should NOT match.
	if len(refs) != 0 {
		t.Fatalf("expected 0 refs inside URL, got %d: %+v", len(refs), refs)
	}
}

// Markdown heading should NOT match as a reference.
func TestParseReferences_MarkdownHeading(t *testing.T) {
	refs := ParseReferences("# Heading")
	// '# ' is followed by a space, not a letter, so it should not match.
	if len(refs) != 0 {
		t.Fatalf("expected 0 refs for markdown heading, got %d: %+v", len(refs), refs)
	}
}
