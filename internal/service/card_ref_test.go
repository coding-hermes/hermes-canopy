package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// cardRefValidJSON is the §6.4 node metadata example. The spec's inline
// context_hash is 63 characters long — one short of a SHA-256 hex digest, so
// the example carries a 64-character digest here (the length the brief and §7
// require) rather than propagating the typo into the fixture.
const cardRefValidJSON = `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"iteration","app_id":"canopy.agent","context_hash":"c4a2f1d6c7b8e9f00112233445566778899aabbccddeeff00112233445566777"}}`

// --- No card_ref: behaves exactly as today ----------------------------------

func TestValidateNodeMetadataCardRef_AbsentIsNoOp(t *testing.T) {
	cases := map[string]string{
		"nil":               "",
		"empty object":      `{}`,
		"other keys":        `{"pinned":true,"color":"red"}`,
		"reserved metadata": `{"multi_reference":{"primarySourceId":"x"}}`,
		"json array":        `[1,2,3]`,
		"json string":       `"metadata"`,
		"json number":       `42`,
		"malformed json":    `{"pinned":`,
	}
	for name, metadata := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateNodeMetadataCardRef([]byte(metadata)); err != nil {
				t.Fatalf("metadata without card_ref must be untouched, got %v", err)
			}
			ref, present, err := ParseNodeMetadataCardRef([]byte(metadata))
			if err != nil || present || ref != nil {
				t.Fatalf("expected (nil,false,nil), got (%+v,%v,%v)", ref, present, err)
			}
		})
	}
}

// --- Valid attachments ------------------------------------------------------

func TestValidateNodeMetadataCardRef_Valid(t *testing.T) {
	cases := map[string]string{
		"spec example":      cardRefValidJSON,
		"no context hash":   `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"compact","app_id":"canopy.task"}}`,
		"extra keys":        `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"expanded","app_id":"canopy.code","extra":{"a":1}}}`,
		"non-v7 uuid":       `{"card_ref":{"id":"0191a9c3-0000-4000-8000-000000000000","card_type":"iteration","app_id":"canopy.agent"}}`,
		"uppercase hash":    `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"iteration","app_id":"canopy.agent","context_hash":"C4A2F1D6C7B8E9F00112233445566778899AABBCCDDEEFF00112233445566777"}}`,
		"beside other keys": `{"pinned":true,"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"iteration","app_id":"canopy.agent"}}`,
	}
	for name, metadata := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateNodeMetadataCardRef([]byte(metadata)); err != nil {
				t.Fatalf("expected a valid card_ref, got %v", err)
			}
		})
	}
}

func TestParseNodeMetadataCardRef_ReadsTheAttachment(t *testing.T) {
	ref, present, err := ParseNodeMetadataCardRef([]byte(cardRefValidJSON))
	if err != nil {
		t.Fatalf("ParseNodeMetadataCardRef: %v", err)
	}
	if !present || ref == nil {
		t.Fatal("expected a present attachment")
	}
	if ref.ID != "0191a9c3-0000-7000-8000-000000000000" {
		t.Errorf("id: got %q", ref.ID)
	}
	if ref.CardType != "iteration" {
		t.Errorf("card_type: got %q", ref.CardType)
	}
	if ref.AppID != "canopy.agent" {
		t.Errorf("app_id: got %q", ref.AppID)
	}
	if ref.ContextHash != "c4a2f1d6c7b8e9f00112233445566778899aabbccddeeff00112233445566777" {
		t.Errorf("context_hash: got %q", ref.ContextHash)
	}
	if !ref.Valid() {
		t.Error("the spec's own example must be structurally valid")
	}
}

// --- Malformed attachments --------------------------------------------------

