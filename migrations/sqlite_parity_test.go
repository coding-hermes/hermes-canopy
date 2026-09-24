// SQLite pivot parity harness — GAP-076 wave 1.
//
// TestSQLiteParity is the load-bearing artifact of the wave: it applies the SQLite
// translation of migrations 000001..000010 to a real (in-memory) SQLite database and
// proves, against the PostgreSQL DDL read back through the embedded FS(), that
//
//	(a) the batch applies cleanly, and unwinds cleanly: after the .down files run in
//	    reverse numeric order no batch table, index or trigger survives;
//	(b) every PostgreSQL table is translated — same table set (no orphan, no silent
//	    skip), same column-name set, same NOT NULL flags, same primary key;
//	(c) every PostgreSQL index is translated under the same name, except the entries in
//	    pgOnlyIndexExemptions, each of which must name its PG file:line and wave-2 owner;
//	(d) every PostgreSQL enum is translated as TEXT + CHECK (col IN (..)) with the same
//	    value list (except the deliberately narrowed entries in enumColumnExemptions,
//	    which must remain a non-empty subset);
//	(e) every FK that PostgreSQL adds with ALTER TABLE .. ADD CONSTRAINT is declared
//	    inline on the SQLite side;
//	(f) the SQLite DDL contains no PostgreSQL-only syntax;
//	(g) nothing is dropped silently: every PG trigger is either recreated under the same
//	    name or listed in retiredPGObjects, and every retired PG function/extension name
//	    appears in the SQLite DDL comments with its file:line and owner.
//
// A parity test that cannot fail is not evidence. Both failure modes were exercised
// before this test was accepted: deleting a translated column (names the table+column)
// and adding an orphan table (names the orphan).
package migrations

