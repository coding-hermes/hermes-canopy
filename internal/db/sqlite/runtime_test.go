package sqlite

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/coding-hermes/hermes-canopy/internal/testutil"
	"github.com/google/uuid"
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

func sha256Hex(value string) string {
	// Kept local to this integration fixture so the expected PG trigger value
	// is explicit at the test boundary.
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}
