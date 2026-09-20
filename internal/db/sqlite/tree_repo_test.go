package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

func TestSQLiteTreeRepoCRUD(t *testing.T) {
	s, trees, nodes, _ := newGraphRepos(t)
	ctx := context.Background()

	owner := uuid.New()
	other := uuid.New()
	callerSupplied := uuid.New()

	created, err := trees.Create(ctx, &db.Tree{
		ID:          callerSupplied,
		OwnerID:     owner,
		Title:       "Alpha",
		Description: "first tree",
		Metadata:    []byte(`{"k":"v"}`),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == uuid.Nil {
		t.Fatal("Create returned a nil id")
	}
	if created.ID == callerSupplied {
		t.Errorf("Create honored the caller-supplied id %s; the store generates ids (PG parity)", callerSupplied)
	}
	if created.OwnerID != owner {
		t.Errorf("owner_id = %s, want %s", created.OwnerID, owner)
	}
	if created.Title != "Alpha" || created.Description != "first tree" {
		t.Errorf("title/description = %q/%q", created.Title, created.Description)
	}
	if string(created.Metadata) != `{"k":"v"}` {
		t.Errorf("metadata = %q, want the stored JSON", created.Metadata)
	}
	if created.CreatedAt.IsZero() || created.CreatedAt.Location() != time.UTC {
		t.Errorf("created_at = %v, want a non-zero UTC instant", created.CreatedAt)
	}
	if created.EditedAt != nil || created.DeletedAt != nil || created.RootNodeID != nil {
		t.Errorf("edited_at/deleted_at/root_node_id = %v/%v/%v, want nil", created.EditedAt, created.DeletedAt, created.RootNodeID)
	}

	// The stored timestamp must be in the canonical D1 shape (RFC3339, UTC, ms, Z).
	storedAt := storedText(t, ctx, s, `SELECT created_at FROM trees WHERE id = ?`, created.ID.String())
	if _, err := DecodeTime(storedAt); err != nil {
		t.Errorf("stored created_at %q does not decode as the canonical shape: %v", storedAt, err)
	}

	got, err := trees.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != created.ID || got.Title != created.Title || got.OwnerID != owner {
		t.Errorf("GetByID returned %+v, want the created row", got)
	}

	// Ownership is a stored, queryable field — this is what the access layer filters on.
	owned, err := trees.GetByOwner(ctx, owner)
	if err != nil {
		t.Fatalf("GetByOwner(owner): %v", err)
	}
	if len(owned) != 1 || owned[0].ID != created.ID {
		t.Errorf("GetByOwner(owner) = %d rows, want exactly the created tree", len(owned))
	}
	if otherOwned, err := trees.GetByOwner(ctx, other); err != nil {
		t.Fatalf("GetByOwner(other): %v", err)
	} else if len(otherOwned) != 0 {
		t.Errorf("GetByOwner(other) = %d rows, want 0", len(otherOwned))
	}

	// Update replaces the mutable fields and bumps edited_at.
	created.Title = "Alpha renamed"
	created.Description = "second"
	created.Metadata = []byte(`{"k":"v2"}`)
	updated, err := trees.Update(ctx, created)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != "Alpha renamed" || updated.Description != "second" {
		t.Errorf("Update returned title/description %q/%q", updated.Title, updated.Description)
	}
	if string(updated.Metadata) != `{"k":"v2"}` {
		t.Errorf("Update metadata = %q", updated.Metadata)
	}
	if updated.EditedAt == nil {
		t.Error("Update did not set edited_at")
	} else if _, err := DecodeTime(EncodeTime(*updated.EditedAt)); err != nil {
		t.Errorf("edited_at %v is not in the canonical shape: %v", updated.EditedAt, err)
	}

	// List / Count.
	listed, err := trees.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Errorf("List = %d rows, want the created tree", len(listed))
	}
	if n, err := trees.Count(ctx); err != nil || n != 1 {
		t.Errorf("Count = %d (err %v), want 1", n, err)
	}

	// Search is ASCII case-insensitive; an empty query is ErrNotFound, as in the PG repo.
	hits, err := trees.Search(ctx, "alph", 10, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != created.ID {
		t.Errorf("Search(\"alph\") = %d rows, want the renamed tree", len(hits))
	}
	if _, err := trees.Search(ctx, "", 10, 0); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Search(\"\") error = %v, want db.ErrNotFound", err)
	}
	if none, err := trees.Search(ctx, "no-such-title", 10, 0); err != nil || len(none) != 0 {
		t.Errorf("Search(no match) = %d rows (err %v), want 0", len(none), err)
	}

	// CountNodesByTreeIDs: a tree with no nodes is absent from the map (zero by convention).
	counts, err := trees.CountNodesByTreeIDs(ctx, []uuid.UUID{created.ID})
	if err != nil {
		t.Fatalf("CountNodesByTreeIDs: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("CountNodesByTreeIDs(empty tree) = %v, want an empty map", counts)
	}
	if empty, err := trees.CountNodesByTreeIDs(ctx, nil); err != nil || len(empty) != 0 {
		t.Errorf("CountNodesByTreeIDs(nil) = %v (err %v), want an empty map without a query", empty, err)
	}
	// The counts are live: with a node present the tree appears with its real count.
	mustNode(t, ctx, nodes, created.ID, nil, "counted")
	if counts, err = trees.CountNodesByTreeIDs(ctx, []uuid.UUID{created.ID}); err != nil {
		t.Fatalf("CountNodesByTreeIDs(with a node): %v", err)
	} else if counts[created.ID] != 1 {
		t.Errorf("CountNodesByTreeIDs = %v, want %s:1", counts, created.ID)
	}

	// Soft delete removes the row from every active read path.
	if err := trees.SoftDelete(ctx, created.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := trees.GetByID(ctx, created.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetByID after SoftDelete error = %v, want db.ErrNotFound", err)
	}
	if err := trees.SoftDelete(ctx, created.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("second SoftDelete error = %v, want db.ErrNotFound", err)
	}
	if listed, err := trees.List(ctx, 10, 0); err != nil || len(listed) != 0 {
		t.Errorf("List after SoftDelete = %d rows (err %v), want 0", len(listed), err)
	}
	if n, err := trees.Count(ctx); err != nil || n != 0 {
		t.Errorf("Count after SoftDelete = %d (err %v), want 0", n, err)
	}
	// The row is still on disk (soft delete flips deleted_at), unlike a hard delete.
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM trees WHERE id = ?`, created.ID.String()); n != 1 {
		t.Errorf("soft-deleted tree row count = %d, want the row to survive with deleted_at set", n)
	}
	// And its nodes are untouched: the cascade belongs to hard deletes, so a soft delete
	// cannot destroy the graph behind the tree.
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM nodes WHERE tree_id = ?`, created.ID.String()); n != 1 {
		t.Errorf("node count under a soft-deleted tree = %d, want 1 (no cascade on soft delete)", n)
	}
}

func TestSQLiteTreeRepoErrorSemantics(t *testing.T) {
	_, trees, _, _ := newGraphRepos(t)
	ctx := context.Background()

	if _, err := trees.Create(ctx, nil); err == nil {
		t.Error("Create(nil) succeeded, want an error")
	}
	if _, err := trees.Update(ctx, nil); err == nil {
		t.Error("Update(nil) succeeded, want an error")
	}
	if _, err := trees.Update(ctx, &db.Tree{ID: uuid.New(), Title: "ghost"}); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Update(unknown id) error = %v, want db.ErrNotFound", err)
	}
	if _, err := trees.GetByID(ctx, uuid.New()); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetByID(unknown id) error = %v, want db.ErrNotFound", err)
	}
	if err := trees.SoftDelete(ctx, uuid.New()); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("SoftDelete(unknown id) error = %v, want db.ErrNotFound", err)
	}
	if rows, err := trees.List(ctx, 0, 0); err != nil || len(rows) != 0 {
		t.Errorf("List on an empty store = %d rows (err %v), want 0", len(rows), err)
	}
}