import (
	"database/sql"
	"fmt"
	iofs "io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// waveOneBatch is the highest migration number translated by this wave (000001..000010).
const waveOneBatch = 10

// migNameRe matches the canonical migration file name.
var migNameRe = regexp.MustCompile(`^(\d{6})_([a-z0-9_]+)\.(up|down)\.sql$`)

// pgRefRe matches a "<NNNNNN_name.up.sql>:<line>" reference, used to validate that every
// exemption and retirement entry points at a real place in the PostgreSQL batch.
var pgRefRe = regexp.MustCompile(`(\d{6}_[a-z0-9_]+\.up\.sql):(\d+)`)

// pgOnlyIndexExemptions lists PostgreSQL indexes in the batch that have no SQLite DDL
// translation. Every reason MUST name the PostgreSQL file:line that owns the clause and
// the wave-2 owner; assertJustification enforces both so an exemption cannot be added
// without a decision behind it.
var pgOnlyIndexExemptions = map[string]string{
	"idx_nodes_content_fts": "000003_nodes.up.sql:35 — PG builds it with USING gin(to_tsvector('english', content)); SQLite has neither GIN nor tsvector. Owner: wave-2 search/query layer (internal/db). FTS5-external-content vs LIKE is an open decision in docs/SQLITE-PIVOT.md.",
}

// enumColumnExemptions lists enum-typed columns whose SQLite CHECK deliberately narrows
// the PostgreSQL enum value list. Reasons carry the same file:line + owner obligation.
var enumColumnExemptions = map[string]string{
	"approval_rules.decision": "000009_approvals.up.sql:109 — PG itself narrows approval_status to ('approved','denied') for this column via CONSTRAINT ck_rule_decision; the SQLite CHECK mirrors that narrowed list. Owner: wave-2 approval repo (internal/db/approval_repo.go).",
}

// retiredPGObjects lists PostgreSQL schema objects whose name cannot exist in SQLite:
// either the logic is translated inline under another mechanism, or it becomes a wave-2
// Go write-path obligation. Each name MUST appear in the SQLite batch text (comments
// included) and, for trigger/index kinds, MUST NOT exist as a SQLite object.
var retiredPGObjects = []struct {
	name   string
	kind   string // trigger | index | function | extension
	reason string
}{
	{"pgcrypto", "extension", "000001_extensions.up.sql:7 — SQLite has no CREATE EXTENSION. Owner: nothing to port (pgcrypto only supplied gen_random_bytes/digest for the fallback generator)."},
	{"pg_uuidv7", "extension", "000001_extensions.up.sql:10 — SQLite has no CREATE EXTENSION and no bundled UUIDv7. Owner: wave-2 repo layer (id generation)."},
	{"uuidv7", "function", "000001_extensions.up.sql:21 — PL/pgSQL fallback generator behind every `DEFAULT uuidv7()`. SQLite has no stored functions and no uuid function (verified: `SELECT sha3('a',256)` -> no such function). Owner: wave-2 repo layer generates RFC 9562 UUIDv7 on insert."},
	{"set_node_sequence", "function", "000003_nodes.up.sql:38 — fills NEW.sequence_num from MAX+1 per tree. Owner: wave-2 repo layer node-insert path (same transaction)."},
	{"trg_node_sequence", "trigger", "000003_nodes.up.sql:48 — SQLite cannot assign NEW.<col> in a BEFORE trigger (verified: `SET NEW.x` -> syntax error) and sequence_num is NOT NULL, so an AFTER trigger cannot repair the row. Owner: wave-2 repo layer node-insert path."},
	{"idx_nodes_content_fts", "index", "000003_nodes.up.sql:35 — GIN full-text index; see pgOnlyIndexExemptions. Owner: wave-2 search/query layer."},
	{"set_content_hash", "function", "000006_node_content_hash.up.sql:5 (last defined 000025_node_content_hash_utf8.up.sql:7) — sha256 hashing inside a trigger. Owner: wave-2 repo layer computes sha256 over UTF-8 content on every node insert/update and backfills on first open."},
	{"trg_node_content_hash", "trigger", "000006_node_content_hash.up.sql:11 — needs sha256; SQLite has no hash function (verified). Owner: wave-2 repo layer."},
	{"trigger_set_updated_at", "function", "000008_users_profiles.up.sql:208 — translated by inlining its single statement into set_users_updated_at / set_profiles_updated_at. Owner: none (translated)."},
	{"set_edited_at", "function", "000003_nodes.up.sql:55 — translated by inlining into the trg_node_edited_at SQLite trigger. Owner: none (translated)."},
	{"update_approval_rule_timestamp", "function", "000009_approvals.up.sql:152 — translated by inlining into the trg_approval_rules_updated SQLite trigger. Owner: none (translated)."},
	{"expire_pending_approvals", "function", "000009_approvals.up.sql:166 — RETURNING-based SQL function; SQLite has no stored functions. Owner: wave-2 expiry job issues the equivalent UPDATE .. RETURNING statement from Go."},
	{"REVOKE", "function", "000009_approvals.up.sql:144 — `REVOKE UPDATE, DELETE ON approval_audit_log FROM PUBLIC` (repeated for the canopy_app role in 000019_canopy_role.up.sql:14, outside this wave). SQLite has no privilege model — and the PG clause is itself a no-op because a new table grants nothing to PUBLIC. Owner: wave-2 audit repo if real immutability is wanted (a BEFORE UPDATE/DELETE trigger with RAISE(ABORT) is expressible)."},
}

// translatedTriggers are the PostgreSQL trigger names that must exist in the SQLite
// schema under the SAME name (the wave-2 behaviour depends on them).
var translatedTriggers = map[string]string{
	"trg_node_edited_at":         "000003_nodes.up.sql:62 — set_edited_at() inlined; WHEN OLD.x IS NOT NEW.x is the SQLite spelling of IS DISTINCT FROM. Owner: none (fully translated).",
	"set_users_updated_at":       "000008_users_profiles.up.sql:217 — trigger_set_updated_at() inlined. Owner: none (fully translated).",
	"set_profiles_updated_at":    "000008_users_profiles.up.sql:221 — trigger_set_updated_at() inlined. Owner: none (fully translated).",
	"trg_approval_rules_updated": "000009_approvals.up.sql:161 — update_approval_rule_timestamp() inlined. Owner: none (fully translated).",
}

// postgresOnlyTokens are the PostgreSQL-only constructs that must never appear in the
// comment-stripped SQLite DDL.
var postgresOnlyTokens = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bjsonb\b`),
	regexp.MustCompile(`(?i)\btimestamptz\b`),
	regexp.MustCompile(`(?i)\bbytea\b`),
	regexp.MustCompile(`(?i)\btsvector\b`),
	regexp.MustCompile(`(?i)\bplpgsql\b`),
	regexp.MustCompile(`(?i)\bclock_timestamp\b`),
	regexp.MustCompile(`(?i)\bchar_length\b`),
	regexp.MustCompile(`(?i)\buuidv7\b`),
	regexp.MustCompile(`(?i)\bgen_random_uuid\b`),
	regexp.MustCompile(`(?i)\bnow\s*\(\s*\)`),
	regexp.MustCompile(`(?i)\buuid\b`),
	regexp.MustCompile(`(?i)\bbigserial\b|\bserial\b`),
	regexp.MustCompile(`(?i)create\s+extension`),
	regexp.MustCompile(`(?i)create\s+(or\s+replace\s+)?function`),
	regexp.MustCompile(`(?i)create\s+type\b`),
	regexp.MustCompile(`(?i)create\s+(role|schema|database)\b`),
	regexp.MustCompile(`(?i)\b(revoke|grant)\b`),
	regexp.MustCompile(`(?i)using\s+gin|using\s+hash`),
	regexp.MustCompile(`(?i)distinct\s+on`),
	regexp.MustCompile(`(?i)\bgenerated\s+always\b`),
	regexp.MustCompile(`::`),
}

// ── statement lexer ──────────────────────────────────────────────────────────────────

type sqlStmt struct {
	text string
	line int
}

// splitStatements lexes SQL and splits it into statements at top-level `;`. It is
// comment-safe (-- and /* */ are removed), string-safe ('..' with ” escapes),
// dollar-quote-safe ($$ .. $$ bodies are copied verbatim, so the `;` inside PL/pgSQL
// bodies does not split a statement) and paren-depth-aware.
//
// It does not track BEGIN..END blocks, so a `;` inside a SQLite trigger body does split
// the statement. That is harmless for this harness: no CREATE TABLE / CREATE INDEX
// statement lives inside a trigger body, and the inventory itself comes from
// PRAGMA table_info / sqlite_master rather than from this lexer.
func splitStatements(src string) []sqlStmt {
	var out []sqlStmt
	var b strings.Builder
	line, startLine := 1, 1
	pending := true // true until the first non-space character of the current statement is written
	// state: 0 = normal, 1 = line comment, 2 = block comment, 3 = single-quoted string
	state := 0
	dollar := ""
	i, n := 0, len(src)

	write := func(s string) {
		// Note: the newline that follows a ';' is written into the builder too, so the
		// "statement is still empty" test has to look at content, not at b.Len().
		if pending && strings.TrimSpace(s) != "" {
			startLine = line
			pending = false
		}
		b.WriteString(s)
	}

	for i < n {
		c := src[i]
		switch state {
		case 1: // line comment
			if c == '\n' {
				state = 0
				line++
			}
			i++
			continue
		case 2: // block comment
			if c == '*' && i+1 < n && src[i+1] == '/' {
				state = 0
				i += 2
				continue
			}
			if c == '\n' {
				line++
			}
			i++
			continue
		case 3: // single-quoted string
			write(string(c))
			if c == '\'' {
				if i+1 < n && src[i+1] == '\'' {
					write("'")
					i += 2
					continue
				}
				state = 0
			}
			if c == '\n' {
				line++
			}
			i++
			continue
		}

		if dollar != "" {
			if strings.HasPrefix(src[i:], dollar) {
				write(dollar)
				i += len(dollar)
				dollar = ""
				continue
			}
			if c == '\n' {
				line++
			}
			write(string(c))
			i++
			continue
		}

		switch {
		case c == '-' && i+1 < n && src[i+1] == '-':
			state = 1
			i += 2
		case c == '/' && i+1 < n && src[i+1] == '*':
			state = 2
			i += 2
		case c == '\'':
			state = 3
			write(string(c))
			i++
		case c == '$':
			if tag, w := dollarTag(src[i:]); tag != "" {
				dollar = tag
				write(tag)
				i += w
			} else {
				write(string(c))
				i++
			}
		case c == ';':
			text := strings.TrimSpace(b.String())
			if text != "" {
				out = append(out, sqlStmt{text: text, line: startLine})
			}
			b.Reset()
			i++
			startLine = line
			pending = true
		default:
			if c == '\n' {
				line++
			}
			write(string(c))
			i++
		}
	}
	if text := strings.TrimSpace(b.String()); text != "" {
		out = append(out, sqlStmt{text: text, line: startLine})
	}
	return out
}

// dollarTag returns the dollar-quote tag starting at s (e.g. "$$") and its width.
func dollarTag(s string) (string, int) {
	if len(s) == 0 || s[0] != '$' {
		return "", 0
	}
	for j := 1; j < len(s) && j < 64; j++ {
		switch {
		case s[j] == '$':
			return s[:j+1], j + 1
		case s[j] == '_' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9':
			continue
		default:
			return "", 0
		}
	}
	return "", 0
}

// stripComments returns src with comments and dollar-quoted bodies removed, for token
// scans that must not be confused by prose in comments.
func stripComments(src string) string {
	var b strings.Builder
	for _, st := range splitStatements(src) {
		// Dollar-quoted bodies survive splitStatements verbatim; drop them here so a
		// function body cannot smuggle PostgreSQL tokens into the syntax scan.
		b.WriteString(dropDollarBodies(st.text))
		b.WriteString(";\n")
	}
	return b.String()
}

func dropDollarBodies(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '$' {
			if tag, w := dollarTag(s[i:]); tag != "" {
				end := strings.Index(s[i+w:], tag)
				if end < 0 {
					break
				}
				b.WriteString(" ")
				i += w + end + len(tag)
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// ── PostgreSQL inventory ─────────────────────────────────────────────────────────────

type pgColumn struct {
	name    string
	notNull bool
	typeTok string
	def     string
	ref     string
}

type pgTable struct {
	name string
	ref  string
	cols map[string]*pgColumn
	pk   []string
}

type pgFKAlter struct {
	table string
	col   string
	refT  string
	ref   string
}

type pgBatch struct {
	raw       string
	cleaned   string
	lineCount map[string]int
	tables    map[string]*pgTable
	enums     map[string][]string
	indexes   map[string]string
	triggers  map[string]string
	functions map[string]string
	fkAlters  []pgFKAlter
}

func parsePGBatch(t *testing.T) *pgBatch {
	t.Helper()
	pg := &pgBatch{
		lineCount: map[string]int{},
		tables:    map[string]*pgTable{},
		enums:     map[string][]string{},
		indexes:   map[string]string{},
		triggers:  map[string]string{},
		functions: map[string]string{},
	}
	up := batchFiles(t, FS(), "PostgreSQL")
	var raw, cleaned strings.Builder
	for n := 1; n <= waveOneBatch; n++ {
		name := up[n].up
		data, err := iofs.ReadFile(FS(), name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(data)
		raw.WriteString(src)
		raw.WriteString("\n")
		cleaned.WriteString(stripComments(src))
		pg.lineCount[name] = strings.Count(src, "\n") + 1

		// Enums live inside `DO $$ BEGIN CREATE TYPE .. AS ENUM (..) END $$;` blocks, so
		// they are scanned from the cleaned text rather than from a parsed statement.
		for _, m := range enumRe.FindAllStringSubmatchIndex(src, -1) {
			enumName := src[m[2]:m[3]]
			pg.enums[enumName] = enumValues(src[m[4]:m[5]])
		}

		for _, st := range splitStatements(src) {
			ref := fmt.Sprintf("%s:%d", name, st.line)
			parsePGStatement(pg, st.text, ref)
		}
	}
	pg.raw = raw.String()
	pg.cleaned = cleaned.String()
	return pg
}

var enumRe = regexp.MustCompile(`(?is)CREATE\s+TYPE\s+([a-z_][a-z0-9_]*)\s+AS\s+ENUM\s*\(([^)]*)\)`)

func enumValues(list string) []string {
	var out []string
	for _, v := range strings.Split(list, ",") {
		v = strings.TrimSpace(v)
		v = strings.Trim(v, "'")
		if v != "" {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

var (
	createTableRe = regexp.MustCompile(`(?is)^CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	alterAddColRe = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)\s+(.*)$`)
	alterSetNNRe  = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+([A-Za-z_][A-Za-z0-9_]*)\s+ALTER\s+(?:COLUMN\s+)?([A-Za-z_][A-Za-z0-9_]*)\s+SET\s+NOT\s+NULL$`)
	alterDropNNRe = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+([A-Za-z_][A-Za-z0-9_]*)\s+ALTER\s+(?:COLUMN\s+)?([A-Za-z_][A-Za-z0-9_]*)\s+DROP\s+NOT\s+NULL$`)
	alterFKRe     = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+([A-Za-z_][A-Za-z0-9_]*)\s+ADD\s+CONSTRAINT\s+([A-Za-z_][A-Za-z0-9_]*)\s+FOREIGN\s+KEY\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)\s*REFERENCES\s+([A-Za-z_][A-Za-z0-9_]*)`)
	createIndexRe = regexp.MustCompile(`(?is)^CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)`)
	createTrgRe   = regexp.MustCompile(`(?is)^CREATE\s+TRIGGER\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)`)
	createFnRe    = regexp.MustCompile(`(?is)^CREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+([A-Za-z_][A-Za-z0-9_]*)`)
	notNullRe     = regexp.MustCompile(`(?i)\bNOT\s+NULL\b`)
)

