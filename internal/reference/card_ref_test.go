// Package reference — #card reference parser tests.
// Covers SPEC-PL-03 §6.4 syntax, §12.4 scenarios 65-67, and the topic-parser
// disambiguation the card syntax exists for.
package reference

import (
	"testing"

	"github.com/google/uuid"
)

// cardRefFixture returns a fixed UUIDv7 and the two textual forms of a card
// reference over it, so offsets and URIs are asserted against literals.
func cardRefFixture(t *testing.T) (id uuid.UUID, raw string) {
	t.Helper()
	parsed, err := uuid.Parse("0191a9c3-0000-7000-8000-000000000000")
	if err != nil {
		t.Fatalf("fixture uuid: %v", err)
	}
	if !IsUUIDv7(parsed) {
		t.Fatal("fixture uuid must be version 7")
	}
	return parsed, parsed.String()
}

// SPEC-PL-03 §12.4 scenario 65 (adapted bare form, §6.4): a #card:<uuid>
// reference parses to ref://card/<uuid> with the raw text, id and offsets.
func TestParseCardReferences_Bare(t *testing.T) {
	id, raw := cardRefFixture(t)

	content := "see #card:" + raw + " now"
	refs := ParseCardReferences(content)
	if len(refs) != 1 {
		t.Fatalf("expected 1 card reference, got %d: %+v", len(refs), refs)
	}
	ref := refs[0]
	if ref.Invalid {
		t.Fatalf("bare reference flagged invalid: %s", ref.Reason)
	}
	if ref.Raw != "#card:"+raw {
		t.Errorf("raw: expected %q, got %q", "#card:"+raw, ref.Raw)
	}
	if ref.URI != "ref://card/"+raw {
		t.Errorf("uri: expected %q, got %q", "ref://card/"+raw, ref.URI)
	}
	if ref.CardID != id {
		t.Errorf("id: expected %s, got %s", id, ref.CardID)
	}
	if !ref.Bare {
		t.Error("bare form must set Bare")
	}
	if ref.CardType != "" {
		t.Errorf("bare form must carry no type, got %q", ref.CardType)
	}
	// '#' is at index 4 of "see #card:...".
	if ref.Offset != 4 {
		t.Errorf("offset: expected 4, got %d", ref.Offset)
	}
	if want := len("#card:") + len(raw); ref.Length != want {
		t.Errorf("length: expected %d, got %d", want, ref.Length)
	}
}

// SPEC-PL-03 §12.4 scenario 65: the typed form parses to
// ref://card/<type>/<uuid> and keeps its type.
func TestParseCardReferences_Typed(t *testing.T) {
	id, raw := cardRefFixture(t)

	for _, cardType := range []string{"compact", "expanded", "iteration"} {
		content := "#card:" + cardType + "/" + raw
		refs := ParseCardReferences(content)
		if len(refs) != 1 {
			t.Fatalf("%s: expected 1 card reference, got %d", cardType, len(refs))
		}
		ref := refs[0]
		if ref.Invalid {
			t.Fatalf("%s: typed reference flagged invalid: %s", cardType, ref.Reason)
		}
		if ref.CardType != cardType {
			t.Errorf("%s: type: got %q", cardType, ref.CardType)
		}
		if ref.CardID != id {
			t.Errorf("%s: id: expected %s, got %s", cardType, id, ref.CardID)
		}
		if ref.Bare {
			t.Errorf("%s: typed form must not set Bare", cardType)
		}
		if want := "ref://card/" + cardType + "/" + raw; ref.URI != want {
			t.Errorf("%s: uri: expected %q, got %q", cardType, want, ref.URI)
		}
		if ref.Offset != 0 {
			t.Errorf("%s: offset: expected 0, got %d", cardType, ref.Offset)
		}
	}
}

// Malformed references FAIL parsing: they are returned flagged, with a reason
// and no id/URI, so a caller can report them instead of losing them (§6.4).
func TestParseCardReferences_Malformed(t *testing.T) {
	_, raw := cardRefFixture(t)

	cases := []struct {
		name   string
		raw    string
		reason string
	}{
		{"invalid uuid", "#card:not-a-uuid", CardRefReasonInvalidUUID},
		{"unknown type", "#card:bogus/" + raw, CardRefReasonUnknownType},
		{"empty type", "#card:/" + raw, CardRefReasonEmptyType},
		{"empty id", "#card:", CardRefReasonEmptyID},
		{"empty typed id", "#card:compact/", CardRefReasonEmptyID},
		{"non-v7 uuid", "#card:0191a9c3-0000-4000-8000-000000000000", CardRefReasonNotUUIDv7},
		{"truncated uuid", "#card:0191a9c3-0000-7000", CardRefReasonInvalidUUID},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refs := ParseCardReferences(tc.raw)
			if len(refs) != 1 {
				t.Fatalf("expected 1 reference attempt, got %d: %+v", len(refs), refs)
			}
			ref := refs[0]
			if !ref.Invalid {
				t.Fatalf("expected %q to fail parsing", tc.raw)
			}
			if ref.Reason != tc.reason {
				t.Errorf("reason: expected %q, got %q", tc.reason, ref.Reason)
			}
			if ref.URI != "" {
				t.Errorf("an invalid reference must carry no URI, got %q", ref.URI)
			}
			if ref.CardID != uuid.Nil {
				t.Errorf("an invalid reference must carry no id, got %s", ref.CardID)
			}
			if ref.Raw != tc.raw {
				t.Errorf("raw: expected %q, got %q", tc.raw, ref.Raw)
			}
		})
	}
}

