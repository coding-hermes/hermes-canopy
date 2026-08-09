package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	_ "modernc.org/sqlite" // pure-Go SQLite driver for the fixture DB

	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

var defaultOwner = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// fixtureSchema is a subset of the live Hermes state.db schema covering
// every column the reader queries.
const fixtureSchema = `
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL,
    display_name TEXT,
    title TEXT,
    model TEXT,
    started_at REAL NOT NULL,
    ended_at REAL,
    message_count INTEGER DEFAULT 0,
    input_tokens INTEGER DEFAULT 0,
    output_tokens INTEGER DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    role TEXT NOT NULL,
    content TEXT,
    tool_name TEXT,
    tool_calls TEXT,
    token_count INTEGER,
    timestamp REAL NOT NULL,
    active INTEGER NOT NULL DEFAULT 1
);`

// buildFixture creates a temp SQLite state.db with the fixture schema and
// runs insert against it. Returns the DB path.
func buildFixture(t *testing.T, insert func(conn *sql.DB)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	conn, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(fixtureSchema); err != nil {
		t.Fatalf("create fixture schema: %v", err)
	}
	if insert != nil {
		insert(conn)
	}
	return path
}

type fixtureSession struct {
	id        string
	source    string
	display   string
	title     string
	model     string
	startedAt float64
	archived  int
}

func insertSession(t *testing.T, conn *sql.DB, s fixtureSession) {
	t.Helper()
	_, err := conn.Exec(`INSERT INTO sessions
		(id, source, display_name, title, model, started_at, archived)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.id, s.source, s.display, s.title, s.model, s.startedAt, s.archived)
	if err != nil {
		t.Fatalf("insert session %s: %v", s.id, err)
	}
}

type fixtureMessage struct {
	id         int64
	sessionID  string
	role       string
	content    string
	toolName   string
	tokenCount *int
	ts         float64
	inactive   bool
}

func insertMessage(t *testing.T, conn *sql.DB, m fixtureMessage) {
	t.Helper()
	var tc any
	if m.tokenCount != nil {
		tc = *m.tokenCount
	}
	active := 1
	if m.inactive {
		active = 0
	}
	_, err := conn.Exec(`INSERT INTO messages
		(id, session_id, role, content, tool_name, token_count, timestamp, active)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.id, m.sessionID, m.role, m.content, m.toolName, tc, m.ts, active)
	if err != nil {
		t.Fatalf("insert message %d: %v", m.id, err)
	}
}

// --- Fakes ------------------------------------------------------------------

type fakeTreeCreator struct {
	calls []service.CreateTreeParams
	trees []*service.Tree
}

func (f *fakeTreeCreator) CreateTree(_ context.Context, p service.CreateTreeParams) (*service.Tree, error) {
	f.calls = append(f.calls, p)
	t := &service.Tree{
		ID:          uuid.New(),
		OwnerID:     p.OwnerID,
		Title:       p.Title,
		Description: p.Description,
		RootNodeID:  uuid.New(),
	}
	f.trees = append(f.trees, t)
	return t, nil
}

type fakeNodeCreator struct {
	calls []service.CreateNodeInput
	nodes []*service.CreateNodeResult
}

func (f *fakeNodeCreator) Create(_ context.Context, treeID uuid.UUID, in service.CreateNodeInput) (*service.CreateNodeResult, error) {
	f.calls = append(f.calls, in)
	n := &service.CreateNodeResult{
		Node: &service.NodeDetail{ID: uuid.New(), TreeID: treeID, ParentID: &in.ParentID},
		Edge: &service.EdgeDetail{SourceNodeID: in.ParentID},
	}
	f.nodes = append(f.nodes, n)
	return n, nil
}

type memWatermarkStore struct {
	wm   Watermark
	save []Watermark
}

func (m *memWatermarkStore) Load() (Watermark, error) { return m.wm, nil }
func (m *memWatermarkStore) Save(w Watermark) error {
	m.wm = w
	m.save = append(m.save, w)
	return nil
}

