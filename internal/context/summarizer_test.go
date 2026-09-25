// GAP-080 phase 3 — the deterministic local summarizer.
//
// The digest replaces the compiler's silent drop with a compact, auditable
// record of what the budget walk omitted. It must be a PURE function of its
// input: no model calls, no clock, no map iteration — two compiles of the
// same dropped tail produce byte-identical text (the phase-5a manifest hash
// depends on it).
package context

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func gap080P3Nodes() []OmittedNode {
	return []OmittedNode{
		{ID: gap080ID("11111111-1111-1111-1111-111111111111"), Author: gap080Author, Content: "Decided to use Postgres for the graph store."},
		{ID: gap080ID("22222222-2222-2222-2222-222222222222"), Author: gap080Author, Content: "second node	with a tab\nlater lines are not part of the first sentence"},
	}
}

func TestSummarizeOmittedNodes_Deterministic(t *testing.T) {
	nodes := gap080P3Nodes()
	est := NewTokenEstimator()
	text1, tokens1 := summarizeOmittedNodes(est, nodes)
	text2, tokens2 := summarizeOmittedNodes(est, nodes)
	if text1 != text2 || tokens1 != tokens2 {
		t.Fatalf("digest not deterministic:\n%q\nvs\n%q (%d/%d tokens)", text1, text2, tokens1, tokens2)
	}
	if tokens1 <= 0 {
		t.Fatalf("tokens = %d, want > 0", tokens1)
	}
	if tokens1 != est.Estimate(text1) {
		t.Errorf("tokens = %d, want Estimate(text) = %d", tokens1, est.Estimate(text1))
	}
}

func TestSummarizeOmittedNodes_Shape(t *testing.T) {
	nodes := gap080P3Nodes()
	text, tokens := summarizeOmittedNodes(NewTokenEstimator(), nodes)

	want := "--- summarized older messages (2) ---\n" +
		"[node 11111111-1111-1111-1111-111111111111 by aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa] Decided to use Postgres for the graph store.\n" +
		"[node 22222222-2222-2222-2222-222222222222 by aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa] second node with a tab"
	if text != want {
		t.Errorf("text =\n%q\nwant\n%q", text, want)
	}
	if tokens != NewTokenEstimator().Estimate(want) {
		t.Errorf("tokens = %d, want %d", tokens, NewTokenEstimator().Estimate(want))
	}
}

func TestSummarizeOmittedNodes_EmptyInput(t *testing.T) {
	text, tokens := summarizeOmittedNodes(NewTokenEstimator(), nil)
	if text != "" || tokens != 0 {
		t.Errorf("empty input produced %q/%d, want \"\"/0", text, tokens)
	}
}

// TestSummarizeOmittedNodes_OrderPreserved pins oldest→newest: the digest
// must read chronologically, so the caller's slice order is the output order.
func TestSummarizeOmittedNodes_OrderPreserved(t *testing.T) {
	nodes := gap080P3Nodes()
	text, _ := summarizeOmittedNodes(NewTokenEstimator(), nodes)
	iFirst := strings.Index(text, "11111111")
	iSecond := strings.Index(text, "22222222")
	if iFirst < 0 || iSecond < 0 || iFirst > iSecond {
		t.Errorf("digest not in oldest→newest order: %q", text)
	}
}

func TestFirstSentence_BoundaryWithin200(t *testing.T) {
	got := firstSentence("First short sentence. Second sentence follows here. More.")
	if got != "First short sentence." {
		t.Errorf("firstSentence = %q, want %q", got, "First short sentence.")
	}
}

func TestFirstSentence_HardCutWithoutBoundary(t *testing.T) {
	long := strings.Repeat("word ", 60) // 300 chars, no sentence terminator
	got := firstSentence(long)
	runes := []rune(got)
	if len(runes) > 160 || len(runes) == 0 {
		t.Errorf("len = %d, want 1..160 (hard cut)", len(runes))
	}
	if !strings.HasPrefix(long, got) {
		t.Errorf("cut %q is not a prefix of the input", got)
	}
	// The cut never dangles on whitespace.
	if strings.HasSuffix(got, " ") || strings.HasSuffix(got, "	") {
		t.Errorf("cut ends on whitespace: %q", got)
	}
}

func TestFirstSentence_BoundaryBeyond200FallsBackToHardCut(t *testing.T) {
	long := strings.Repeat("a", 220) + ". tail"
	got := firstSentence(long)
	runes := []rune(got)
	if len(runes) != 160 {
		t.Errorf("len = %d, want 160 (no boundary within the 200-rune window)", len(runes))
	}
}

func TestFirstSentence_EmptyAndWhitespace(t *testing.T) {
	for _, in := range []string{"", "   \n\t ", "\n\n\n"} {
		if got := firstSentence(in); got != "(empty)" {
			t.Errorf("firstSentence(%q) = %q, want \"(empty)\"", in, got)
		}
	}
}

func TestFirstSentence_LeadingBlankLinesSkipped(t *testing.T) {
	got := firstSentence("\n\n   Real opening line. Later text.")
	if got != "Real opening line." {
		t.Errorf("firstSentence = %q, want %q", got, "Real opening line.")
	}
}

func TestFirstSentence_UnicodeCountedByRunes(t *testing.T) {
	long := strings.Repeat("é", 180) // 180 runes, 360 bytes — no boundary
	got := firstSentence(long)
	if n := len([]rune(got)); n != 160 {
		t.Errorf("len runes = %d, want 160 (rune-safe cut, not bytes)", n)
	}
}

var _ = uuid.Nil // keep the uuid import anchored if helpers move
