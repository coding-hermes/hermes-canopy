// DF-HERMES-CANOPY-44 — NodeDetail.authorDisplayName was declared on the wire
// shape but NEVER populated: nodeToDetail() has no database access (db.Node
// carries no display-name column) and no node path queried users, so a fresh
// dev database answered POST /api/v1/trees/{id}/nodes with authorId
// 00000000-0000-0000-0000-000000000001 and authorDisplayName "".
//
// The fix resolves users.display_name in the service layer (the contract the
// merge path already implements). These tests pin:
//   - the helper contract without a database (unknown author -> "", query
//     failure -> ErrDatabaseUnavailable, empty batch -> no query), and
//   - every node surface against a REAL PostgreSQL: create / reply / get /
//     list / update carry the label, the provisioned dev user resolves
//     'Dev User', and an unknown author id stays empty WITHOUT an error.
//
// Run: CANOPY_TEST_ALLOW_SHARED_DB=1 go test -count=1 -run 'TestDF44' ./internal/service/
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// --- helper-contract tests (no database) ------------------------------------

// df44Row is a pgx.Row answering a display-name lookup from memory.
type df44Row struct {
	name string
	err  error
}

func (r df44Row) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return fmt.Errorf("df44Row: want 1 scan destination, got %d", len(dest))
	}
	target, ok := dest[0].(*string)
	if !ok {
		return fmt.Errorf("df44Row: scan destination is %T, want *string", dest[0])
	}
	*target = r.name
	return nil
}

// df44Querier is a displayNameQuerier whose single-row answer is scripted.
// Query is only reachable through the batch helper, which must not call it on
// empty input — a call is therefore recorded and reported as an error.
type df44Querier struct {
	row     pgx.Row
	queries int
}

func (q *df44Querier) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	q.queries++
	return q.row
}

func (q *df44Querier) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	q.queries++
	return nil, errors.New("df44Querier: unexpected Query call")
}

// TestDF44_ResolveAuthorDisplayNameContract pins the three outcomes the helper
// shares with the merge path.
func TestDF44_ResolveAuthorDisplayNameContract(t *testing.T) {
	ctx := context.Background()
	authorID := uuid.New()

	t.Run("known author returns the stored name", func(t *testing.T) {
		q := &df44Querier{row: df44Row{name: "Dev User"}}
		name, err := resolveAuthorDisplayName(ctx, q, authorID)
		if err != nil {
			t.Fatalf("resolveAuthorDisplayName: %v", err)
		}
		if name != "Dev User" {
			t.Errorf("name = %q, want %q", name, "Dev User")
		}
	})

	t.Run("unknown author is empty and NOT an error", func(t *testing.T) {
		q := &df44Querier{row: df44Row{err: pgx.ErrNoRows}}
		name, err := resolveAuthorDisplayName(ctx, q, authorID)
		if err != nil {
			t.Fatalf("unknown author returned an error: %v (nodes.author_id has no FK — must stay \"\" with nil error)", err)
		}
		if name != "" {
			t.Errorf("name = %q, want empty string", name)
		}
	})

	t.Run("query failure wraps ErrDatabaseUnavailable", func(t *testing.T) {
		q := &df44Querier{row: df44Row{err: errors.New("boom")}}
		name, err := resolveAuthorDisplayName(ctx, q, authorID)
		if !errors.Is(err, ErrDatabaseUnavailable) {
			t.Fatalf("error = %v, want it to wrap ErrDatabaseUnavailable", err)
		}
		if name != "" {
			t.Errorf("name = %q, want empty string on failure", name)
		}
		if !strings.Contains(err.Error(), "author display name") {
			t.Errorf("error = %q, want it to name the failing lookup", err.Error())
		}
	})
}

// TestDF44_ResolveAuthorDisplayNamesEmptyInputSkipsQuery pins the batch
// helper's short circuit: ListByTree on an empty tree must not spend a round
// trip (and must not error).
func TestDF44_ResolveAuthorDisplayNamesEmptyInputSkipsQuery(t *testing.T) {
	q := &df44Querier{row: df44Row{err: pgx.ErrNoRows}}
	names, err := resolveAuthorDisplayNames(context.Background(), q, nil)
	if err != nil {
		t.Fatalf("resolveAuthorDisplayNames(nil): %v", err)
	}
	if len(names) != 0 {
		t.Errorf("names = %v, want an empty map", names)
	}
	if q.queries != 0 {
		t.Errorf("queries = %d, want 0 (empty input must not hit the database)", q.queries)
	}
}

