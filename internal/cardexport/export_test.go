package cardexport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
)

// ── fixtures ──────────────────────────────────────────────────────────────

// cardIDs is a fixed, deliberately unsorted UUID set: the lexicographic id
// order is ids[1] < ids[3] < ids[0] < ids[2], which is the order every export
// assertion expects — never the insertion order.
var cardIDs = []uuid.UUID{
	uuid.MustParse("bbbbbbbb-0000-4000-8000-000000000002"),
	uuid.MustParse("11111111-0000-4000-8000-000000000001"),
	uuid.MustParse("dddddddd-0000-4000-8000-000000000004"),
	uuid.MustParse("22222222-0000-4000-8000-000000000003"),
}

// fixtureRows builds `n` cards (ids in cardIDs order) plus `eventsPerCard`
// events on every card, THROUGH the real writer — card.NewCardDBManager →
// Repository → Create → AppendEvent. This is the drift guard: the exporter's
// hand-written SELECT list and its explicit row scanning are exercised against
// whatever the production writer actually stores.
//
// The manager is CLOSED before returning, which is also what makes the store
// quiescent: a clean close checkpoints the WAL and removes the `-wal`/`-shm`
// sidecars, leaving `<type>.db` alone. A test that needs the live-WAL shape
// keeps its own writer open instead of using this helper.
func fixtureRows(t *testing.T, dir string, ctype card.CardType, n, eventsPerCard int) {
	t.Helper()
	mgr := card.NewCardDBManager(dir)

	repo, err := mgr.Repository(ctype)
	if err != nil {
		t.Fatalf("fixture: Repository(%s): %v", ctype, err)
	}
	for i := 0; i < n; i++ {
		id := cardIDs[i%len(cardIDs)]
		data := fmt.Sprintf(`{"title":"card-%d","html":"<b>a&b</b>"}`, i)
		created, err := repo.Create(context.Background(), card.CreateCardInput{
			ID:          id,
			TreeID:      uuid.MustParse("aaaaaaaa-0000-4000-8000-000000000001"),
			NodeID:      uuid.MustParse("aaaaaaaa-0000-4000-8000-000000000002"),
			AppID:       "app-fixture",
			CardType:    ctype,
			Data:        json.RawMessage(data),
			Actions:     []card.CardAction{{Label: "Open", Handler: "open"}},
			ContextHash: "hash-" + id.String()[:8],
		})
		if err != nil {
			t.Fatalf("fixture: Create(%s): %v", id, err)
		}
		if created.Revision != 1 {
			t.Fatalf("fixture: Create(%s) revision = %d, want 1", id, created.Revision)
		}
		for e := 0; e < eventsPerCard; e++ {
			if _, err := repo.AppendEvent(context.Background(), id, card.AppendEventInput{
				EventID:   uuid.New(),
				EventType: card.EventAgentProgress,
				ActorKind: card.ActorAgent,
				ActorID:   "agent-fixture",
				Payload:   json.RawMessage(fmt.Sprintf(`{"step":%d}`, e)),
			}); err != nil {
				t.Fatalf("fixture: AppendEvent(%s, %d): %v", id, e, err)
			}
		}
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("fixture: Close: %v", err)
	}
}

// rawExec runs statements against a database with the WRITE dsn (the fixture
// side, never the exporter's). The path is turned into a `file:` URI the same
// way the exporter does, so a fixture whose path contains `?`, `#`, `%` or a
// space can still be created. `foreign_keys` is left at SQLite's default OFF so
// a test can plant an orphan event row.
func rawExec(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+escapeURIPath(path)+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("rawExec: open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("rawExec: %s: %v", path, err)
		}
	}
}

// handSchema is the cards/events schema from internal/card/database.go, written
// out here on purpose: it proves the exporter reads a store it did not create
// (no migrate step of its own).
const handCardsSchema = `CREATE TABLE cards (
	id              TEXT PRIMARY KEY NOT NULL,
	tree_id         TEXT NOT NULL DEFAULT '',
	node_id         TEXT NOT NULL DEFAULT '',
	app_id          TEXT NOT NULL,
	card_type       TEXT NOT NULL DEFAULT 'notes',
	data            TEXT NOT NULL DEFAULT '{}',
	actions         TEXT NOT NULL DEFAULT '[]',
	status          TEXT NOT NULL DEFAULT 'active',
	context_hash    TEXT NOT NULL,
	revision        INTEGER NOT NULL DEFAULT 1,
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL,
	dismissed_at    TEXT,
	archived_at     TEXT
)`

