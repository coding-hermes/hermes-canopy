package testutil

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestIntegration_Migration verifies that migrations apply cleanly
// against a real PostgreSQL instance. This is the canary test for
// the BE-12a integration test framework.
func TestIntegration_Migration(t *testing.T) {
	SkipIfNoDB(t)

	pool := NewIntegrationPool(t)
	ctx := context.Background()

	// Verify essential tables exist by selecting from them.
	var count int
	rows := []string{
		"trees", "nodes", "edges", "tree_snapshots",
		"tree_events", "users", "profiles",
		"approvals", "approval_audit_log",
		"transport_connections", "transport_configs", "transport_events",
		"mls_groups", "mls_group_members", "mls_key_packages", "mls_pending_proposals",
		"topics", "topic_members",
		"workspaces",
	}
	for _, table := range rows {
		if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("table %s unreachable: %v", table, err)
		}
	}

	// Verify functions exist.
	var fnExists bool
	if err := pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_proc WHERE proname = 'uuidv7')",
	).Scan(&fnExists); err != nil {
		t.Fatalf("uuidv7 function check: %v", err)
	}
	if !fnExists {
		t.Fatal("uuidv7 function not found after migration")
	}
}

// TestIntegration_Truncate verifies TruncateAll works. Creates a tree,
// truncates, and confirms the table is empty.
func TestIntegration_Truncate(t *testing.T) {
	SkipIfNoDB(t)

	pool := NewIntegrationPool(t)
	ctx := context.Background()

	// Insert a row into trees.
	if _, err := pool.Exec(ctx,
		`INSERT INTO trees (owner_id, title) VALUES ('00000000-0000-0000-0000-000000000001', 'test-truncate')`,
	); err != nil {
		t.Fatalf("insert test row: %v", err)
	}

	TruncateAll(t, pool)

	var count int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM trees").Scan(&count); err != nil {
		t.Fatalf("count trees: %v", err)
	}
	if count != 0 {
		t.Fatalf("trees table not empty after TruncateAll: got %d rows", count)
	}
}

// TestHostPortFromURL exercises the host:port parser used by the short-mode
// reachability probe. Pure unit test — no PostgreSQL required.
func TestHostPortFromURL(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		wantH string
		wantP string
	}{
		{name: "default admin URL", raw: defaultAdminURL, wantH: "localhost", wantP: "5437"},
		{name: "postgres scheme with port", raw: "postgres://u:p@db.example.com:6543/db?sslmode=disable", wantH: "db.example.com", wantP: "6543"},
		{name: "postgresql scheme", raw: "postgresql://u:p@1.2.3.4:5432/db", wantH: "1.2.3.4", wantP: "5432"},
		{name: "url without port defaults 5432", raw: "postgres://u:p@db.example.com/db", wantH: "db.example.com", wantP: "5432"},
		{name: "libpq key=value DSN", raw: "host=db.local port=6432 user=canopy", wantH: "db.local", wantP: "6432"},
		{name: "libpq DSN without port", raw: "host=db.local", wantH: "db.local", wantP: "5432"},
		{name: "empty string defaults", raw: "", wantH: "localhost", wantP: "5432"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotH, gotP := hostPortFromURL(tt.raw)
			if gotH != tt.wantH || gotP != tt.wantP {
				t.Fatalf("hostPortFromURL(%q) = (%q, %q), want (%q, %q)", tt.raw, gotH, gotP, tt.wantH, tt.wantP)
			}
		})
	}
}

// TestResolveAdminURL confirms env-var precedence for the admin URL used by
// the reachability probe. Pure unit test — no PostgreSQL required.
//
// Note: uses t.Setenv("", "") rather than os.Unsetenv to "unset" vars within
// each subtest — os.Unsetenv does NOT register cleanup, so it would leak an
// unset value into sibling tests in the same binary (e.g. TestSweepKeepsFreshDB
// would see the default URL instead of a caller-provided override).
func TestResolveAdminURL(t *testing.T) {
	const adminURL = "postgres://canopy:***@db-a:1111/postgres?sslmode=disable"
	const testURL = "postgres://canopy:***@db-b:2222/canopy?sslmode=disable"

	t.Run("admin wins over test", func(t *testing.T) {
		t.Setenv("CANOPY_ADMIN_DB_URL", adminURL)
		t.Setenv("CANOPY_TEST_DB_URL", testURL)
		if got := resolveAdminURL(); got != adminURL {
			t.Fatalf("resolveAdminURL() = %q, want %q", got, adminURL)
		}
	})
	t.Run("test fallback when admin unset", func(t *testing.T) {
		t.Setenv("CANOPY_ADMIN_DB_URL", "")
		t.Setenv("CANOPY_TEST_DB_URL", testURL)
		if got := resolveAdminURL(); got != testURL {
			t.Fatalf("resolveAdminURL() = %q, want %q", got, testURL)
		}
	})
	t.Run("default when both unset", func(t *testing.T) {
		t.Setenv("CANOPY_ADMIN_DB_URL", "")
		t.Setenv("CANOPY_TEST_DB_URL", "")
		if got := resolveAdminURL(); got != defaultAdminURL {
			t.Fatalf("resolveAdminURL() = %q, want %q", got, defaultAdminURL)
		}
	})
}

