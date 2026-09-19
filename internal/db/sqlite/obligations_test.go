package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ── NewID (uuidv7() obligation) ──────────────────────────────────────────────────────

func TestNewIDIsTimeOrderedUUIDv7(t *testing.T) {
	first, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if first == uuid.Nil {
		t.Fatal("NewID returned the nil UUID")
	}
	if got := first.Version(); got != 7 {
		t.Errorf("NewID version = %d, want 7 (RFC 9562 UUIDv7)", got)
	}
	if got := first.Variant(); got != uuid.RFC4122 {
		t.Errorf("NewID variant = %v, want RFC4122", got)
	}

	// Back-to-back ids in the same millisecond must still sort in generation order:
	// github.com/google/uuid's v7 keeps (milliseconds<<12 | sequence) strictly
	// increasing, so the low 12 bits of rand_a break the tie upward.
	ids := []uuid.UUID{first}
	for i := 0; i < 3; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID #%d: %v", i+2, err)
		}
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		if !(ids[i-1].String() < ids[i].String()) {
			t.Errorf("ids out of order: ids[%d]=%s is not < ids[%d]=%s",
				i-1, ids[i-1], i, ids[i])
		}
	}

	// Across an elapsed millisecond the 48-bit timestamp field orders them regardless of
	// the random tail.
	time.Sleep(2 * time.Millisecond)
	later, err := NewID()
	if err != nil {
		t.Fatalf("NewID (later): %v", err)
	}
	if !(ids[len(ids)-1].String() < later.String()) {
		t.Errorf("id generated 2ms later does not sort after the previous one: %s vs %s",
			ids[len(ids)-1], later)
	}
}

// ── ContentHash (set_content_hash()/trg_node_content_hash obligation) ────────────────

// The two ASCII vectors are the anti-drift anchor: they are the well-known SHA-256 values
// for the empty string and for "hello", recorded independently of this package.
const (
	sha256EmptyString = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	sha256HelloString = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
)

func TestContentHashMatchesSHA256HexOfUTF8Bytes(t *testing.T) {
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)

	cases := []struct{ name, content, want string }{
		{"empty", "", sha256EmptyString},
		{"ascii", "hello", sha256HelloString},
		// "é" is U+00E9: two UTF-8 bytes (0xC3 0xA9). PG hashes
		// convert_to(content,'UTF8'), i.e. those two bytes — not the rune's code point
		// and not a client encoding.
		{"two-byte utf8", "é", ""},
		{"mixed utf8", "Canopy — graph-native ✓ (ノード)", ""},
		{"whitespace", "\n\t  ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if want == "" {
				sum := sha256.Sum256([]byte(tc.content))
				want = hex.EncodeToString(sum[:])
			}
			got := ContentHash(tc.content)
			if got != want {
				t.Fatalf("ContentHash(%q) = %s, want %s", tc.content, got, want)
			}
			if !hex64.MatchString(got) {
				t.Errorf("ContentHash(%q) = %q is not 64 lowercase hex characters", tc.content, got)
			}
		})
	}

	// The UTF-8 vector spelled out in bytes, so a rune-based implementation cannot pass:
	// hashing one 0xE9 byte (Latin-1 semantics) would give a different digest.
	if got, want := ContentHash("é"), hex.EncodeToString(sha256Sum([]byte{0xC3, 0xA9})); got != want {
		t.Errorf("ContentHash(\"é\") = %s, want the SHA-256 of its UTF-8 bytes %s", got, want)
	}
	if ContentHash("é") == hex.EncodeToString(sha256Sum([]byte{0xE9})) {
		t.Error("ContentHash hashes code points, not UTF-8 bytes")
	}
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// ── EncodeTime / DecodeTime (D1 timestamp obligation) ───────────────────────────────

