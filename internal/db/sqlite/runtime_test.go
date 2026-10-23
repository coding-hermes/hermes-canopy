package sqlite

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/testutil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestOpenRuntimeAppliesSchemaAndParity(t *testing.T) {
	store, err := OpenRuntime(context.Background(), t.TempDir()+"/canopy.sqlite")
	if err != nil {
		t.Fatalf("OpenRuntime() = %v", err)
	}
	defer store.Close()
	version, err := CoreSchemaVersion(context.Background(), store)
	if err != nil {
		t.Fatalf("CoreSchemaVersion() = %v", err)
	}
	if version != EmbeddedCoreVersion {
		t.Fatalf("CoreSchemaVersion() = %d, want %d", version, EmbeddedCoreVersion)
	}
	if err := CheckCoreSchema(context.Background(), store); err != nil {
		t.Fatalf("CheckCoreSchema() after OpenRuntime() = %v", err)
	}
}

func TestOpenRuntimeRejectsNewerAppliedMigration(t *testing.T) {
	path := t.TempDir() + "/future.sqlite"
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TABLE canopy_schema_migrations (name TEXT NOT NULL PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO canopy_schema_migrations (name, applied_at) VALUES ('000011_future.up.sql', 'now')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRuntime(context.Background(), path); err == nil || !strings.Contains(err.Error(), "STALE BUILD") {
		t.Fatalf("OpenRuntime() = %v, want stale-build refusal", err)
	}
}

func TestCheckCoreSchemaRejectsDrift(t *testing.T) {
	store, err := OpenRuntime(context.Background(), t.TempDir()+"/canopy.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB().Exec(`ALTER TABLE trees RENAME COLUMN title TO title_drift`); err != nil {
		t.Fatal(err)
	}
	if err := CheckCoreSchema(context.Background(), store); err == nil {
		t.Fatal("CheckCoreSchema() = nil after a column rename, want drift error")
	}
}

func TestExportPostgresRoundTripCoreGraph(t *testing.T) {
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	userID := uuid.New()
	treeID := uuid.New()
	nodeID := uuid.New()
	node2ID := uuid.New()
	edgeID := uuid.New()
	content := "GAP-097 export fixture"
	hash := sha256Hex(content)
	_, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, email, display_name) VALUES ($1,$2,$3,$4)`, userID, "gap097-hermes", "gap097@example.com", "GAP-097")
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO trees (id, owner_id, title, description, metadata) VALUES ($1,$2,$3,$4,'{}')`, treeID, userID, "GAP-097", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO nodes (id,tree_id,author_id,content,content_format,node_type,sequence_num,metadata,content_hash) VALUES ($1,$2,$3,$4,'plain','message',1,'{}',$5)`, nodeID, treeID, userID, content, hash)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO nodes (id,tree_id,author_id,content,content_format,node_type,sequence_num,metadata,content_hash) VALUES ($1,$2,$3,$4,'plain','message',2,'{}',$5)`, node2ID, treeID, userID, "second node", sha256Hex("second node"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO edges (id,tree_id,source_id,target_id,edge_type,sequence_num,metadata) VALUES ($1,$2,$3,$4,'reply',1,'{}')`, edgeID, treeID, nodeID, node2ID)
	if err != nil {
		t.Fatal(err)
	}

	target := t.TempDir() + "/export.sqlite"
	if err := ExportPostgres(ctx, pool.Config().ConnConfig.ConnString(), target); err != nil {
		t.Fatalf("ExportPostgres() = %v", err)
	}
	store, err := OpenRuntime(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for table, want := range map[string]int{"users": 1, "trees": 1, "nodes": 2, "edges": 1} {
		var count int
		if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != want {
			t.Errorf("count %s = %d, want %d", table, count, want)
		}
	}
}

// TestExportPostgresConcurrentWriterStaysConsistent is the DF-HERMES-CANOPY-97
// AC-1 regression: against a live PostgreSQL whose writer keeps inserting
// nodes+edges (>=20 writes), every export attempt must either succeed with a
// referentially consistent snapshot (AC-2: foreign_key_check empty, no orphan
// parents or edge endpoints) or fail with an error naming concurrent source
// writes. Before the fix, 12/12 dogfood attempts (run 18) failed with the bare
// "FOREIGN KEY constraint failed" / "row count mismatch" messages instead.
func TestExportPostgresConcurrentWriterStaysConsistent(t *testing.T) {
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	dsn := pool.Config().ConnConfig.ConnString()

	userID := uuid.New()
	treeID := uuid.New()
	rootID := uuid.New()
	rootContent := "DF-97 concurrent root"
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, email, display_name) VALUES ($1,$2,$3,$4)`, userID, "df97-hermes", "df97@example.com", "DF-97"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trees (id, owner_id, title, description, metadata) VALUES ($1,$2,$3,$4,'{}')`, treeID, userID, "DF-97", "concurrent fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO nodes (id,tree_id,author_id,content,content_format,node_type,sequence_num,metadata,content_hash) VALUES ($1,$2,$3,$4,'plain','message',1,'{}',$5)`, rootID, treeID, userID, rootContent, sha256Hex(rootContent)); err != nil {
		t.Fatal(err)
	}

	writer := newConcurrentNodeWriter(t, dsn, treeID, userID, rootID)
	defer writer.stop()

	const attempts = 10
	for i := 0; i < attempts; i++ {
		target := filepath.Join(t.TempDir(), fmt.Sprintf("export-%d.sqlite", i))
		err := ExportPostgres(ctx, dsn, target)
		if err == nil {
			assertExportConsistent(t, ctx, target)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "concurrent") && !strings.Contains(msg, "source changed") {
			t.Fatalf("attempt %d: export failed with an error that does not name concurrent source writes: %v", i, err)
		}
		if strings.Contains(msg, "FOREIGN KEY constraint failed") || strings.Contains(msg, "row count mismatch") {
			t.Fatalf("attempt %d: export failed with a pre-fix bare symptom: %v", i, err)
		}
		if _, statErr := os.Stat(target); statErr == nil {
			t.Fatalf("attempt %d: failed export still published the target file", i)
		}
	}
	if got := writer.writes(); got < 20 {
		t.Fatalf("concurrent writer performed %d writes, want >= 20 (fixture did not stress the export)", got)
	}
}