// TestTargetURLForDBOverride proves CANOPY_TEST_DB_URL selects the server and
// connection options without pinning a pool to the override's database.
// Pure unit test — no PostgreSQL or network required.
func TestTargetURLForDBOverride(t *testing.T) {
	const baseURL = "postgresql://canopy:p%40ss@db.example:6543/service_db?sslmode=verify-full&application_name=canopy-ci"
	const dbName = "canopy_0123abcd"

	got, err := targetURLForDB(baseURL, dbName)
	if err != nil {
		t.Fatalf("targetURLForDB() error = %v", err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse target URL %q: %v", got, err)
	}
	if parsed.Scheme != "postgresql" {
		t.Fatalf("target scheme = %q, want postgresql", parsed.Scheme)
	}
	if parsed.Host != "db.example:6543" {
		t.Fatalf("target host = %q, want db.example:6543", parsed.Host)
	}
	if parsed.User == nil || parsed.User.Username() != "canopy" {
		t.Fatalf("target user = %v, want canopy", parsed.User)
	}
	password, ok := parsed.User.Password()
	if !ok || password != "p@ss" {
		t.Fatalf("target password = %q, want p@ss", password)
	}
	if parsed.Path != "/"+dbName {
		t.Fatalf("target database path = %q, want /%s", parsed.Path, dbName)
	}
	if parsed.RawQuery != "sslmode=verify-full&application_name=canopy-ci" {
		t.Fatalf("target query = %q, want original query", parsed.RawQuery)
	}
	if strings.Contains(parsed.Path, "service_db") {
		t.Fatalf("target still uses fixed override database: %q", parsed.Path)
	}
}

// TestTargetURLForDBDefault preserves the built-in target URL when no override
// is configured.
func TestTargetURLForDBDefault(t *testing.T) {
	const dbName = "canopy_89abcdef"

	got, err := targetURLForDB("", dbName)
	if err != nil {
		t.Fatalf("targetURLForDB() error = %v", err)
	}
	want := "postgres://canopy:canopy@localhost:5437/" + dbName + "?sslmode=disable"
	if got != want {
		t.Fatalf("targetURLForDB() = %q, want %q", got, want)
	}
}

// TestSharedDBRefused exercises the GAP-069 shared-DB gate decision table.
// Pure unit test — no PostgreSQL, no network.
func TestSharedDBRefused(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantRef bool
	}{
		{
			name:    "clean env refuses the implicit shared default",
			env:     map[string]string{},
			wantRef: true,
		},
		{
			name:    "explicit opt-in allows shared DB",
			env:     map[string]string{"CANOPY_TEST_ALLOW_SHARED_DB": "1"},
			wantRef: false,
		},
		{
			name:    "opt-in set to anything but 1 still refuses",
			env:     map[string]string{"CANOPY_TEST_ALLOW_SHARED_DB": "true"},
			wantRef: true,
		},
		{
			name:    "CI-style CANOPY_REQUIRE_DB=1 allows the default",
			env:     map[string]string{"CANOPY_REQUIRE_DB": "1"},
			wantRef: false,
		},
		{
			name:    "explicit admin URL always wins",
			env:     map[string]string{"CANOPY_ADMIN_DB_URL": "postgres://u:p@dedicated:5432/postgres"},
			wantRef: false,
		},
		{
			name:    "explicit test URL always wins",
			env:     map[string]string{"CANOPY_TEST_DB_URL": "postgres://u:p@dedicated:5432/canopy"},
			wantRef: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CANOPY_ADMIN_DB_URL", "")
			t.Setenv("CANOPY_TEST_DB_URL", "")
			t.Setenv("CANOPY_TEST_ALLOW_SHARED_DB", "")
			t.Setenv("CANOPY_REQUIRE_DB", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if got := sharedDBRefused(); got != tt.wantRef {
				t.Fatalf("sharedDBRefused() with %v = %v, want %v", tt.env, got, tt.wantRef)
			}
		})
	}
}