func parsePGStatement(pg *pgBatch, text, ref string) {
	if m := createTableRe.FindStringSubmatch(text); m != nil {
		name := strings.ToLower(m[1])
		open := strings.Index(text, "(")
		close := matchParen(text, open)
		if close < 0 {
			return
		}
		tbl := pg.tables[name]
		if tbl == nil {
			tbl = &pgTable{name: name, ref: ref, cols: map[string]*pgColumn{}}
			pg.tables[name] = tbl
		}
		for _, chunk := range splitTopLevel(text[open+1 : close]) {
			chunk = strings.TrimSpace(chunk)
			if chunk == "" {
				continue
			}
			upper := strings.ToUpper(chunk)
			if strings.HasPrefix(upper, "CONSTRAINT") || strings.HasPrefix(upper, "PRIMARY KEY") ||
				strings.HasPrefix(upper, "UNIQUE") || strings.HasPrefix(upper, "CHECK") ||
				strings.HasPrefix(upper, "FOREIGN KEY") {
				if strings.HasPrefix(upper, "PRIMARY KEY") {
					tbl.pk = append(tbl.pk, columnList(chunk)...)
				}
				continue
			}
			fields := strings.Fields(chunk)
			if len(fields) < 2 {
				continue
			}
			colName := strings.ToLower(fields[0])
			def := strings.TrimSpace(strings.TrimPrefix(chunk, fields[0]))
			col := &pgColumn{name: colName, typeTok: strings.ToLower(fields[1]), def: def, ref: ref}
			col.notNull = isNotNull(def)
			if strings.Contains(strings.ToUpper(def), "PRIMARY KEY") {
				col.notNull = true
				tbl.pk = append(tbl.pk, colName)
			}
			tbl.cols[colName] = col
		}
		return
	}
	if m := alterAddColRe.FindStringSubmatch(text); m != nil {
		tbl := pg.tables[strings.ToLower(m[1])]
		if tbl == nil {
			return
		}
		name := strings.ToLower(m[2])
		def := strings.TrimSpace(m[3])
		tbl.cols[name] = &pgColumn{name: name, typeTok: "?", def: def, ref: ref, notNull: isNotNull(def)}
		return
	}
	if m := alterSetNNRe.FindStringSubmatch(text); m != nil {
		if tbl := pg.tables[strings.ToLower(m[1])]; tbl != nil {
			if col := tbl.cols[strings.ToLower(m[2])]; col != nil {
				col.notNull = true
			}
		}
		return
	}
	if m := alterDropNNRe.FindStringSubmatch(text); m != nil {
		if tbl := pg.tables[strings.ToLower(m[1])]; tbl != nil {
			if col := tbl.cols[strings.ToLower(m[2])]; col != nil {
				col.notNull = false
			}
		}
		return
	}
	if m := alterFKRe.FindStringSubmatch(text); m != nil {
		pg.fkAlters = append(pg.fkAlters, pgFKAlter{
			table: strings.ToLower(m[1]),
			col:   strings.ToLower(m[3]),
			refT:  strings.ToLower(m[4]),
			ref:   ref,
		})
		return
	}
	if m := createIndexRe.FindStringSubmatch(text); m != nil {
		pg.indexes[strings.ToLower(m[1])] = ref
		return
	}
	if m := createTrgRe.FindStringSubmatch(text); m != nil {
		pg.triggers[strings.ToLower(m[1])] = ref
		return
	}
	if m := createFnRe.FindStringSubmatch(text); m != nil {
		pg.functions[strings.ToLower(m[1])] = ref
		return
	}
}