const handEventsSchema = `CREATE TABLE events (
	sequence         INTEGER PRIMARY KEY AUTOINCREMENT,
	event_id         TEXT NOT NULL UNIQUE,
	card_id          TEXT NOT NULL,
	event_type       TEXT NOT NULL,
	actor_kind       TEXT NOT NULL,
	actor_id         TEXT NOT NULL,
	payload          TEXT NOT NULL DEFAULT '{}',
	created_at       TEXT NOT NULL
)`

// handCard is one raw card row builder for the hand-made databases.
func handCardInsert(id, cardType, data, createdAt string) string {
	return fmt.Sprintf(`INSERT INTO cards (id, app_id, card_type, data, context_hash, created_at, updated_at)
		VALUES ('%s', 'app-hand', '%s', '%s', 'hash-hand', '%s', '%s')`, id, cardType, data, createdAt, createdAt)
}

// exportedLine is the decoded shape every assertion reads.
type exportedLine struct {
	Record   string           `json:"record"`
	CardType string           `json:"card_type"`
	Card     card.Card        `json:"card"`
	Events   []card.CardEvent `json:"events"`
}

// export runs Export over dir and returns the bytes, the result and the decoded
// lines.
func export(t *testing.T, dir string) ([]byte, Result, []exportedLine) {
	t.Helper()
	var buf bytes.Buffer
	res, err := Export(context.Background(), Options{DataDir: dir, Out: &buf})
	if err != nil {
		t.Fatalf("Export(%s): %v", dir, err)
	}
	if res.Bytes != buf.Len() {
		t.Fatalf("Result.Bytes = %d, want %d (the byte count must describe the writer)", res.Bytes, buf.Len())
	}
	raw := buf.Bytes()
	var lines []exportedLine
	if len(raw) > 0 {
		if raw[len(raw)-1] != '\n' {
			t.Fatalf("export does not end with a newline: %q", tail(raw, 40))
		}
		for i, l := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
			var rec exportedLine
			if err := json.Unmarshal([]byte(l), &rec); err != nil {
				t.Fatalf("line %d does not parse on its own: %v\n%s", i+1, err, l)
			}
			lines = append(lines, rec)
		}
	}
	return raw, res, lines
}

func tail(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[len(b)-n:])
}

// ── tests ─────────────────────────────────────────────────────────────────