// clearDBGateEnv pins every gate-relevant env var to its neutral value and
// returns restore handles via t.Setenv cleanup. Use at the top of any
// subtest that drives evaluateDBGate.
func clearDBGateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CANOPY_SKIP_INTEGRATION", "")
	t.Setenv("CANOPY_ADMIN_DB_URL", "")
	t.Setenv("CANOPY_TEST_DB_URL", "")
	t.Setenv("CANOPY_TEST_ALLOW_SHARED_DB", "")
	t.Setenv("CANOPY_REQUIRE_DB", "")
}

// TestEvaluateDBGate_NoEnv_GateBeforeProbe proves the core GAP-069 behavior:
// with a clean environment the gate skips with the opt-in message BEFORE any
// reachability probing. pgReachable is stubbed via the package seam so the
// assertion "the gate fires before I/O" is structural — if the gate ever
// moves back behind the probe, pgReachableCallCount != 0 fails the test.
func TestEvaluateDBGate_NoEnv_GateBeforeProbe(t *testing.T) {
	clearDBGateEnv(t)
	restore := StubPGReachable(false)
	defer restore()

	for _, short := range []bool{false, true} {
		d := evaluateDBGate(short)
		if !d.Skip || d.Fail {
			t.Fatalf("short=%v: gate did not skip: %+v", short, d)
		}
		if d.Message != sharedDBGateMessage {
			t.Fatalf("short=%v: message = %q, want the GAP-069 gate message", short, d.Message)
		}
		for _, tok := range []string{"GAP-069", "CANOPY_TEST_ALLOW_SHARED_DB=1", "CANOPY_ADMIN_DB_URL", "CANOPY_REQUIRE_DB=1"} {
			if !strings.Contains(d.Message, tok) {
				t.Fatalf("message %q missing token %q", d.Message, tok)
			}
		}
	}
	if n := ProbeCallCount(); n != 0 {
		t.Fatalf("gate probed the shared server %d time(s) before refusing — zero-IO contract broken", n)
	}
}

// TestEvaluateDBGate_OptIn allows the implicit default again and exercises
// the reachability branches with a stubbed probe (DB down → skip in short
// mode / skip in full mode; DB up → pass through).
func TestEvaluateDBGate_OptIn(t *testing.T) {
	t.Run("opt-in with DB down skips via reachability, not the gate", func(t *testing.T) {
		clearDBGateEnv(t)
		t.Setenv("CANOPY_TEST_ALLOW_SHARED_DB", "1")
		restore := StubPGReachable(false)
		defer restore()

		d := evaluateDBGate(true) // short mode
		if !d.Skip || d.Fail {
			t.Fatalf("short mode with DB down must skip: %+v", d)
		}
		if strings.Contains(d.Message, "GAP-069") {
			t.Fatalf("gate fired despite explicit opt-in: %q", d.Message)
		}
		if !strings.Contains(d.Message, "short mode") {
			t.Fatalf("expected reachability-branch message, got %q", d.Message)
		}

		d = evaluateDBGate(false) // full mode
		if !d.Skip || d.Fail {
			t.Fatalf("full mode with DB down (no REQUIRE_DB) must skip: %+v", d)
		}
		if strings.Contains(d.Message, "GAP-069") {
			t.Fatalf("gate fired despite explicit opt-in: %q", d.Message)
		}
	})

	t.Run("opt-in with DB up passes through", func(t *testing.T) {
		clearDBGateEnv(t)
		t.Setenv("CANOPY_TEST_ALLOW_SHARED_DB", "1")
		restore := StubPGReachable(true)
		defer restore()

		for _, short := range []bool{false, true} {
			if d := evaluateDBGate(short); d.Skip || d.Fail {
				t.Fatalf("short=%v: gate/reachability refused an opted-in, reachable DB: %+v", short, d)
			}
		}
	})
}

// TestEvaluateDBGate_ExplicitOverride proves CANOPY_ADMIN_DB_URL is honoured:
// the gate passes it through and the reachability branch probes THAT URL.
func TestEvaluateDBGate_ExplicitOverride(t *testing.T) {
	clearDBGateEnv(t)
	t.Setenv("CANOPY_ADMIN_DB_URL", "postgres://canopy:canopy@127.0.0.1:1/postgres?sslmode=disable")
	restore := StubPGReachable(false)
	defer restore()

	d := evaluateDBGate(false)
	if !d.Skip || d.Fail {
		t.Fatalf("unreachable explicit override must skip: %+v", d)
	}
	if strings.Contains(d.Message, "GAP-069") {
		t.Fatalf("explicit override was refused by the shared-DB gate: %q", d.Message)
	}
	if !strings.Contains(d.Message, "127.0.0.1:1") {
		t.Fatalf("skip message %q does not name the overridden URL", d.Message)
	}
}