// Every malformed variant is rejected with the NAMED error, so the HTTP layer
// can classify it without string matching.
func TestValidateNodeMetadataCardRef_Invalid(t *testing.T) {
	valid := `{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"iteration","app_id":"canopy.agent"}`

	cases := []struct {
		name     string
		metadata string
		want     string // substring of the reason
	}{
		{"card_ref is a string", `{"card_ref":"iteration"}`, "must be an object"},
		{"card_ref is an array", `{"card_ref":[]}`, "must be an object"},
		{"card_ref is a number", `{"card_ref":7}`, "must be an object"},
		{"card_ref is null", `{"card_ref":null}`, "id is required"},
		{"card_ref is empty object", `{"card_ref":{}}`, "id is required"},
		{"id missing", `{"card_ref":{"card_type":"iteration","app_id":"canopy.agent"}}`, "id is required"},
		{"id empty", `{"card_ref":{"id":"","card_type":"iteration","app_id":"canopy.agent"}}`, "id is required"},
		{"id not a uuid", `{"card_ref":{"id":"not-a-uuid","card_type":"iteration","app_id":"canopy.agent"}}`, "id must be a UUID"},
		{"id wrong type", `{"card_ref":{"id":12,"card_type":"iteration","app_id":"canopy.agent"}}`, "must be an object"},
		{"card_type missing", `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","app_id":"canopy.agent"}}`, "card_type must be one of"},
		{"card_type unknown", `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"bogus","app_id":"canopy.agent"}}`, "card_type must be one of"},
		{"card_type wrong case", `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"Compact","app_id":"canopy.agent"}}`, "card_type must be one of"},
		{"app_id missing", `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"iteration"}}`, "app_id is required"},
		{"app_id empty", `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"iteration","app_id":""}}`, "app_id is required"},
		{"app_id blank", `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"iteration","app_id":"   "}}`, "app_id is required"},
		{"app_id wrong type", `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"iteration","app_id":3}}`, "must be an object"},
		{"context_hash too short", `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"iteration","app_id":"canopy.agent","context_hash":"c4a2f1d6"}}`, "context_hash must be 64 hex"},
		{"context_hash too long", `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"iteration","app_id":"canopy.agent","context_hash":"` + strings.Repeat("a", 65) + `"}}`, "context_hash must be 64 hex"},
		{"context_hash not hex", `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"iteration","app_id":"canopy.agent","context_hash":"` + strings.Repeat("z", 64) + `"}}`, "context_hash must be 64 hex"},
		{"context_hash wrong type", `{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"iteration","app_id":"canopy.agent","context_hash":1234}}`, "must be an object"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateNodeMetadataCardRef([]byte(tc.metadata))
			if err == nil {
				t.Fatalf("expected %s to be rejected", tc.metadata)
			}
			if !errors.Is(err, ErrInvalidCardRef) {
				t.Fatalf("error must be the named sentinel, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("reason %q does not mention %q", err.Error(), tc.want)
			}
		})
	}

	// Sanity: the reference fixture used above is itself accepted, so the
	// table's rejections are about the mutation, not the base.
	if err := ValidateNodeMetadataCardRef([]byte(`{"card_ref":` + valid + `}`)); err != nil {
		t.Fatalf("baseline attachment must be valid: %v", err)
	}
}

// --- Write-path seams -------------------------------------------------------

// The create-path validator is a pure function: this exercises the seam the
// node write path calls, with no database involved.
func TestValidateCreateInput_CardRefSeam(t *testing.T) {
	base := CreateNodeInput{
		Content:       "hello",
		ContentFormat: "markdown",
		NodeType:      "message",
		EdgeType:      "reply",
	}

	// Absent card_ref: unchanged behavior.
	if err := validateCreateInput(base); err != nil {
		t.Fatalf("metadata-less create must validate, got %v", err)
	}

	// A valid attachment passes the seam.
	withValid := base
	withValid.Metadata = json.RawMessage(cardRefValidJSON)
	if err := validateCreateInput(withValid); err != nil {
		t.Fatalf("valid card_ref must validate, got %v", err)
	}

	// A malformed attachment is rejected with the named error.
	withInvalid := base
	withInvalid.Metadata = json.RawMessage(`{"card_ref":{"id":"nope","card_type":"iteration","app_id":"canopy.agent"}}`)
	err := validateCreateInput(withInvalid)
	if !errors.Is(err, ErrInvalidCardRef) {
		t.Fatalf("expected ErrInvalidCardRef, got %v", err)
	}
}

