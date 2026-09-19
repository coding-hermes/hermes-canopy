// DF-HERMES-CANOPY-29 — the ANY-TERM match mode's sanitizer, unit-tested
// without a database (the repo's SQL branch is covered separately against
// real PostgreSQL in topic_search_any_term_pg_test.go).
//
// The property under test is the INJECTION boundary: an ANY-TERM query is
// fed to `to_tsquery`, which INTERPRETS operators, so the operand string must
// be built from tokens that cannot express one. Quoting is not a defence —
// on PG 16.14 `to_tsquery('english', quote_literal('a | b'))` yields the
// lexeme 'b', i.e. the `|` was still parsed as the OR operator (probed live).
package db

import (
	"regexp"
	"strings"
	"testing"
)

// tsQueryOperandShape matches a string built only from `[a-z0-9]`-style word
// tokens joined by the OR operator, which is the ONLY shape this package is
// allowed to hand to to_tsquery.
var tsQueryOperandShape = regexp.MustCompile(`^[^|&!()<>:*'"\\\s]+( \| [^|&!()<>:*'"\\\s]+)*$`)

func TestBuildAnyTermQuery_SanitizesOperatorsOutOfTheInput(t *testing.T) {
	// Every hostile spelling must collapse to the same operand string as its
	// plain-words equivalent: the operator characters act as SEPARATORS, they
	// never survive into the tsquery text.
	want := "zebra | runbook"
	cases := map[string]string{
		"plain words":        "zebra runbook",
		"OR operator":        "zebra | runbook",
		"AND operator":       "zebra & runbook",
		"negation":           "zebra !runbook",
		"phrase operator":    "zebra <-> runbook",
		"prefix operator":    "zebra:* runbook",
		"parentheses":        "(zebra) (runbook)",
		"single quotes":      "'zebra' 'runbook'",
		"doubled quotes":     "'zebra'' | ''runbook'",
		"backslash":          `zebra\ runbook`,
		"punctuation":        "zebra,;:()[]{}|&!<>*\\'\" - runbook",
		"duplicate words":    "zebra zebra runbook",
		"mixed case":         "ZEBRA RuNbOoK",
		"unicode separators": "zebra\u00a0|\u2028runbook",
	}
	for name, in := range cases {
		got := buildAnyTermQuery(in, maxAnyTermLexemes)
		if got != want {
			t.Errorf("%s: buildAnyTermQuery(%q) = %q, want %q", name, in, got, want)
		}
		if !tsQueryOperandShape.MatchString(got) {
			t.Errorf("%s: %q is not a canonical operand string", name, got)
		}
	}
}

func TestBuildAnyTermQuery_HostileInputsNeverCarryOperators(t *testing.T) {
	hostile := []string{
		`x')) | (('y`,
		`a | b & c ! d`,
		`'; DROP TABLE topics; --`,
		`zebra <-> runbook`,
		`zebra:*`,
		`\`,
		`'`,
		`|`,
		`&|!()`,
		`zebra'||'runbook`,
	}
	for _, in := range hostile {
		got := buildAnyTermQuery(in, maxAnyTermLexemes)
		if got == "" {
			continue // no usable term — the caller maps this to a stop-words-only error
		}
		if !tsQueryOperandShape.MatchString(got) {
			t.Errorf("buildAnyTermQuery(%q) = %q — not a canonical operand string", in, got)
		}
		for _, ch := range []string{"| |", "&", "!", "(", ")", "<", ">", ":", "*", "'", "\\", "--"} {
			// " | " is the only permitted use of the pipe.
			if ch == "| |" {
				continue
			}
			if strings.Contains(got, ch) {
				t.Errorf("buildAnyTermQuery(%q) = %q — carries %q", in, got, ch)
			}
		}
	}
}

func TestBuildAnyTermQuery_CapsAtMaxTerms(t *testing.T) {
	// 20 distinct multi-rune tokens; the operand string must carry exactly the
	// first maxAnyTermLexemes of them, in first-occurrence order.
	var words []string
	for _, w := range []string{
		"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf",
		"hotel", "india", "juliet", "kilo", "lima", "mike", "november",
		"oscar", "papa", "quebec", "romeo", "sierra", "tango",
	} {
		words = append(words, w)
	}
	got := buildAnyTermQuery(strings.Join(words, " "), maxAnyTermLexemes)

	terms := strings.Split(got, " | ")
	if len(terms) != maxAnyTermLexemes {
		t.Fatalf("operand carries %d terms, want the %d-term cap: %q", len(terms), maxAnyTermLexemes, got)
	}
	want := strings.Join(words[:maxAnyTermLexemes], " | ")
	if got != want {
		t.Errorf("buildAnyTermQuery = %q, want the FIRST %d terms in order (%q)", got, maxAnyTermLexemes, want)
	}
	// The 13th term is beyond the cap and must be absent.
	if strings.Contains(got, "mike") {
		t.Errorf("operand %q carries a term beyond the cap", got)
	}
}

func TestBuildAnyTermQuery_CapIsHonouredForSmallerLimits(t *testing.T) {
	got := buildAnyTermQuery("alpha bravo charlie delta", 2)
	if got != "alpha | bravo" {
		t.Errorf("buildAnyTermQuery(…, 2) = %q, want %q", got, "alpha | bravo")
	}
}