// concurrentNodeWriter inserts child nodes plus a reply edge referencing the
// previous node on its own connection until stopped, counting successful writes.
type concurrentNodeWriter struct {
	t        *testing.T
	cancel   context.CancelFunc
	done     chan struct{}
	counter  atomic.Int64
	failOnce sync.Once
}

func newConcurrentNodeWriter(t *testing.T, dsn string, treeID, userID, rootID uuid.UUID) *concurrentNodeWriter {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("writer connect: %v", err)
	}
	wctx, cancel := context.WithCancel(context.Background())
	w := &concurrentNodeWriter{t: t, cancel: cancel, done: make(chan struct{})}
	prev := rootID
	seq := int64(2)
	go func() {
		defer close(w.done)
		defer conn.Close(wctx)
		for wctx.Err() == nil {
			child := uuid.New()
			content := fmt.Sprintf("DF-97 concurrent child %d", seq)
			if _, err := conn.Exec(wctx, `INSERT INTO nodes (id,tree_id,parent_id,author_id,content,content_format,node_type,sequence_num,metadata,content_hash) VALUES ($1,$2,$3,$4,$5,'plain','message',$6,'{}',$7)`,
				child, treeID, prev, userID, content, seq, sha256Hex(content)); err != nil {
				if wctx.Err() == nil {
					w.failOnce.Do(func() { t.Errorf("writer node insert: %v", err) })
				}
				return
			}
			edge := uuid.New()
			if _, err := conn.Exec(wctx, `INSERT INTO edges (id,tree_id,source_id,target_id,edge_type,sequence_num,metadata) VALUES ($1,$2,$3,$4,'reply',$5,'{}')`,
				edge, treeID, prev, child, seq); err != nil {
				if wctx.Err() == nil {
					w.failOnce.Do(func() { t.Errorf("writer edge insert: %v", err) })
				}
				return
			}
			w.counter.Add(2)
			prev = child
			seq++
			time.Sleep(2 * time.Millisecond)
		}
	}()
	return w
}

func (w *concurrentNodeWriter) writes() int64 { return w.counter.Load() }

func (w *concurrentNodeWriter) stop() {
	w.cancel()
	<-w.done
}

// assertExportConsistent pins AC-2 on a published file: PRAGMA foreign_key_check
// empty, no node parent or edge endpoint missing from the same file.
func assertExportConsistent(t *testing.T, ctx context.Context, target string) {
	t.Helper()
	store, err := OpenRuntime(ctx, target)
	if err != nil {
		t.Fatalf("open exported file: %v", err)
	}
	defer store.Close()
	if rows, err := store.DB().QueryContext(ctx, `PRAGMA foreign_key_check`); err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	} else {
		violations := 0
		for rows.Next() {
			violations++
		}
		_ = rows.Err()
		rows.Close()
		if violations != 0 {
			t.Fatalf("exported file has %d foreign_key_check violations", violations)
		}
	}
	var orphans int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM nodes n WHERE n.parent_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM nodes p WHERE p.id = n.parent_id)`).Scan(&orphans); err != nil {
		t.Fatalf("orphan parent count: %v", err)
	}
	if orphans != 0 {
		t.Fatalf("exported file has %d orphan node parents", orphans)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM edges e WHERE NOT EXISTS (SELECT 1 FROM nodes s WHERE s.id = e.source_id) OR NOT EXISTS (SELECT 1 FROM nodes t WHERE t.id = e.target_id)`).Scan(&orphans); err != nil {
		t.Fatalf("orphan edge count: %v", err)
	}
	if orphans != 0 {
		t.Fatalf("exported file has %d orphan edge endpoints", orphans)
	}
}

// TestConcurrencyErrorNaming proves the AC-1 error mapping in isolation: a
// serialization failure surfaces as an actionable message naming concurrent
// writes, and any other error passes through unchanged so the documented
// exit-code incumbents keep their messages.
func TestConcurrencyErrorNaming(t *testing.T) {
	for _, code := range []string{"40001", "40P01"} {
		base := &pgconn.PgError{Code: code, Message: "could not serialize access due to concurrent update"}
		got := concurrencyError(fmt.Errorf("sqlite export: read nodes: %w", base))
		if !strings.Contains(got.Error(), "concurrent") || !strings.Contains(got.Error(), "source changed") {
			t.Fatalf("concurrencyError(SQLSTATE %s) = %v, want message naming concurrent source changes", code, got)
		}
		if !errors.Is(got, base) {
			t.Fatalf("concurrencyError(SQLSTATE %s) must wrap the original error", code)
		}
	}
	plain := errors.New("some other failure")
	if got := concurrencyError(plain); got != plain {
		t.Fatalf("concurrencyError(pass-through) = %v, want the original error unchanged", got)
	}
	if got := concurrencyError(nil); got != nil {
		t.Fatalf("concurrencyError(nil) = %v, want nil", got)
	}
}

func sha256Hex(value string) string {
	// Kept local to this integration fixture so the expected PG trigger value
	// is explicit at the test boundary.
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}