// isNotNull reports whether a column definition declares NOT NULL. `IS NOT NULL` inside
// a column-level CHECK is not a declaration, so those occurrences are skipped.
func isNotNull(def string) bool {
	up := strings.ToUpper(def)
	for off := 0; ; {
		idx := strings.Index(up[off:], "NOT NULL")
		if idx < 0 {
			return false
		}
		abs := off + idx
		before := strings.TrimSpace(up[:abs])
		if !strings.HasSuffix(before, "IS") {
			return true
		}
		off = abs + len("NOT NULL")
	}
}

func columnList(s string) []string {
	open := strings.Index(s, "(")
	if open < 0 {
		return nil
	}
	close := matchParen(s, open)
	if close < 0 {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s[open+1:close], ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, strings.ToLower(part))
		}
	}
	return out
}

// matchParen returns the index of the ')' matching the '(' at open (string-aware).
func matchParen(s string, open int) int {
	if open < 0 {
		return -1
	}
	depth, inStr := 0, false
	for i := open; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\'' {
				inStr = false
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTopLevel splits s on commas that are not nested in parentheses or strings.
func splitTopLevel(s string) []string {
	var out []string
	depth, inStr := 0, false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\'' {
				inStr = false
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

// ── batch file discovery ─────────────────────────────────────────────────────────────

type batch struct {
	num    int
	name   string
	up     string
	down   string
	upText string
}

// batchFiles returns the up/down file names for migrations 000001..000010 of fsys.
func batchFiles(t *testing.T, fsys iofs.FS, label string) map[int]*struct{ up, down string } {
	t.Helper()
	entries, err := iofs.ReadDir(fsys, ".")
	if err != nil {
		t.Fatalf("%s: read embedded dir: %v", label, err)
	}
	out := map[int]*struct{ up, down string }{}
	for _, e := range entries {
		m := migNameRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		num := atoi(t, m[1])
		if num > waveOneBatch {
			continue
		}
		if out[num] == nil {
			out[num] = &struct{ up, down string }{}
		}
		if m[3] == "up" {
			out[num].up = e.Name()
		} else {
			out[num].down = e.Name()
		}
	}
	for n := 1; n <= waveOneBatch; n++ {
		f := out[n]
		if f == nil || f.up == "" || f.down == "" {
			t.Fatalf("%s: migration %06d is missing an up or down file (got %+v)", label, n, f)
		}
	}
	return out
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("non-numeric migration number %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// ── SQLite side ──────────────────────────────────────────────────────────────────────

type sqliteTableInfo struct {
	name string
	cols map[string]int // column name -> notnull flag
	pk   []string       // primary-key columns in pk-ordinal order
	body string         // raw CREATE TABLE body (SQLite text), for CHECK/FK checks
}

type sqliteInv struct {
	tables   map[string]*sqliteTableInfo
	indexes  map[string]bool
	triggers map[string]bool
	raw      string
	cleaned  string
}

func loadSQLiteBatch(t *testing.T) (map[int]string, map[int]string) {
	t.Helper()
	files := batchFiles(t, SQLiteFS(), "sqlite")
	up := map[int]string{}
	down := map[int]string{}
	for n := 1; n <= waveOneBatch; n++ {
		u, err := iofs.ReadFile(SQLiteFS(), files[n].up)
		if err != nil {
			t.Fatalf("read sqlite %s: %v", files[n].up, err)
		}
		d, err := iofs.ReadFile(SQLiteFS(), files[n].down)
		if err != nil {
			t.Fatalf("read sqlite %s: %v", files[n].down, err)
		}
		up[n] = string(u)
		down[n] = string(d)
	}
	return up, down
}

func openMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	// A `:memory:` database lives inside its connection. database/sql would happily open
	// a second connection and hand back an empty database, so pin the pool to one.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func applyUp(t *testing.T, up map[int]string) *sql.DB {
	t.Helper()
	db := openMemoryDB(t)
	for n := 1; n <= waveOneBatch; n++ {
		if _, err := db.Exec(up[n]); err != nil {
			t.Fatalf("apply sqlite %06d up: %v", n, err)
		}
	}
	return db
}

func applyDown(t *testing.T, db *sql.DB, down map[int]string) {
	t.Helper()
	for n := waveOneBatch; n >= 1; n-- {
		if _, err := db.Exec(down[n]); err != nil {
			t.Fatalf("apply sqlite %06d down: %v", n, err)
		}
	}
}

func introspectSQLite(t *testing.T, db *sql.DB) *sqliteInv {
	t.Helper()
	inv := &sqliteInv{tables: map[string]*sqliteTableInfo{}, indexes: map[string]bool{}, triggers: map[string]bool{}}

	rows, err := db.Query(`SELECT type, name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	var names []struct{ typ, name string }
	for rows.Next() {
		var typ, name string
		if err := rows.Scan(&typ, &name); err != nil {
			t.Fatal(err)
		}
		names = append(names, struct{ typ, name string }{typ, name})
	}
	rows.Close()

	for _, e := range names {
		switch e.typ {
		case "table":
			inv.tables[e.name] = tableInfo(t, db, e.name)
		case "index":
			inv.indexes[e.name] = true
		case "trigger":
			inv.triggers[e.name] = true
		}
	}
	return inv
}

func tableInfo(t *testing.T, db *sql.DB, name string) *sqliteTableInfo {
	t.Helper()
	info := &sqliteTableInfo{name: name, cols: map[string]int{}}
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%q)", name))
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", name, err)
	}
	type pkCol struct {
		ord  int
		name string
	}
	var pks []pkCol
	for rows.Next() {
		var cid, notnull, pk int
		var colName, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &colName, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		info.cols[strings.ToLower(colName)] = notnull
		if pk > 0 {
			pks = append(pks, pkCol{pk, strings.ToLower(colName)})
		}
	}
	rows.Close()
	sort.Slice(pks, func(i, j int) bool { return pks[i].ord < pks[j].ord })
	for _, p := range pks {
		info.pk = append(info.pk, p.name)
	}
	var ddl sql.NullString
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name = ?`, name).Scan(&ddl); err == nil {
		info.body = ddl.String
	}
	return info
}

// parseSQLiteSchema extracts the CREATE TABLE bodies from the SQLite text, so the enum
// CHECK lists and inline FKs can be inspected as written.
func parseSQLiteSchema(up map[int]string) map[string]string {
	bodies := map[string]string{}
	for n := 1; n <= waveOneBatch; n++ {
		for _, st := range splitStatements(up[n]) {
			m := createTableRe.FindStringSubmatch(st.text)
			if m == nil {
				continue
			}
			open := strings.Index(st.text, "(")
			close := matchParen(st.text, open)
			if close < 0 {
				continue
			}
			bodies[strings.ToLower(m[1])] = st.text[open+1 : close]
		}
	}
	return bodies
}

type sqliteColDef struct {
	def string
}

// sqliteColumns maps "<table>.<column>" to its raw definition text.
func sqliteColumns(bodies map[string]string) map[string]sqliteColDef {
	out := map[string]sqliteColDef{}
	for tbl, body := range bodies {
		for _, chunk := range splitTopLevel(body) {
			chunk = strings.TrimSpace(chunk)
			if chunk == "" {
				continue
			}
			upper := strings.ToUpper(chunk)
			if strings.HasPrefix(upper, "CONSTRAINT") || strings.HasPrefix(upper, "PRIMARY KEY") ||
				strings.HasPrefix(upper, "UNIQUE") || strings.HasPrefix(upper, "CHECK") ||
				strings.HasPrefix(upper, "FOREIGN KEY") {
				continue
			}
			fields := strings.Fields(chunk)
			if len(fields) < 2 {
				continue
			}
			out[tbl+"."+strings.ToLower(fields[0])] = sqliteColDef{def: chunk}
		}
	}
	return out
}

// ── the test ─────────────────────────────────────────────────────────────────────────

func TestSQLiteParity(t *testing.T) {
	pg := parsePGBatch(t)
	up, down := loadSQLiteBatch(t)
	db := applyUp(t, up)
	inv := introspectSQLite(t, db)

	var sqliteRaw, sqliteCleaned strings.Builder
	for n := 1; n <= waveOneBatch; n++ {
		sqliteRaw.WriteString(up[n])
		sqliteRaw.WriteString("\n")
		sqliteCleaned.WriteString(stripComments(up[n]))
	}
	inv.raw = sqliteRaw.String()
	inv.cleaned = sqliteCleaned.String()
	bodies := parseSQLiteSchema(up)
	cols := sqliteColumns(bodies)

	t.Run("table-inventory-parity", func(t *testing.T) { checkTableParity(t, pg, inv) })
	t.Run("index-parity", func(t *testing.T) { checkIndexParity(t, pg, inv) })
	t.Run("enum-check-parity", func(t *testing.T) { checkEnumParity(t, pg, cols) })
	t.Run("inline-fk-parity", func(t *testing.T) { checkFKParity(t, pg, bodies) })
	t.Run("no-postgres-only-syntax", func(t *testing.T) { checkNoPostgresSyntax(t, up) })
	t.Run("no-silent-object-drop", func(t *testing.T) { checkNoSilentDrop(t, pg, inv) })
	t.Run("migration-000001-is-a-documented-noop", func(t *testing.T) { check000001Noop(t, up) })
	t.Run("embed-scope-is-unchanged", func(t *testing.T) { checkEmbedScope(t) })
	t.Run("down-migrations-clean-up", func(t *testing.T) { checkDown(t, db, down) })
}

// checkEmbedScope pins the property that makes the added subdirectory safe, plus the
// coverage claim: `*.sql` stays a single-directory pattern, so FS() exposes regular
// migration files only (never a directory entry for sqlite/), and the SQLite set has no
// file outside the batch the parity harness actually applies.
func checkEmbedScope(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		label string
		fsys  iofs.FS
	}{
		{"FS() (PostgreSQL)", FS()},
		{"SQLiteFS()", SQLiteFS()},
	} {
		entries, err := iofs.ReadDir(tc.fsys, ".")
		if err != nil {
			t.Fatalf("%s: ReadDir: %v", tc.label, err)
		}
		if len(entries) == 0 {
			t.Fatalf("%s: ReadDir returned nothing", tc.label)
		}
		for _, e := range entries {
			if e.IsDir() {
				t.Errorf("%s: ReadDir exposes directory %q — the embed pattern now matches more than *.sql",
					tc.label, e.Name())
				continue
			}
			if migNameRe.FindStringSubmatch(e.Name()) == nil {
				t.Errorf("%s: ReadDir exposes %q, which is not a NNNNNN_name.up|down.sql migration file",
					tc.label, e.Name())
			}
		}
	}
	entries, err := iofs.ReadDir(SQLiteFS(), ".")
	if err != nil {
		t.Fatalf("SQLiteFS(): ReadDir: %v", err)
	}
	for _, e := range entries {
		m := migNameRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if num := atoi(t, m[1]); num > waveOneBatch {
			t.Errorf("UNTRANSLATED SQLITE FILE: sqlite/%s is outside the wave-1 batch (1..%d), so the parity "+
				"harness never applies it — extend the batch and waveOneBatch before adding later-wave files",
				e.Name(), waveOneBatch)
		}
	}
}

func checkTableParity(t *testing.T, pg *pgBatch, inv *sqliteInv) {
	t.Helper()
	for name := range inv.tables {
		if _, ok := pg.tables[name]; !ok {
			t.Errorf("ORPHAN TABLE: sqlite defines %q which the PostgreSQL batch does not (pg tables: %v); "+
				"delete it or add the matching PG migration to the batch", name, sortedKeys(pg.tables))
		}
	}
	for name, want := range pg.tables {
		got, ok := inv.tables[name]
		if !ok {
			t.Errorf("MISSING TABLE: PG %s (defined at %s) with %d columns is not translated in the SQLite batch; "+
				"no silent skips allowed — translate it or record the clause in docs/SQLITE-PIVOT.md",
				name, want.ref, len(want.cols))
			continue
		}
		for col, wc := range want.cols {
			gn, ok := got.cols[col]
			if !ok {
				t.Errorf("COLUMN MISSING: %s.%s (PG %s) is absent from the SQLite table (sqlite has %v)",
					name, col, wc.ref, sortedKeyInts(got.cols))
				continue
			}
			if wc.notNull && gn == 0 {
				t.Errorf("NULLABILITY MISMATCH: %s.%s is NOT NULL in PostgreSQL (%s) but nullable in SQLite",
					name, col, wc.ref)
			}
			if !wc.notNull && gn != 0 {
				t.Errorf("NULLABILITY MISMATCH: %s.%s is nullable in PostgreSQL (%s) but NOT NULL in SQLite",
					name, col, wc.ref)
			}
		}
		for col := range got.cols {
			if _, ok := want.cols[col]; !ok {
				t.Errorf("ORPHAN COLUMN: sqlite defines %s.%s which PostgreSQL (%s) does not",
					name, col, want.ref)
			}
		}
		wantPK := append([]string(nil), want.pk...)
		sort.Strings(wantPK)
		gotPK := append([]string(nil), got.pk...)
		sort.Strings(gotPK)
		if strings.Join(wantPK, ",") != strings.Join(gotPK, ",") {
			t.Errorf("PRIMARY KEY MISMATCH: %s pk is [%s] in PostgreSQL (%s) but [%s] in SQLite",
				name, strings.Join(wantPK, ","), want.ref, strings.Join(gotPK, ","))
		}
	}
}

func checkIndexParity(t *testing.T, pg *pgBatch, inv *sqliteInv) {
	t.Helper()
	for name, ref := range pg.indexes {
		if _, exempt := pgOnlyIndexExemptions[name]; exempt {
			if inv.indexes[name] {
				t.Errorf("STALE EXEMPTION: %q is exempted as untranslatable but the SQLite batch defines it", name)
			}
			continue
		}
		if !inv.indexes[name] {
			t.Errorf("INDEX MISSING: PG index %s (%s) has no SQLite translation under the same name "+
				"(sqlite indexes: %v)", name, ref, sortedBoolKeys(inv.indexes))
		}
	}
	for name := range inv.indexes {
		if _, ok := pg.indexes[name]; !ok {
			t.Errorf("ORPHAN INDEX: sqlite defines index %q which the PostgreSQL batch does not; "+
				"indexes must keep the PG names so the inventory stays comparable", name)
		}
	}
	for name, reason := range pgOnlyIndexExemptions {
		if _, ok := pg.indexes[name]; !ok {
			t.Errorf("STALE EXEMPTION: index %q is exempted but no longer exists in the PostgreSQL batch (%s)", name, reason)
		}
		assertJustification(t, fmt.Sprintf("pgOnlyIndexExemptions[%s]", name), reason, pg)
	}
}

func checkEnumParity(t *testing.T, pg *pgBatch, cols map[string]sqliteColDef) {
	t.Helper()
	inListRe := regexp.MustCompile(`(?is)IN\s*\(([^)]*)\)`)
	checked := 0
	names := sortedKeys(pg.tables)
	for _, tblName := range names {
		tbl := pg.tables[tblName]
		for colName, col := range tbl.cols {
			values, isEnum := pg.enums[col.typeTok]
			if !isEnum {
				continue
			}
			checked++
			key := tblName + "." + colName
			def, ok := cols[key]
			if !ok {
				t.Errorf("ENUM COLUMN MISSING: %s has no SQLite column definition to check (PG %s)", key, col.ref)
				continue
			}
			m := inListRe.FindStringSubmatch(def.def)
			if m == nil {
				t.Errorf("ENUM CHECK MISSING: %s is of PG enum type %s (%s) but its SQLite definition has no "+
					"CHECK (col IN (..)): %q", key, col.typeTok, col.ref, def.def)
				continue
			}
			got := enumValues(m[1])
			if reason, exempt := enumColumnExemptions[key]; exempt {
				if len(got) == 0 || !isSubset(got, values) {
					t.Errorf("BAD NARROWING EXEMPTION: %s SQLite values %v are not a non-empty subset of PG enum %s %v (%s)",
						key, got, col.typeTok, values, reason)
				}
				assertJustification(t, fmt.Sprintf("enumColumnExemptions[%s]", key), reason, pg)
				continue
			}
			if strings.Join(got, ",") != strings.Join(values, ",") {
				t.Errorf("ENUM VALUE MISMATCH: %s SQLite CHECK allows [%s] but PG enum %s (%s) allows [%s]",
					key, strings.Join(got, ","), col.typeTok, col.ref, strings.Join(values, ","))
			}
		}
	}
	if checked == 0 {
		t.Fatalf("enum parity checked 0 columns — the PG enum scan found no enum-typed columns (%d enums): the check is vacuous", len(pg.enums))
	}
}

func checkFKParity(t *testing.T, pg *pgBatch, bodies map[string]string) {
	t.Helper()
	if len(pg.fkAlters) == 0 {
		t.Fatalf("inline FK parity found no ALTER TABLE .. ADD CONSTRAINT FOREIGN KEY in the PG batch: the check is vacuous")
	}
	for _, fk := range pg.fkAlters {
		body, ok := bodies[fk.table]
		if !ok {
			t.Errorf("FK TARGET MISSING: PG adds a FK from %s.%s to %s (%s) but %s is not translated",
				fk.table, fk.col, fk.refT, fk.ref, fk.table)
			continue
		}
		if !strings.Contains(strings.ToLower(body), "references "+fk.refT) {
			t.Errorf("FK NOT DECLARED: PG adds a FK from %s.%s to %s (%s); SQLite cannot ALTER TABLE .. ADD "+
				"CONSTRAINT, so the FK must be declared inline on the SQLite %s definition",
				fk.table, fk.col, fk.refT, fk.ref, fk.table)
		}
	}
}

func checkNoPostgresSyntax(t *testing.T, up map[int]string) {
	t.Helper()
	for n := 1; n <= waveOneBatch; n++ {
		clean := dropDollarBodies(stripComments(up[n]))
		for _, re := range postgresOnlyTokens {
			if loc := re.FindStringIndex(clean); loc != nil {
				ctx := clean[maxInt(0, loc[0]-40):minInt(len(clean), loc[1]+40)]
				t.Errorf("POSTGRES-ONLY SYNTAX in sqlite migration %06d: %q appears in %q "+
					"(only comments may mention PostgreSQL constructs)", n, re.String(), strings.TrimSpace(ctx))
			}
		}
	}
}

func checkNoSilentDrop(t *testing.T, pg *pgBatch, inv *sqliteInv) {
	t.Helper()
	retired := map[string]string{}
	for _, r := range retiredPGObjects {
		retired[r.name] = r.reason
	}
	// Every PG trigger is either recreated under the same name or explicitly retired.
	for name, ref := range pg.triggers {
		if _, ok := translatedTriggers[name]; ok {
			if !inv.triggers[name] {
				t.Errorf("TRIGGER MISSING: %s is declared translated (%s) but the SQLite schema has no such trigger",
					name, ref)
			}
			continue
		}
		reason, ok := retired[name]
		if !ok {
			t.Errorf("SILENT TRIGGER DROP: PG trigger %s (%s) is neither translated (translatedTriggers) nor "+
				"retired with a file:line + owner (retiredPGObjects)", name, ref)
			continue
		}
		assertJustification(t, fmt.Sprintf("retiredPGObjects[%s]", name), reason, pg)
		if inv.triggers[name] {
			t.Errorf("STALE RETIREMENT: %s is listed as retired but the SQLite schema defines it", name)
		}
	}
	for name, reason := range translatedTriggers {
		if _, ok := pg.triggers[name]; !ok {
			t.Errorf("STALE TRANSLATION ENTRY: %s is claimed translated but no such PG trigger exists in the batch", name)
		}
		assertJustification(t, fmt.Sprintf("translatedTriggers[%s]", name), reason, pg)
	}
	// Functions and extensions cannot exist in SQLite, so they must be NAMED in the
	// SQLite text (comments included) instead of silently disappearing.
	for _, r := range retiredPGObjects {
		if r.kind != "function" && r.kind != "extension" {
			continue
		}
		if !strings.Contains(strings.ToLower(inv.raw), strings.ToLower(r.name)) {
			t.Errorf("SILENT OBJECT DROP: PG %s %q is retired (%s) but is never named in the SQLite DDL",
				r.kind, r.name, r.reason)
		}
		assertJustification(t, fmt.Sprintf("retiredPGObjects[%s]", r.name), r.reason, pg)
	}
	// Every PG function/extension in the batch must be accounted for by name.
	for name, ref := range pg.functions {
		if _, ok := retired[name]; !ok {
			t.Errorf("UNACCOUNTED PG FUNCTION: %s (%s) is not mentioned in retiredPGObjects — name it with its "+
				"wave-2 owner or translate it", name, ref)
		}
	}
	if len(pg.functions) == 0 {
		t.Fatalf("function scan found no CREATE FUNCTION in the PG batch: the check is vacuous")
	}
}

func check000001Noop(t *testing.T, up map[int]string) {
	t.Helper()
	// The 000001 pair is a documented no-op: comments only, and the reason must name both
	// the missing mechanism and the wave-2 owner.
	down, err := iofs.ReadFile(SQLiteFS(), "000001_extensions.down.sql")
	if err != nil {
		t.Fatalf("read 000001_extensions.down.sql: %v", err)
	}
	for fn, src := range map[string]string{
		"000001_extensions.up.sql":   up[1],
		"000001_extensions.down.sql": string(down),
	} {
		if clean := strings.TrimSpace(dropDollarBodies(stripComments(src))); clean != "" {
			t.Errorf("%s must contain no SQLite DDL (extensions/uuidv7 have no SQLite form) but has: %q", fn, clean)
		}
		low := strings.ToLower(src)
		if !strings.Contains(low, "no-op") {
			t.Errorf("%s must say it is a no-op", fn)
		}
		if !strings.Contains(low, "extension") {
			t.Errorf("%s must name the missing mechanism (CREATE EXTENSION)", fn)
		}
		if !strings.Contains(low, "uuidv7") {
			t.Errorf("%s must name the uuidv7() DEFAULT that becomes a Go write-path obligation", fn)
		}
	}
}

func checkDown(t *testing.T, db *sql.DB, down map[int]string) {
	t.Helper()
	before := countSchemaObjects(t, db)
	if before == 0 {
		t.Fatal("schema is empty before the down pass — the up pass did not create anything")
	}
	applyDown(t, db, down)
	if left := countSchemaObjects(t, db); left != 0 {
		rows, err := db.Query(`SELECT type, name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
		if err != nil {
			t.Fatal(err)
		}
		var leftNames []string
		for rows.Next() {
			var typ, name string
			if err := rows.Scan(&typ, &name); err != nil {
				t.Fatal(err)
			}
			leftNames = append(leftNames, typ+":"+name)
		}
		rows.Close()
		sort.Strings(leftNames)
		t.Errorf("DOWN MIGRATIONS INCOMPLETE: %d object(s) survive the reverse pass: %v", left, leftNames)
	}
}

func countSchemaObjects(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`).Scan(&n); err != nil {
		t.Fatalf("count sqlite_master: %v", err)
	}
	return n
}

// assertJustification enforces the "no exemption without a named owner" rule: every
// exemption/retirement reason must reference a real PG file:line inside the batch and
// name a wave-2 owner.
func assertJustification(t *testing.T, label, reason string, pg *pgBatch) {
	t.Helper()
	m := pgRefRe.FindStringSubmatch(reason)
	if m == nil {
		t.Errorf("JUSTIFICATION MISSING: %s carries no `<NNNNNN_name.up.sql>:<line>` reference (%q)", label, reason)
		return
	}
	file, lineStr := m[1], m[2]
	count, ok := pg.lineCount[file]
	if !ok {
		t.Errorf("JUSTIFICATION STALE: %s references %s, which is not part of the wave-1 batch", label, file)
		return
	}
	line := atoi(t, lineStr)
	if line < 1 || line > count {
		t.Errorf("JUSTIFICATION STALE: %s references %s:%d but that file has only %d lines", label, file, line, count)
	}
	if !strings.Contains(strings.ToLower(reason), "owner") {
		t.Errorf("JUSTIFICATION INCOMPLETE: %s must name the wave-2 owner (%q)", label, reason)
	}
}

// ── small helpers ────────────────────────────────────────────────────────────────────

func sortedKeys(m map[string]*pgTable) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeyInts(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedBoolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func isSubset(sub, super []string) bool {
	set := map[string]bool{}
	for _, s := range super {
		set[s] = true
	}
	for _, s := range sub {
		if !set[s] {
			return false
		}
	}
	return true
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
