// Unit tests for the §9.3 reference-context envelope. No database is
// required: buildReferenceContext is pure, so the shape, the ordering, the
// truncation and the verify_hash semantics are all pinned here.
package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// referenceContextFixture builds a persisted-looking manifest: three sources
// in canonical order, a stored manifest hash derived from the ORIGINAL source
// hashes, and a branch span whose ancestor is the tree root.
func referenceContextFixture(t *testing.T) (referenceContextNode, db.MultiReferenceMetadata, []referenceContextSourceRow) {
	t.Helper()

	treeID := uuid.MustParse("0191a8b2-7fff-7000-9000-000000000001")
	nodeID := uuid.MustParse("0191a8b2-7fff-7000-9000-000000000301")
	root := uuid.MustParse("0191a8b2-7fff-7000-9000-000000000010")
	srcA := uuid.MustParse("0191a8b2-7fff-7000-9000-000000000101")
	srcB := uuid.MustParse("0191a8b2-7fff-7000-9000-000000000202")
	srcC := uuid.MustParse("0191a8b2-7fff-7000-9000-000000000203")

	rows := []referenceContextSourceRow{
		{EdgeID: uuid.New(), SourceID: srcA, Content: "source A body", ContentHash: "hash-a", SequenceNum: 11},
		{EdgeID: uuid.New(), SourceID: srcB, Content: "source B body", ContentHash: "hash-b", SequenceNum: 22},
		{EdgeID: uuid.New(), SourceID: srcC, Content: "source C body", ContentHash: "hash-c", SequenceNum: 33},
	}
	live := []referenceSource{
		{ID: srcA, ContentHash: "hash-a", SequenceNum: 11},
		{ID: srcB, ContentHash: "hash-b", SequenceNum: 22},
		{ID: srcC, ContentHash: "hash-c", SequenceNum: 33},
	}
	const budget = 8192
	manifest := referenceManifestHash(treeID, srcA, live, budget)

	node := referenceContextNode{
		ID:         nodeID,
		TreeID:     treeID,
		ParentID:   &srcA,
		ParentMode: string(db.ParentModeMultiReference),
	}
	reserved := db.MultiReferenceMetadata{
		Version:               db.MultiReferenceMetadataVersion,
		PrimarySourceID:       srcA,
		CanonicalSourceIDs:    []uuid.UUID{srcA, srcB, srcC},
		IsSyntheticMergePoint: true,
		BranchSpan: &db.BranchSpanMetadata{
			CommonAncestorID: root,
			SourceBranches: []db.ReferenceBranchSource{
				{SourceID: srcA, BranchRootID: srcA, DistanceFromRoot: 0},
				{SourceID: srcB, BranchRootID: srcB, DistanceFromRoot: 0},
				{SourceID: srcC, BranchRootID: root, DistanceFromRoot: 4},
			},
		},
		ContextManifestHash: manifest,
		ContextTokenBudget:  budget,
	}
	return node, reserved, rows
}

// defaultReferenceContextOptions mirrors the §9.3 defaults.
func defaultReferenceContextOptions() ReferenceContextOptions {
	return ReferenceContextOptions{IncludeContent: true, VerifyHash: true}
}

func TestReferenceContextBuild_PersistsCreationOrderAndLabels(t *testing.T) {
	node, reserved, rows := referenceContextFixture(t)

	result, err := buildReferenceContext(node, reserved, rows, defaultReferenceContextOptions())
	require.NoError(t, err)

	assert.Equal(t, node.ID, result.NodeID)
	assert.Equal(t, node.TreeID, result.TreeID)
	assert.Equal(t, "multi_reference", result.ParentMode)
	assert.Equal(t, reserved.PrimarySourceID, result.PrimarySourceID)

	require.Len(t, result.Context.Sources, 3)
	for i, wantID := range []uuid.UUID{rows[0].SourceID, rows[1].SourceID, rows[2].SourceID} {
		assert.Equal(t, wantID, result.Context.Sources[i].NodeID, "source order is persisted selection order")
		assert.Equal(t, db.ReferenceSourceLabel(i), result.Context.Sources[i].SourceLabel)
		require.NotNil(t, result.Context.Sources[i].Content)
		assert.Equal(t, rows[i].Content, *result.Context.Sources[i].Content)
		assert.False(t, result.Context.Sources[i].Truncated)
	}
	assert.Equal(t, "R1", result.Context.Sources[0].SourceLabel)
	assert.Equal(t, "R3", result.Context.Sources[2].SourceLabel)

	assert.True(t, result.Context.IsSyntheticMergePoint)
	assert.EqualValues(t, reserved.ContextTokenBudget, result.Context.TokenBudget)
	// tokens_used is the §6.2 estimate over the live sources.
	assert.EqualValues(t, estimateSourceTokens(rows[0].Content)+
		estimateSourceTokens(rows[1].Content)+
		estimateSourceTokens(rows[2].Content), result.Context.TokensUsed)

	// The stored hash is returned verbatim — it is the provenance record.
	assert.Equal(t, reserved.ContextManifestHash, result.Context.ManifestHash)
	assert.False(t, result.SourceChangedSinceCreation, "an untouched snapshot is not flagged")
}