// The full create path surfaces the named error, exactly like the metadata
// size rule does (no database needed: validation runs before the repo call).
func TestCreateNode_InvalidCardRef(t *testing.T) {
	svc := newNodeService()
	_, err := svc.Create(context.Background(), uuid.New(), CreateNodeInput{
		Content:       "hello",
		ContentFormat: "markdown",
		NodeType:      "message",
		EdgeType:      "reply",
		Metadata:      json.RawMessage(`{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"bogus","app_id":"canopy.agent"}}`),
	})
	if !errors.Is(err, ErrInvalidCardRef) {
		t.Fatalf("Create() error = %v, want ErrInvalidCardRef", err)
	}
}

// Merge writes a synthesis node's metadata through its own seam; the same
// structural rule applies and maps onto the §3.3 catalog row.
func TestNormalizeMergeMetadata_CardRefValidated(t *testing.T) {
	if _, err := normalizeMergeMetadata(json.RawMessage(cardRefValidJSON)); err != nil {
		t.Fatalf("valid card_ref must survive the merge seam, got %v", err)
	}

	_, err := normalizeMergeMetadata(json.RawMessage(`{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"iteration","app_id":""}}`))
	if !errors.Is(err, ErrInvalidCardRef) {
		t.Fatalf("expected ErrInvalidCardRef, got %v", err)
	}
	var apiErr *MergeAPIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected a MergeAPIError, got %T", err)
	}
	if apiErr.Code != "INVALID_CARD_REF" {
		t.Errorf("code: expected INVALID_CARD_REF, got %q", apiErr.Code)
	}
	if apiErr.Status != 400 {
		t.Errorf("status: expected 400, got %d", apiErr.Status)
	}
}

// Update writes metadata through the same rule, but only after its own 404
// lookup succeeds — so the seam needs a repo whose node exists.
type cardRefNodeRepoStub struct {
	nodeRepoStub
	node *db.Node
}

func (s *cardRefNodeRepoStub) GetByID(_ context.Context, _ uuid.UUID) (*db.Node, error) {
	return s.node, nil
}

// The update seam rejects a malformed card_ref before any write. The node
// exists, so the 404 branch is passed and the metadata rule is what answers.
func TestUpdate_InvalidCardRef(t *testing.T) {
	svc := newNodeService()
	nodeID := uuid.New()
	svc.nodeRepo = &cardRefNodeRepoStub{node: &db.Node{ID: nodeID, Content: "hello"}}

	metadata := json.RawMessage(`{"card_ref":{"id":"0191a9c3-0000-7000-8000-000000000000","card_type":"bogus","app_id":"canopy.agent"}}`)
	_, err := svc.Update(context.Background(), nodeID, UpdateNodeInput{Metadata: &metadata})
	if !errors.Is(err, ErrInvalidCardRef) {
		t.Fatalf("Update() error = %v, want ErrInvalidCardRef", err)
	}
}

// A missing node still answers 404, not the metadata rule: the new guard must
// not reorder the existing error precedence (nodeRepoStub's GetByID is a miss).
func TestUpdate_MissingNodeStaysNotFound(t *testing.T) {
	svc := newNodeService()
	metadata := json.RawMessage(`{"card_ref":{"id":"nope","card_type":"bogus","app_id":""}}`)
	_, err := svc.Update(context.Background(), uuid.New(), UpdateNodeInput{Metadata: &metadata})
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("Update() error = %v, want ErrNodeNotFound", err)
	}
}

// The merge catalog and the node handler agree on the sentinel's identity:
// a missing catalog row would silently degrade to a 500-class response.
func TestCardRefErrorIsCatalogued(t *testing.T) {
	spec, ok := mergeErrorCatalog[ErrInvalidCardRef]
	if !ok {
		t.Fatal("ErrInvalidCardRef is missing from the merge error catalog")
	}
	if spec.status != 400 {
		t.Errorf("catalog status: expected 400, got %d", spec.status)
	}
	if spec.code == "" {
		t.Error("catalog row must carry a wire code")
	}
}
