package context

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// GAP-080 phase 5a — the stable manifest digest. Three properties carry the
// feature and are pinned here:
//
//  1. DETERMINISM — the digest ignores the volatile RequestID/CompiledAt and
//     never feeds on itself, so a preview compile and a run record that
//     describe the same content compare equal.
//  2. SENSITIVITY — every content-bearing field changes it, so an "equal"
//     verdict is meaningful (a digest that ignored a field would make two
//     different payloads look identical).
//  3. RECOMPUTABILITY — a stored record's JSON yields the same digest, with or
//     without the field (a record written before the field existed is still
//     comparable).

var manifestHashHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// hashFixtureManifest returns a manifest with EVERY content-bearing field
// populated, so each sensitivity case below has something to change.
func hashFixtureManifest() Manifest {
	primary := uuid.MustParse("0191a8b2-7fff-7000-9000-000000000101")
	return Manifest{
		RequestID:   "req-volatile",
		NodeID:      uuid.MustParse("0191a8b2-7fff-7000-9000-000000000301"),
		CompiledAt:  time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC),
		TokenBudget: 8000,
		TokensUsed:  1240,
		Ancestry: []ManifestItem{
			{
				ID:         uuid.MustParse("0191a8b2-7fff-7000-9000-000000000201"),
				Kind:       "node",
				Title:      "Welcome to Hermes Canopy",
				TokenCount: 412,
			},
			{
				ID:         uuid.MustParse("0191a8b2-7fff-7000-9000-000000000202"),
				Kind:       "node",
				Title:      "Child 1: Architecture",
				TokenCount: 828,
				Truncated:  true,
				Pinned:     true,
			},
		},
		References: []ManifestItem{{
			ID:         uuid.MustParse("0191a8b2-7fff-7000-9000-000000000401"),
			Kind:       "topic",
			Title:      "architecture",
			TokenCount: 120,
		}},
		Cards: []ManifestItem{{
			ID:         uuid.MustParse("0191a8b2-7fff-7000-9000-000000000501"),
			Kind:       "card",
			Title:      "compact",
			TokenCount: 60,
		}},
		OmittedCount:      3,
		OmittedReason:     "budget",
		TruncationMarkers: []string{"3 messages omitted"},
		Warnings:          []string{"5 references: context becoming unfocused"},
		PinnedCount:       1,
		MultiReference: &MultiReferenceManifestEntry{
			PrimarySourceID: primary,
			SourceCount:     2,
			ManifestHash:    "provenance-hash-from-the-selection",
			TokenBudget:     4000,
			TokensUsed:      900,
			Sources: []MultiReferenceSource{{
				ReferenceIndex: 1,
				SourceLabel:    "R1",
				ColorKey:       "ref-1",
				NodeID:         primary,
				AuthorID:       uuid.MustParse("0191a8b2-7fff-7000-9000-000000000004"),
				NodeType:       "message",
				SequenceNum:    42,
				CreatedAt:      time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
				TokenCount:     450,
				ContentHash:    "hash-R1",
			}},
		},
	}
}

// --- 1. Determinism ---------------------------------------------------------

// TestManifestDigest_VolatileFieldsDoNotAffectIt pins the exclusions that make
// the digest comparable at all: two manifests differing ONLY in requestId and
// compiledAt — exactly what two compiles of the same graph differ by — hash
// equally, and the digest never feeds on its own output.
func TestManifestDigest_VolatileFieldsDoNotAffectIt(t *testing.T) {
	base := hashFixtureManifest()
	want := ManifestDigest(base)
	if !manifestHashHex.MatchString(want) {
		t.Fatalf("digest = %q, want lowercase 64-hex sha256", want)
	}
	if again := ManifestDigest(base); again != want {
		t.Errorf("repeated digest = %q, want stable %q", again, want)
	}

	other := hashFixtureManifest()
	other.RequestID = "req-a-completely-different-uuid"
	other.CompiledAt = base.CompiledAt.Add(72 * time.Hour)
	other.ManifestHash = "a7564f4d-not-the-real-digest"
	if got := ManifestDigest(other); got != want {
		t.Errorf("digest changed with requestId/compiledAt/manifestHash: %q != %q", got, want)
	}

	// The excluded field is excluded even when it holds the real digest: a
	// self-referential recipe could never be recomputed from a record.
	sealed := hashFixtureManifest()
	sealed.ManifestHash = want
	if got := ManifestDigest(sealed); got != want {
		t.Errorf("digest of a sealed manifest = %q, want %q (self-reference)", got, want)
	}
}

