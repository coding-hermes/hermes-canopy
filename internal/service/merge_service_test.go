// Package service — unit tests for the merge request validation table
// (SPEC-API-04 §3.3) and the §3.2 defaults. Pure: no database.
package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// mergeV7 returns a fresh UUIDv7, the id form SPEC-API-04 §2 mandates.
func mergeV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7: %v", err)
	}
	return id
}

// mergeIDStrings renders ids as the §3.2 wire representation.
func mergeIDStrings(ids []uuid.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}

// mergeSources builds n distinct UUIDv7 strings.
func mergeSources(t *testing.T, n int) []string {
	t.Helper()
	ids := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, mergeV7(t))
	}
	return mergeIDStrings(ids)
}

func mergePtr(s string) *string { return &s }

// TestCreateMergeRequestToInputValidationTable walks every request-body row of
// the §3.3 table. Rows that need the database (existence, deleted, tree
// membership, the defaulted target) are covered by the router-level table in
// internal/handler/merge_integration_test.go.
func TestCreateMergeRequestToInputValidationTable(t *testing.T) {
	two := mergeSources(t, 2)

	cases := []struct {
		name string
		req  CreateMergeRequest
		code string // §3.3 code expected from MergeErrorFrom
	}{
		{
			name: "absent source_node_ids",
			req:  CreateMergeRequest{},
			code: "INVALID_SOURCE_NODE_IDS",
		},
		{
			name: "null source_node_ids",
			req:  CreateMergeRequest{SourceNodeIDs: nil},
			code: "INVALID_SOURCE_NODE_IDS",
		},
		{
			name: "one source node",
			req:  CreateMergeRequest{SourceNodeIDs: mergeSources(t, 1)},
			code: "MIN_SOURCE_NODES",
		},
		{
			name: "empty source array",
			req:  CreateMergeRequest{SourceNodeIDs: []string{}},
			code: "MIN_SOURCE_NODES",
		},
		{
			name: "101 source nodes",
			req:  CreateMergeRequest{SourceNodeIDs: mergeSources(t, 101)},
			code: "MAX_SOURCE_NODES",
		},
		{
			name: "duplicate source nodes",
			req:  CreateMergeRequest{SourceNodeIDs: []string{two[0], two[1], two[0]}},
			code: "DUPLICATE_SOURCE_NODES",
		},
		{
			name: "malformed source id",
			req:  CreateMergeRequest{SourceNodeIDs: []string{two[0], "not-a-uuid"}},
			code: "INVALID_SOURCE_NODE_ID",
		},
		{
			name: "nil-uuid source id",
			req:  CreateMergeRequest{SourceNodeIDs: []string{two[0], uuid.Nil.String()}},
			code: "INVALID_SOURCE_NODE_ID",
		},
		{
			name: "content too long",
			req:  CreateMergeRequest{SourceNodeIDs: two, Content: strings.Repeat("x", maxContentLen+1)},
			code: "CONTENT_TOO_LONG",
		},
		{
			name: "invalid content format",
			req:  CreateMergeRequest{SourceNodeIDs: two, ContentFormat: "html"},
			code: "INVALID_CONTENT_FORMAT",
		},
		{
			name: "invalid target parent id",
			req:  CreateMergeRequest{SourceNodeIDs: two, TargetParentID: mergePtr("nope")},
			code: "INVALID_TARGET_PARENT_ID",
		},
		{
			name: "empty target parent id",
			req:  CreateMergeRequest{SourceNodeIDs: two, TargetParentID: mergePtr("")},
			code: "INVALID_TARGET_PARENT_ID",
		},
		{
			name: "metadata too large",
			req: CreateMergeRequest{SourceNodeIDs: two, Metadata: json.RawMessage(
				`{"blob":"` + strings.Repeat("y", maxMetadataBytes) + `"}`)},
			code: "METADATA_TOO_LARGE",
		},
		{
			name: "source overlaps explicit target",
			req:  CreateMergeRequest{SourceNodeIDs: two, TargetParentID: mergePtr(two[0])},
			code: "SOURCE_TARGET_OVERLAP",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.req.ToInput()
			if err == nil {
				t.Fatalf("ToInput accepted an invalid request, want error code %s", tc.code)
			}
			apiErr, ok := MergeErrorFrom(err)
			if !ok {
				t.Fatalf("error %v is not a *MergeAPIError (cannot be answered with a §3.3 code)", err)
			}
			if apiErr.Code != tc.code {
				t.Fatalf("error code = %q, want %q (message: %s)", apiErr.Code, tc.code, apiErr.Message)
			}
			if apiErr.Message == "" {
				t.Fatal("error carries an empty message — the envelope would name no cause")
			}
		})
	}
}