// A '#card:' attempt is never also a topic reference: the topic parser does
// not emit slug "card" for it (§6.4 disambiguation). Removing the skip in
// ParseReferences turns this red.
func TestParseReferences_SkipsCardSyntax(t *testing.T) {
	_, raw := cardRefFixture(t)

	for _, content := range []string{
		"see #card:" + raw,
		"see #card:compact/" + raw,
		"see #card:not-a-uuid",
		"see #card:",
		"see #card:/" + raw,
	} {
		refs := ParseReferences(content)
		if len(refs) != 0 {
			t.Errorf("content %q: expected 0 topic references, got %d: %+v", content, len(refs), refs)
		}
	}
}

// A topic reference next to a card reference still parses, with the card span
// consumed: the two syntaxes are independent.
func TestParseReferences_CardAndTopicCoexist(t *testing.T) {
	_, raw := cardRefFixture(t)

	refs := ParseReferences("#topic-one and #card:" + raw + " and #topic-two")
	if len(refs) != 2 {
		t.Fatalf("expected 2 topic references, got %d: %+v", len(refs), refs)
	}
	if refs[0].Slug != "topic-one" || refs[0].Offset != 0 {
		t.Errorf("ref[0]: got slug %q offset %d", refs[0].Slug, refs[0].Offset)
	}
	if refs[1].Slug != "topic-two" {
		t.Errorf("ref[1]: got slug %q", refs[1].Slug)
	}

	// The card reference is reported by the card parser with the true offset.
	cards := ParseCardReferences("#topic-one and #card:" + raw + " and #topic-two")
	if len(cards) != 1 {
		t.Fatalf("expected 1 card reference, got %d", len(cards))
	}
	if cards[0].Offset != 15 {
		t.Errorf("card offset: expected 15, got %d", cards[0].Offset)
	}
}

// A bare '#card' with no colon is still a topic slug — only the card SYNTAX
// is intercepted.
func TestParseReferences_BareHashCardIsStillATopic(t *testing.T) {
	refs := ParseReferences("see #card here")
	if len(refs) != 1 || refs[0].Slug != "card" {
		t.Fatalf("expected topic slug 'card', got %+v", refs)
	}
}

// '#' inside a URL never starts a card reference (same boundary rule as the
// topic parser).
func TestParseCardReferences_NoMatchInsideURL(t *testing.T) {
	_, raw := cardRefFixture(t)
	refs := ParseCardReferences("https://example.com/x#card:" + raw)
	if len(refs) != 0 {
		t.Fatalf("expected 0 card references inside a URL, got %d: %+v", len(refs), refs)
	}
}

// Duplicates are preserved (dedup is the resolution layer's job), and each
// occurrence keeps its own offset.
func TestParseCardReferences_DuplicatesPreserved(t *testing.T) {
	_, raw := cardRefFixture(t)
	refs := ParseCardReferences("#card:" + raw + " and #card:" + raw)
	if len(refs) != 2 {
		t.Fatalf("expected 2 references, got %d", len(refs))
	}
	if refs[0].Offset != 0 {
		t.Errorf("ref[0] offset: expected 0, got %d", refs[0].Offset)
	}
	if want := len("#card:") + len(raw) + len(" and "); refs[1].Offset != want {
		t.Errorf("ref[1] offset: expected %d, got %d", want, refs[1].Offset)
	}
}

// Empty content yields nil, matching ParseReferences.
func TestParseCardReferences_Empty(t *testing.T) {
	if refs := ParseCardReferences(""); refs != nil {
		t.Fatalf("expected nil for empty content, got %v", refs)
	}
	if refs := ParseCardReferences("plain text"); refs != nil {
		t.Fatalf("expected nil for text without card syntax, got %v", refs)
	}
}

// IsUUIDv7 is the §6.4 version gate.
func TestIsUUIDv7(t *testing.T) {
	v7, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7: %v", err)
	}
	if !IsUUIDv7(v7) {
		t.Errorf("NewV7 id %s must be v7", v7)
	}
	if IsUUIDv7(uuid.New()) {
		t.Error("a random (v4) id must not be reported as v7")
	}
}