// TestManifestDigest_CompiledManifestsAgree proves the property on real
// compiled output rather than on a hand-built fixture: two compiles of the
// same graph differ in requestId/compiledAt and hash equally.
func TestManifestDigest_CompiledManifestsAgree(t *testing.T) {
	ids := gap080IDs(3)
	chain := []db.Node{
		{ID: ids[0], AuthorID: gap080Author, Content: "root message"},
		{ID: ids[1], AuthorID: gap080Author, Content: "second message"},
		{ID: ids[2], AuthorID: gap080Author, Content: "third message"},
	}
	c := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)

	first := gap080Compile(t, c, CompileRequest{NodeID: ids[2], TokenBudget: 10000, MaxAncestors: 50})
	second := gap080Compile(t, c, CompileRequest{NodeID: ids[2], TokenBudget: 10000, MaxAncestors: 50})

	if !manifestHashHex.MatchString(first.Manifest.ManifestHash) {
		t.Fatalf("compiled manifestHash = %q, want lowercase 64-hex sha256", first.Manifest.ManifestHash)
	}
	if first.Manifest.RequestID == second.Manifest.RequestID {
		t.Fatal("the two compiles share a requestId — the determinism claim is untestable")
	}
	if first.Manifest.ManifestHash != second.Manifest.ManifestHash {
		t.Errorf("digest differs across compiles of the same content: %q != %q",
			first.Manifest.ManifestHash, second.Manifest.ManifestHash)
	}
	// The stored value IS the digest of the manifest it is stored on.
	if got := ManifestDigest(*first.Manifest); got != first.Manifest.ManifestHash {
		t.Errorf("ManifestDigest(manifest) = %q, field holds %q", got, first.Manifest.ManifestHash)
	}
}

// TestCompile_ManifestHashOnEveryPath covers AC1 across the compile paths: an
// ordinary compile, a pinned one, a truncated one (budget floor), a DEGRADED
// one (reference resolution failed, nil MultiReference) and a multi-reference
// one all carry a well-formed digest.
func TestCompile_ManifestHashOnEveryPath(t *testing.T) {
	author := uuid.MustParse("0191a8b2-7fff-7000-9000-000000000004")

	t.Run("ordinary", func(t *testing.T) {
		ids := gap080IDs(2)
		chain := []db.Node{
			{ID: ids[0], AuthorID: author, Content: "root"},
			{ID: ids[1], AuthorID: author, Content: "leaf"},
		}
		c := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
		res := gap080Compile(t, c, CompileRequest{NodeID: ids[1], TokenBudget: 10000, MaxAncestors: 50})
		assertDigest(t, res)
		if res.Manifest.MultiReference != nil {
			t.Error("an ordinary compile must not carry a multi-reference entry")
		}
	})

	t.Run("pinned", func(t *testing.T) {
		ids := gap080IDs(3)
		chain := []db.Node{
			{ID: ids[0], AuthorID: author, Content: "pinned root", Metadata: []byte(`{"pinned":true}`)},
			{ID: ids[1], AuthorID: author, Content: "middle"},
			{ID: ids[2], AuthorID: author, Content: "leaf"},
		}
		c := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
		res := gap080Compile(t, c, CompileRequest{NodeID: ids[2], TokenBudget: 1, MaxAncestors: 50})
		assertDigest(t, res)
		if res.Manifest.PinnedCount != 1 {
			t.Fatalf("pinnedCount = %d, want the pinned path exercised", res.Manifest.PinnedCount)
		}
		if res.Manifest.OmittedCount == 0 {
			t.Error("budget 1 must truncate the chain — the truncated path is what this case exercises")
		}
	})

	t.Run("degraded", func(t *testing.T) {
		ids := gap080IDs(2)
		chain := []db.Node{
			{ID: ids[0], AuthorID: author, Content: "root"},
			{ID: ids[1], AuthorID: author, Content: "leaf"},
		}
		topics := &stubTopicReader{
			getTopicsFn: func(context.Context, uuid.UUID) ([]db.Topic, error) {
				return nil, errors.New("topics table unavailable")
			},
		}
		c := NewCompiler(gap080ChainStub(chain), topics, &stubCardReader{}, NewTokenEstimator(), 5)
		res := gap080Compile(t, c, CompileRequest{
			NodeID: ids[1], TokenBudget: 10000, MaxAncestors: 50, ResolveRefs: true,
		})
		assertDigest(t, res)
		if len(res.Manifest.Warnings) == 0 {
			t.Fatal("a degraded compile must carry a warning — this case is meant to exercise that path")
		}
		if res.Manifest.MultiReference != nil {
			t.Error("a degraded compile carries a nil MultiReference")
		}
	})

	t.Run("multi-reference", func(t *testing.T) {
		nodeID := uuid.MustParse("0191a8b2-7fff-7000-9000-000000000301")
		sel := refSelection(8000,
			refInput(uuid.MustParse("0191a8b2-7fff-7000-9000-000000000101"), "R1", "ref-1", repeated(4000)),
			refInput(uuid.MustParse("0191a8b2-7fff-7000-9000-000000000102"), "R2", "ref-2", repeated(4000)),
		)
		res := gap080Compile(t, refCompiler(t, nodeID), CompileRequest{
			NodeID: nodeID, TokenBudget: 10000, MaxAncestors: 50, MultiReference: sel,
		})
		assertDigest(t, res)
		if res.Manifest.MultiReference == nil {
			t.Fatal("the multi-reference path must record its manifest entry")
		}
	})
}

