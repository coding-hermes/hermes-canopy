package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Card-reference node metadata validation (SPEC-PL-03 §6.4).
//
// A graph node carries its card attachment in `metadata.card_ref`:
//
//	{
//	  "card_ref": {
//	    "id": "0191a9c3-0000-7000-8000-000000000000",
//	    "card_type": "iteration",
//	    "app_id": "canopy.agent",
//	    "context_hash": "c4a2f1d6…(64 hex)"
//	  }
//	}
//
// The check is STRUCTURAL ONLY: the shape, the id, the card type, the app id
// and the context hash. The referenced card is never looked up here — §6.4
// makes a missing (or dismissed, or archived) card a status the resolver
// reports, not a reason to reject the write. Metadata that carries no
// `card_ref` at all is untouched: absent card_ref behaves exactly as it did
// before this rule existed.
//
// Spec: SPEC-PL-03 §6.4, §12.4 scenario 69.

// ErrInvalidCardRef is returned when node metadata carries a card_ref object
// that is not structurally valid. The HTTP layer maps it onto the same
// 400-class validation response as ErrMetadataTooLarge.
var ErrInvalidCardRef = errors.New("node service: metadata card_ref is invalid")

// cardRefHashLen is the length of the lowercase SHA-256 hex digest a card's
// context_hash carries (§6.4 / §7 "lowercase SHA-256 of the compiled context
// manifest").
const cardRefHashLen = 64

// CardRefMetadata is the §6.4 node-level card attachment, as it appears in
// node metadata. Field names are the spec's own snake_case keys — this object
// is stored in jsonb, not served through the camelCase REST envelope.
//
// ContextHash is optional: a card may be attached before its context binding
// is known.
type CardRefMetadata struct {
	ID          string `json:"id"`
	CardType    string `json:"card_type"`
	AppID       string `json:"app_id"`
	ContextHash string `json:"context_hash"`
}

// Valid reports whether the parsed attachment is structurally valid: a valid
// UUID id, a card type in the closed §1 set, a non-empty app id, and an
// optional 64-hex context hash.
func (c CardRefMetadata) Valid() bool {
	return c.validate() == nil
}

// validate is the single structural rule set both the accessor and the write
// path go through, so the two can never disagree.
func (c CardRefMetadata) validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidCardRef)
	}
	if _, err := uuid.Parse(c.ID); err != nil {
		return fmt.Errorf("%w: id must be a UUID", ErrInvalidCardRef)
	}
	if !validCardRefType(c.CardType) {
		return fmt.Errorf("%w: card_type must be one of: %s, %s, %s",
			ErrInvalidCardRef, CardTypeCompact, CardTypeExpanded, CardTypeIteration)
	}
	if strings.TrimSpace(c.AppID) == "" {
		return fmt.Errorf("%w: app_id is required", ErrInvalidCardRef)
	}
	if c.ContextHash != "" && !validCardRefHash(c.ContextHash) {
		return fmt.Errorf("%w: context_hash must be %d hex characters",
			ErrInvalidCardRef, cardRefHashLen)
	}
	return nil
}

// ParseNodeMetadataCardRef reads the §6.4 card attachment out of node
// metadata. It returns (nil, false, nil) whenever the metadata carries no
// card_ref — absent, empty, non-object metadata, or an explicit null — which
// is the "behaves exactly as today" case. A card_ref that is PRESENT but not
// a valid object is an error.
//
// nil, false, nil is never an error: a node that never attached a card must
// not start failing writes because of a rule aimed at nodes that did.
func ParseNodeMetadataCardRef(metadata []byte) (*CardRefMetadata, bool, error) {
	if len(metadata) == 0 {
		return nil, false, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &fields); err != nil {
		// Not a JSON object: there is no card_ref in it to validate.
		return nil, false, nil
	}
	raw, ok := fields["card_ref"]
	if !ok {
		return nil, false, nil
	}

	var ref CardRefMetadata
	if err := json.Unmarshal(raw, &ref); err != nil {
		return nil, true, fmt.Errorf("%w: card_ref must be an object with id, card_type and app_id", ErrInvalidCardRef)
	}

	return &ref, true, nil
}

// ValidateNodeMetadataCardRef validates the optional §6.4 card attachment in
// node metadata. Metadata without a card_ref is always accepted.
//
// It is a pure function of the bytes: no store, no pool, no lookup — the
// referenced card's existence is deliberately NOT part of the rule (§6.4
// structural validation).
func ValidateNodeMetadataCardRef(metadata []byte) error {
	ref, present, err := ParseNodeMetadataCardRef(metadata)
	if err != nil {
		return err
	}
	if !present || ref == nil {
		return nil
	}
	return ref.validate()
}

// validCardRefType reports whether t is one of the three §1 card types.
func validCardRefType(t string) bool {
	switch CardType(t) {
	case CardTypeCompact, CardTypeExpanded, CardTypeIteration:
		return true
	}
	return false
}

// validCardRefHash reports whether h is a 64-character hex digest. Case is
// not structural, so both cases are accepted; the spec's lowercase convention
// is a producer rule the shipped card surface follows.
func validCardRefHash(h string) bool {
	if len(h) != cardRefHashLen {
		return false
	}
	for _, r := range h {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
