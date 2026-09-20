package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ExportPostgres copies the core graph tables from PostgreSQL into a new
// SQLite file. A non-empty target is refused rather than merged, making a
// repeated operator invocation fail closed and avoiding accidental overwrites.
// The source pool is closed before the function returns.
func ExportPostgres(ctx context.Context, postgresDSN, sqlitePath string) error {
	if strings.TrimSpace(postgresDSN) == "" {
		return errors.New("sqlite export: empty PostgreSQL DSN")
	}
	if strings.TrimSpace(sqlitePath) == "" {
		return errors.New("sqlite export: empty SQLite path")
	}
	if info, err := os.Stat(sqlitePath); err == nil {
		if info.Size() != 0 {
			return fmt.Errorf("sqlite export: refusing non-empty target %q; choose a new path", sqlitePath)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("sqlite export: inspect target: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(sqlitePath), 0o700); err != nil {
		return fmt.Errorf("sqlite export: create target directory: %w", err)
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(sqlitePath), "."+filepath.Base(sqlitePath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("sqlite export: create temporary target: %w", err)
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("sqlite export: close temporary target: %w", err)
	}
	defer func() { _ = os.Remove(tmpPath) }()

	pg, err := pgxpool.New(ctx, postgresDSN)
	if err != nil {
		return fmt.Errorf("sqlite export: connect PostgreSQL: %w", err)
	}
	defer pg.Close()
	if err := pg.Ping(ctx); err != nil {
		return fmt.Errorf("sqlite export: ping PostgreSQL: %w", err)
	}

	target, err := OpenRuntime(ctx, tmpPath)
	if err != nil {
		return fmt.Errorf("sqlite export: open target: %w", err)
	}
	defer func() { _ = target.Close() }()

	tx, err := target.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite export: begin target transaction: %w", err)
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}

	counts := make(map[string]int)
	if count, err := exportUsers(ctx, pg, tx); err != nil {
		return rollback(err)
	} else {
		counts["users"] = count
	}
	if count, err := exportTrees(ctx, pg, tx); err != nil {
		return rollback(err)
	} else {
		counts["trees"] = count
	}
	if count, err := exportNodes(ctx, pg, tx); err != nil {
		return rollback(err)
	} else {
		counts["nodes"] = count
	}
	if count, err := exportEdges(ctx, pg, tx); err != nil {
		return rollback(err)
	} else {
		counts["edges"] = count
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite export: commit target transaction: %w", err)
	}

	for table, got := range counts {
		var want int
		if err := pg.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&want); err != nil {
			return fmt.Errorf("sqlite export: count PostgreSQL %s: %w", table, err)
		}
		if got != want {
			return fmt.Errorf("sqlite export: row count mismatch for %s: PostgreSQL=%d SQLite=%d", table, want, got)
		}
	}
	if err := verifyNodeHashes(ctx, pg, target); err != nil {
		return err
	}
	if err := target.Close(); err != nil {
		return fmt.Errorf("sqlite export: close temporary target: %w", err)
	}
	if err := os.Rename(tmpPath, sqlitePath); err != nil {
		return fmt.Errorf("sqlite export: publish target: %w", err)
	}
	return nil
}

func exportUsers(ctx context.Context, pg *pgxpool.Pool, tx *sql.Tx) (int, error) {
	rows, err := pg.Query(ctx, `SELECT id::text, hermes_user_id, COALESCE(email,''), display_name,
		COALESCE(avatar_url,''), created_at::text,
		updated_at::text,
		COALESCE(last_seen_at::text,''),
		CASE WHEN is_active THEN 1 ELSE 0 END, COALESCE(deleted_at::text,'')
		FROM users ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("sqlite export: read users: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, hermesID, email, displayName, avatar, created, updated, lastSeen, deleted string
		var active int
		if err := rows.Scan(&id, &hermesID, &email, &displayName, &avatar, &created, &updated, &lastSeen, &active, &deleted); err != nil {
			return count, fmt.Errorf("sqlite export: scan users: %w", err)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO users (id,hermes_user_id,email,display_name,avatar_url,created_at,updated_at,last_seen_at,is_active,deleted_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, id, hermesID, emptyToNil(email), displayName, emptyToNil(avatar), created, updated, emptyToNil(lastSeen), active, emptyToNil(deleted))
		if err != nil {
			return count, fmt.Errorf("sqlite export: insert users: %w", err)
		}
		count++
	}
	return count, rows.Err()
}

func exportTrees(ctx context.Context, pg *pgxpool.Pool, tx *sql.Tx) (int, error) {
	rows, err := pg.Query(ctx, `SELECT id::text, owner_id::text, title, description, COALESCE(root_node_id::text,''), metadata::text,
		created_at::text, COALESCE(edited_at::text,''), COALESCE(deleted_at::text,'') FROM trees ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("sqlite export: read trees: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, owner, title, description, root, metadata, created, edited, deleted string
		if err := rows.Scan(&id, &owner, &title, &description, &root, &metadata, &created, &edited, &deleted); err != nil {
			return count, fmt.Errorf("sqlite export: scan trees: %w", err)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO trees (id,owner_id,title,description,root_node_id,metadata,created_at,edited_at,deleted_at) VALUES (?,?,?,?,?,?,?,?,?)`, id, owner, title, description, emptyToNil(root), metadata, created, emptyToNil(edited), emptyToNil(deleted))
		if err != nil {
			return count, fmt.Errorf("sqlite export: insert trees: %w", err)
		}
		count++
	}
	return count, rows.Err()
}

func exportNodes(ctx context.Context, pg *pgxpool.Pool, tx *sql.Tx) (int, error) {
	rows, err := pg.Query(ctx, `SELECT id::text, tree_id::text, COALESCE(parent_id::text,''), author_id::text, content, content_format, node_type, sequence_num, metadata::text, content_hash,
		created_at::text, COALESCE(edited_at::text,''), COALESCE(deleted_at::text,'') FROM nodes ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("sqlite export: read nodes: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, treeID, parent, author, content, format, nodeType, metadata, hash, created, edited, deleted string
		var sequence int
		if err := rows.Scan(&id, &treeID, &parent, &author, &content, &format, &nodeType, &sequence, &metadata, &hash, &created, &edited, &deleted); err != nil {
			return count, fmt.Errorf("sqlite export: scan nodes: %w", err)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO nodes (id,tree_id,parent_id,author_id,content,content_format,node_type,sequence_num,metadata,created_at,edited_at,deleted_at,content_hash) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, treeID, emptyToNil(parent), author, content, format, nodeType, sequence, metadata, created, emptyToNil(edited), emptyToNil(deleted), hash)
		if err != nil {
			return count, fmt.Errorf("sqlite export: insert nodes: %w", err)
		}
		count++
	}
	return count, rows.Err()
}

func exportEdges(ctx context.Context, pg *pgxpool.Pool, tx *sql.Tx) (int, error) {
	rows, err := pg.Query(ctx, `SELECT id::text, tree_id::text, source_id::text, target_id::text, edge_type, sequence_num, metadata::text,
		created_at::text, COALESCE(deleted_at::text,'') FROM edges ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("sqlite export: read edges: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, treeID, source, target, edgeType, metadata, created, deleted string
		var sequence int
		if err := rows.Scan(&id, &treeID, &source, &target, &edgeType, &sequence, &metadata, &created, &deleted); err != nil {
			return count, fmt.Errorf("sqlite export: scan edges: %w", err)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO edges (id,tree_id,source_id,target_id,edge_type,sequence_num,metadata,created_at,deleted_at) VALUES (?,?,?,?,?,?,?,?,?)`, id, treeID, source, target, edgeType, sequence, metadata, created, emptyToNil(deleted))
		if err != nil {
			return count, fmt.Errorf("sqlite export: insert edges: %w", err)
		}
		count++
	}
	return count, rows.Err()
}

func verifyNodeHashes(ctx context.Context, pg *pgxpool.Pool, target *Store) error {
	rows, err := pg.Query(ctx, `SELECT id::text, content, content_hash FROM nodes ORDER BY id`)
	if err != nil {
		return fmt.Errorf("sqlite export: read node hashes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, content, sourceHash string
		if err := rows.Scan(&id, &content, &sourceHash); err != nil {
			return fmt.Errorf("sqlite export: scan node hash: %w", err)
		}
		digest := sha256.Sum256([]byte(content))
		expected := hex.EncodeToString(digest[:])
		if sourceHash != expected {
			return fmt.Errorf("sqlite export: source content hash mismatch for node %s: source=%s calculated=%s", id, sourceHash, expected)
		}
		var targetHash string
		if err := target.DB().QueryRowContext(ctx, `SELECT content_hash FROM nodes WHERE id = ?`, id).Scan(&targetHash); err != nil {
			return fmt.Errorf("sqlite export: verify node %s: %w", id, err)
		}
		if targetHash != expected {
			return fmt.Errorf("sqlite export: target content hash mismatch for node %s", id)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite export: read node hashes: %w", err)
	}
	return nil
}

func emptyToNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}