// TestExportDeterminism is AC2: two exports of an unchanged store are
// byte-identical, the file carries no unit of volatile data, and every line
// parses on its own.
func TestExportDeterminism(t *testing.T) {
	dir := t.TempDir()
	fixtureRows(t, dir, card.CardTypeCompact, 4, 2)
	fixtureRows(t, dir, card.CardTypeExpanded, 2, 0)

	first, res1, lines := export(t, dir)
	second, res2, _ := export(t, dir)

	if !bytes.Equal(first, second) {
		t.Fatalf("two exports of the same store differ:\n--- first ---\n%s\n--- second ---\n%s",
			first, second)
	}
	if res1 != res2 {
		t.Fatalf("Result differs between exports: %+v vs %+v", res1, res2)
	}
	if res1.Files != 2 || res1.Cards != 6 || res1.Events != 8 {
		t.Fatalf("Result = %+v, want {Files:2 Cards:6 Events:8}", res1)
	}
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want 6", len(lines))
	}

	// No volatile field: the only keys are the pinned four, and the card keys
	// are exactly card.Card's json tags.
	var rawLines []map[string]json.RawMessage
	for _, l := range strings.Split(strings.TrimSuffix(string(first), "\n"), "\n") {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("unmarshal line: %v", err)
		}
		rawLines = append(rawLines, m)
	}
	for i, m := range rawLines {
		keys := sortedKeys(m)
		if got, want := strings.Join(keys, ","), "card,card_type,events,record"; got != want {
			t.Errorf("line %d keys = %s, want %s (a volatile field would show up here)", i+1, got, want)
		}
	}
	for _, banned := range []string{"generated_at", "exported_at", "hostname", "version", "data_dir", "timestamp"} {
		if bytes.Contains(first, []byte(banned)) {
			t.Errorf("export contains %q — the format must carry no volatile field", banned)
		}
	}
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestExportTotalOrder is AC3: lines are ordered by (card_type, id) and each
// card's events by sequence ascending — regardless of insertion order or
// directory iteration order.
func TestExportTotalOrder(t *testing.T) {
	dir := t.TempDir()
	// Insert in an order that is neither the id order nor the type order.
	fixtureRows(t, dir, card.CardTypeExpanded, 3, 3)
	fixtureRows(t, dir, card.CardTypeCompact, 4, 3)

	_, _, lines := export(t, dir)

	type lineKey struct{ typ, id string }
	var gotKeys []lineKey
	for _, l := range lines {
		gotKeys = append(gotKeys, lineKey{l.CardType, l.Card.ID.String()})
		for i := 1; i < len(l.Events); i++ {
			if l.Events[i-1].Sequence >= l.Events[i].Sequence {
				t.Fatalf("card %s events out of sequence order: %d then %d",
					l.Card.ID, l.Events[i-1].Sequence, l.Events[i].Sequence)
			}
		}
		if len(l.Events) != 3 {
			t.Fatalf("card %s has %d events, want 3", l.Card.ID, len(l.Events))
		}
	}

	want := append([]lineKey(nil), gotKeys...)
	sort.Slice(want, func(i, j int) bool {
		if want[i].typ != want[j].typ {
			return want[i].typ < want[j].typ
		}
		return want[i].id < want[j].id
	})
	for i := range gotKeys {
		if gotKeys[i] != want[i] {
			t.Fatalf("line %d = %v, want %v (full order: %v)", i+1, gotKeys[i], want[i], gotKeys)
		}
	}
}

// TestExportUnknownTypeStem is AC4: every `*.db` is exported and the record's
// card_type is the FILENAME STEM, so a store that is not one of the three
// built-ins is included rather than dropped.
func TestExportUnknownTypeStem(t *testing.T) {
	dir := t.TempDir()
	fixtureRows(t, dir, card.CardTypeCompact, 1, 0)

	// A hand-made store with a stem the package has never heard of, plus a
	// stray non-database file that must not be exported.
	notes := filepath.Join(dir, "notes.db")
	rawExec(t, notes,
		handCardsSchema,
		handEventsSchema,
		handCardInsert("33333333-0000-4000-8000-000000000009", "custom-row-type", `{"note":"hi"}`, "2026-09-17T10:00:00Z"),
	)
	if err := os.WriteFile(filepath.Join(dir, "legacy.db-wal"), []byte("not a database"), 0o600); err != nil {
		t.Fatalf("write wal sidecar: %v", err)
	}

	_, res, lines := export(t, dir)
	if res.Files != 2 {
		t.Fatalf("Files = %d, want 2 (compact.db + notes.db, -wal skipped)", res.Files)
	}

	byType := map[string]exportedLine{}
	for _, l := range lines {
		byType[l.CardType] = l
	}
	n, ok := byType["notes"]
	if !ok {
		t.Fatalf("notes.db was not exported; got card types %v", keysOf(byType))
	}
	if n.Record != "card" {
		t.Errorf("record = %q, want %q", n.Record, "card")
	}
	if n.Card.ID.String() != "33333333-0000-4000-8000-000000000009" {
		t.Errorf("card id = %s", n.Card.ID)
	}
	// The nested card keeps the value stored in the ROW; the record-level
	// card_type is the stem. Both are documented, and they can legitimately
	// differ for a hand-written store.
	if string(n.Card.CardType) != "custom-row-type" {
		t.Errorf("nested card.card_type = %q, want the stored value %q", n.Card.CardType, "custom-row-type")
	}
	if _, ok := byType["compact"]; !ok {
		t.Errorf("compact.db missing from the export: %v", keysOf(byType))
	}
}

