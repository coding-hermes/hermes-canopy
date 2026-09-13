package fileviewer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sha256Of is the hex digest helper used across the fileviewer tests.
func sha256Of(t *testing.T, s string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// timeFuture/timePast bound PurgeDeleted and GetStats queries.
func timeFuture() time.Time { return time.Now().Add(time.Hour) }
func timePast() time.Time   { return time.Now().Add(-time.Hour) }

// seedProfile inserts one user+profile pair and returns the profile id.
func seedProfile(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	userID := uuid.New()
	profileID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, hermes_user_id, display_name) VALUES ($1, $2, 'fv-test-user')`,
		userID, "fv-"+userID.String()); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO profiles (id, owner_id, name, display_name) VALUES ($1, $2, $3, 'FV Test')`,
		profileID, userID, "fv-"+profileID.String()); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	return profileID
}

// poolOf digs the pool out of a repo for raw assertions (trigger checks).
func poolOf(t *testing.T, r *PGFileMetadataRepo) *pgxpool.Pool {
	t.Helper()
	return r.pool
}