func TestReferenceContextBuild_BranchSpanUsesSnakeCaseWire(t *testing.T) {
	node, reserved, rows := referenceContextFixture(t)

	result, err := buildReferenceContext(node, reserved, rows, defaultReferenceContextOptions())
	require.NoError(t, err)

	require.NotNil(t, result.Context.BranchSpan.CommonAncestorID)
	assert.Equal(t, reserved.BranchSpan.CommonAncestorID, *result.Context.BranchSpan.CommonAncestorID)
	require.Len(t, result.Context.BranchSpan.SourceBranches, 3)
	assert.Equal(t, reserved.BranchSpan.SourceBranches[2].SourceID, result.Context.BranchSpan.SourceBranches[2].SourceID)
	assert.Equal(t, reserved.BranchSpan.SourceBranches[2].BranchRootID, result.Context.BranchSpan.SourceBranches[2].BranchRootID)
	assert.Equal(t, reserved.BranchSpan.SourceBranches[2].DistanceFromRoot, result.Context.BranchSpan.SourceBranches[2].DistanceFromRoot)

	raw, err := json.Marshal(result.Context.BranchSpan)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	_, hasSnake := decoded["common_ancestor_id"]
	_, hasCamel := decoded["commonAncestorId"]
	assert.True(t, hasSnake, "branch span must use snake_case on this boundary")
	assert.False(t, hasCamel)
}

func TestReferenceContextBuild_NullableBranchSpanIsNullAndEmpty(t *testing.T) {
	node, reserved, rows := referenceContextFixture(t)
	reserved.BranchSpan = nil

	result, err := buildReferenceContext(node, reserved, rows, defaultReferenceContextOptions())
	require.NoError(t, err)

	assert.Nil(t, result.Context.BranchSpan.CommonAncestorID)
	assert.NotNil(t, result.Context.BranchSpan.SourceBranches, "source_branches is an empty list, never null")
	assert.Empty(t, result.Context.BranchSpan.SourceBranches)

	raw, err := json.Marshal(result.Context)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"branch_span":{"common_ancestor_id":null,"source_branches":[]}`)
}

func TestReferenceContextBuild_OmitsContentWhenNotRequested(t *testing.T) {
	node, reserved, rows := referenceContextFixture(t)
	opts := defaultReferenceContextOptions()
	opts.IncludeContent = false

	result, err := buildReferenceContext(node, reserved, rows, opts)
	require.NoError(t, err)

	for _, src := range result.Context.Sources {
		assert.Nil(t, src.Content, "include_content=false omits content")
	}

	raw, err := json.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"content"`)
	assert.Contains(t, string(raw), `"source_label"`)
	assert.Contains(t, string(raw), `"truncated"`)
}