func keysOf(m map[string]exportedLine) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestExportFixtureDrift pins the exporter's hand-written SELECT/scanning against
// rows the REAL writer created: every stored field survives into the record, and
// the zero-event card exports an empty (non-null) array.
func TestExportFixtureDrift(t *testing.T) {
	dir := t.TempDir()
	fixtureRows(t, dir, card.CardTypeCompact, 2, 2)
	fixtureRows(t, dir, card.CardTypeIteration, 1, 0)

	_, _, lines := export(t, dir)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}

	var withEvents, withoutEvents *exportedLine
	for i := range lines {
		switch lines[i].CardType {
		case "compact":
			withEvents = &lines[i]
		case "iteration":
			withoutEvents = &lines[i]
		}
	}
	if withEvents == nil || withoutEvents == nil {
		t.Fatalf("missing card types in export: %v", lines)
	}

	c := withEvents.Card
	if c.AppID != "app-fixture" {
		t.Errorf("app_id = %q, want app-fixture", c.AppID)
	}
	if c.Status != card.CardStatusActive {
		t.Errorf("status = %q, want active", c.Status)
	}
	if c.Revision != 1 {
		t.Errorf("revision = %d, want 1", c.Revision)
	}
	if c.CreatedAt.IsZero() || !c.CreatedAt.Equal(c.UpdatedAt) {
		t.Errorf("created_at/updated_at not carried through: %v / %v", c.CreatedAt, c.UpdatedAt)
	}
	if len(c.Actions) != 1 || c.Actions[0].Handler != "open" {
		t.Errorf("actions = %+v, want the stored single action", c.Actions)
	}
	if !strings.Contains(string(c.Data), `"title":"card-0"`) {
		t.Errorf("data = %s, want the stored JSON text", c.Data)
	}
	if len(withEvents.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(withEvents.Events))
	}
	if withEvents.Events[0].Sequence >= withEvents.Events[1].Sequence {
		t.Errorf("event sequences not ascending: %d, %d", withEvents.Events[0].Sequence, withEvents.Events[1].Sequence)
	}
	if withEvents.Events[0].CardID != c.ID {
		t.Errorf("event card_id = %s, want %s", withEvents.Events[0].CardID, c.ID)
	}
	if withEvents.Events[0].ActorKind != card.ActorAgent || withEvents.Events[0].ActorID != "agent-fixture" {
		t.Errorf("event actor = %s/%s", withEvents.Events[0].ActorKind, withEvents.Events[0].ActorID)
	}
	if !strings.Contains(string(withEvents.Events[0].Payload), `"step":0`) {
		t.Errorf("event payload = %s", withEvents.Events[0].Payload)
	}
	if withoutEvents.Events == nil || len(withoutEvents.Events) != 0 {
		t.Errorf("a card with no events must export an empty array, got %#v", withoutEvents.Events)
	}
	// The empty array must be [] and NOT null: a null there is a schema lie a
	// consumer would have to special-case.
	if !bytes.Contains(mustJSON(t, withoutEvents), []byte(`"events":[]`)) {
		t.Errorf("zero-event card did not render \"events\":[]: %s", mustJSON(t, withoutEvents))
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestExportNoHTMLEscaping pins SetEscapeHTML(false): Go's default would rewrite
// `<`, `>` and `&` as \u003c etc. — churn on every line that contains markup.
func TestExportNoHTMLEscaping(t *testing.T) {
	dir := t.TempDir()
	fixtureRows(t, dir, card.CardTypeCompact, 1, 0)

	raw, _, _ := export(t, dir)
	if !bytes.Contains(raw, []byte(`<b>a&b</b>`)) {
		t.Errorf("data markup was escaped or rewritten; line:\n%s", raw)
	}
	for _, escaped := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if bytes.Contains(raw, []byte(escaped)) {
			t.Errorf("export contains %s — SetEscapeHTML(false) is not in effect", escaped)
		}
	}
}

// TestExportEmptyDir: an empty store writes zero bytes and is not an error (no
// header, no trailing newline).
func TestExportEmptyDir(t *testing.T) {
	dir := t.TempDir()
	raw, res, lines := export(t, dir)

	if len(raw) != 0 {
		t.Fatalf("empty store wrote %d bytes: %q", len(raw), raw)
	}
	if res.Files != 0 || res.Cards != 0 || res.Events != 0 || res.Bytes != 0 {
		t.Fatalf("Result = %+v, want the zero Result", res)
	}
	if len(lines) != 0 {
		t.Fatalf("got %d lines from an empty store", len(lines))
	}
}