func TestBuildAnyTermQuery_DropsSingleRuneTokens(t *testing.T) {
	// Single-character lexemes are real in english tsvectors ("d'Artagnan"
	// splits into `d` + `artagnan`), and an OR over single letters matches
	// almost any topic — they are noise for a recall fallback.
	got := buildAnyTermQuery("d'Artagnan x runbook a b", maxAnyTermLexemes)
	if got != "artagnan | runbook" {
		t.Errorf("buildAnyTermQuery = %q, want %q", got, "artagnan | runbook")
	}
}

func TestBuildAnyTermQuery_KeepsStopWordsForPostgresToFilter(t *testing.T) {
	// The sanitizer does NOT filter stop words: to_tsquery runs the same
	// 'english' configuration the indexes were built with, so IT applies the
	// stop-word list and the stemmer. Pinned here so a future "optimization"
	// that starts filtering stop words in Go is a deliberate, visible change.
	got := buildAnyTermQuery("the planning of the cutover", maxAnyTermLexemes)
	if got != "the | planning | of | cutover" {
		t.Errorf("buildAnyTermQuery = %q, want %q", got, "the | planning | of | cutover")
	}
}

func TestBuildAnyTermQuery_EmptyOrUnusableInput(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"whitespace":       "   \t\n  ",
		"punctuation only": "! | & ( ) ' \\ , . ; :",
		"single runes":     "a i o u x",
	}
	for name, in := range cases {
		if got := buildAnyTermQuery(in, maxAnyTermLexemes); got != "" {
			t.Errorf("%s: buildAnyTermQuery(%q) = %q, want \"\"", name, in, got)
		}
	}
	if got := buildAnyTermQuery("zebra runbook", 0); got != "" {
		t.Errorf("non-positive maxTerms = %q, want \"\"", got)
	}
	if got := buildAnyTermQuery("zebra runbook", -1); got != "" {
		t.Errorf("negative maxTerms = %q, want \"\"", got)
	}
}

func TestBuildAnyTermQuery_LongProseIsBounded(t *testing.T) {
	// The realistic shape: a repeated 300-rune sentence (the compiler's query
	// cap). Repetition collapses through the dedupe, so the operand carries
	// each distinct term once and never exceeds the cap.
	prose := strings.Repeat("the zebra migration runbook planning for cutover window ", 6)
	got := buildAnyTermQuery(prose, maxAnyTermLexemes)
	terms := strings.Split(got, " | ")
	if len(terms) > maxAnyTermLexemes {
		t.Fatalf("operand carries %d terms, want at most the %d-term cap: %q", len(terms), maxAnyTermLexemes, got)
	}
	want := []string{"the", "zebra", "migration", "runbook", "planning", "for", "cutover", "window"}
	if len(terms) != len(want) {
		t.Fatalf("terms = %v, want the %d distinct terms %v", terms, len(want), want)
	}
	for i, w := range want {
		if terms[i] != w {
			t.Fatalf("terms = %v, want %v (first-occurrence order, deduplicated)", terms, want)
		}
	}
}

func TestTsQueryExprFor_DefaultModeBindsTheRawQuery(t *testing.T) {
	// ALL-TERMS (the zero value) is untouched: the RAW query is bound, hostile
	// characters included — plainto_tsquery is what makes that safe, and this
	// mode's behaviour must not change for existing callers.
	for _, q := range []string{"zebra runbook", "a | b", "'; DROP TABLE topics; --"} {
		expr, arg, ok := tsQueryExprFor(false, q)
		if !ok {
			t.Fatalf("ALL-TERMS(%q) reported no usable term", q)
		}
		if expr != plainTSQueryExpr {
			t.Errorf("ALL-TERMS(%q) expr = %q, want %q", q, expr, plainTSQueryExpr)
		}
		if arg != q {
			t.Errorf("ALL-TERMS(%q) arg = %q, want the raw query verbatim", q, arg)
		}
	}
}

func TestTsQueryExprFor_AnyTermModeBindsTheSanitizedArg(t *testing.T) {
	expr, arg, ok := tsQueryExprFor(true, "zebra migration runbook planning for the zebra cutover")
	if !ok {
		t.Fatal("ANY-TERM on prose reported no usable term")
	}
	if expr != anyTermTSQueryExpr {
		t.Errorf("ANY-TERM expr = %q, want %q", expr, anyTermTSQueryExpr)
	}
	want := "zebra | migration | runbook | planning | for | the | cutover"
	if arg != want {
		t.Errorf("ANY-TERM arg = %q, want %q", arg, want)
	}
	if strings.Contains(arg, "|") && !tsQueryOperandShape.MatchString(arg) {
		t.Errorf("ANY-TERM arg %q is not a canonical operand string", arg)
	}
}

func TestTsQueryExprFor_AnyTermModeWithNoUsableTerm(t *testing.T) {
	expr, arg, ok := tsQueryExprFor(true, "a i o")
	if ok {
		t.Errorf("ANY-TERM on single-rune input reported ok=true (arg %q)", arg)
	}
	if arg != "" {
		t.Errorf("ANY-TERM arg = %q, want \"\" when nothing survives sanitization", arg)
	}
	if expr != anyTermTSQueryExpr {
		t.Errorf("ANY-TERM expr = %q, want %q", expr, anyTermTSQueryExpr)
	}
}
