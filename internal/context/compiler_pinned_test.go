// GAP-080 phase 1 — pinned messages in the context compiler.
//
// A node is PINNED iff its metadata JSON is an OBJECT with `"pinned": true`
// (exact boolean true). A pinned ancestry item is never dropped by the
// token-budget walk: it may push the accounted tokens past the budget (the
// overage is reported in the manifest), and it is never counted as omitted.
//
// With zero pins the walk must be byte-identical to the pre-GAP-080
// behaviour — the parity tests below pin the exact pre-fix values, captured
// from the pre-fix revision (d60a76e) before the compiler was touched.
package context

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// --- helpers ---------------------------------------------------------------

func gap080ID(s string) uuid.UUID { return uuid.MustParse(s) }

// gap080ChainStub builds a NodeReader stub for a chain given oldest→newest.
// GetAncestors returns [self, parent, ..., root] (newest first), matching the
// real repo contract; GetByID resolves the newest node.
func gap080ChainStub(oldestToNewest []db.Node) *stubNodeReader {
	anc := make([]db.Node, 0, len(oldestToNewest))
	nodes := map[uuid.UUID]*db.Node{}
	for i := len(oldestToNewest) - 1; i >= 0; i-- {
		cp := oldestToNewest[i]
		nodes[cp.ID] = &cp
		anc = append(anc, cp)
	}
	return &stubNodeReader{
		nodes:    nodes,
		getAncFn: func(context.Context, uuid.UUID) ([]db.Node, error) { return anc, nil },
	}
}