// TestExportDirsThatFailLoudly: a missing data dir, a path that is a file, and a
// data dir with no databases in it behave differently on purpose — the first two
// are errors naming the path, the third is a legitimate empty export.
func TestExportDirsThatFailLoudly(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	var buf bytes.Buffer
	_, err := Export(context.Background(), Options{DataDir: missing, Out: &buf})
	if err == nil {
		t.Fatal("Export on a missing directory returned no error")
	}
	if !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error must name the path and the reason, got: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a failed export wrote %d bytes", buf.Len())
	}

	notDir := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err = Export(context.Background(), Options{DataDir: notDir, Out: &buf})
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Errorf("Export(file) error = %v, want a not-a-directory refusal", err)
	}

	if _, err := Export(context.Background(), Options{DataDir: t.TempDir()}); err == nil {
		t.Error("Export with no writer returned no error")
	}
	if _, err := Export(context.Background(), Options{Out: &buf}); err == nil {
		t.Error("Export with no data dir returned no error")
	}
}

// TestExportBrokenStoreFailsLoudly: a `*.db` without a cards table and a file
// that is not SQLite both fail, naming the path — never an empty file silently
// standing in for a broken store.
func TestExportBrokenStoreFailsLoudly(t *testing.T) {
	t.Run("no cards table", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "compact.db")
		rawExec(t, path, `CREATE TABLE something_else (x INTEGER)`)

		var buf bytes.Buffer
		_, err := Export(context.Background(), Options{DataDir: dir, Out: &buf})
		if err == nil {
			t.Fatal("a database without a cards table exported anyway")
		}
		if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "cards table") {
			t.Errorf("error must name the path and the missing table, got: %v", err)
		}
		if buf.Len() != 0 {
			t.Errorf("wrote %d bytes for a broken store", buf.Len())
		}
	})

	t.Run("not a database", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "compact.db")
		if err := os.WriteFile(path, []byte("this is not sqlite"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		var buf bytes.Buffer
		_, err := Export(context.Background(), Options{DataDir: dir, Out: &buf})
		if err == nil {
			t.Fatal("a non-database file exported anyway")
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error must name the path, got: %v", err)
		}
	})

	t.Run("invalid JSON column", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "notes.db")
		rawExec(t, path, handCardsSchema,
			handCardInsert("44444444-0000-4000-8000-00000000000a", "notes", `not-json`, "2026-09-17T10:00:00Z"))

		var buf bytes.Buffer
		_, err := Export(context.Background(), Options{DataDir: dir, Out: &buf})
		if err == nil {
			t.Fatal("a row with a non-JSON data column exported anyway")
		}
		if !strings.Contains(err.Error(), "44444444-0000-4000-8000-00000000000a") {
			t.Errorf("error must name the offending row, got: %v", err)
		}
	})
}