func newTestImporter(t *testing.T, dbPath string) (*Importer, *fakeTreeCreator, *fakeNodeCreator, *memWatermarkStore) {
	t.Helper()
	r, err := OpenReader(dbPath)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	trees := &fakeTreeCreator{}
	nodes := &fakeNodeCreator{}
	store := &memWatermarkStore{}
	return NewImporter(r, trees, nodes, store, defaultOwner), trees, nodes, store
}

// --- Tests ------------------------------------------------------------------

func TestImporterHappyPath(t *testing.T) {
	path := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "sess_a", source: "cli", title: "Alpha Session", model: "deepseek-v4-pro", startedAt: 1000.5})
		insertMessage(t, conn, fixtureMessage{id: 1, sessionID: "sess_a", role: "system", content: "boot", ts: 100.0})
		insertMessage(t, conn, fixtureMessage{id: 2, sessionID: "sess_a", role: "user", content: "hello world", ts: 101.0})
		insertMessage(t, conn, fixtureMessage{id: 3, sessionID: "sess_a", role: "assistant", content: "hi there", ts: 102.0})
		insertMessage(t, conn, fixtureMessage{id: 4, sessionID: "sess_a", role: "tool", content: "ls output", toolName: "bash", ts: 103.0})
	})
	imp, trees, nodes, store := newTestImporter(t, path)
	ctx := context.Background()

	sum, err := imp.Run(ctx, ImportOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if sum.DryRun || sum.SessionsImported != 1 || sum.TreesCreated != 1 || sum.NodesCreated != 3 {
		t.Fatalf("summary = %+v, want 1 session / 1 tree / 3 nodes", sum)
	}

	// Tree mapping.
	if len(trees.calls) != 1 {
		t.Fatalf("tree creations = %d, want 1", len(trees.calls))
	}
	tp := trees.calls[0]
	if tp.Title != "Alpha Session" {
		t.Errorf("title = %q, want %q", tp.Title, "Alpha Session")
	}
	if tp.OwnerID != defaultOwner {
		t.Errorf("owner = %v, want %v", tp.OwnerID, defaultOwner)
	}
	if tp.RootContent != "hello world" {
		t.Errorf("root content = %q, want first user message", tp.RootContent)
	}
	if string(tp.ContentFormat) != "markdown" || string(tp.NodeType) != "message" {
		t.Errorf("root format/type = %s/%s, want markdown/message", tp.ContentFormat, tp.NodeType)
	}
	if !strings.Contains(tp.Description, "deepseek-v4-pro") || !strings.Contains(tp.Description, "cli") {
		t.Errorf("description = %q, want model+source", tp.Description)
	}

	// Node mapping: order, role tags, chaining, author.
	if len(nodes.calls) != 3 {
		t.Fatalf("node creations = %d, want 3", len(nodes.calls))
	}
	rootID := trees.trees[0].RootNodeID
	if nodes.calls[0].ParentID != rootID {
		t.Errorf("first node parent = %v, want root %v", nodes.calls[0].ParentID, rootID)
	}
	if nodes.calls[1].ParentID != nodes.nodes[0].Node.ID {
		t.Errorf("second node parent = %v, want first node %v", nodes.calls[1].ParentID, nodes.nodes[0].Node.ID)
	}
	if nodes.calls[2].ParentID != nodes.nodes[1].Node.ID {
		t.Errorf("third node parent = %v, want second node %v", nodes.calls[2].ParentID, nodes.nodes[1].Node.ID)
	}
	wantContents := []string{"**system:** boot", "**assistant:** hi there", "**tool (bash):** ls output"}
	for i, w := range wantContents {
		if nodes.calls[i].Content != w {
			t.Errorf("node %d content = %q, want %q", i, nodes.calls[i].Content, w)
		}
	}
	for i, c := range nodes.calls {
		if c.EdgeType != "reply" || c.NodeType != "message" || c.ContentFormat != "markdown" {
			t.Errorf("node %d edge/type/format = %s/%s/%s, want reply/message/markdown",
				i, c.EdgeType, c.NodeType, c.ContentFormat)
		}
		if c.TreeID != trees.trees[0].ID {
			t.Errorf("node %d tree = %v, want %v", i, c.TreeID, trees.trees[0].ID)
		}
		if c.AuthorID != defaultOwner {
			t.Errorf("node %d author = %v, want %v", i, c.AuthorID, defaultOwner)
		}
	}

	// Metadata JSON on the tool node.
	var meta map[string]any
	if err := json.Unmarshal(nodes.calls[2].Metadata, &meta); err != nil {
		t.Fatalf("tool metadata: %v", err)
	}
	if meta["session_id"] != "sess_a" || meta["role"] != "tool" ||
		meta["message_id"] != float64(4) || meta["tool_name"] != "bash" {
		t.Errorf("tool metadata = %v", meta)
	}
	// Metadata on a non-tool node omits tool_name.
	var sysMeta map[string]any
	if err := json.Unmarshal(nodes.calls[0].Metadata, &sysMeta); err != nil {
		t.Fatalf("system metadata: %v", err)
	}
	if sysMeta["session_id"] != "sess_a" || sysMeta["role"] != "system" || sysMeta["message_id"] != float64(1) {
		t.Errorf("system metadata = %v", sysMeta)
	}
	if _, ok := sysMeta["tool_name"]; ok {
		t.Errorf("system metadata should omit tool_name: %v", sysMeta)
	}

	// Watermark persisted with the imported session.
	if len(store.save) != 1 {
		t.Fatalf("watermark saves = %d, want 1", len(store.save))
	}
	if wm := store.wm; wm.LastSessionID != "sess_a" {
		t.Errorf("watermark = %+v, want last_session_id sess_a", wm)
	}
}

