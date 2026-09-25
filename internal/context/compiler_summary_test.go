// GAP-080 phase 3 — compiler wiring for the summary digest.
//
// The budget walk captures every node it drops; when the leftover budget can
// pay for the digest, the compiler appends it as the payload's FINAL section
// and records summaryText/summaryTokenCount/summarizedCount on the manifest.
// When it cannot (or pinned overage consumed the budget), it warns and the
// payload stays byte-identical to the pre-phase-3 compiler.
//
// The zero-omission cases here are the hard back-compat gate: a compile that
// drops nothing must produce exactly the pre-phase-3 payload — no summary
// fields, no extra section, identical manifest hash.
package context

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// gap080P3Chain builds a 4-node chain with long content so a small budget
// drops the older nodes: contents[0] is the OLDEST.
func gap080P3Chain(contents []string) []db.Node {
	ids := gap080IDs(len(contents))
	chain := make([]db.Node, 0, len(contents))
	for i, c := range contents {
		chain = append(chain, db.Node{ID: ids[i], AuthorID: gap080Author, Content: c})
	}
	return chain
}

func gap080P3Compile(t *testing.T, chain []db.Node, budget int) (*CompiledContext, error) {
	t.Helper()
	c := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	return c.Compile(context.Background(), CompileRequest{
		NodeID:       chain[len(chain)-1].ID,
		TokenBudget:  budget,
		MaxAncestors: 50,
		ResolveRefs:  false,
	})
}

// AC: the digest covers exactly the budget-dropped nodes, oldest→newest, and
// rides as the payload's final section with its tokens counted.
//
// Fixture arithmetic (4 chars/token): newest 200c → 67 tokens kept; the two
// 700-char older nodes cost 192 each and drop at budget 230 (leftover 163);
// their digest costs ~132 and fits the leftover.
func TestGAP080P3_SummaryDigestAppended(t *testing.T) {
	chain := gap080P3Chain([]string{
		strings.Repeat("Oldest node filler without a sentence boundary ", 14) + "end",
		strings.Repeat("Middle node filler without a sentence boundary ", 14) + "end",
		strings.Repeat("Newest node filler without a sentence boundary ", 4) + "end",
	})
	res, err := gap080P3Compile(t, chain, 230)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	if res.Manifest.SummarizedCount != 2 {
		t.Fatalf("SummarizedCount = %d, want 2 (OmittedCount = %d)", res.Manifest.SummarizedCount, res.Manifest.OmittedCount)
	}
	if res.Manifest.OmittedCount != 2 {
		t.Errorf("OmittedCount = %d, want 2", res.Manifest.OmittedCount)
	}
	if res.Manifest.SummaryText == "" {
		t.Fatal("SummaryText empty — digest was not built")
	}
	if !strings.HasPrefix(res.Manifest.SummaryText, "--- summarized older messages (2) ---") {
		t.Errorf("SummaryText missing header: %q", res.Manifest.SummaryText)
	}
	// Oldest→newest: the oldest node's line comes before the middle one.
	iOld := strings.Index(res.Manifest.SummaryText, chain[0].ID.String())
	iMid := strings.Index(res.Manifest.SummaryText, chain[1].ID.String())
	if iOld < 0 || iMid < 0 || iOld > iMid {
		t.Errorf("digest not oldest→newest: %q", res.Manifest.SummaryText)
	}
	// The digest is the payload's FINAL section.
	if !strings.HasSuffix(res.Content, res.Manifest.SummaryText) {
		t.Errorf("digest is not the final payload section:\n%q", res.Content)
	}
	// Its tokens are accounted: TokensUsed == full-payload estimate, and the
	// manifest fields agree with the estimator.
	if res.Manifest.SummaryTokenCount != NewTokenEstimator().Estimate(res.Manifest.SummaryText) {
		t.Errorf("SummaryTokenCount = %d, want %d",
			res.Manifest.SummaryTokenCount, NewTokenEstimator().Estimate(res.Manifest.SummaryText))
	}
	if res.Manifest.TokensUsed != NewTokenEstimator().Estimate(res.Content) {
		t.Errorf("TokensUsed = %d, want %d (payload estimate)",
			res.Manifest.TokensUsed, NewTokenEstimator().Estimate(res.Content))
	}
	if !strings.Contains(res.Content, strings.Repeat("Oldest node filler", 1)[:18]) || !strings.Contains(res.Content, "summarized older messages") {
		t.Errorf("digest lost the oldest node's first sentence:\n%.200q", res.Content)
	}
}