// TestExportReadOnly is AC5, in four parts:
//
//  1. a write on the exporter's own connection (built by readOnlyDSN) is REFUSED
//     — proven for both DDL and DML;
//  2. the `file:` prefix is load-bearing: the same query string without it is
//     accepted by modernc.org/sqlite and the write goes through, so the prefix
//     can never be dropped as cosmetic;
//  3. an export leaves a QUIESCENT store completely alone — same files, same
//     bytes — which is the case a plain `mode=ro` connection breaks by creating
//     a 32 KiB `-shm` and a zero-length `-wal`;
//  4. an export of a store with a LIVE WAL still leaves the data files alone
//     (the main database and the `-wal` are byte-identical; the `-shm` is
//     SQLite's shared-memory WAL index, so only its presence is compared).
func TestExportReadOnly(t *testing.T) {
	dir := t.TempDir()
	fixtureRows(t, dir, card.CardTypeCompact, 2, 1)
	path := filepath.Join(dir, "compact.db")

	if got := names(t, dir); len(got) != 1 {
		t.Fatalf("the fixture is not quiescent: %v (a clean writer close leaves the main file alone)", got)
	}
	if got := readOnlyDSN(path); !strings.Contains(got, "immutable=1") {
		t.Errorf("a quiescent store got %q, want the immutable form (mode=ro would create sidecars)", got)
	}

	before := storeState(t, dir)

	ro, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	var n int
	if err := ro.QueryRow(`SELECT COUNT(*) FROM cards`).Scan(&n); err != nil {
		t.Fatalf("read-only connection cannot even read: %v", err)
	}
	if n != 2 {
		t.Fatalf("read-only connection counted %d cards, want 2", n)
	}
	if _, err := ro.Exec(`INSERT INTO cards (id, app_id, context_hash, created_at, updated_at) VALUES ('x','a','h','2026-09-17T10:00:00Z','2026-09-17T10:00:00Z')`); err == nil {
		t.Error("INSERT on the exporter's connection succeeded — the connection is not read-only")
	}
	if _, err := ro.Exec(`CREATE TABLE extra (x INTEGER)`); err == nil {
		t.Error("CREATE TABLE on the exporter's connection succeeded — the connection is not read-only")
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("close read-only: %v", err)
	}
	if got := storeState(t, dir); len(got) != len(before) {
		t.Fatalf("opening the exporter's connection changed the store's files: %v → %v", before, got)
	}

	// A read-only export must not disturb the store at all.
	_, _, lines := export(t, dir)
	if len(lines) != 2 {
		t.Fatalf("export produced %d lines, want 2", len(lines))
	}
	after := storeState(t, dir)
	if len(before) != len(after) {
		t.Fatalf("export changed the store's files:\nbefore: %v\nafter:  %v", before, after)
	}
	for name, sum := range before {
		if after[name] != sum {
			t.Errorf("%s changed across an export: %s → %s", name, sum, after[name])
		}
	}

	// The `file:` prefix is not cosmetic: without it the driver consumes the
	// query string itself (mode=ro is not one of the parameters it knows) and
	// opens the database READ-WRITE. Probed on a COPY so the store under test
	// stays untouched.
	probeDir := t.TempDir()
	probePath := filepath.Join(probeDir, "compact.db")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture db: %v", err)
	}
	if err := os.WriteFile(probePath, b, 0o600); err != nil {
		t.Fatalf("write probe db: %v", err)
	}
	rw, err := sql.Open("sqlite", probePath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open without the file: prefix: %v", err)
	}
	if _, err := rw.Exec(`CREATE TABLE prefix_probe (x INTEGER)`); err != nil {
		t.Fatalf("the un-prefixed DSN refused a write (%v) — readOnlyDSN's prefix note is wrong", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close probe: %v", err)
	}
}