func TestImporterIncremental(t *testing.T) {
	// Two sessions sharing one started_at (like real subagent batches) plus
	// a third at a later timestamp.
	path := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "20260809_000733_aaa", source: "subagent", startedAt: 100.0})
		insertSession(t, conn, fixtureSession{id: "20260809_000733_bbb", source: "subagent", startedAt: 100.0})
		insertSession(t, conn, fixtureSession{id: "sess_later", source: "cli", title: "Later", startedAt: 200.0})
		insertMessage(t, conn, fixtureMessage{id: 1, sessionID: "20260809_000733_aaa", role: "user", content: "one", ts: 10})
		insertMessage(t, conn, fixtureMessage{id: 2, sessionID: "20260809_000733_aaa", role: "assistant", content: "one-reply", ts: 11})
		insertMessage(t, conn, fixtureMessage{id: 3, sessionID: "20260809_000733_bbb", role: "user", content: "two", ts: 20})
		insertMessage(t, conn, fixtureMessage{id: 4, sessionID: "20260809_000733_bbb", role: "assistant", content: "two-reply", ts: 21})
		insertMessage(t, conn, fixtureMessage{id: 5, sessionID: "sess_later", role: "user", content: "three", ts: 30})
		insertMessage(t, conn, fixtureMessage{id: 6, sessionID: "sess_later", role: "assistant", content: "three-reply", ts: 31})
	})
	imp, trees, nodes, store := newTestImporter(t, path)
	ctx := context.Background()

	sum1, err := imp.Run(ctx, ImportOptions{})
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	if sum1.SessionsImported != 3 {
		t.Fatalf("run1 imported %d, want 3", sum1.SessionsImported)
	}
	if len(store.save) != 1 {
		t.Fatalf("run1 watermark saves = %d, want 1", len(store.save))
	}
	if wm := store.wm; wm.LastSessionID != "sess_later" || wm.LastStartedAt != 200.0 {
		t.Errorf("run1 watermark = %+v, want (sess_later, 200)", wm)
	}

	// Re-run: zero new imports, no extra watermark save.
	sum2, err := imp.Run(ctx, ImportOptions{})
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if sum2.SessionsImported != 0 || sum2.TreesCreated != 0 || sum2.NodesCreated != 0 {
		t.Fatalf("run2 = %+v, want zero imports", sum2)
	}
	if len(trees.calls) != 3 || len(nodes.calls) != 3 {
		t.Fatalf("total creations = %d trees / %d nodes, want 3/3", len(trees.calls), len(nodes.calls))
	}
	if len(store.save) != 1 {
		t.Fatalf("run2 re-saved watermark (%d saves), want still 1", len(store.save))
	}

	// A brand-new session after the watermark imports on the next run.
	conn, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen fixture: %v", err)
	}
	insertSession(t, conn, fixtureSession{id: "sess_new", source: "cli", title: "New", startedAt: 300.0})
	_ = conn.Close()

	sum3, err := imp.Run(ctx, ImportOptions{})
	if err != nil {
		t.Fatalf("run3: %v", err)
	}
	if sum3.SessionsImported != 1 || sum3.TreesCreated != 1 {
		t.Fatalf("run3 = %+v, want 1 session / 1 tree", sum3)
	}
}

