// GAP-080 phase 1 — the pin WRITE PATH must never clobber reserved metadata.
//
// `PATCH /api/v1/nodes/{node_id}` with `"pinned": true` merges the key into
// the node's existing `metadata` object; `false` removes exactly that key.
// Every other key — in particular the reserved `metadata.multi_reference`
// object (SPEC-PL-06) — survives verbatim, because the merge happens in ONE
// UPDATE (no read-modify-write race).
//
// DB-backed (shared integration pool): the properties under test are what the
// ROW holds after the real SQL, so a repo stub could not tell a merge from a
// wholesale replace.
//
// Run: CANOPY_TEST_ALLOW_SHARED_DB=1 go test -count=1 -run 'TestGAP080P1' ./internal/service/
package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// --- helpers ---------------------------------------------------------------

func gap080P1Tree(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	treeID := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO trees (id, owner_id, title) VALUES ($1, $2, 'GAP-080 P1 pin tree')`,
		treeID, uuid.New()); err != nil {
		t.Fatalf("insert tree: %v", err)
	}
	return treeID
}

// gap080P1Node inserts a node whose metadata is the given raw JSON (nil →
// NULL, used to prove the NOT NULL premise).
func gap080P1Node(t *testing.T, pool *pgxpool.Pool, treeID uuid.UUID, meta []byte) uuid.UUID {
	t.Helper()
	nodeID := uuid.New()
	if _, err := pool.Exec(context.Background(), `
        INSERT INTO nodes (id, tree_id, author_id, content, node_type, metadata)
        VALUES ($1, $2, $3, 'seed content', 'message', $4)`,
		nodeID, treeID, uuid.New(), meta); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	return nodeID
}

// gap080P1Meta reads the node's metadata as jsonb-canonical text.
func gap080P1Meta(t *testing.T, pool *pgxpool.Pool, nodeID uuid.UUID) string {
	t.Helper()
	var out string
	if err := pool.QueryRow(context.Background(),
		`SELECT metadata::text FROM nodes WHERE id = $1`, nodeID).Scan(&out); err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	return out
}

func gap080P1Patch(t *testing.T, svc *NodeServiceImpl, nodeID uuid.UUID, in UpdateNodeInput) *NodeDetail {
	t.Helper()
	detail, err := svc.Update(context.Background(), nodeID, in)
	if err != nil {
		t.Fatalf("Update(%+v): %v", in, err)
	}
	return detail
}

func gap080P1BoolPtr(b bool) *bool { return &b }

// --- AC5 -------------------------------------------------------------------

// TestGAP080P1_PinMergePreservesReservedMetadata is the core write-path test:
// a node carrying the reserved multi_reference object keeps it byte-equal
// through pin → unpin.
func TestGAP080P1_PinMergePreservesReservedMetadata(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	svc := NewNodeService(db.NewPGNodeRepo(pool), db.NewPGEdgeRepo(pool), pool, nil)
	treeID := gap080P1Tree(t, pool)

	// The reserved SPEC-PL-06 metadata object, written the way the graph
	// service writes it (canonical jsonb text is what the row stores).
	const reservedJSON = `{"multi_reference": {"v": 1, "sources": [{"nodeId": "00000000-0000-0000-0000-000000000000", "label": "src"}], "branch_span": null}}`
	nodeID := gap080P1Node(t, pool, treeID, []byte(reservedJSON))

	before := gap080P1Meta(t, pool, nodeID)
	var reservedBefore string
	if err := pool.QueryRow(context.Background(),
		`SELECT (metadata->'multi_reference')::text FROM nodes WHERE id = $1`, nodeID).Scan(&reservedBefore); err != nil {
		t.Fatalf("read reserved before: %v", err)
	}

	// ── pin: {"pinned": true} merged in, everything else untouched ───────────
	detail := gap080P1Patch(t, svc, nodeID, UpdateNodeInput{Pinned: gap080P1BoolPtr(true)})
	if detail.ID != nodeID {
		t.Fatalf("updated node id = %s, want %s", detail.ID, nodeID)
	}

	after := gap080P1Meta(t, pool, nodeID)
	var reservedAfter string
	if err := pool.QueryRow(context.Background(),
		`SELECT (metadata->'multi_reference')::text FROM nodes WHERE id = $1`, nodeID).Scan(&reservedAfter); err != nil {
		t.Fatalf("read reserved after: %v", err)
	}
	if reservedAfter != reservedBefore {
		t.Errorf("multi_reference changed by pin:\n before %s\n after  %s", reservedBefore, reservedAfter)
	}

	var pinned, hasPinned bool
	if err := pool.QueryRow(context.Background(),
		`SELECT (metadata->'pinned')::text = 'true', metadata ? 'pinned' FROM nodes WHERE id = $1`,
		nodeID).Scan(&pinned, &hasPinned); err != nil {
		t.Fatalf("read pinned: %v", err)
	}
	if !hasPinned || !pinned {
		t.Errorf("after pin: pinned present=%v true=%v, want both true (metadata=%s)", hasPinned, pinned, after)
	}
	// The merged object carries exactly the original keys plus `pinned`.
	var keysAfter []string
	rows, err := pool.Query(context.Background(),
		`SELECT jsonb_object_keys(metadata) FROM nodes WHERE id = $1 ORDER BY 1`, nodeID)
	if err != nil {
		t.Fatalf("key scan: %v", err)
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			t.Fatalf("key scan row: %v", err)
		}
		keysAfter = append(keysAfter, k)
	}
	rows.Close()
	if len(keysAfter) != 2 || keysAfter[0] != "multi_reference" || keysAfter[1] != "pinned" {
		t.Errorf("keys after pin = %v, want [multi_reference pinned]", keysAfter)
	}

	// A pin is a node edit: the update path stamps edited_at.
	var edited *string
	if err := pool.QueryRow(context.Background(),
		`SELECT edited_at::text FROM nodes WHERE id = $1`, nodeID).Scan(&edited); err != nil {
		t.Fatalf("read edited_at: %v", err)
	}
	if edited == nil {
		t.Error("edited_at is NULL after a pin — a pin must count as a node edit")
	}

	// ── unpin: exactly that key removed, the rest byte-equal ────────────────
	gap080P1Patch(t, svc, nodeID, UpdateNodeInput{Pinned: gap080P1BoolPtr(false)})
	afterUnpin := gap080P1Meta(t, pool, nodeID)
	if afterUnpin != before {
		t.Errorf("unpin did not restore the original metadata:\n before  %s\n after   %s", before, afterUnpin)
	}
	var stillPinned bool
	if err := pool.QueryRow(context.Background(),
		`SELECT metadata ? 'pinned' FROM nodes WHERE id = $1`, nodeID).Scan(&stillPinned); err != nil {
		t.Fatalf("read pinned after unpin: %v", err)
	}
	if stillPinned {
		t.Errorf("unpin left the pinned key in place: %s", afterUnpin)
	}
	var reservedAfterUnpin string
	if err := pool.QueryRow(context.Background(),
		`SELECT (metadata->'multi_reference')::text FROM nodes WHERE id = $1`, nodeID).Scan(&reservedAfterUnpin); err != nil {
		t.Fatalf("read reserved after unpin: %v", err)
	}
	if reservedAfterUnpin != reservedBefore {
		t.Errorf("unpin changed multi_reference:\n before %s\n after  %s", reservedBefore, reservedAfterUnpin)
	}
}

// TestGAP080P1_PinWithOtherMetadataShapes covers the reachable non-object /
// empty shapes, the metadata+pinned precedence in one body, and the
// content-only no-op on metadata.
func TestGAP080P1_PinWithOtherMetadataShapes(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	svc := NewNodeService(db.NewPGNodeRepo(pool), db.NewPGEdgeRepo(pool), pool, nil)
	treeID := gap080P1Tree(t, pool)

	t.Run("empty object metadata gains the pin", func(t *testing.T) {
		nodeID := gap080P1Node(t, pool, treeID, []byte(`{}`))
		gap080P1Patch(t, svc, nodeID, UpdateNodeInput{Pinned: gap080P1BoolPtr(true)})
		if got := gap080P1Meta(t, pool, nodeID); got != `{"pinned": true}` {
			t.Errorf("metadata = %s, want {\"pinned\": true}", got)
		}
		gap080P1Patch(t, svc, nodeID, UpdateNodeInput{Pinned: gap080P1BoolPtr(false)})
		if got := gap080P1Meta(t, pool, nodeID); got != `{}` {
			t.Errorf("after unpin metadata = %s, want {}", got)
		}
	})

	t.Run("non-object metadata is replaced by the pin object", func(t *testing.T) {
		// jsonb accepts arrays/scalars, so this row is reachable in practice:
		// the merge base must be '{}' when the stored value is not an object.
		nodeID := gap080P1Node(t, pool, treeID, []byte(`[1,2]`))
		gap080P1Patch(t, svc, nodeID, UpdateNodeInput{Pinned: gap080P1BoolPtr(true)})
		if got := gap080P1Meta(t, pool, nodeID); got != `{"pinned": true}` {
			t.Errorf("metadata = %s, want {\"pinned\": true}", got)
		}
	})

	t.Run("explicit metadata is applied first, then the pin merges", func(t *testing.T) {
		nodeID := gap080P1Node(t, pool, treeID, []byte(`{"multi_reference":{"v":1},"keep":2}`))
		raw := json.RawMessage(`{"replaced":true}`)
		gap080P1Patch(t, svc, nodeID, UpdateNodeInput{Metadata: &raw, Pinned: gap080P1BoolPtr(true)})
		got := gap080P1Meta(t, pool, nodeID)
		if got != `{"pinned": true, "replaced": true}` {
			t.Errorf("metadata = %s, want {\"pinned\": true, \"replaced\": true} (metadata first, pin on top)", got)
		}

		raw2 := json.RawMessage(`{"second":1}`)
		gap080P1Patch(t, svc, nodeID, UpdateNodeInput{Metadata: &raw2, Pinned: gap080P1BoolPtr(false)})
		if got := gap080P1Meta(t, pool, nodeID); got != `{"second": 1}` {
			t.Errorf("metadata = %s, want {\"second\": 1}", got)
		}
	})

	t.Run("content-only patch leaves metadata untouched", func(t *testing.T) {
		nodeID := gap080P1Node(t, pool, treeID, []byte(`{"multi_reference":{"v":9}}`))
		before := gap080P1Meta(t, pool, nodeID)
		content := "content-only update"
		detail := gap080P1Patch(t, svc, nodeID, UpdateNodeInput{Content: &content})
		if detail.Content != content {
			t.Errorf("content = %q, want %q", detail.Content, content)
		}
		if got := gap080P1Meta(t, pool, nodeID); got != before {
			t.Errorf("content-only patch changed metadata: %s → %s", before, got)
		}
	})

	t.Run("pin-only body is not rejected as an empty update", func(t *testing.T) {
		nodeID := gap080P1Node(t, pool, treeID, []byte(`{}`))
		if _, err := svc.Update(context.Background(), nodeID,
			UpdateNodeInput{Pinned: gap080P1BoolPtr(true)}); err != nil {
			t.Fatalf("pin-only update: %v (want no ErrNoUpdateFields)", err)
		}
		// And a truly empty body still is.
		_, err := svc.Update(context.Background(), nodeID, UpdateNodeInput{})
		if !errors.Is(err, ErrNoUpdateFields) {
			t.Errorf("empty update error = %v, want ErrNoUpdateFields", err)
		}
	})
}

// TestGAP080P1_NullMetadataArm proves the NULL arm of the merge expression.
//
// nodes.metadata is NOT NULL DEFAULT '{}', so a NULL-metadata ROW cannot
// exist (the premise is asserted below against the real server). The NULL arm
// is therefore proven by executing the SAME merge expression the UPDATE uses
// against a NULL jsonb value in a temp table — not by an unreachable row.
func TestGAP080P1_NullMetadataArm(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	// Premise: a NULL metadata insert is refused by the DDL.
	_, err := pool.Exec(ctx, `
        INSERT INTO nodes (id, tree_id, author_id, content, node_type, metadata)
        VALUES ($1, $2, $3, 'x', 'message', NULL)`, uuid.New(), uuid.New(), uuid.New())
	if err == nil {
		t.Fatal("premise broken: inserting NULL metadata succeeded, but nodes.metadata is NOT NULL")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23502" {
		t.Errorf("NULL metadata insert error = %v, want SQLSTATE 23502 (not-null violation)", err)
	}

	// Execute the production merge expression (the exact const the UPDATE
	// interpolates) against a NULL jsonb value. The target is a temp table
	// because nodes.metadata is NOT NULL; every earlier parameter is typed by
	// the no-op COALESCE assignments so $4/$5 keep their production meaning.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE gap080p1_null_probe (id uuid, content text, content_format text, metadata jsonb) ON COMMIT DROP`); err != nil {
		t.Fatalf("create temp table: %v", err)
	}
	probeID := uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO gap080p1_null_probe (id, metadata) VALUES ($1, NULL)`, probeID); err != nil {
		t.Fatalf("insert NULL metadata: %v", err)
	}
	if _, err := tx.Exec(ctx, `
        UPDATE gap080p1_null_probe
        SET metadata = `+metadataPinMergeExpr+`,
            content = COALESCE($1::text, content),
            content_format = COALESCE($2::text, content_format)
        WHERE id = $3::uuid`,
		nil, nil, probeID, nil, "pin"); err != nil {
		t.Fatalf("apply merge expression to NULL: %v", err)
	}
	var got string
	if err := tx.QueryRow(ctx, `SELECT metadata::text FROM gap080p1_null_probe`).Scan(&got); err != nil {
		t.Fatalf("read probe: %v", err)
	}
	if got != `{"pinned": true}` {
		t.Errorf("NULL metadata + pin = %s, want {\"pinned\": true}", got)
	}
}

// TestGAP080P1_UpdateNodeInputPinnedIsOptional pins the input contract: an
// absent pinned key (nil) leaves metadata alone on both routes.
func TestGAP080P1_UpdateNodeInputPinnedIsOptional(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	svc := NewNodeService(db.NewPGNodeRepo(pool), db.NewPGEdgeRepo(pool), pool, nil)
	treeID := gap080P1Tree(t, pool)

	nodeID := gap080P1Node(t, pool, treeID, []byte(`{"pinned": true}`))
	before := gap080P1Meta(t, pool, nodeID)
	content := "no pin in this body"
	gap080P1Patch(t, svc, nodeID, UpdateNodeInput{Content: &content})
	if got := gap080P1Meta(t, pool, nodeID); got != before {
		t.Errorf("Pinned=nil changed metadata: %s → %s", before, got)
	}
	if !strings.Contains(before, `"pinned": true`) {
		t.Fatalf("fixture drift: %s does not carry a pin", before)
	}
}