func TestSQLiteTreeRepoMetadataIsStoredAsJSONText(t *testing.T) {
	s, trees, _, _ := newGraphRepos(t)
	ctx := context.Background()

	created, err := trees.Create(ctx, &db.Tree{OwnerID: uuid.New(), Title: "json", Metadata: []byte(`{"a":1}`)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A []byte argument would land as blob storage class, which the edited_at trigger then
	// reads as a CHANGE on every no-op update (package comment, point 5). TEXT is what the
	// DDL declares and what json_valid/json_extract expect.
	if typ := storedText(t, ctx, s, `SELECT typeof(metadata) FROM trees WHERE id = ?`, created.ID.String()); typ != "text" {
		t.Errorf("typeof(metadata) = %q, want \"text\"", typ)
	}
	if valid := storedText(t, ctx, s, `SELECT json_valid(metadata) FROM trees WHERE id = ?`, created.ID.String()); valid != "1" {
		t.Errorf("json_valid(metadata) = %q, want 1", valid)
	}
	// An empty metadata is stored as '{}' (the PG repo's COALESCE), never as a NULL or an
	// empty string that would fail the CHECK.
	blank, err := trees.Create(ctx, &db.Tree{OwnerID: uuid.New(), Title: "no metadata"})
	if err != nil {
		t.Fatalf("Create without metadata: %v", err)
	}
	if string(blank.Metadata) != "{}" {
		t.Errorf("empty metadata stored as %q, want {}", blank.Metadata)
	}
}

func TestSQLiteTreeRepoListKeysetPaginatesWithoutGaps(t *testing.T) {
	s, trees, _, _ := newGraphRepos(t)
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	created := make([]*db.Tree, 0, 4)
	for i := 0; i < 4; i++ {
		tree := mustTree(t, ctx, trees, fmt.Sprintf("tree-%d", i))
		setTreeCreatedAt(t, ctx, s, tree.ID, base.Add(time.Duration(i)*time.Minute))
		created = append(created, tree)
	}
	// A soft-deleted tree with the newest timestamp must not appear in any page.
	newest := mustTree(t, ctx, trees, "deleted-newest")
	setTreeCreatedAt(t, ctx, s, newest.ID, base.Add(time.Hour))
	if err := trees.SoftDelete(ctx, newest.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	page1, err := trees.ListKeyset(ctx, nil, 2)
	if err != nil {
		t.Fatalf("ListKeyset(first page): %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("first page = %d rows, want 2", len(page1))
	}
	if page1[0].ID != created[3].ID || page1[1].ID != created[2].ID {
		t.Errorf("first page order = [%s %s], want the two newest, newest first", page1[0].ID, page1[1].ID)
	}

	cursor := page1[len(page1)-1].ID
	page2, err := trees.ListKeyset(ctx, &cursor, 2)
	if err != nil {
		t.Fatalf("ListKeyset(second page): %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("second page = %d rows, want 2", len(page2))
	}
	if page2[0].ID != created[1].ID || page2[1].ID != created[0].ID {
		t.Errorf("second page = [%s %s], want the two oldest", page2[0].ID, page2[1].ID)
	}

	tail, err := trees.ListKeyset(ctx, &page2[1].ID, 2)
	if err != nil {
		t.Fatalf("ListKeyset(tail page): %v", err)
	}
	if len(tail) != 0 {
		t.Errorf("tail page = %d rows, want 0 (no gaps, no repeats)", len(tail))
	}

	seen := map[uuid.UUID]bool{}
	for _, page := range [][]db.Tree{page1, page2} {
		for _, tree := range page {
			if seen[tree.ID] {
				t.Errorf("keyset pagination returned %s twice", tree.ID)
			}
			seen[tree.ID] = true
		}
	}
	for _, tree := range created {
		if !seen[tree.ID] {
			t.Errorf("keyset pagination skipped %s", tree.ID)
		}
	}
}

func TestSQLiteTreeRepoSchemaConstraintsAreEnforced(t *testing.T) {
	_, trees, _, _ := newGraphRepos(t)
	ctx := context.Background()

	// chk_tree_title: PG's char_length(title) <= 500.
	_, err := trees.Create(ctx, &db.Tree{OwnerID: uuid.New(), Title: strings.Repeat("x", 501)})
	if err == nil {
		t.Fatal("Create with a 501-character title succeeded, want the CHECK to reject it")
	}
	// An empty title is legal (the DDL default), a 500-character one is the boundary.
	if _, err := trees.Create(ctx, &db.Tree{OwnerID: uuid.New(), Title: strings.Repeat("x", 500)}); err != nil {
		t.Errorf("Create with a 500-character title failed: %v", err)
	}
	if _, err := trees.Create(ctx, &db.Tree{OwnerID: uuid.New()}); err != nil {
		t.Errorf("Create without a title failed: %v", err)
	}
	// Metadata is validated on write (json_valid), the guarantee jsonb gave PostgreSQL.
	if _, err := trees.Create(ctx, &db.Tree{OwnerID: uuid.New(), Title: "bad json", Metadata: []byte(`{`)}); err == nil {
		t.Error("Create with malformed metadata succeeded, want the json_valid CHECK to reject it")
	}
}

// GAP-094: LastActivityByTreeIDs returns MAX(nodes.created_at) per tree over
// live nodes only. Timestamps are written explicitly (the wall-clock default
// has millisecond resolution, so rapid inserts would tie and make "which node
// is newest" unassertable), and a nodeless sibling tree proves the absence
// convention.
func TestSQLiteTreeRepoLastActivityByTreeIDs(t *testing.T) {
	s, trees, nodes, _ := newGraphRepos(t)
	ctx := context.Background()

	tree := mustTree(t, ctx, trees, "activity")
	nodeless := mustTree(t, ctx, trees, "quiet")

	if empty, err := trees.LastActivityByTreeIDs(ctx, nil); err != nil || len(empty) != 0 {
		t.Errorf("LastActivityByTreeIDs(nil) = %v (err %v), want an empty map without a query", empty, err)
	}
	if latest, err := trees.LastActivityByTreeIDs(ctx, []uuid.UUID{tree.ID, nodeless.ID}); err != nil {
		t.Fatalf("LastActivityByTreeIDs: %v", err)
	} else if len(latest) != 0 {
		t.Errorf("LastActivityByTreeIDs = %v, want an empty map (no nodes yet)", latest)
	}

	old := mustNode(t, ctx, nodes, tree.ID, nil, "older")
	newer := mustNode(t, ctx, nodes, tree.ID, nil, "newer")
	// Backdate the first node so the MAX is unambiguous even at ms resolution.
	if _, err := s.DB().ExecContext(ctx, `UPDATE nodes SET created_at = ? WHERE id = ?`,
		EncodeTime(newer.CreatedAt.Add(-time.Hour)), old.ID.String()); err != nil {
		t.Fatalf("backdate older node: %v", err)
	}

	latest, err := trees.LastActivityByTreeIDs(ctx, []uuid.UUID{tree.ID, nodeless.ID})
	if err != nil {
		t.Fatalf("LastActivityByTreeIDs(2 trees): %v", err)
	}
	if got, ok := latest[tree.ID]; !ok {
		t.Errorf("tree %s absent from %v, want its newest node's created_at", tree.ID, latest)
	} else if !got.Equal(newer.CreatedAt) {
		t.Errorf("LastActivityByTreeIDs[tree] = %v, want the newest node's created_at %v", got, newer.CreatedAt)
	}
	if _, ok := latest[nodeless.ID]; ok {
		t.Errorf("nodeless tree %s present in %v, want absent (zero by convention)", nodeless.ID, latest)
	}

	// Soft-deleted nodes do not count as activity.
	if err := nodes.SoftDelete(ctx, newer.ID); err != nil {
		t.Fatalf("SoftDelete(newest node): %v", err)
	}
	latest, err = trees.LastActivityByTreeIDs(ctx, []uuid.UUID{tree.ID})
	if err != nil {
		t.Fatalf("LastActivityByTreeIDs(after soft delete): %v", err)
	}
	if got := latest[tree.ID]; !got.Equal(newer.CreatedAt.Add(-time.Hour)) {
		t.Errorf("LastActivityByTreeIDs after soft-deleting the newest node = %v, want the backdated survivor %v", got, newer.CreatedAt.Add(-time.Hour))
	}
}
