package db_test

import (
	"context"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// TestContentHashTrigger_BackslashEscape is a regression test for BUG-034.
// The set_content_hash trigger previously used NEW.content::bytea, which
// parses TEXT as bytea literal syntax. Content containing backslashes or
// \x escape sequences (real session import data from
// 20260606_155331_5054b7f3 — tool output with \x escapes) caused a 22P02
// invalid input syntax for type bytea error on INSERT. The fix
// (migration 000025) switches to convert_to(content, 'UTF8').
//
// This test inserts a node whose content contains the exact failing pattern
// and asserts the INSERT succeeds with a valid 64-char hex content_hash.
func TestContentHashTrigger_BackslashEscape(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	tree := createTestTree(t, pool)

	// Real failing content from session 20260606_155331_5054b7f3:
	// tool output containing backslash and \x escape sequences.
	const failingContent = `tool output with \xZZ escape and \x41 hex and back\\slash`

	in := &db.Node{
		TreeID:        tree.ID,
		AuthorID:      uuid.New(),
		Content:       failingContent,
		ContentFormat: db.ContentFormatMarkdown,
		NodeType:      db.NodeTypeMessage,
	}
	out, err := repo.Create(ctx, in)
	require.NoError(t, err, "INSERT must not throw 22P02 on content with \\x escapes")
	require.NotNil(t, out)

	// content_hash is not exposed on the Node struct / nodeColumns, so
	// query it directly from the DB to verify the trigger computed it.
	hash := getContentHash(t, pool, out.ID)
	assert.NotEmpty(t, hash, "content_hash must be populated by the trigger")
	assert.Len(t, hash, 64, "content_hash must be 64 hex chars (sha256)")
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), hash,
		"content_hash must be valid lowercase hex")
}

// getContentHash reads the content_hash column for the given node directly.
func getContentHash(t *testing.T, pool *pgxpool.Pool, nodeID uuid.UUID) string {
	t.Helper()
	var hash string
	err := pool.QueryRow(context.Background(),
		`SELECT content_hash FROM nodes WHERE id = $1`, nodeID).Scan(&hash)
	require.NoError(t, err)
	return hash
}