// TestEvaluateDBGate_RequireDB_CIPath proves CANOPY_REQUIRE_DB=1 (CI) keeps
// working: the gate passes the implicit default, and with the DB down the
// decision is a loud FAIL (never a silent skip).
func TestEvaluateDBGate_RequireDB_CIPath(t *testing.T) {
	clearDBGateEnv(t)
	t.Setenv("CANOPY_REQUIRE_DB", "1")
	restore := StubPGReachable(false)
	defer restore()

	d := evaluateDBGate(false)
	if !d.Fail || d.Skip {
		t.Fatalf("CANOPY_REQUIRE_DB=1 with DB down must FAIL loudly: %+v", d)
	}
	if strings.Contains(d.Message, "GAP-069") {
		t.Fatalf("CI path was refused by the shared-DB gate: %q", d.Message)
	}

	// And with the DB up, CI passes through untouched.
	restore2 := StubPGReachable(true)
	defer restore2()
	if d := evaluateDBGate(false); d.Skip || d.Fail {
		t.Fatalf("CANOPY_REQUIRE_DB=1 with DB up must pass: %+v", d)
	}
}

// TestStaleTestDBs exercises the stale-database decision logic of the
// pre-run sweep. Pure unit test — no PostgreSQL required, runs in the
// non-PG gate too.
func TestStaleTestDBs(t *testing.T) {
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	fresh := now.Add(-30 * time.Minute)

	tests := []struct {
		name  string
		cands []testDBInfo
		want  []string
	}{
		{
			name: "old and idle is dropped",
			cands: []testDBInfo{
				{Name: "canopy_aaaaaaaa", Created: old, HasCreated: true},
			},
			want: []string{"canopy_aaaaaaaa"},
		},
		{
			name: "old but active connections is kept",
			cands: []testDBInfo{
				{Name: "canopy_aaaaaaaa", Created: old, HasCreated: true, Active: 1},
			},
			want: nil,
		},
		{
			name: "fresh and idle is kept",
			cands: []testDBInfo{
				{Name: "canopy_aaaaaaaa", Created: fresh, HasCreated: true},
			},
			want: nil,
		},
		{
			name: "exactly one hour old is kept (strictly older required)",
			cands: []testDBInfo{
				{Name: "canopy_aaaaaaaa", Created: now.Add(-time.Hour), HasCreated: true},
			},
			want: nil,
		},
		{
			name: "unknown creation time is never dropped",
			cands: []testDBInfo{
				{Name: "canopy_aaaaaaaa", Created: old, HasCreated: false},
			},
			want: nil,
		},
		{
			name: "mixed candidates",
			cands: []testDBInfo{
				{Name: "canopy_aaaaaaaa", Created: old, HasCreated: true},
				{Name: "canopy_bbbbbbbb", Created: old, HasCreated: true, Active: 2},
				{Name: "canopy_cccccccc", Created: fresh, HasCreated: true},
				{Name: "canopy_dddddddd", Created: old, HasCreated: true},
			},
			want: []string{"canopy_aaaaaaaa", "canopy_dddddddd"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := staleTestDBs(tt.cands, now)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("staleTestDBs() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSweepKeepsFreshDB verifies the live sweep never drops a database that
// belongs to a running test: a freshly-created database must survive
// sweepStaleTestDBs untouched (the 1-hour age gate protects live tests).
// Requires a live PostgreSQL — skips when CANOPY_SKIP_INTEGRATION is set.
func TestSweepKeepsFreshDB(t *testing.T) {
	SkipIfNoDB(t)

	ctx := context.Background()
	adminURL := "postgres://canopy:canopy@localhost:5437/postgres?sslmode=disable"
	if u := os.Getenv("CANOPY_ADMIN_DB_URL"); u != "" {
		adminURL = u
	}

	// Create a fresh uniquely-named database (drop+recreate, same as
	// NewIntegrationPool does for its own test database).
	name := uniqueDBName()
	if err := dropTestDBByName(ctx, adminURL, name); err != nil {
		t.Fatalf("create fresh DB %s: %v", name, err)
	}
	t.Cleanup(func() {
		dropCtx := context.Background()
		conn, err := pgx.Connect(dropCtx, adminURL)
		if err != nil {
			return
		}
		defer conn.Close(dropCtx)
		_, _ = conn.Exec(dropCtx,
			fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name))
	})

	sweepStaleTestDBs(ctx, adminURL)

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	var exists bool
	if err := conn.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)",
		name).Scan(&exists); err != nil {
		t.Fatalf("check %s exists: %v", name, err)
	}
	if !exists {
		t.Fatalf("sweep dropped freshly-created database %s — age gate broken", name)
	}
}