// AC: recompiling the same tree yields byte-identical summaryText AND an
// identical manifestHash (phase-5a determinism must survive phase 3).
func TestGAP080P3_RecompileDeterminism(t *testing.T) {
	chain := gap080P3Chain([]string{
		strings.Repeat("first node filler content ", 28),                  // 700c — dropped
		strings.Repeat("second node filler content ", 27),                 // 700c — dropped
		strings.Repeat("third node, the newest, kept filler content ", 6), // 300c — kept
	})
	res1, err := gap080P3Compile(t, chain, 250)
	if err != nil {
		t.Fatalf("compile 1: %v", err)
	}
	res2, err := gap080P3Compile(t, chain, 250)
	if err != nil {
		t.Fatalf("compile 2: %v", err)
	}
	if res1.Manifest.SummaryText == "" {
		t.Fatal("SummaryText empty — scenario dropped nothing; fix the fixture budget")
	}
	if res1.Manifest.SummaryText != res2.Manifest.SummaryText {
		t.Errorf("summaryText differs across recompiles:\n%q\nvs\n%q",
			res1.Manifest.SummaryText, res2.Manifest.SummaryText)
	}
	if res1.Manifest.ManifestHash == "" || res1.Manifest.ManifestHash != res2.Manifest.ManifestHash {
		t.Errorf("manifestHash differs or empty: %q vs %q",
			res1.Manifest.ManifestHash, res2.Manifest.ManifestHash)
	}
	if res1.Content != res2.Content {
		t.Errorf("payload differs across recompiles")
	}
}

// AC (hard back-compat gate): a compile that drops NOTHING carries no summary
// fields and a payload identical to the pre-phase-3 compiler.
func TestGAP080P3_ZeroOmissionByteIdentical(t *testing.T) {
	chain := gap080P3Chain([]string{"short one", "short two", "short three"})
	res, err := gap080P3Compile(t, chain, 500)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if res.Manifest.OmittedCount != 0 {
		t.Fatalf("OmittedCount = %d, want 0 — fixture must drop nothing", res.Manifest.OmittedCount)
	}
	if res.Manifest.SummaryText != "" || res.Manifest.SummaryTokenCount != 0 || res.Manifest.SummarizedCount != 0 {
		t.Errorf("summary fields = %q/%d/%d, want zero on a zero-omission compile",
			res.Manifest.SummaryText, res.Manifest.SummaryTokenCount, res.Manifest.SummarizedCount)
	}
	if strings.Contains(res.Content, "summarized older messages") {
		t.Errorf("payload gained a summary section on a zero-omission compile:\n%q", res.Content)
	}
	// The ancestry renders newest-first, so the payload's tail is the OLDEST
	// kept node's content.
	if !strings.HasSuffix(res.Content, "short one") {
		t.Errorf("payload tail changed: %q", res.Content)
	}
	if res.Manifest.ManifestHash == "" {
		t.Error("manifestHash empty")
	}
}