func TestImporterArchivedSkip(t *testing.T) {
	path := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "a1", source: "cli", title: "Active", startedAt: 100.0})
		insertSession(t, conn, fixtureSession{id: "a2", source: "cron", title: "Archived", startedAt: 200.0, archived: 1})
		insertMessage(t, conn, fixtureMessage{id: 1, sessionID: "a1", role: "user", content: "hi", ts: 10})
		insertMessage(t, conn, fixtureMessage{id: 2, sessionID: "a2", role: "user", content: "bye", ts: 20})
	})
	imp, trees, _, store := newTestImporter(t, path)
	ctx := context.Background()

	// Default: archived session skipped, watermark stays at the active one.
	sum, err := imp.Run(ctx, ImportOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if sum.SessionsImported != 1 || sum.SkippedArchived != 1 {
		t.Fatalf("summary = %+v, want 1 imported / 1 archived skipped", sum)
	}
	if len(trees.calls) != 1 || trees.calls[0].Title != "Active" {
		t.Fatalf("trees = %+v, want only Active", trees.calls)
	}
	if wm := store.wm; wm.LastSessionID != "a1" {
		t.Errorf("watermark = %+v, want a1 (archived skip must not advance it)", wm)
	}

	// --include-archived picks the archived session up on the next run.
	sum2, err := imp.Run(ctx, ImportOptions{IncludeArchived: true})
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if sum2.SessionsImported != 1 || sum2.TreesCreated != 1 {
		t.Fatalf("run2 = %+v, want archived session imported", sum2)
	}
	if len(trees.calls) != 2 || trees.calls[1].Title != "Archived" {
		t.Fatalf("trees after run2 = %+v, want Archived", trees.calls)
	}
}

func TestImporterEmptySession(t *testing.T) {
	path := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "no_msgs", source: "cli", title: "Empty", startedAt: 100.0})
		insertSession(t, conn, fixtureSession{id: "no_user", source: "cli", title: "NoUser", startedAt: 200.0})
		insertMessage(t, conn, fixtureMessage{id: 1, sessionID: "no_user", role: "assistant", content: "first word", ts: 10})
	})
	imp, trees, nodes, _ := newTestImporter(t, path)

	sum, err := imp.Run(context.Background(), ImportOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if sum.SessionsImported != 2 || sum.TreesCreated != 2 || sum.NodesCreated != 1 {
		t.Fatalf("summary = %+v, want 2 sessions / 2 trees / 1 node", sum)
	}
	// Session without messages: root = title.
	if trees.calls[0].RootContent != "Empty" {
		t.Errorf("empty-session root = %q, want title", trees.calls[0].RootContent)
	}
	// Session without user messages: root = title, assistant msg is a child.
	if trees.calls[1].RootContent != "NoUser" {
		t.Errorf("no-user-session root = %q, want title", trees.calls[1].RootContent)
	}
	if len(nodes.calls) != 1 || nodes.calls[0].Content != "**assistant:** first word" {
		t.Errorf("no-user-session nodes = %+v, want one assistant child", nodes.calls)
	}
}

