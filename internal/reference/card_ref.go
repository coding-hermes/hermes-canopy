// Package reference — #card reference parsing (SPEC-PL-03 §6.4).
//
// SPEC-PL-03 §6.4 extends the SPEC-TM-04 topic parser with card references:
//
//	#card:<type>/<id>  ->  ref://card/<type>/<id>
//
// where <type> is one of compact | expanded | iteration and <id> is a UUIDv7.
//
// This repository's card surface is SINGLE-ID: every card route carries only
// {card_id} (internal/handler/card_handler.go, mounted under /cards in
// internal/server/server.go) and the card service resolves an id across the
// per-type stores — the type is not a routing parameter a reference must
// carry. The BARE form is therefore the primary adaptation here:
//
//	#card:<id>  ->  ref://card/<id>
//
// The typed form is still accepted as the literal §6.4 syntax, with its type
// keyword validated syntactically (see the typed/actual type-disagreement
// rule in internal/context/card_reference.go).
//
// Only malformed references fail: an id that is not a UUID, an id that is a
// valid UUID but not version 7 (§6.4 "validates the type and UUIDv7"), an
// unknown type keyword, an empty type or an empty id. A syntactically valid
// reference whose card is missing, dismissed or archived still RESOLVES —
// with its status — at the resolution layer (§6.4); it never fails parsing.
package reference