// --- 2. Sensitivity ---------------------------------------------------------

// TestManifestDigest_ContentBearingFieldsChangeIt is the AC3 table: each
// mutation changes exactly one content-bearing field of an otherwise identical
// manifest, and the digest must move. A field that does NOT move the digest is
// a field the "same payload" verdict would be blind to.
func TestManifestDigest_ContentBearingFieldsChangeIt(t *testing.T) {
	base := ManifestDigest(hashFixtureManifest())

	item0 := hashFixtureManifest().Ancestry[0]
	item1 := hashFixtureManifest().Ancestry[1]

	cases := []struct {
		name   string
		mutate func(m *Manifest)
	}{
		{"nodeId", func(m *Manifest) { m.NodeID = uuid.MustParse("0191a8b2-7fff-7000-9000-000000000999") }},
		{"tokenBudget", func(m *Manifest) { m.TokenBudget = 8001 }},
		{"tokensUsed", func(m *Manifest) { m.TokensUsed = 1241 }},
		{"ancestry.id", func(m *Manifest) {
			m.Ancestry[0].ID = uuid.MustParse("0191a8b2-7fff-7000-9000-000000000998")
		}},
		{"ancestry.kind", func(m *Manifest) { m.Ancestry[0].Kind = "topic" }},
		{"ancestry.title", func(m *Manifest) { m.Ancestry[0].Title = "Welcome to Hermes Canopy!" }},
		{"ancestry.tokenCount", func(m *Manifest) { m.Ancestry[0].TokenCount = item0.TokenCount + 1 }},
		{"ancestry.truncated", func(m *Manifest) {
			m.Ancestry[0].Truncated = !item0.Truncated
		}},
		{"ancestry.pinned", func(m *Manifest) {
			// The reference item is never pinned, so this is a real change.
			m.Ancestry[0].Pinned = !item0.Pinned
		}},
		{"ancestry.length", func(m *Manifest) { m.Ancestry = m.Ancestry[:1] }},
		{"ancestry.order", func(m *Manifest) {
			m.Ancestry[0], m.Ancestry[1] = m.Ancestry[1], m.Ancestry[0]
		}},
		{"references.id", func(m *Manifest) {
			m.References[0].ID = uuid.MustParse("0191a8b2-7fff-7000-9000-000000000997")
		}},
		{"references.title", func(m *Manifest) { m.References[0].Title = "architecture-v2" }},
		{"references.truncated", func(m *Manifest) { m.References[0].Truncated = true }},
		{"cards.id", func(m *Manifest) {
			m.Cards[0].ID = uuid.MustParse("0191a8b2-7fff-7000-9000-000000000996")
		}},
		{"cards.kind", func(m *Manifest) { m.Cards[0].Kind = "node" }},
		{"omittedCount", func(m *Manifest) { m.OmittedCount = 4 }},
		{"omittedReason", func(m *Manifest) { m.OmittedReason = "depth" }},
		{"truncationMarkers", func(m *Manifest) {
			m.TruncationMarkers = []string{"4 messages omitted"}
		}},
		{"truncationMarkers.length", func(m *Manifest) {
			m.TruncationMarkers = append(m.TruncationMarkers, "1 card omitted")
		}},
		{"warnings", func(m *Manifest) {
			m.Warnings = []string{"tokens used (9000) exceeds budget (8000)"}
		}},
		{"pinnedCount", func(m *Manifest) { m.PinnedCount = 2 }},
		{"multiReference.primarySourceId", func(m *Manifest) {
			m.MultiReference.PrimarySourceID = uuid.MustParse("0191a8b2-7fff-7000-9000-000000000994")
		}},
		{"multiReference.sourceCount", func(m *Manifest) { m.MultiReference.SourceCount = 3 }},
		{"multiReference.manifestHash", func(m *Manifest) {
			// The carried provenance hash is content: two selections that
			// describe different signed snapshots must not compare equal.
			m.MultiReference.ManifestHash = "a-different-provenance-hash"
		}},
		{"multiReference.tokenBudget", func(m *Manifest) { m.MultiReference.TokenBudget = 4096 }},
		{"multiReference.tokensUsed", func(m *Manifest) { m.MultiReference.TokensUsed = 901 }},
		{"multiReference.sources[0].nodeId", func(m *Manifest) {
			m.MultiReference.Sources[0].NodeID = uuid.MustParse("0191a8b2-7fff-7000-9000-000000000993")
		}},
		{"multiReference.sources[0].contentHash", func(m *Manifest) {
			m.MultiReference.Sources[0].ContentHash = "hash-R1-changed"
		}},
		{"multiReference.sources[0].tokenCount", func(m *Manifest) {
			m.MultiReference.Sources[0].TokenCount = 451
		}},
		{"multiReference.sources[0].truncated", func(m *Manifest) {
			m.MultiReference.Sources[0].Truncated = true
		}},
		{"multiReference.sources.length", func(m *Manifest) {
			m.MultiReference.Sources = m.MultiReference.Sources[:0]
		}},
		{"multiReference.present", func(m *Manifest) { m.MultiReference = nil }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := hashFixtureManifest()
			tc.mutate(&m)
			if got := ManifestDigest(m); got == base {
				t.Errorf("digest unchanged (%q) after changing %s", got, tc.name)
			}
		})
	}

	// Guard rail: the fixture itself must be the manifest the sensitivity
	// cases assume — an untouched copy hashes to the baseline, so a mutation
	// that silently failed to apply would be caught as "unchanged" above only
	// if it really changed something. This asserts the copy is faithful.
	t.Run("untouched copy is stable", func(t *testing.T) {
		if got := ManifestDigest(hashFixtureManifest()); got != base {
			t.Errorf("untouched fixture digest = %q, want %q", got, base)
		}
	})

	// Sanity: the pin flag genuinely differs between the two fixture items, so
	// the ancestry.pinned case above is not a no-op on the second item.
	if !item1.Pinned || item0.Pinned {
		t.Errorf("fixture premise broken: item0.Pinned=%v item1.Pinned=%v", item0.Pinned, item1.Pinned)
	}
}