// TestCreateMergeRequestToInputMetadataMustBeObject pins the one payload-shape
// rejection the §3.3 table has no row for: §3.2 types metadata as an object,
// so a scalar or array is refused with the sentinel the handler answers as
// 400 VALIDATION_ERROR (the same treatment the multi-reference surface gives
// the same mistake).
func TestCreateMergeRequestToInputMetadataMustBeObject(t *testing.T) {
	two := mergeSources(t, 2)
	for _, raw := range []string{`[]`, `"scalar"`, `5`, `true`} {
		t.Run(raw, func(t *testing.T) {
			_, err := CreateMergeRequest{SourceNodeIDs: two, Metadata: json.RawMessage(raw)}.ToInput()
			if err == nil {
				t.Fatalf("ToInput accepted metadata %s, want ErrMergeMetadataNotObject", raw)
			}
			if _, ok := MergeErrorFrom(err); ok {
				t.Fatalf("metadata shape error %v should not carry a §3.3 catalog code", err)
			}
			if !strings.Contains(err.Error(), "JSON object") {
				t.Fatalf("error %q does not name the metadata shape", err)
			}
		})
	}
}

// TestCreateMergeRequestToInputDefaultsAndBoundaries pins the §3.2 defaults
// (content_format=markdown, target_parent_id=tree root, metadata={}) and the
// inclusive count boundaries (2 and 100 sources are valid).
func TestCreateMergeRequestToInputDefaultsAndBoundaries(t *testing.T) {
	t.Run("defaults applied", func(t *testing.T) {
		two := mergeSources(t, 2)
		input, err := CreateMergeRequest{SourceNodeIDs: two, Content: "synth"}.ToInput()
		if err != nil {
			t.Fatalf("ToInput: %v", err)
		}
		if input.ContentFormat != string(NodeFormatMarkdown) {
			t.Fatalf("content_format = %q, want the markdown default", input.ContentFormat)
		}
		if input.TargetParentID != nil {
			t.Fatalf("target_parent_id = %v, want nil (tree root)", input.TargetParentID)
		}
		if string(input.Metadata) != "{}" {
			t.Fatalf("metadata = %s, want {}", input.Metadata)
		}
		if len(input.SourceNodeIDs) != 2 || input.SourceNodeIDs[0].String() != two[0] {
			t.Fatalf("source ids = %v, want %v in request order", input.SourceNodeIDs, two)
		}
	})

	t.Run("empty content accepted", func(t *testing.T) {
		// §11.1 scenario 23: a synthesis note may have no body.
		if _, err := (CreateMergeRequest{SourceNodeIDs: mergeSources(t, 2)}).ToInput(); err != nil {
			t.Fatalf("empty content rejected: %v", err)
		}
	})

	t.Run("explicit target and metadata survive", func(t *testing.T) {
		two := mergeSources(t, 2)
		target := mergeV7(t).String()
		input, err := CreateMergeRequest{
			SourceNodeIDs:  two,
			Content:        "synth",
			ContentFormat:  "plain",
			TargetParentID: mergePtr(target),
			Metadata:       json.RawMessage(`{"decision":"CTEs"}`),
		}.ToInput()
		if err != nil {
			t.Fatalf("ToInput: %v", err)
		}
		if input.TargetParentID == nil || input.TargetParentID.String() != target {
			t.Fatalf("target_parent_id = %v, want %s", input.TargetParentID, target)
		}
		if string(input.Metadata) != `{"decision":"CTEs"}` {
			t.Fatalf("metadata = %s, want the request object", input.Metadata)
		}
	})

	t.Run("100 sources is the inclusive maximum", func(t *testing.T) {
		input, err := CreateMergeRequest{SourceNodeIDs: mergeSources(t, 100)}.ToInput()
		if err != nil {
			t.Fatalf("100 sources rejected: %v", err)
		}
		if len(input.SourceNodeIDs) != 100 {
			t.Fatalf("parsed %d sources, want 100", len(input.SourceNodeIDs))
		}
	})

	t.Run("2 sources is the inclusive minimum", func(t *testing.T) {
		if _, err := (CreateMergeRequest{SourceNodeIDs: mergeSources(t, 2)}).ToInput(); err != nil {
			t.Fatalf("2 sources rejected: %v", err)
		}
	})
}

// TestCreateMergeRequestMetadataSizeIsMeasuredCompact proves the 16 KiB check
// uses the compact serialization: whitespace in the request cannot push a
// small object over the limit.
func TestCreateMergeRequestMetadataSizeIsMeasuredCompact(t *testing.T) {
	two := mergeSources(t, 2)
	padded := "\n\t  " + strings.Repeat(" ", maxMetadataBytes) + `"decision":"CTEs"` + strings.Repeat(" ", maxMetadataBytes) + "\n"
	req := CreateMergeRequest{SourceNodeIDs: two, Metadata: json.RawMessage(`{` + padded + `}`)}
	input, err := req.ToInput()
	if err != nil {
		t.Fatalf("whitespace-inflated metadata rejected: %v", err)
	}
	if string(input.Metadata) != `{"decision":"CTEs"}` {
		t.Fatalf("metadata = %s, want the compact object", input.Metadata)
	}
}