// AC: depth-only omission (OmittedReason "depth") produces NO digest — depth
// dropping is a separate concept and stays silent as before.
func TestGAP080P3_DepthOnlyOmissionNoDigest(t *testing.T) {
	ids := gap080IDs(5)
	chain := make([]db.Node, 0, 5)
	for i := 0; i < 5; i++ {
		chain = append(chain, db.Node{ID: ids[i], AuthorID: gap080Author, Content: strings.Repeat("x", 40)})
	}
	c := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	res, err := c.Compile(context.Background(), CompileRequest{
		NodeID: ids[4], TokenBudget: 10000, MaxAncestors: 2, ResolveRefs: false,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if res.Manifest.OmittedReason != "depth" || res.Manifest.OmittedCount != 3 {
		t.Fatalf("OmittedReason/Count = %q/%d, want depth/3", res.Manifest.OmittedReason, res.Manifest.OmittedCount)
	}
	if res.Manifest.SummaryText != "" || res.Manifest.SummarizedCount != 0 {
		t.Errorf("summary fields = %q/%d, want zero for a depth-only omission",
			res.Manifest.SummaryText, res.Manifest.SummarizedCount)
	}
	if strings.Contains(res.Content, "summarized older messages") {
		t.Errorf("depth-only compile gained a summary section:\n%q", res.Content)
	}
}

// AC: when the leftover budget cannot pay for the digest, the compiler warns
// and sets no summary fields (no budget overage is introduced by phase 3).
//
// Fixture: newest 300c kept at budget 200 (92 tokens, leftover 108); the two
// 700c older nodes (192 each) drop; their ~132-token digest does not fit 108.
func TestGAP080P3_NoBudgetWarningPath(t *testing.T) {
	chain := gap080P3Chain([]string{
		strings.Repeat("old filler text ", 44),
		strings.Repeat("mid filler text ", 44),
		strings.Repeat("new filler text ", 19),
	})
	res, err := gap080P3Compile(t, chain, 200)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if res.Manifest.SummarizedCount != 0 || res.Manifest.SummaryText != "" {
		t.Errorf("summary fields = %q/%d, want zero on the no-budget path",
			res.Manifest.SummaryText, res.Manifest.SummarizedCount)
	}
	found := false
	for _, w := range res.Manifest.Warnings {
		if w == "summary omitted: no budget for digest" {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings %v missing the digest-omitted warning", res.Manifest.Warnings)
	}
	if strings.Contains(res.Content, "summarized older messages") {
		t.Errorf("payload gained a digest it could not afford:\n%q", res.Content)
	}
}

// AC (§7 interplay): when the budget is too small for even one node, the
// forced newest node is KEPT and the genuinely-dropped oldest is the only
// digest candidate — but the walk's leftover can no longer pay for a digest
// (the forced keep never deducted), so the observed contract is the
// digest-omitted warning with zero summary fields.
func TestGAP080P3_ForcedNewestNodeNotSummarized(t *testing.T) {
	chain := gap080P3Chain([]string{"oldest long node content", "newest node content"})
	// Budget 2 forces the newest node in with a warning; the oldest is dropped.
	res, err := gap080P3Compile(t, chain, 2)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if res.Manifest.OmittedCount != 1 {
		t.Errorf("OmittedCount = %d, want 1", res.Manifest.OmittedCount)
	}
	if res.Manifest.SummarizedCount != 0 || res.Manifest.SummaryText != "" {
		t.Errorf("summary fields = %q/%d, want zero (leftover cannot pay the digest)",
			res.Manifest.SummaryText, res.Manifest.SummarizedCount)
	}
	found := false
	for _, w := range res.Manifest.Warnings {
		if w == "summary omitted: no budget for digest" {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings %v missing the digest-omitted warning", res.Manifest.Warnings)
	}
	// The kept node must never appear in a digest.
	if strings.Contains(res.Manifest.SummaryText, chain[1].ID.String()) {
		t.Errorf("digest covers the KEPT newest node: %q", res.Manifest.SummaryText)
	}
}

// AC: pinned nodes are exempt from the walk (phase 1); a pinned node must
// never land in the digest.
func TestGAP080P3_PinnedNodeNeverSummarized(t *testing.T) {
	ids := gap080IDs(2)
	pinnedMeta := []byte(`{"pinned": true}`)
	chain := []db.Node{
		{ID: ids[0], AuthorID: gap080Author, Content: strings.Repeat("pinned old ", 20), Metadata: pinnedMeta},
		{ID: ids[1], AuthorID: gap080Author, Content: strings.Repeat("new ", 40)},
	}
	res, err := gap080P3Compile(t, chain, 100)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if res.Manifest.PinnedCount != 1 {
		t.Fatalf("PinnedCount = %d, want 1", res.Manifest.PinnedCount)
	}
	if strings.Contains(res.Manifest.SummaryText, ids[0].String()) {
		t.Errorf("digest covers a PINNED node: %q", res.Manifest.SummaryText)
	}
	// The pinned node was kept, so nothing was dropped by budget.
	if res.Manifest.SummarizedCount != 0 || res.Manifest.SummaryText != "" {
		t.Errorf("summary fields = %q/%d, want zero (nothing was budget-dropped)",
			res.Manifest.SummaryText, res.Manifest.SummarizedCount)
	}
}

var _ = uuid.Nil