// TestExportReadOnlyWithLiveWAL: the store is still open in a writer with
// uncheckpointed frames — the case where the WAL must be read, and where an
// `immutable=1` read would silently miss the data.
func TestExportReadOnlyWithLiveWAL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compact.db")

	mgr := card.NewCardDBManager(dir)
	defer func() { _ = mgr.Close() }()
	repo, err := mgr.Repository(card.CardTypeCompact)
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	for i, id := range cardIDs[:2] {
		if _, err := repo.Create(context.Background(), card.CreateCardInput{
			ID: id, AppID: "live", CardType: card.CardTypeCompact,
			ContextHash: fmt.Sprintf("h%d", i),
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	wal, err := os.Stat(path + "-wal")
	if err != nil || wal.Size() == 0 {
		t.Fatalf("the live fixture has no WAL with frames (%v) — this test needs one", err)
	}
	if got := readOnlyDSN(path); strings.Contains(got, "immutable=1") {
		t.Fatalf("a store with a live WAL got %q — immutable would read a stale database", got)
	}

	// The choice is not cosmetic: an immutable read of this same store is
	// provably INCOMPLETE, which is exactly why readOnlyDSN does not use it
	// while the WAL holds frames. Either the query fails (the cards table is not
	// visible without the WAL) or it returns fewer rows than the store has.
	imm, err := sql.Open("sqlite", "file:"+escapeURIPath(path)+"?mode=ro&immutable=1&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open immutable: %v", err)
	}
	var immutableCards int
	immErr := imm.QueryRow(`SELECT COUNT(*) FROM cards`).Scan(&immutableCards)
	if err := imm.Close(); err != nil {
		t.Fatalf("close immutable: %v", err)
	}
	if immErr == nil && immutableCards >= 2 {
		t.Errorf("an immutable read saw all %d cards, so this store does not exercise the WAL branch — the test needs a store whose rows are still in the WAL", immutableCards)
	}
	t.Logf("immutable read of the live store: err=%v cards=%d (the mode=ro read below must see 2)", immErr, immutableCards)

	before := storeState(t, dir)
	_, res, lines := export(t, dir)
	after := storeState(t, dir)

	if res.Cards != 2 || len(lines) != 2 {
		t.Fatalf("export saw %d cards, want the 2 committed in the live WAL (an immutable read would see none)", res.Cards)
	}
	if len(before) != len(after) {
		t.Fatalf("export changed the store's files:\nbefore: %v\nafter:  %v", before, after)
	}
	for name, sum := range before {
		if after[name] != sum {
			t.Errorf("%s changed across an export: %s → %s", name, sum, after[name])
		}
	}
}

// TestExportPathWithURIMetacharacters: a data directory whose name contains the
// characters that terminate or reinterpret a SQLite URI filename (`?`, `#`, `%`,
// space) is still read — the exporter escapes the path, and escaping a `/` would
// have turned the path into one long filename component.
func TestExportPathWithURIMetacharacters(t *testing.T) {
	dir := t.TempDir()
	odd := filepath.Join(dir, "odd dir?name#%.db")
	rawExec(t, odd, handCardsSchema, handEventsSchema,
		handCardInsert("55555555-0000-4000-8000-00000000000b", "odd", `{}`, "2026-09-17T10:00:00Z"))

	_, res, lines := export(t, dir)
	if res.Files != 1 || len(lines) != 1 {
		t.Fatalf("a path with URI metacharacters did not export: %+v / %d lines", res, len(lines))
	}
	if lines[0].CardType != "odd dir?name#%" {
		t.Errorf("card_type = %q, want the full filename stem", lines[0].CardType)
	}
	if lines[0].Card.ID.String() != "55555555-0000-4000-8000-00000000000b" {
		t.Errorf("card id = %s", lines[0].Card.ID)
	}
}

// storeState maps every file in a store directory to its sha256 — except a
// `-shm`, which is SQLite's shared-memory WAL index rather than store data and is
// rewritten by ANY connection, read-only included; for it only presence is
// recorded.
func storeState(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range names(t, dir) {
		if strings.HasSuffix(name, "-shm") {
			out[name] = "present"
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out[name] = fmt.Sprintf("%x", sha256.Sum256(b))
	}
	return out
}

// names lists the files in dir.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// TestExportOrphanEventsAreCounted: an event whose card row is absent is not
// silently dropped — it is excluded from the JSONL (a record is keyed by its
// card) and reported in Result.
func TestExportOrphanEventsAreCounted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.db")
	rawExec(t, path, handCardsSchema, handEventsSchema,
		handCardInsert("66666666-0000-4000-8000-00000000000c", "notes", `{}`, "2026-09-17T10:00:00Z"),
		`INSERT INTO events (event_id, card_id, event_type, actor_kind, actor_id, payload, created_at)
		 VALUES ('77777777-0000-4000-8000-00000000000d','99999999-0000-4000-8000-00000000000e','agent_progress','agent','a','{}','2026-09-17T10:00:01Z')`,
	)

	_, res, lines := export(t, dir)
	if res.OrphanEvents != 1 {
		t.Errorf("OrphanEvents = %d, want 1", res.OrphanEvents)
	}
	if res.Events != 0 || len(lines) != 1 || len(lines[0].Events) != 0 {
		t.Errorf("an orphan event must not be attached to any card: %+v %+v", res, lines)
	}
}

// TestExportEmptyJSONColumns: the store's NOT NULL text columns can still hold an
// empty string; a zero-length json.RawMessage is JSON poison (it fails the whole
// enclosing encode with `unexpected end of JSON input`), so it must normalise to
// null instead.
func TestExportEmptyJSONColumns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.db")
	rawExec(t, path, handCardsSchema, handEventsSchema,
		`INSERT INTO cards (id, app_id, card_type, data, actions, context_hash, created_at, updated_at)
		 VALUES ('88888888-0000-4000-8000-00000000000f','app','notes','','','h','2026-09-17T10:00:00Z','2026-09-17T10:00:00Z')`,
		`INSERT INTO events (event_id, card_id, event_type, actor_kind, actor_id, payload, created_at)
		 VALUES ('99999999-0000-4000-8000-000000000010','88888888-0000-4000-8000-00000000000f','agent_output','agent','a','','2026-09-17T10:00:02Z')`,
	)

	raw, _, lines := export(t, dir)
	if len(lines) != 1 || len(lines[0].Events) != 1 {
		t.Fatalf("got %d lines / %d events", len(lines), len(lines[0].Events))
	}
	if !bytes.Contains(raw, []byte(`"data":null`)) {
		t.Errorf("empty data column did not normalise to null: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"payload":null`)) {
		t.Errorf("empty payload column did not normalise to null: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"actions":[]`)) {
		t.Errorf("empty actions column did not normalise to []: %s", raw)
	}
}

// TestExportTimestampHandling: the layouts internal/card writes are parsed and
// normalised to UTC (so an export is stable across writers' offsets), a
// SQLite-`datetime('now')` value is accepted, and an unrecognised value is
// counted rather than silently zeroed.
func TestExportTimestampHandling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.db")
	rawExec(t, path, handCardsSchema,
		handCardInsert("aaaa1111-0000-4000-8000-000000000001", "notes", `{}`, "2026-09-17T05:00:00-05:00"),
		handCardInsert("aaaa1111-0000-4000-8000-000000000002", "notes", `{}`, "2026-09-17 06:00:00"),
		handCardInsert("aaaa1111-0000-4000-8000-000000000003", "notes", `{}`, "not a timestamp"),
	)

	_, res, lines := export(t, dir)
	// Counted per COLUMN, not per row: the unrecognised value is in both
	// created_at and updated_at of one hand-written card.
	if res.UnparsedTimestamps != 2 {
		t.Errorf("UnparsedTimestamps = %d, want 2 (one per unrecognised timestamp column)", res.UnparsedTimestamps)
	}
	byID := map[string]card.Card{}
	for _, l := range lines {
		byID[l.Card.ID.String()] = l.Card
	}
	if got := byID["aaaa1111-0000-4000-8000-000000000001"].CreatedAt; !got.Equal(time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("offset timestamp = %v, want 2026-09-17T10:00:00Z (UTC-normalised)", got)
	}
	if got := byID["aaaa1111-0000-4000-8000-000000000002"].CreatedAt; got.IsZero() || got.Location() != time.UTC {
		t.Errorf("sqlite datetime() timestamp = %v, want the parsed UTC instant", got)
	}
	if got := byID["aaaa1111-0000-4000-8000-000000000003"].CreatedAt; !got.IsZero() {
		t.Errorf("unrecognised timestamp = %v, want the zero time plus a count", got)
	}
}

// TestExportCancelledContext: a cancelled context fails the export instead of
// producing a partial file that looks complete.
func TestExportCancelledContext(t *testing.T) {
	dir := t.TempDir()
	fixtureRows(t, dir, card.CardTypeCompact, 2, 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	if _, err := Export(ctx, Options{DataDir: dir, Out: &buf}); err == nil {
		t.Fatal("Export with a cancelled context returned no error")
	}
}

// TestExportDeterministicAcrossWriterReopen pins the fixture-drift guard's other
// half: an unchanged store exported after the writer has been reopened (which is
// when SQLite rewrites the -wal into the database file) still yields the same
// bytes.
func TestExportDeterministicAcrossWriterReopen(t *testing.T) {
	dir := t.TempDir()
	fixtureRows(t, dir, card.CardTypeCompact, 3, 2)
	first, _, _ := export(t, dir)

	mgr := card.NewCardDBManager(dir)
	repo, err := mgr.Repository(card.CardTypeCompact)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	cards, err := repo.List(context.Background(), card.ListCardsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("reopen list: %v", err)
	}
	if len(cards) != 3 {
		t.Fatalf("reopen: %d cards, want 3", len(cards))
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("reopen close: %v", err)
	}

	second, _, _ := export(t, dir)
	if !bytes.Equal(first, second) {
		t.Fatalf("the export changed after a read-only reopen:\n%s\nvs\n%s", first, second)
	}
}