func TestEncodeDecodeTimeRoundTrip(t *testing.T) {
	base := time.Date(2026, 9, 18, 19, 0, 0, 123_000_000, time.UTC)

	enc := EncodeTime(base)
	if want := "2026-09-18T19:00:00.123Z"; enc != want {
		t.Errorf("EncodeTime = %q, want %q", enc, want)
	}
	if strings.Count(enc, "Z") != 1 || !strings.HasSuffix(enc, "Z") {
		t.Errorf("EncodeTime = %q, want exactly one trailing Z (UTC, no offset)", enc)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`).MatchString(enc) {
		t.Errorf("EncodeTime = %q, want RFC3339 UTC with millisecond precision", enc)
	}

	dec, err := DecodeTime(enc)
	if err != nil {
		t.Fatalf("DecodeTime(%q): %v", enc, err)
	}
	if !dec.Equal(base) {
		t.Errorf("round trip = %v, want %v", dec, base)
	}
	if loc := dec.Location(); loc != time.UTC {
		t.Errorf("DecodeTime location = %v, want UTC", loc)
	}
	if again := EncodeTime(dec); again != enc {
		t.Errorf("round-trip is not stable: Encode(Decode(%q)) = %q", enc, again)
	}

	// Millisecond truncation, not rounding: 123.999ms must encode as .123 (rounding would
	// make it .124), and the last representable millisecond stays .999 (no carry).
	if got := EncodeTime(base.Add(999 * time.Microsecond)); got != enc {
		t.Errorf("EncodeTime(123.999ms) = %q, want %q (truncated)", got, enc)
	}
	lastMS := time.Date(2026, 9, 18, 19, 0, 0, 999_999_999, time.UTC)
	if got, want := EncodeTime(lastMS), "2026-09-18T19:00:00.999Z"; got != want {
		t.Errorf("EncodeTime(999.999999ms) = %q, want %q", got, want)
	}

	// The shape is FIXED-width, not "up to 3 digits": a millisecond value with trailing
	// zeros (and zero itself) must still print three digits, otherwise a `.999` layout
	// (which strips them) would produce "…:00.12Z" / "…:00Z" and break both the lexicographic
	// ordering and the DDL's `%f` equivalence.
	for _, tc := range []struct {
		nsec int
		want string
	}{
		{120_000_000, "2026-09-18T19:00:00.120Z"},
		{100_000_000, "2026-09-18T19:00:00.100Z"},
		{0, "2026-09-18T19:00:00.000Z"},
	} {
		got := EncodeTime(time.Date(2026, 9, 18, 19, 0, 0, tc.nsec, time.UTC))
		if got != tc.want {
			t.Errorf("EncodeTime(%dns) = %q, want %q (fixed 3-digit milliseconds)", tc.nsec, got, tc.want)
		}
	}

	// A non-UTC input encodes to the same instant, so no offset ever reaches the store.
	zone := time.FixedZone("UTC-5", -5*3600)
	if got := EncodeTime(base.In(zone)); got != enc {
		t.Errorf("EncodeTime(non-UTC input) = %q, want %q (normalised to UTC)", got, enc)
	}

	// Lexicographic ordering, the property the TEXT representation is chosen for: values
	// 1ms apart sort by plain string comparison.
	next := EncodeTime(base.Add(time.Millisecond))
	if !(enc < next) {
		t.Errorf("timestamps 1ms apart do not sort lexicographically: %q vs %q", enc, next)
	}
	if !(EncodeTime(base.Add(-time.Millisecond)) < enc) {
		t.Error("an earlier timestamp does not sort before a later one")
	}
}

func TestDecodeTimeRejectsNonCanonicalValues(t *testing.T) {
	for _, s := range []string{
		"",
		"2026-09-18T19:00:00.123",       // no Z
		"2026-09-18T19:00:00Z",          // no milliseconds
		"2026-09-18T19:00:00.12Z",       // 2-digit fraction
		"2026-09-18T19:00:00.123456Z",   // microseconds
		"2026-09-18T19:00:00.123+00:00", // numeric offset
		"2026-09-18 19:00:00.123Z",      // space separator
		"2026-09-18T19:00:00.123z",      // lowercase z
		"not-a-timestamp",
	} {
		if got, err := DecodeTime(s); err == nil {
			t.Errorf("DecodeTime(%q) = %v, want an error (only the canonical shape is accepted)", s, got)
		}
	}
}

// ── NextNodeSequence (set_node_sequence()/trg_node_sequence obligation) ─────────────

func TestNextNodeSequenceAllocatesPerTree(t *testing.T) {
	s := newTestStoreWithSchema(t)
	ctx := context.Background()
	treeA := insertTree(t, ctx, s)
	treeB := insertTree(t, ctx, s)

	// A fresh tree starts at 1 and advances by one per committed insert.
	for want := int64(1); want <= 3; want++ {
		tx, err := s.DB().BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		got, err := NextNodeSequence(ctx, tx, treeA)
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("NextNodeSequence (tree A, node %d): %v", want, err)
		}
		if got != want {
			_ = tx.Rollback()
			t.Fatalf("NextNodeSequence (tree A, node %d) = %d, want %d", want, got, want)
		}
		if _, err := tx.ExecContext(ctx, insertNodeSQL, mustID(t), treeA, mustID(t), "n", got, ContentHash("n")); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert node with sequence %d: %v", got, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit node %d: %v", want, err)
		}
	}

	// Independent per tree: the second tree is still at 1 while A sits at 4.
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if got, err := NextNodeSequence(ctx, tx, treeA); err != nil || got != 4 {
		t.Errorf("tree A next sequence = %d (err %v), want 4", got, err)
	}
	if got, err := NextNodeSequence(ctx, tx, treeB); err != nil || got != 1 {
		t.Errorf("tree B next sequence = %d (err %v), want 1 (sequences are tree-scoped)", got, err)
	}
}

func TestNextNodeSequenceRejectsBadInput(t *testing.T) {
	s := newTestStoreWithSchema(t)
	ctx := context.Background()

	if _, err := NextNodeSequence(ctx, nil, "any-tree"); err == nil {
		t.Error("NextNodeSequence(ctx, nil, ...) succeeded, want an error")
	}
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, treeID := range []string{"", "   "} {
		if _, err := NextNodeSequence(ctx, tx, treeID); err == nil {
			t.Errorf("NextNodeSequence(ctx, tx, %q) succeeded, want an error", treeID)
		}
	}
}