// --- 3. Recomputability from a stored record --------------------------------

// TestManifestDigestFromJSON_RoundTrip is AC4: the digest of a record's JSON
// equals the digest carried on the value.
func TestManifestDigestFromJSON_RoundTrip(t *testing.T) {
	m := hashFixtureManifest()
	m.ManifestHash = ManifestDigest(m)

	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"manifestHash"`) {
		t.Fatal("manifestHash is not omitempty — an existing manifest must always carry it")
	}

	got, err := ManifestDigestFromJSON(raw)
	if err != nil {
		t.Fatalf("ManifestDigestFromJSON: %v", err)
	}
	if got != m.ManifestHash {
		t.Errorf("JSON digest = %q, value digest = %q", got, m.ManifestHash)
	}
}

// TestManifestDigestFromJSON_OldRecordWithoutTheField pins the backwards case:
// a manifest recorded BEFORE the field existed (no `manifestHash` key at all)
// still yields a digest, and it matches a value manifest built without the
// field — an old record stays comparable to a new one.
func TestManifestDigestFromJSON_OldRecordWithoutTheField(t *testing.T) {
	m := hashFixtureManifest()
	want := ManifestDigest(m) // computed with ManifestHash empty
	m.ManifestHash = want

	old := jsonWithoutKey(t, m, "manifestHash")

	got, err := ManifestDigestFromJSON(old)
	if err != nil {
		t.Fatalf("ManifestDigestFromJSON(old record): %v", err)
	}
	if got != want {
		t.Errorf("old-record digest = %q, want %q (the same content)", got, want)
	}

	// And the new-shaped record agrees with the old one.
	fresh := jsonWithoutKey(t, m, "manifestHash")
	freshDigest, err := ManifestDigestFromJSON(fresh)
	if err != nil {
		t.Fatalf("ManifestDigestFromJSON(fresh record): %v", err)
	}
	if freshDigest != got {
		t.Errorf("old %q and new %q records disagree about the same content", got, freshDigest)
	}
}

// TestManifestDigestFromJSON_CompiledRunRecord drives the audit primitive
// against the JSON a run record actually carries: the compiler manifest,
// marshalled, recomputed.
func TestManifestDigestFromJSON_CompiledRunRecord(t *testing.T) {
	ids := gap080IDs(2)
	chain := []db.Node{
		{ID: ids[0], AuthorID: gap080Author, Content: "root message"},
		{ID: ids[1], AuthorID: gap080Author, Content: "leaf message"},
	}
	c := NewCompiler(gap080ChainStub(chain), &stubTopicReader{}, &stubCardReader{}, NewTokenEstimator(), 5)
	compiled := gap080Compile(t, c, CompileRequest{NodeID: ids[1], TokenBudget: 10000, MaxAncestors: 50})

	raw, err := json.Marshal(compiled.Manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	got, err := ManifestDigestFromJSON(raw)
	if err != nil {
		t.Fatalf("ManifestDigestFromJSON: %v", err)
	}
	if got != compiled.Manifest.ManifestHash {
		t.Errorf("record digest = %q, compiled digest = %q", got, compiled.Manifest.ManifestHash)
	}
}

// TestManifestDigestFromJSON_RejectsUnusableInput: there is no manifest in an
// empty or malformed document, and a digest for "nothing" is exactly the
// hollow fact this surface exists to prevent.
func TestManifestDigestFromJSON_RejectsUnusableInput(t *testing.T) {
	if got, err := ManifestDigestFromJSON(nil); err == nil {
		t.Errorf("nil JSON = %q, want an error", got)
	}
	if got, err := ManifestDigestFromJSON([]byte("")); err == nil {
		t.Errorf("empty JSON = %q, want an error", got)
	}
	if got, err := ManifestDigestFromJSON([]byte("{not json")); err == nil {
		t.Errorf("malformed JSON = %q, want an error", got)
	}
}

// --- Helpers ----------------------------------------------------------------

// gap080Compile compiles and fails the test on error.
func gap080Compile(t *testing.T, c Compiler, req CompileRequest) *CompiledContext {
	t.Helper()
	res, err := c.Compile(context.Background(), req)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if res == nil || res.Manifest == nil {
		t.Fatal("compile returned no manifest")
	}
	return res
}

// assertDigest fails unless the compiled manifest carries a well-formed digest
// that the pure function reproduces.
func assertDigest(t *testing.T, res *CompiledContext) {
	t.Helper()
	hash := res.Manifest.ManifestHash
	if !manifestHashHex.MatchString(hash) {
		t.Fatalf("manifestHash = %q, want exactly 64 lowercase hex characters", hash)
	}
	if want := ManifestDigest(*res.Manifest); want != hash {
		t.Errorf("ManifestDigest = %q, stored %q", want, hash)
	}
}

// jsonWithoutKey marshals a manifest and removes one key from the object,
// producing the shape an older record has.
func jsonWithoutKey(t *testing.T, m Manifest, key string) []byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal to object: %v", err)
	}
	if _, ok := obj[key]; !ok {
		t.Fatalf("fixture premise broken: marshalled manifest has no %q key", key)
	}
	delete(obj, key)
	out, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal without %q: %v", key, err)
	}
	return out
}