func TestReferenceContextBuild_ContentOmittedOnlyWhenAsked(t *testing.T) {
	node, reserved, rows := referenceContextFixture(t)
	rows[0].Content = ""
	rows[1].Content = ""

	result, err := buildReferenceContext(node, reserved, rows, defaultReferenceContextOptions())
	require.NoError(t, err)

	// An empty string is still EMITTED when content was requested: the
	// client must be able to tell "empty" from "not asked for".
	require.NotNil(t, result.Context.Sources[0].Content)
	assert.Equal(t, "", *result.Context.Sources[0].Content)

	raw, err := json.Marshal(result.Context.Sources[0])
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"content":""`)
}

func TestReferenceContextBuild_TruncatesLongSourceWithMarker(t *testing.T) {
	node, reserved, rows := referenceContextFixture(t)
	long := strings.Repeat("abcdefghij", 600) // 6000 chars ≈ 1500 tokens
	rows[0].Content = long
	rows[0].ContentHash = "hash-a"
	// Re-derive the stored manifest from the fixture rows so verify_hash is
	// not the signal under test here.
	reserved.ContextManifestHash = referenceManifestHash(node.TreeID, reserved.PrimarySourceID, []referenceSource{
		{ID: rows[0].SourceID, ContentHash: rows[0].ContentHash, SequenceNum: rows[0].SequenceNum},
		{ID: rows[1].SourceID, ContentHash: rows[1].ContentHash, SequenceNum: rows[1].SequenceNum},
		{ID: rows[2].SourceID, ContentHash: rows[2].ContentHash, SequenceNum: rows[2].SequenceNum},
	}, reserved.ContextTokenBudget)

	opts := defaultReferenceContextOptions()
	opts.MaxSourceTokens = 256

	result, err := buildReferenceContext(node, reserved, rows, opts)
	require.NoError(t, err)

	require.NotNil(t, result.Context.Sources[0].Content)
	got := *result.Context.Sources[0].Content
	require.True(t, result.Context.Sources[0].Truncated, "a 1500-token source exceeds a 256-token allowance")
	assert.Less(t, len(got), len(long), "truncated content is shorter than the source")
	assert.Contains(t, got, "tokens omitted from source R1", "§6.1 omission marker names the source")
	assert.True(t, strings.HasPrefix(got, "abcdefghij"), "head is preserved")
	assert.True(t, strings.HasSuffix(got, "abcdefghij"), "tail is preserved")

	// A short source is untouched by the same allowance.
	assert.False(t, result.Context.Sources[1].Truncated)
	assert.Equal(t, rows[1].Content, *result.Context.Sources[1].Content)
}

func TestReferenceContextBuild_DefaultAllowanceIsPerSourceCeiling(t *testing.T) {
	node, reserved, rows := referenceContextFixture(t)
	// ≈ 4000 tokens: above the §6.2 per-source ceiling of 2,048.
	rows[0].Content = strings.Repeat("x", 16000)
	reserved.ContextManifestHash = referenceManifestHash(node.TreeID, reserved.PrimarySourceID, []referenceSource{
		{ID: rows[0].SourceID, ContentHash: rows[0].ContentHash, SequenceNum: rows[0].SequenceNum},
		{ID: rows[1].SourceID, ContentHash: rows[1].ContentHash, SequenceNum: rows[1].SequenceNum},
		{ID: rows[2].SourceID, ContentHash: rows[2].ContentHash, SequenceNum: rows[2].SequenceNum},
	}, reserved.ContextTokenBudget)

	result, err := buildReferenceContext(node, reserved, rows, defaultReferenceContextOptions())
	require.NoError(t, err)

	assert.True(t, result.Context.Sources[0].Truncated,
		"the default allowance is the §6.2 per-source ceiling (2,048 tokens)")
}

func TestReferenceContextBuild_VerifyHashFlagsChangedSource(t *testing.T) {
	node, reserved, rows := referenceContextFixture(t)

	// The source was edited after creation: the live content hash no longer
	// matches the snapshot the manifest was built from.
	rows[1].Content = "source B body, edited"
	rows[1].ContentHash = "hash-b-edited"

	result, err := buildReferenceContext(node, reserved, rows, defaultReferenceContextOptions())
	require.NoError(t, err)

	assert.True(t, result.SourceChangedSinceCreation)
	// The provenance hash is still the STORED one, never a fresh digest.
	assert.Equal(t, reserved.ContextManifestHash, result.Context.ManifestHash)
}

func TestReferenceContextBuild_VerifyHashDisabledIsSilent(t *testing.T) {
	node, reserved, rows := referenceContextFixture(t)
	rows[1].ContentHash = "hash-b-edited"

	opts := defaultReferenceContextOptions()
	opts.VerifyHash = false

	result, err := buildReferenceContext(node, reserved, rows, opts)
	require.NoError(t, err)

	assert.False(t, result.SourceChangedSinceCreation)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "source_changed_since_creation")
}

func TestReferenceContextBuild_VerifyHashFlagsDeletedSource(t *testing.T) {
	node, reserved, rows := referenceContextFixture(t)
	rows[2].SourceDeleted = true

	result, err := buildReferenceContext(node, reserved, rows, defaultReferenceContextOptions())
	require.NoError(t, err)

	assert.True(t, result.SourceChangedSinceCreation,
		"a soft-deleted source is a provenance change (§10.1 invalidated row)")
	require.Len(t, result.Context.Sources, 3, "the edge (and so the source) is still reported")
}

func TestReferenceContextBuild_PrimaryFallsBackToDisplayAnchor(t *testing.T) {
	node, reserved, rows := referenceContextFixture(t)
	reserved.PrimarySourceID = uuid.Nil

	result, err := buildReferenceContext(node, reserved, rows, defaultReferenceContextOptions())
	require.NoError(t, err)

	require.NotNil(t, node.ParentID)
	assert.Equal(t, *node.ParentID, result.PrimarySourceID)
}

func TestReferenceContextBuild_RejectsUnreadableManifest(t *testing.T) {
	node, reserved, rows := referenceContextFixture(t)
	reserved.ContextManifestHash = "deadbeef"

	_, err := buildReferenceContext(node, reserved, rows, defaultReferenceContextOptions())
	require.Error(t, err)
	assert.ErrorIs(t, err, errReferenceContextManifestUnreadable)

	// It is NOT a §9.4 catalog rejection: a corrupt manifest is a 500-class
	// server error, never a 404 that would hide the corruption.
	_, isCatalog := ReferenceErrorFrom(err)
	assert.False(t, isCatalog)
}

func TestReferenceContextBuild_JSONShapeIsSnakeCase(t *testing.T) {
	node, reserved, rows := referenceContextFixture(t)

	result, err := buildReferenceContext(node, reserved, rows, defaultReferenceContextOptions())
	require.NoError(t, err)

	raw, err := json.Marshal(result)
	require.NoError(t, err)

	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &top))
	for _, key := range []string{"node_id", "tree_id", "parent_mode", "primary_source_id", "context"} {
		_, present := top[key]
		assert.Truef(t, present, "§9.3 envelope missing %q: %s", key, string(raw))
	}
	for _, camel := range []string{"nodeId", "treeId", "parentMode", "primarySourceId"} {
		_, present := top[camel]
		assert.Falsef(t, present, "camelCase key %q leaked onto the HTTP boundary", camel)
	}

	var context map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(top["context"], &context))
	for _, key := range []string{"sources", "is_synthetic_merge_point", "branch_span", "token_budget", "tokens_used", "manifest_hash"} {
		_, present := context[key]
		assert.Truef(t, present, "§9.3 context missing %q: %s", key, string(raw))
	}

	var sources []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(context["sources"], &sources))
	require.Len(t, sources, 3)
	for _, key := range []string{"source_label", "node_id", "content", "truncated"} {
		_, present := sources[0][key]
		assert.Truef(t, present, "§9.3 source missing %q", key)
	}
}

func TestReferenceContextOptions_MaxSourceTokensClamp(t *testing.T) {
	node, reserved, rows := referenceContextFixture(t)
	rows[0].Content = strings.Repeat("y", 40000) // ≈ 10,000 tokens
	reserved.ContextManifestHash = referenceManifestHash(node.TreeID, reserved.PrimarySourceID, []referenceSource{
		{ID: rows[0].SourceID, ContentHash: rows[0].ContentHash, SequenceNum: rows[0].SequenceNum},
		{ID: rows[1].SourceID, ContentHash: rows[1].ContentHash, SequenceNum: rows[1].SequenceNum},
		{ID: rows[2].SourceID, ContentHash: rows[2].ContentHash, SequenceNum: rows[2].SequenceNum},
	}, reserved.ContextTokenBudget)

	// An out-of-range value cannot widen the allowance past 2,048 tokens.
	opts := defaultReferenceContextOptions()
	opts.MaxSourceTokens = 1_000_000

	result, err := buildReferenceContext(node, reserved, rows, opts)
	require.NoError(t, err)

	require.NotNil(t, result.Context.Sources[0].Content)
	assert.True(t, result.Context.Sources[0].Truncated)
	assert.Less(t, estimateSourceTokens(*result.Context.Sources[0].Content), 3000,
		"retained text stays within the clamped allowance (±the marker)")
}

func TestReferenceTruncateContent_SmallAllowanceKeepsBothEnds(t *testing.T) {
	content := "0123456789abcdefghij" // 20 runes = 5 tokens
	got, truncated := referenceTruncateContent(content, 2, "R2")

	require.True(t, truncated)
	assert.Contains(t, got, "tokens omitted from source R2")
	assert.True(t, strings.HasPrefix(got, "0123"), "head preserved: %q", got)
	assert.True(t, strings.HasSuffix(got, "ghij"), "tail preserved: %q", got)

	// Content inside the allowance is returned verbatim.
	same, cut := referenceTruncateContent("short body", 2048, "R1")
	assert.False(t, cut)
	assert.Equal(t, "short body", same)
}