// gap080AncestryIDs returns the manifest ancestry ids in walk order.
func gap080AncestryIDs(items []ManifestItem) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func gap080ContainsWarn(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// gap080Author / gap080IDs are the fixed identities used by the exact-value
// assertions (fixed ids keep lengths and hashes stable across runs).
var gap080Author = gap080ID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

func gap080IDs(n int) []uuid.UUID {
	seed := []uuid.UUID{
		gap080ID("11111111-1111-1111-1111-111111111111"),
		gap080ID("22222222-2222-2222-2222-222222222222"),
		gap080ID("33333333-3333-3333-3333-333333333333"),
		gap080ID("44444444-4444-4444-4444-444444444444"),
		gap080ID("55555555-5555-5555-5555-555555555555"),
		gap080ID("66666666-6666-6666-6666-666666666666"),
		gap080ID("77777777-7777-7777-7777-777777777777"),
		gap080ID("88888888-8888-8888-8888-888888888888"),
		gap080ID("99999999-9999-9999-9999-999999999999"),
		gap080ID("00000000-0000-0000-0000-000000000000"),
	}
	return seed[:n]
}

// --- AC1: a pinned node at the OLDEST end survives an overflowing walk ------

func TestGAP080_PinnedOldestSurvivesBudget(t *testing.T) {
	ids := gap080IDs(6)
	// Each section renders to 89 prefix chars + 40 content chars = 129 chars
	// = 33 tokens under the 4-chars-per-token estimator.
	chain := make([]db.Node, 0, 6)
	for i := 0; i < 6; i++ {
		chain = append(chain, db.Node{
			ID:       ids[i],
			AuthorID: gap080Author,
			Content:  strings.Repeat(string(rune('a'+i)), 40),
		})
	}
	// The OLDEST node (index 0) is pinned; its newer neighbours are not.
	chain[0].Metadata = []byte(`{"pinned": true}`)

	c := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	res, err := c.Compile(context.Background(), CompileRequest{
		NodeID: ids[5], TokenBudget: 100, MaxAncestors: 50, ResolveRefs: false,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Kept: the three newest that fit (66666666, 55555555, 44444444) PLUS the
	// pinned oldest (11111111). Omitted (unpinned, do not fit): 33333333,
	// 22222222 — exactly the same set the pre-fix walk dropped, minus the pin.
	wantOrder := []uuid.UUID{ids[5], ids[4], ids[3], ids[0]}
	if got := gap080AncestryIDs(res.Manifest.Ancestry); !equalIDs(got, wantOrder) {
		t.Errorf("ancestry ids = %v, want %v (newest→oldest, pinned oldest kept)", got, wantOrder)
	}

	// Every kept id is in Content; the omitted ids are not.
	for _, id := range wantOrder {
		if !strings.Contains(res.Content, id.String()) {
			t.Errorf("Content missing kept node %s", id)
		}
	}
	for _, id := range []uuid.UUID{ids[2], ids[1]} {
		if strings.Contains(res.Content, id.String()) {
			t.Errorf("Content contains omitted node %s", id)
		}
	}

	// Manifest says WHICH item was pinned, and how many.
	if len(res.Manifest.Ancestry) != 4 {
		t.Fatalf("ancestry len = %d, want 4", len(res.Manifest.Ancestry))
	}
	for i, item := range res.Manifest.Ancestry {
		wantPinned := i == 3
		if item.Pinned != wantPinned {
			t.Errorf("ancestry[%d].Pinned = %v, want %v (id %s)", i, item.Pinned, wantPinned, item.ID)
		}
	}
	if res.Manifest.PinnedCount != 1 {
		t.Errorf("PinnedCount = %d, want 1", res.Manifest.PinnedCount)
	}

	// Omitted accounting counts ONLY the unpinned items.
	if res.Manifest.OmittedCount != 2 {
		t.Errorf("OmittedCount = %d, want 2 (pinned items are never omitted)", res.Manifest.OmittedCount)
	}
	if res.Manifest.OmittedReason != "budget" {
		t.Errorf("OmittedReason = %q, want %q", res.Manifest.OmittedReason, "budget")
	}
	if len(res.Manifest.TruncationMarkers) != 1 || res.Manifest.TruncationMarkers[0] != "2 messages omitted" {
		t.Errorf("TruncationMarkers = %v, want [\"2 messages omitted\"]", res.Manifest.TruncationMarkers)
	}

	// The pinned node pushed the accounting 32 tokens past the budget: the
	// manifest names the exact overage.
	wantWarn := "pinned nodes exceed the token budget by 32 tokens"
	if !gap080ContainsWarn(res.Manifest.Warnings, wantWarn) {
		t.Errorf("Warnings %v missing %q", res.Manifest.Warnings, wantWarn)
	}
	if res.Manifest.TokensUsed <= res.Manifest.TokenBudget {
		t.Errorf("TokensUsed = %d, want > budget %d (pinned node kept, never dropped to fit)",
			res.Manifest.TokensUsed, res.Manifest.TokenBudget)
	}
}

// --- AC2: zero pins ⇒ byte-identical pre-fix behaviour ----------------------

// TestGAP080_NoPinsParity pins the EXACT pre-fix values for three shapes,
// captured from the pre-fix revision (d60a76e) before the compiler changed.
func TestGAP080_NoPinsParity(t *testing.T) {
	t.Run("tiny chain exact content", func(t *testing.T) {
		ids := gap080IDs(4)
		chain := []db.Node{
			{ID: ids[0], AuthorID: gap080Author, Content: "m1"},
			{ID: ids[1], AuthorID: gap080Author, Content: "m2"},
			{ID: ids[2], AuthorID: gap080Author, Content: "m3"},
			{ID: ids[3], AuthorID: gap080Author, Content: "m4"},
		}
		c := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
		res, err := c.Compile(context.Background(), CompileRequest{
			NodeID: ids[3], TokenBudget: 80, MaxAncestors: 50, ResolveRefs: false,
		})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		want := "--- node 44444444-4444-4444-4444-444444444444 (aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa) ---\nm4" +
			"\n\n--- node 33333333-3333-3333-3333-333333333333 (aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa) ---\nm3" +
			"\n\n--- node 22222222-2222-2222-2222-222222222222 (aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa) ---\nm2"
		if res.Content != want {
			t.Errorf("Content = %q\nwant %q", res.Content, want)
		}
		if res.Manifest.OmittedCount != 1 {
			t.Errorf("OmittedCount = %d, want 1", res.Manifest.OmittedCount)
		}
		if res.Manifest.OmittedReason != "budget" {
			t.Errorf("OmittedReason = %q, want budget", res.Manifest.OmittedReason)
		}
		if len(res.Manifest.TruncationMarkers) != 1 || res.Manifest.TruncationMarkers[0] != "1 messages omitted" {
			t.Errorf("TruncationMarkers = %v, want [\"1 messages omitted\"]", res.Manifest.TruncationMarkers)
		}
		// GAP-080 phase 3: the single dropped node's digest (31 tokens) does
		// not fit the 10-token leftover, so the compiler warns that the
		// digest was omitted instead of summarizing. The exact-bytes payload
		// assertion above is unchanged — no summary section was appended.
		if len(res.Manifest.Warnings) != 1 || res.Manifest.Warnings[0] != "summary omitted: no budget for digest" {
			t.Errorf("Warnings = %v, want [\"summary omitted: no budget for digest\"]", res.Manifest.Warnings)
		}
		if res.Manifest.SummaryText != "" || res.Manifest.SummaryTokenCount != 0 || res.Manifest.SummarizedCount != 0 {
			t.Errorf("summary fields = %q/%d/%d, want zero (digest did not fit)",
				res.Manifest.SummaryText, res.Manifest.SummaryTokenCount, res.Manifest.SummarizedCount)
		}
		if res.Manifest.TokensUsed != 70 || len(res.Manifest.Ancestry) != 3 {
			t.Errorf("TokensUsed/Ancestry = %d/%d, want 70/3", res.Manifest.TokensUsed, len(res.Manifest.Ancestry))
		}
	})

	t.Run("spec scenario 2 shape 10 nodes budget 500", func(t *testing.T) {
		ids := gap080IDs(10)
		chain := make([]db.Node, 0, 10)
		for i := 0; i < 10; i++ {
			chain = append(chain, db.Node{ID: ids[i], AuthorID: gap080Author, Content: strings.Repeat("x", 200)})
		}
		c := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
		res, err := c.Compile(context.Background(), CompileRequest{
			NodeID: ids[9], TokenBudget: 500, MaxAncestors: 50, ResolveRefs: false,
		})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if res.Manifest.OmittedCount != 4 {
			t.Errorf("OmittedCount = %d, want 4", res.Manifest.OmittedCount)
		}
		if res.Manifest.OmittedReason != "budget" {
			t.Errorf("OmittedReason = %q, want budget", res.Manifest.OmittedReason)
		}
		if len(res.Manifest.TruncationMarkers) != 1 || res.Manifest.TruncationMarkers[0] != "4 messages omitted" {
			t.Errorf("TruncationMarkers = %v, want [\"4 messages omitted\"]", res.Manifest.TruncationMarkers)
		}
		if len(res.Manifest.Warnings) != 1 || res.Manifest.Warnings[0] != "summary omitted: no budget for digest" {
			t.Errorf("Warnings = %v, want [\"summary omitted: no budget for digest\"] (GAP-080 phase 3: 4-node digest does not fit the 64-token leftover)", res.Manifest.Warnings)
		}
		if res.Manifest.SummaryText != "" || res.Manifest.SummaryTokenCount != 0 || res.Manifest.SummarizedCount != 0 {
			t.Errorf("summary fields = %q/%d/%d, want zero (digest did not fit)",
				res.Manifest.SummaryText, res.Manifest.SummaryTokenCount, res.Manifest.SummarizedCount)
		}
		// Content is 6 sections of 289 chars + 5 blank-line joins = 1744 chars;
		// the pre-fix SHA-256 pins the bytes exactly (the literal is 1.7 KB).
		sum := sha256.Sum256([]byte(res.Content))
		const wantSHA = "31cc18bc366f6e01320a25f88533d47f9fe038b73319c4547bbc7fce91f40c81"
		if got := hex.EncodeToString(sum[:]); got != wantSHA {
			t.Errorf("Content SHA-256 = %s, want %s (len=%d)", got, wantSHA, len(res.Content))
		}
		if len(res.Content) != 1744 {
			t.Errorf("Content len = %d, want 1744", len(res.Content))
		}
		if n := strings.Count(res.Content, "--- node "); n != 6 {
			t.Errorf("sections = %d, want 6", n)
		}
		// The 6 newest survive, the 4 oldest are gone — in that order.
		wantOrder := []uuid.UUID{ids[9], ids[8], ids[7], ids[6], ids[5], ids[4]}
		if got := gap080AncestryIDs(res.Manifest.Ancestry); !equalIDs(got, wantOrder) {
			t.Errorf("ancestry ids = %v, want %v", got, wantOrder)
		}
		if res.Manifest.TokensUsed != 436 {
			t.Errorf("TokensUsed = %d, want 436", res.Manifest.TokensUsed)
		}
	})

	t.Run("budget too small for one node special case", func(t *testing.T) {
		ids := gap080IDs(1)
		chain := []db.Node{{ID: ids[0], AuthorID: gap080Author, Content: "hello"}}
		c := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
		res, err := c.Compile(context.Background(), CompileRequest{
			NodeID: ids[0], TokenBudget: 2, ResolveRefs: false,
		})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		want := "--- node 11111111-1111-1111-1111-111111111111 (aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa) ---\nhello"
		if res.Content != want {
			t.Errorf("Content = %q, want %q", res.Content, want)
		}
		// The single newest node is kept even though it does not fit, and it is
		// NOT counted as omitted: a node that lands in manifest.Ancestry is
		// never part of manifest.OmittedCount (DF-HERMES-CANOPY-21). Nothing
		// was dropped here, so the whole omission accounting is empty.
		if len(res.Manifest.Ancestry) != 1 || res.Manifest.Ancestry[0].ID != ids[0] {
			t.Fatalf("ancestry = %v, want exactly the kept node %s", gap080AncestryIDs(res.Manifest.Ancestry), ids[0])
		}
		if res.Manifest.OmittedCount != 0 {
			t.Errorf("OmittedCount = %d, want 0 (the kept node is not omitted)", res.Manifest.OmittedCount)
		}
		if res.Manifest.OmittedReason != "" {
			t.Errorf("OmittedReason = %q, want \"\" (nothing was dropped)", res.Manifest.OmittedReason)
		}
		if len(res.Manifest.TruncationMarkers) != 0 {
			t.Errorf("TruncationMarkers = %v, want none", res.Manifest.TruncationMarkers)
		}
		wantWarnings := []string{
			"budget too small for single node",
			"tokens used (24) exceeds budget (2)",
		}
		if len(res.Manifest.Warnings) != len(wantWarnings) {
			t.Fatalf("Warnings = %v, want exactly %v", res.Manifest.Warnings, wantWarnings)
		}
		for i, w := range wantWarnings {
			if res.Manifest.Warnings[i] != w {
				t.Errorf("Warnings[%d] = %q, want %q", i, res.Manifest.Warnings[i], w)
			}
		}
		if res.Manifest.TokensUsed != 24 {
			t.Errorf("TokensUsed = %d, want 24", res.Manifest.TokensUsed)
		}
	})

	t.Run("metadata that is not a pin never changes the payload", func(t *testing.T) {
		ids := gap080IDs(4)
		plain := make([]db.Node, 0, 4)
		meta := make([]db.Node, 0, 4)
		variants := [][]byte{
			nil, []byte(""), []byte("{}"), []byte("null"),
		}
		for i := 0; i < 4; i++ {
			plain = append(plain, db.Node{ID: ids[i], AuthorID: gap080Author, Content: "m" + string(rune('1'+i))})
			n := db.Node{ID: ids[i], AuthorID: gap080Author, Content: "m" + string(rune('1'+i)), Metadata: variants[i]}
			meta = append(meta, n)
		}
		compile := func(chain []db.Node) *CompiledContext {
			t.Helper()
			c := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
			res, err := c.Compile(context.Background(), CompileRequest{
				NodeID: ids[3], TokenBudget: 80, MaxAncestors: 50, ResolveRefs: false,
			})
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			return res
		}
		a, b := compile(plain), compile(meta)
		if a.Content != b.Content {
			t.Errorf("non-pin metadata changed Content:\n%q\nvs\n%q", a.Content, b.Content)
		}
		if a.Manifest.OmittedCount != b.Manifest.OmittedCount || b.Manifest.PinnedCount != 0 {
			t.Errorf("non-pin metadata changed omitted/pinned accounting: %d/%d pinned=%d",
				a.Manifest.OmittedCount, b.Manifest.OmittedCount, b.Manifest.PinnedCount)
		}
	})
}

// --- AC3: pinned content may exceed the budget — all pins are kept ---------

func TestGAP080_PinnedOverageKeepsAllPinned(t *testing.T) {
	ids := gap080IDs(3)
	chain := []db.Node{
		{ID: ids[0], AuthorID: gap080Author, Content: strings.Repeat("a", 40), Metadata: []byte(`{"pinned": true}`)},
		{ID: ids[1], AuthorID: gap080Author, Content: strings.Repeat("b", 40), Metadata: []byte(`{"pinned":true,"multi_reference":{"v":1}}`)},
		{ID: ids[2], AuthorID: gap080Author, Content: strings.Repeat("c", 40)},
	}
	// Budget 30 < one section (33 tokens): the newest unpinned node cannot
	// fit, but BOTH pinned nodes are kept anyway.
	c := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	res, err := c.Compile(context.Background(), CompileRequest{
		NodeID: ids[2], TokenBudget: 30, MaxAncestors: 50, ResolveRefs: false,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	wantOrder := []uuid.UUID{ids[1], ids[0]}
	if got := gap080AncestryIDs(res.Manifest.Ancestry); !equalIDs(got, wantOrder) {
		t.Fatalf("ancestry ids = %v, want %v (all pinned kept, newest→oldest)", got, wantOrder)
	}
	for i, item := range res.Manifest.Ancestry {
		if !item.Pinned {
			t.Errorf("ancestry[%d].Pinned = false, want true", i)
		}
		if !strings.Contains(res.Content, item.ID.String()) {
			t.Errorf("Content missing pinned node %s", item.ID)
		}
	}
	if res.Manifest.PinnedCount != 2 {
		t.Errorf("PinnedCount = %d, want 2", res.Manifest.PinnedCount)
	}
	// Only the unpinned newest node is omitted.
	if res.Manifest.OmittedCount != 1 {
		t.Errorf("OmittedCount = %d, want 1 (pinned nodes are never counted as omitted)", res.Manifest.OmittedCount)
	}
	// SPEC-IMPL-GAP-001 §7 "budget smaller than one node" — pinned exception
	// (amended 2026-09-18, DF-HERMES-CANOPY-20): with older PINNED nodes kept,
	// the unpinned newest node is NOT forced in; Content is already non-empty.
	// Do not relax this assertion to satisfy a literal reading of the clause.
	if strings.Contains(res.Content, ids[2].String()) {
		t.Error("Content contains the unpinned node that could not fit")
	}
	// TokensUsed > TokenBudget, and the exact overage is reported.
	if res.Manifest.TokensUsed <= res.Manifest.TokenBudget {
		t.Errorf("TokensUsed = %d, want > TokenBudget %d", res.Manifest.TokensUsed, res.Manifest.TokenBudget)
	}
	wantWarn := "pinned nodes exceed the token budget by 36 tokens"
	if !gap080ContainsWarn(res.Manifest.Warnings, wantWarn) {
		t.Errorf("Warnings %v missing %q", res.Manifest.Warnings, wantWarn)
	}
}

// --- AC4: metadata tolerance ----------------------------------------------

func TestGAP080_MetadataTolerance(t *testing.T) {
	cases := []struct {
		name string
		meta []byte
	}{
		{"nil", nil},
		{"empty", []byte("")},
		{"empty object", []byte("{}")},
		{"null", []byte("null")},
		{"array", []byte("[1,2]")},
		{"malformed", []byte("{")},
		{"non-boolean string", []byte(`{"pinned":"yes"}`)},
		{"explicit false", []byte(`{"pinned":false}`)},
		{"number", []byte(`{"pinned":1}`)},
		{"null value", []byte(`{"pinned":null}`)},
		{"nested object", []byte(`{"pinned":{"on":true}}`)},
		{"other keys only", []byte(`{"multi_reference":{"v":1}}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids := gap080IDs(3)
			chain := make([]db.Node, 0, 3)
			for i := 0; i < 3; i++ {
				// Every item is marked with the same variant; a pin anywhere
				// would show up as a kept item beyond the budget.
				chain = append(chain, db.Node{
					ID: ids[i], AuthorID: gap080Author,
					Content:  strings.Repeat("q", 40),
					Metadata: tc.meta,
				})
			}
			c := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
			res, err := c.Compile(context.Background(), CompileRequest{
				NodeID: ids[2], TokenBudget: 40, MaxAncestors: 50, ResolveRefs: false,
			})
			if err != nil {
				t.Fatalf("compile with metadata %q: %v", tc.meta, err)
			}
			if res.Manifest.PinnedCount != 0 {
				t.Errorf("PinnedCount = %d, want 0 for metadata %q", res.Manifest.PinnedCount, tc.meta)
			}
			for i, item := range res.Manifest.Ancestry {
				if item.Pinned {
					t.Errorf("ancestry[%d].Pinned = true for metadata %q", i, tc.meta)
				}
			}
			// Budget 40 keeps exactly one item: no pin exemption happened.
			if len(res.Manifest.Ancestry) != 1 {
				t.Errorf("ancestry len = %d, want 1 (no pin exemption for %q)", len(res.Manifest.Ancestry), tc.meta)
			}
			if res.Manifest.OmittedCount != 2 {
				t.Errorf("OmittedCount = %d, want 2 for metadata %q", res.Manifest.OmittedCount, tc.meta)
			}
			wantWarn := "pinned nodes exceed the token budget"
			if gap080ContainsWarn(res.Manifest.Warnings, wantWarn) {
				t.Errorf("Warnings %v reported a pinned overage for metadata %q", res.Manifest.Warnings, tc.meta)
			}
		})
	}
}

// TestGAP080_IsPinned pins the predicate directly.
func TestGAP080_IsPinned(t *testing.T) {
	cases := []struct {
		meta []byte
		want bool
	}{
		{nil, false},
		{[]byte(""), false},
		{[]byte("{}"), false},
		{[]byte("null"), false},
		{[]byte("[1,2]"), false},
		{[]byte("{"), false},
		{[]byte(`{"pinned":false}`), false},
		{[]byte(`{"pinned":"yes"}`), false},
		{[]byte(`{"pinned":1}`), false},
		{[]byte(`{"pinned":null}`), false},
		{[]byte(`{"pinned":{}}`), false},
		{[]byte(`{"other":true}`), false},
		{[]byte(`{"pinned":true}`), true},
		{[]byte(" {\"pinned\" : true } "), true},
		{[]byte(`{"pinned":true,"multi_reference":{"v":1}}`), true},
		{[]byte("{\n  \"pinned\": true\n}"), true},
	}
	for _, tc := range cases {
		if got := isPinned(tc.meta); got != tc.want {
			t.Errorf("isPinned(%q) = %v, want %v", tc.meta, got, tc.want)
		}
	}
}

// TestGAP080_ManifestJSONUnchangedWithoutPins proves the two new manifest
// fields are omitted from an unpinned payload's JSON (omitempty), so existing
// consumers see byte-identical manifests.
func TestGAP080_ManifestJSONUnchangedWithoutPins(t *testing.T) {
	ids := gap080IDs(2)
	chain := []db.Node{
		{ID: ids[0], AuthorID: gap080Author, Content: "m1", Metadata: []byte(`{"multi_reference":{"v":1}}`)},
		{ID: ids[1], AuthorID: gap080Author, Content: "m2"},
	}
	c := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	res, err := c.Compile(context.Background(), CompileRequest{
		NodeID: ids[1], TokenBudget: 10000, MaxAncestors: 50, ResolveRefs: false,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	raw, err := json.Marshal(res.Manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if strings.Contains(string(raw), `"pinned"`) || strings.Contains(string(raw), "pinnedCount") {
		t.Errorf("unpinned manifest JSON leaks pin fields: %s", raw)
	}

	// The pinned case DOES carry them.
	chain[0].Metadata = []byte(`{"pinned":true}`)
	c2 := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	res2, err := c2.Compile(context.Background(), CompileRequest{
		NodeID: ids[1], TokenBudget: 10000, MaxAncestors: 50, ResolveRefs: false,
	})
	if err != nil {
		t.Fatalf("compile pinned: %v", err)
	}
	raw2, err := json.Marshal(res2.Manifest)
	if err != nil {
		t.Fatalf("marshal pinned manifest: %v", err)
	}
	if !strings.Contains(string(raw2), `"pinned":true`) {
		t.Errorf("pinned manifest JSON missing item pin marker: %s", raw2)
	}
	if !strings.Contains(string(raw2), `"pinnedCount":1`) {
		t.Errorf("pinned manifest JSON missing pinnedCount: %s", raw2)
	}
}

// equalIDs compares two id slices element-wise.
func equalIDs(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
