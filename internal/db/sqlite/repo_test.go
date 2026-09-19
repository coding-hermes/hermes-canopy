package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// ── shared helpers for the core-graph repository tests ───────────────────────────────
//
// Every test opens its own store under t.TempDir() (newTestStoreWithSchema) and reaches the
// repositories only through their public constructors, so the tests exercise real DDL,
// real SQL and real FK enforcement. There are no mocks and no skips in this file's package.

// newGraphRepos opens a schema-applied store plus the three core-graph repositories over it.
func newGraphRepos(t *testing.T) (*Store, *TreeRepo, *NodeRepo, *EdgeRepo) {
	t.Helper()
	s := newTestStoreWithSchema(t)
	return s, NewTreeRepo(s), NewNodeRepo(s), NewEdgeRepo(s)
}

// mustTree creates a tree owned by a fresh owner.
func mustTree(t *testing.T, ctx context.Context, r *TreeRepo, title string) *db.Tree {
	t.Helper()
	tree, err := r.Create(ctx, &db.Tree{OwnerID: uuid.New(), Title: title})
	if err != nil {
		t.Fatalf("TreeRepo.Create(%q): %v", title, err)
	}
	return tree
}

// mustNode creates a node with a fresh author. parentID is the display anchor (nil for a
// root); the graph walks that use edges are driven by mustEdge.
func mustNode(t *testing.T, ctx context.Context, r *NodeRepo, treeID uuid.UUID, parentID *uuid.UUID, content string) *db.Node {
	t.Helper()
	n, err := r.Create(ctx, &db.Node{
		TreeID:   treeID,
		AuthorID: uuid.New(),
		ParentID: parentID,
		Content:  content,
	})
	if err != nil {
		t.Fatalf("NodeRepo.Create(%q): %v", content, err)
	}
	return n
}

// mustEdge creates a reply edge from source to target.
func mustEdge(t *testing.T, ctx context.Context, r *EdgeRepo, treeID, sourceID, targetID uuid.UUID) *db.Edge {
	t.Helper()
	e, err := r.Create(ctx, &db.Edge{
		TreeID:   treeID,
		SourceID: sourceID,
		TargetID: targetID,
		EdgeType: db.EdgeTypeReply,
	})
	if err != nil {
		t.Fatalf("EdgeRepo.Create(%s -> %s): %v", sourceID, targetID, err)
	}
	return e
}

// indepHash is an INDEPENDENT sha256-over-UTF-8 implementation of the PostgreSQL formula
// `encode(sha256(convert_to(content,'UTF8')),'hex')`. It deliberately does not call
// ContentHash: the point of the hash tests is that the value the write path stores equals
// the value the PostgreSQL schema would have computed, so the expectation must not come
// from the production helper.
func indepHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// storedContentHash reads nodes.content_hash with plain SQL. db.Node carries no hash field,
// so this is the only way to observe the write-path obligation.
func storedContentHash(t *testing.T, ctx context.Context, s *Store, id uuid.UUID) string {
	t.Helper()
	var h string
	if err := s.DB().QueryRowContext(ctx,
		`SELECT content_hash FROM nodes WHERE id = ?`, id.String()).Scan(&h); err != nil {
		t.Fatalf("select content_hash: %v", err)
	}
	return h
}

// storedText reads one TEXT column of one row with plain SQL.
func storedText(t *testing.T, ctx context.Context, s *Store, query string, args ...any) string {
	t.Helper()
	var v string
	if err := s.DB().QueryRowContext(ctx, query, args...).Scan(&v); err != nil {
		t.Fatalf("select %q: %v", query, err)
	}
	return v
}

// setNodeEditedAt forces a node's edited_at to a sentinel instant, so a later no-op update
// can be distinguished from a real one (the trigger only fires when content or metadata
// actually change).
func setNodeEditedAt(t *testing.T, ctx context.Context, s *Store, id uuid.UUID, at time.Time) string {
	t.Helper()
	ts := EncodeTime(at)
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE nodes SET edited_at = ? WHERE id = ?`, ts, id.String()); err != nil {
		t.Fatalf("seed nodes.edited_at: %v", err)
	}
	return ts
}

// setTreeCreatedAt forces trees.created_at, so keyset pagination can be tested against
// distinct, known instants instead of millisecond-frequency inserts.
func setTreeCreatedAt(t *testing.T, ctx context.Context, s *Store, id uuid.UUID, at time.Time) {
	t.Helper()
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE trees SET created_at = ? WHERE id = ?`, EncodeTime(at), id.String()); err != nil {
		t.Fatalf("seed trees.created_at: %v", err)
	}
}

// countRows is a plain-SQL row count, used to prove that a rejected write left nothing
// behind.
func countRows(t *testing.T, ctx context.Context, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}