func TestImporterTitleFallback(t *testing.T) {
	path := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "s_1", source: "subagent", startedAt: 100.0}) // no title, no display
		insertSession(t, conn, fixtureSession{id: "s_2", source: "cli", display: "Display Name", startedAt: 200.0})
	})
	imp, trees, _, _ := newTestImporter(t, path)

	if _, err := imp.Run(context.Background(), ImportOptions{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if trees.calls[0].Title != "Hermes session s_1" {
		t.Errorf("fallback title = %q, want %q", trees.calls[0].Title, "Hermes session s_1")
	}
	if trees.calls[1].Title != "Display Name" {
		t.Errorf("display_name title = %q, want %q", trees.calls[1].Title, "Display Name")
	}
}

func TestImporterLimit(t *testing.T) {
	path := buildFixture(t, func(conn *sql.DB) {
		for i := 1; i <= 3; i++ {
			id := fmt.Sprintf("s%d", i)
			insertSession(t, conn, fixtureSession{id: id, source: "cli", title: fmt.Sprintf("S%d", i), startedAt: float64(i) * 100.0})
			insertMessage(t, conn, fixtureMessage{id: int64(i) * 10, sessionID: id, role: "user", content: fmt.Sprintf("m%d", i), ts: float64(i)})
			insertMessage(t, conn, fixtureMessage{id: int64(i)*10 + 1, sessionID: id, role: "assistant", content: fmt.Sprintf("r%d", i), ts: float64(i) + 0.5})
		}
	})
	imp, trees, nodes, store := newTestImporter(t, path)
	ctx := context.Background()

	sum1, err := imp.Run(ctx, ImportOptions{Limit: 2})
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	if sum1.SessionsImported != 2 {
		t.Fatalf("run1 imported %d, want 2", sum1.SessionsImported)
	}
	if wm := store.wm; wm.LastSessionID != "s2" {
		t.Errorf("run1 watermark = %+v, want s2", wm)
	}

	sum2, err := imp.Run(ctx, ImportOptions{})
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if sum2.SessionsImported != 1 || sum2.Titles[0] != "S3" {
		t.Fatalf("run2 = %+v, want remaining session S3", sum2)
	}
	if len(trees.calls) != 3 || len(nodes.calls) != 3 {
		t.Fatalf("total creations = %d trees / %d nodes, want 3/3", len(trees.calls), len(nodes.calls))
	}
}

func TestImporterDryRun(t *testing.T) {
	path := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "d1", source: "cli", title: "Dry", startedAt: 100.0})
		insertMessage(t, conn, fixtureMessage{id: 1, sessionID: "d1", role: "user", content: "hello", ts: 10})
		insertMessage(t, conn, fixtureMessage{id: 2, sessionID: "d1", role: "assistant", content: "world", ts: 20})
	})
	imp, trees, nodes, store := newTestImporter(t, path)

	sum, err := imp.Run(context.Background(), ImportOptions{DryRun: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !sum.DryRun {
		t.Error("summary.DryRun = false, want true")
	}
	if sum.SessionsImported != 1 || sum.TreesCreated != 1 || sum.NodesCreated != 1 {
		t.Fatalf("summary = %+v, want would-be 1 session / 1 tree / 1 node", sum)
	}
	if len(sum.Titles) != 1 || sum.Titles[0] != "Dry" {
		t.Errorf("titles = %v, want [Dry]", sum.Titles)
	}
	// Dry run must not create anything or persist the watermark.
	if len(trees.calls) != 0 || len(nodes.calls) != 0 {
		t.Fatalf("dry run created %d trees / %d nodes", len(trees.calls), len(nodes.calls))
	}
	if len(store.save) != 0 {
		t.Fatalf("dry run saved watermark %d times, want 0", len(store.save))
	}
}

func TestTruncation(t *testing.T) {
	longRoot := strings.Repeat("r", 200000)
	longNode := strings.Repeat("n", 5000)
	path := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "t1", source: "cli", title: "T", startedAt: 100.0})
		insertMessage(t, conn, fixtureMessage{id: 1, sessionID: "t1", role: "user", content: longRoot, ts: 10})
		insertMessage(t, conn, fixtureMessage{id: 2, sessionID: "t1", role: "assistant", content: longNode, ts: 20})
	})
	imp, trees, nodes, _ := newTestImporter(t, path)

	if _, err := imp.Run(context.Background(), ImportOptions{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := utf8.RuneCountInString(trees.calls[0].RootContent); got != maxRootContentLen {
		t.Errorf("root runes = %d, want %d (service cap)", got, maxRootContentLen)
	}
	if got := utf8.RuneCountInString(nodes.calls[0].Content); got != len("**assistant:** ")+maxNodeContentLen {
		t.Errorf("node runes = %d, want %d (tag + body cap)", got, len("**assistant:** ")+maxNodeContentLen)
	}
	if !strings.HasSuffix(nodes.calls[0].Content, truncationSuffix) {
		t.Error("node content missing truncation suffix")
	}
}

func TestMessageOrdering(t *testing.T) {
	// Insert deliberately out of (timestamp, id) order — the mapper must
	// still emit child nodes in (timestamp, id) order.
	path := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "o1", source: "cli", title: "O", startedAt: 100.0})
		insertMessage(t, conn, fixtureMessage{id: 30, sessionID: "o1", role: "assistant", content: "third", ts: 300.0})
		insertMessage(t, conn, fixtureMessage{id: 10, sessionID: "o1", role: "user", content: "first", ts: 100.0})
		insertMessage(t, conn, fixtureMessage{id: 20, sessionID: "o1", role: "assistant", content: "second", ts: 200.0})
	})
	imp, trees, nodes, _ := newTestImporter(t, path)

	if _, err := imp.Run(context.Background(), ImportOptions{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if trees.calls[0].RootContent != "first" {
		t.Errorf("root = %q, want first user message", trees.calls[0].RootContent)
	}
	if len(nodes.calls) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes.calls))
	}
	if nodes.calls[0].Content != "**assistant:** second" || nodes.calls[1].Content != "**assistant:** third" {
		t.Errorf("node order = %q, %q — want second, third", nodes.calls[0].Content, nodes.calls[1].Content)
	}
}

func TestReaderSkipsInactiveMessages(t *testing.T) {
	path := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "r1", source: "cli", title: "R", startedAt: 100.0})
		insertMessage(t, conn, fixtureMessage{id: 1, sessionID: "r1", role: "user", content: "keep", ts: 10})
		insertMessage(t, conn, fixtureMessage{id: 2, sessionID: "r1", role: "assistant", content: "hidden", ts: 20, inactive: true})
	})
	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	msgs, err := r.ListMessages(context.Background(), "r1")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "keep" || msgs[0].Role != "user" {
		t.Fatalf("messages = %+v, want only the active user message", msgs)
	}
}

func TestFileWatermarkStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "session-import.json")
	store := &FileWatermarkStore{Path: path}

	// Missing file → zero watermark, no error.
	wm, err := store.Load()
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if wm.LastSessionID != "" {
		t.Errorf("missing-file watermark = %+v, want zero", wm)
	}

	// Save + reload round-trip.
	now := time.Date(2026, 8, 9, 0, 12, 0, 0, time.UTC)
	want := Watermark{LastSessionID: "sess_x", LastStartedAt: 1234.5, LastImportedAt: now}
	if err := store.Save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}