import (
	"errors"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// ── #card reference syntax (SPEC-PL-03 §6.4) ──────────────────────────────

const (
	// CardRefURIPrefix is the internal reference URI prefix every parsed card
	// reference produces (§6.4).
	CardRefURIPrefix = "ref://card/"

	// cardRefKeyword is the literal the syntax hangs off. A '#' immediately
	// followed by it is never a topic slug — it is a card reference attempt,
	// which is exactly the disambiguation SPEC-PL-03 §5 gives this syntax
	// ("disambiguating cards from topic slugs").
	cardRefKeyword = "#card:"

	// Malformed-reference reasons. A CardReference carrying Invalid=true has
	// failed parsing and must never be resolved.
	CardRefReasonInvalidUUID = "invalid_uuid"      // id is not a UUID
	CardRefReasonNotUUIDv7   = "not_uuid_v7"       // id is a UUID of another version
	CardRefReasonUnknownType = "unknown_card_type" // type keyword outside the closed set
	CardRefReasonEmptyType   = "empty_card_type"   // "#card:/<id>"
	CardRefReasonEmptyID     = "empty_card_id"     // "#card:" or "#card:<type>/"
)

// CardRefTypes is the closed §6.4 type set the typed form accepts. It mirrors
// the card package's ValidCardTypes and service's CardType constants; it is
// redeclared here rather than imported because internal/reference must stay a
// leaf package (internal/card imports internal/service, so a shared home
// would drag the whole service layer into the parser). Change the three
// together.
var CardRefTypes = map[string]bool{
	"compact":   true,
	"expanded":  true,
	"iteration": true,
}

var (
	// cardRefRe matches a '#card:' reference attempt, malformed ones
	// included: the token after '#card:' is the run of characters a
	// well-formed reference can be built from, and it may be EMPTY — a bare
	// "#card:" with no id is malformed syntax, not a topic slug "#card".
	//
	// The boundary rule is the topic parser's: the '#' must start the text or
	// follow a non-word, non-'#' character, so '#card:' inside a URL never
	// matches. '/' is in the token class so the typed form and the empty-type
	// case reach the classifier instead of being silently truncated.
	cardRefRe = regexp.MustCompile(`(?:^|[^a-zA-Z0-9#])#card:([0-9A-Za-z/-]*)`)
)

// ── Parsed type ───────────────────────────────────────────────────────────

// CardReference is a single #card reference found in message content
// (SPEC-PL-03 §6.4).
//
// Invalid reports that the reference is malformed; such a reference carries a
// Reason, no URI and no id, and callers must not resolve it. A valid
// reference always carries its URI and id.
type CardReference struct {
	Raw      string    `json:"raw"`                 // full match: "#card:compact/<uuid>"
	URI      string    `json:"uri,omitempty"`       // "ref://card/compact/<uuid>" when valid
	CardType string    `json:"card_type,omitempty"` // typed-form type; "" for the bare form
	CardID   uuid.UUID `json:"card_id"`             // parsed id; uuid.Nil when invalid
	Bare     bool      `json:"bare,omitempty"`      // the single-id form #card:<uuid>
	Offset   int       `json:"offset"`              // character offset of '#' in content
	Length   int       `json:"length"`              // length of the matched text (including '#')
	Invalid  bool      `json:"invalid,omitempty"`
	Reason   string    `json:"reason,omitempty"`
}

// cardRefSpan is one '#card:' attempt located in content: the span it occupies
// and the raw token between '#card:' and the end of the match.
type cardRefSpan struct {
	start int    // offset of '#'
	end   int    // end of the matched text
	token string // text after '#card:'
}

// cardRefSpans locates every '#card:' attempt in content, valid or malformed.
// ParseReferences uses it to keep card syntax out of topic parsing;
// ParseCardReferences turns the same spans into CardReference values, so both
// agree on exactly what a card reference occupies.
func cardRefSpans(content string) []cardRefSpan {
	matches := cardRefRe.FindAllStringSubmatchIndex(content, -1)
	if matches == nil {
		return nil
	}
	spans := make([]cardRefSpan, 0, len(matches))
	for _, m := range matches {
		// m[2]:m[3] is the token; the '#' sits len("#card:") before it.
		tokenStart, tokenEnd := m[2], m[3]
		hashIdx := tokenStart - len(cardRefKeyword)
		spans = append(spans, cardRefSpan{
			start: hashIdx,
			end:   tokenEnd,
			token: content[tokenStart:tokenEnd],
		})
	}
	return spans
}

// ParseCardReferences extracts every #card reference attempt from content in
// order of appearance. Malformed attempts are returned too, flagged Invalid
// with a Reason, so a caller can report them rather than lose them: "only
// malformed references fail parsing" (§6.4) means they FAIL, not that they
// vanish.
//
// Duplicate references are preserved (each occurrence is returned), matching
// ParseReferences; the resolution layer deduplicates by card id.
func ParseCardReferences(content string) []CardReference {
	if content == "" {
		return nil
	}

	spans := cardRefSpans(content)
	if len(spans) == 0 {
		return nil
	}

	refs := make([]CardReference, 0, len(spans))
	for _, span := range spans {
		ref := CardReference{
			Raw:    content[span.start:span.end],
			Offset: span.start,
			Length: span.end - span.start,
		}

		cardType, id, bare, reason := classifyCardRefToken(span.token)
		ref.CardType = cardType
		ref.Bare = bare
		if reason != "" {
			ref.Invalid = true
			ref.Reason = reason
			refs = append(refs, ref)
			continue
		}

		ref.CardID = id
		ref.URI = cardRefURI(cardType, id)
		refs = append(refs, ref)
	}

	return refs
}

// classifyCardRefToken validates the text between '#card:' and the end of the
// match. It returns the typed form's type ("" for the bare form), the parsed
// id, whether the reference is the bare form, and — for malformed input — the
// reason. A non-empty reason always means the reference failed parsing, in
// which case the returned id is uuid.Nil.
func classifyCardRefToken(token string) (cardType string, id uuid.UUID, bare bool, reason string) {
	if token == "" {
		return "", uuid.Nil, true, CardRefReasonEmptyID
	}

	if slash := strings.Index(token, "/"); slash >= 0 {
		typ, rawID := token[:slash], token[slash+1:]
		if typ == "" {
			return "", uuid.Nil, false, CardRefReasonEmptyType
		}
		if !CardRefTypes[typ] {
			return "", uuid.Nil, false, CardRefReasonUnknownType
		}
		if rawID == "" {
			return typ, uuid.Nil, false, CardRefReasonEmptyID
		}
		parsed, err := parseCardRefID(rawID)
		if err != nil {
			return typ, uuid.Nil, false, cardRefIDReason(err)
		}
		return typ, parsed, false, ""
	}

	parsed, err := parseCardRefID(token)
	if err != nil {
		return "", uuid.Nil, true, cardRefIDReason(err)
	}
	return "", parsed, true, ""
}

// errNotUUIDv7 marks a syntactically valid UUID whose version is not 7.
var errNotUUIDv7 = errors.New("card reference id is not a UUIDv7")

// parseCardRefID parses a reference id, requiring an RFC 9562 version-7 UUID
// (§6.4 "validates the type and UUIDv7").
func parseCardRefID(raw string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, err
	}
	if !IsUUIDv7(parsed) {
		return uuid.Nil, errNotUUIDv7
	}
	return parsed, nil
}

// cardRefIDReason maps a parse failure onto its malformed-reference reason.
func cardRefIDReason(err error) string {
	if errors.Is(err, errNotUUIDv7) {
		return CardRefReasonNotUUIDv7
	}
	return CardRefReasonInvalidUUID
}

// IsUUIDv7 reports whether u is an RFC 9562 version-7 UUID. §6.4 validates
// the reference id as UUIDv7; a parseable id of any other version is a
// malformed reference.
func IsUUIDv7(u uuid.UUID) bool {
	return u.Version() == 7
}

// cardRefURI builds the internal reference URI for a reference (§6.4): the
// typed form keeps its type, the bare form is ref://card/<id>.
func cardRefURI(cardType string, id uuid.UUID) string {
	if cardType == "" {
		return CardRefURIPrefix + id.String()
	}
	return CardRefURIPrefix + cardType + "/" + id.String()
}