// --- database-backed tests ---------------------------------------------------

// df44Tree seeds a tree directly (no members FK needed, same shape the other
// PG-backed service tests use).
func df44Tree(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	treeID := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO trees (id, owner_id, title) VALUES ($1, $2, 'DF-44 author name tree')`,
		treeID, uuid.New()); err != nil {
		t.Fatalf("insert tree: %v", err)
	}
	return treeID
}

// df44Service returns a PG-backed node service plus the provisioned dev user
// id — the exact combination a fresh dev database presents.
func df44Service(t *testing.T, pool *pgxpool.Pool) (*NodeServiceImpl, uuid.UUID) {
	t.Helper()
	if _, err := db.EnsureDevJWTUser(context.Background(), pool); err != nil {
		t.Fatalf("EnsureDevJWTUser: %v", err)
	}
	return NewNodeService(db.NewPGNodeRepo(pool), db.NewPGEdgeRepo(pool), pool, nil),
		mustParseUUID(db.DevJWTUserID)
}

// TestDF44_NodeSurfacesCarryTheAuthorLabel is AC1 + AC2: create, reply, get,
// list and update all return users.display_name for the node's author, and the
// provisioned dev user reads back as 'Dev User'.
func TestDF44_NodeSurfacesCarryTheAuthorLabel(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	svc, devID := df44Service(t, pool)
	treeID := df44Tree(t, pool)

	const wantName = "Dev User"

	// AC1 — the POST /trees/{id}/nodes response.
	created, err := svc.Create(ctx, treeID, CreateNodeInput{
		Content:       "root message",
		ContentFormat: string(NodeFormatMarkdown),
		NodeType:      string(NodeKindMessage),
		EdgeType:      string(NodeEdgeReply),
		AuthorID:      devID,
		TreeID:        treeID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Node.AuthorID != devID {
		t.Fatalf("created authorId = %s, want the dev id %s", created.Node.AuthorID, devID)
	}
	if created.Node.AuthorDisplayName != wantName {
		t.Errorf("Create authorDisplayName = %q, want %q", created.Node.AuthorDisplayName, wantName)
	}

	// AC2 — the reply response.
	reply, err := svc.Reply(ctx, created.Node.ID, ReplyInput{
		Content:  "a reply",
		AuthorID: devID,
	})
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if reply.Node.AuthorDisplayName != wantName {
		t.Errorf("Reply authorDisplayName = %q, want %q", reply.Node.AuthorDisplayName, wantName)
	}

	// AC2 — fork goes through the same Create path and must not regress.
	// (Fork requires an existing child, which the reply above just provided.)
	fork, err := svc.Fork(ctx, created.Node.ID, ForkInput{Content: "a fork", AuthorID: devID})
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if fork.Node.AuthorDisplayName != wantName {
		t.Errorf("Fork authorDisplayName = %q, want %q", fork.Node.AuthorDisplayName, wantName)
	}

	// AC2 — GET /nodes/{id}.
	got, err := svc.GetByID(ctx, created.Node.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.AuthorDisplayName != wantName {
		t.Errorf("GetByID authorDisplayName = %q, want %q", got.AuthorDisplayName, wantName)
	}

	// AC2 — the PATCH response.
	edited := "root message (edited)"
	updated, err := svc.Update(ctx, created.Node.ID, UpdateNodeInput{Content: &edited})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.AuthorDisplayName != wantName {
		t.Errorf("Update authorDisplayName = %q, want %q", updated.AuthorDisplayName, wantName)
	}

	// AC2 — GET /trees/{id}/nodes (batched lookup, one query for the tree).
	list, err := svc.ListByTree(ctx, treeID)
	if err != nil {
		t.Fatalf("ListByTree: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("ListByTree returned %d nodes, want 3 (root + reply + fork)", len(list))
	}
	for _, n := range list {
		if n.AuthorDisplayName != wantName {
			t.Errorf("ListByTree node %s authorDisplayName = %q, want %q",
				n.ID, n.AuthorDisplayName, wantName)
		}
	}
}

// TestDF44_UnknownAuthorStaysEmptyWithoutError is AC3: an author id with no
// users row (nodes.author_id has no FK) answers "" on every surface instead of
// failing the request, while a known author in the SAME tree keeps its label.
func TestDF44_UnknownAuthorStaysEmptyWithoutError(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	svc, devID := df44Service(t, pool)
	treeID := df44Tree(t, pool)

	orphanAuthor := uuid.New() // deliberately never inserted into users

	// Create with an unknown author: empty label, no error.
	created, err := svc.Create(ctx, treeID, CreateNodeInput{
		Content:       "message from a vanished user",
		ContentFormat: string(NodeFormatMarkdown),
		NodeType:      string(NodeKindMessage),
		EdgeType:      string(NodeEdgeReply),
		AuthorID:      orphanAuthor,
		TreeID:        treeID,
	})
	if err != nil {
		t.Fatalf("Create with unknown author: %v (want success with an empty label)", err)
	}
	if created.Node.AuthorDisplayName != "" {
		t.Errorf("Create authorDisplayName = %q, want empty for an unknown author",
			created.Node.AuthorDisplayName)
	}

	// A known author in the same tree still resolves.
	devNode, err := svc.Create(ctx, treeID, CreateNodeInput{
		Content:       "message from the dev user",
		ContentFormat: string(NodeFormatMarkdown),
		NodeType:      string(NodeKindMessage),
		EdgeType:      string(NodeEdgeReply),
		AuthorID:      devID,
		TreeID:        treeID,
	})
	if err != nil {
		t.Fatalf("Create as dev user: %v", err)
	}
	if devNode.Node.AuthorDisplayName != "Dev User" {
		t.Errorf("Create authorDisplayName = %q, want %q", devNode.Node.AuthorDisplayName, "Dev User")
	}

	// GetByID on the unknown-author node: "" and no error.
	got, err := svc.GetByID(ctx, created.Node.ID)
	if err != nil {
		t.Fatalf("GetByID with unknown author: %v (want success)", err)
	}
	if got.AuthorDisplayName != "" {
		t.Errorf("GetByID authorDisplayName = %q, want empty", got.AuthorDisplayName)
	}

	// Update on the unknown-author node: "" and no error.
	edited := "edited by nobody"
	updated, err := svc.Update(ctx, created.Node.ID, UpdateNodeInput{Content: &edited})
	if err != nil {
		t.Fatalf("Update with unknown author: %v (want success)", err)
	}
	if updated.AuthorDisplayName != "" {
		t.Errorf("Update authorDisplayName = %q, want empty", updated.AuthorDisplayName)
	}

	// Reply from the unknown author: "" and no error.
	reply, err := svc.Reply(ctx, created.Node.ID, ReplyInput{Content: "reply", AuthorID: orphanAuthor})
	if err != nil {
		t.Fatalf("Reply with unknown author: %v (want success)", err)
	}
	if reply.Node.AuthorDisplayName != "" {
		t.Errorf("Reply authorDisplayName = %q, want empty", reply.Node.AuthorDisplayName)
	}

	// ListByTree resolves per author: the dev node keeps its name, the orphan
	// nodes stay empty — one batched lookup, no error.
	list, err := svc.ListByTree(ctx, treeID)
	if err != nil {
		t.Fatalf("ListByTree: %v", err)
	}
	names := make(map[uuid.UUID]string, len(list))
	for _, n := range list {
		names[n.ID] = n.AuthorDisplayName
	}
	if got := names[devNode.Node.ID]; got != "Dev User" {
		t.Errorf("ListByTree dev node authorDisplayName = %q, want %q", got, "Dev User")
	}
	if got, ok := names[created.Node.ID]; !ok || got != "" {
		t.Errorf("ListByTree orphan node authorDisplayName = %q (present=%v), want empty and present", got, ok)
	}
}

// TestDF44_MergePathStillResolvesTheAuthorLabel is AC4's direct check: the
// shared helper did not change the merge path's §3.7 contract — its own
// authorDisplayName wrapper answers the same way, including for an unknown
// author.
func TestDF44_MergePathStillResolvesTheAuthorLabel(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	if _, err := db.EnsureDevJWTUser(ctx, pool); err != nil {
		t.Fatalf("EnsureDevJWTUser: %v", err)
	}
	devID := mustParseUUID(db.DevJWTUserID)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	name, err := authorDisplayName(ctx, tx, devID)
	if err != nil {
		t.Fatalf("merge authorDisplayName(dev): %v", err)
	}
	if name != "Dev User" {
		t.Errorf("merge authorDisplayName = %q, want %q", name, "Dev User")
	}

	name, err = authorDisplayName(ctx, tx, uuid.New())
	if err != nil {
		t.Fatalf("merge authorDisplayName(unknown): %v (want \"\" and nil)", err)
	}
	if name != "" {
		t.Errorf("merge authorDisplayName(unknown) = %q, want empty", name)
	}
}
