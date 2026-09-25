package context

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// --- GAP-080 phase 3: deterministic local summarizer ------------------------

// OmittedNode is one budget-dropped ancestry node handed to the summarizer.
type OmittedNode struct {
	ID      uuid.UUID
	Author  uuid.UUID
	Content string
}

// Budget note (GAP-080 phase 3): the digest carries NO separate reserve —
// it competes for the walk's leftover budget directly, so a compile omits
// it only when the leftover cannot pay for it (including pinned overage).

// summarizeOmittedNodes builds the deterministic digest for the oldest nodes
// the budget walk dropped. Same input → byte-identical output (SPEC-TM-004
// determinism; the phase-5a manifest hash must stay stable across recompiles
// of the same tree), so there is no model call, no clock, no map iteration —
// the output is a pure function of the input records in the order given.
//
// Shape (one line per node, oldest→newest):
//
//	--- summarized older messages (N) ---
//	[node <id> by <author>] <first sentence>
//
// Depth-omitted nodes are NOT summarized here — depth omission is a separate
// concept (OmittedReason "depth") and only the budget walk's drops are digested.
func summarizeOmittedNodes(est TokenEstimator, nodes []OmittedNode) (text string, tokens int) {
	if len(nodes) == 0 {
		return "", 0
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- summarized older messages (%d) ---", len(nodes))
	for _, n := range nodes {
		fmt.Fprintf(&b, "\n[node %s by %s] %s", n.ID, n.Author, firstSentence(n.Content))
	}
	text = b.String()
	return text, est.Estimate(text)
}

// firstSentence extracts the node content's first non-empty line, collapses
// whitespace runs to single spaces, and trims it to a sentence boundary when
// one falls within the first 200 runes; otherwise a hard 160-rune cut.
// Empty/whitespace-only content renders as "(empty)".
func firstSentence(content string) string {
	var line string
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) != "" {
			line = l
			break
		}
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "(empty)"
	}
	// Collapse every whitespace run (spaces, tabs) to a single space.
	line = strings.Join(strings.Fields(line), " ")

	runes := []rune(line)
	if len(runes) > 200 {
		runes = runes[:200]
	}

	cut := -1
	for i, r := range runes {
		if r == '.' || r == '!' || r == '?' {
			cut = i + 1
			break
		}
	}
	if cut > 0 {
		return strings.TrimSpace(string(runes[:cut]))
	}
	if len(runes) > 160 {
		return strings.TrimSpace(string(runes[:160]))
	}
	return string(runes)
}
