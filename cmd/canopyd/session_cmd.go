package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/session"
)

// defaultOwnerID is the dev-user UUID used when CANOPY_OWNER_ID is unset.
var defaultOwnerID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// sessionSourceFlags are the read-source flags shared by the session
// subcommands (GAP-077). They select the Hermes data Canopy reads:
//
//   - default: the newest Hermes state SNAPSHOT under
//     $HOME/.hermes/state-backups (6-hourly, zstd/gzip/plain), read read-only
//     through an ATTACH — the live state.db is never opened, so an import can
//     never contend with (or write to) the gateway's database.
//   - --db: read exactly that file, read-only. The escape hatch for a
//     specific database; no snapshot resolution, no substitution.
//   - --snapshot-dir / --max-snapshot-age: choose a different snapshot
//     directory or relax the recency bound (0 = unbounded).
type sessionSourceFlags struct {
	dbPath      *string
	snapshotDir *string
	maxSnapAge  *time.Duration
}

// newSessionSourceFlags registers the shared source flags on fs.
func newSessionSourceFlags(fs *flag.FlagSet) *sessionSourceFlags {
	return &sessionSourceFlags{
		dbPath:      fs.String("db", "", "read this Hermes state.db directly (read-only); bypasses the snapshot source"),
		snapshotDir: fs.String("snapshot-dir", "", "directory of Hermes state snapshots (default $HOME/.hermes/state-backups)"),
		maxSnapAge:  fs.Duration("max-snapshot-age", session.DefaultSnapshotMaxAge, "refuse the newest snapshot when it is older than this (0 disables the bound)"),
	}
}

// open resolves the read source, returning the reader and a one-line
// provenance description for the operator. A missing/stale snapshot is an
// error — this never silently falls back to the live state.db.
func (f *sessionSourceFlags) open() (*session.Reader, string, error) {
	if *f.dbPath != "" {
		r, err := session.OpenReader(*f.dbPath)
		if err != nil {
			return nil, "", err
		}
		return r, fmt.Sprintf("direct database %s (read-only)", *f.dbPath), nil
	}
	opts, err := session.DefaultSnapshotOptions()
	if err != nil {
		return nil, "", err
	}
	if *f.snapshotDir != "" {
		opts.Dir = *f.snapshotDir
	}
	opts.MaxAge = *f.maxSnapAge
	r, err := session.OpenSnapshotReader(opts)
	if err != nil {
		return nil, "", err
	}
	spec, ok := r.Snapshot()
	if !ok {
		return r, "snapshot (metadata unavailable)", nil
	}
	return r, fmt.Sprintf("snapshot %s (captured %s, %s old)",
		spec.Path, spec.Stamp.Format(time.RFC3339), spec.Age(time.Now()).Round(time.Minute)), nil
}

// runSessionCmd dispatches session sub-subcommands.
func runSessionCmd(args []string) {
	os.Exit(runSessionCmdE(args))
}

// runSessionCmdE is runSessionCmd without the process exit so tests can assert
// exit codes. `canopyd session --help` / `-h` prints usage and returns 0;
// `canopyd session` with no arguments returns 1; only a genuinely unknown
// session subcommand returns nonzero (DF-HERMES-CANOPY-10).
func runSessionCmdE(args []string) int {
	if len(args) == 0 {
		printSessionUsage()
		return 1
	}
	if args[0] == "-h" || args[0] == "--help" {
		printSessionUsage()
		return 0
	}
	switch args[0] {
	case "browse":
		sessionBrowse(args[1:])
	case "import":
		sessionImport(args[1:])
	case "associations-backfill":
		sessionAssociationsBackfill(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown session subcommand: %s\n", args[0])
		fmt.Fprintf(os.Stderr, "Available: browse, import, associations-backfill\n")
		return 1
	}
	return 0
}

// printSessionUsage documents the session subcommands. It is printed by
// `canopyd session --help` (exit 0) and by a bare `canopyd session` (exit 1).
func printSessionUsage() {
	fmt.Fprintf(os.Stderr, "Usage: canopyd session <browse|import|associations-backfill> [flags...]\n\n")
	fmt.Fprintf(os.Stderr, "Subcommands:\n")
	fmt.Fprintf(os.Stderr, "  browse                  List sessions (or one session's messages) from the read source\n")
	fmt.Fprintf(os.Stderr, "  import                  Import Hermes sessions from the read source into Canopy trees\n")
	fmt.Fprintf(os.Stderr, "  associations-backfill   Recompute association metadata for already-imported sessions\n\n")
	fmt.Fprintf(os.Stderr, "Read source: the newest 6-hourly Hermes state snapshot in\n")
	fmt.Fprintf(os.Stderr, "$HOME/.hermes/state-backups (state_<YYYYMMDD>-<HHMMSS>.db[.zst|.gz]), read\n")
	fmt.Fprintf(os.Stderr, "read-only via SQLite ATTACH — the live ~/.hermes/state.db is never opened.\n")
	fmt.Fprintf(os.Stderr, "Override with --db <path> (that file only), --snapshot-dir <dir>, or\n")
	fmt.Fprintf(os.Stderr, "--max-snapshot-age <duration> (0 = no age bound).\n\n")
	fmt.Fprintf(os.Stderr, "import and associations-backfill write to the PostgreSQL database configured\n")
	fmt.Fprintf(os.Stderr, "by DB_*/CANOPY_DB_URL — they are in-process importers, not HTTP clients of a\n")
	fmt.Fprintf(os.Stderr, "running canopyd. browse never writes to Canopy (or to the source).\n")
}

// sessionImport imports new Hermes sessions from the read source into Canopy
// trees (WIRE-003). It runs in-process: the Hermes data is opened read-only
// (the 6-hourly snapshot by default — GAP-077 — or an explicit --db file), the
// Canopy services are built against the same PostgreSQL pool/config the server
// uses, and the import is incremental — a watermark file under ~/.canopy/
// records the last imported session so re-runs never duplicate.
func sessionImport(args []string) {
	fs := flag.NewFlagSet("session import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	src := newSessionSourceFlags(fs)
	limit := fs.Int("limit", 0, "maximum number of new sessions to import (0 = unlimited)")
	includeArchived := fs.Bool("include-archived", false, "also import archived sessions")
	dryRun := fs.Bool("dry-run", false, "print what would be imported without writing")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: home directory: %v\n", err)
		os.Exit(1)
	}
	watermarkPath := filepath.Join(home, ".canopy", "session-import.json")

	owner := defaultOwnerID
	if v := os.Getenv("CANOPY_OWNER_ID"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid CANOPY_OWNER_ID %q: %v\n", v, err)
			os.Exit(1)
		}
		owner = id
	}

	ctx := context.Background()

	reader, source, err := src.open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = reader.Close() }()
	fmt.Printf("Source: %s\n", source)

	// Canopy services — same pool/config the server uses.
	cfg := config.FromEnv()
	database, err := db.New(ctx, db.PoolConfig{DSN: cfg.DSN()})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: connect to Canopy database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	treeSvc := service.NewTreeService(database.Trees, database.Nodes, database.Edges, database.Pool)
	// CLI mode has no SSE subscribers; nil hub is safe (broadcast is skipped).
	nodeSvc := service.NewNodeService(database.Nodes, database.Edges, database.Pool, nil)

	imp := session.NewImporter(reader, treeSvc, nodeSvc,
		&session.FileWatermarkStore{Path: watermarkPath}, owner)
	imp.SetSessionChecker(treeSvc)

	sum, err := imp.Run(ctx, session.ImportOptions{
		Limit:           *limit,
		IncludeArchived: *includeArchived,
		DryRun:          *dryRun,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	printImportSummary(sum, watermarkPath)
}

// printImportSummary renders the import result to stdout, mirroring the
// terse style of the existing tree subcommands.
func printImportSummary(sum *session.ImportSummary, watermarkPath string) {
	if sum.DryRun {
		fmt.Println("Dry run — nothing was written.")
		fmt.Printf("  Would import sessions: %d\n", sum.SessionsImported)
		fmt.Printf("  Would create trees:    %d\n", sum.TreesCreated)
		fmt.Printf("  Would create nodes:    %d\n", sum.NodesCreated)
	} else {
		fmt.Println("Session import complete.")
		fmt.Printf("  Sessions imported: %d\n", sum.SessionsImported)
		fmt.Printf("  Trees created:     %d\n", sum.TreesCreated)
		fmt.Printf("  Nodes created:     %d\n", sum.NodesCreated)
		if sum.SessionsImported > 0 {
			fmt.Printf("  Watermark saved:   %s\n", watermarkPath)
		}
	}
	if sum.SkippedArchived > 0 {
		fmt.Printf("  Skipped (archived): %d\n", sum.SkippedArchived)
	}
	if sum.SkippedDuplicates > 0 {
		fmt.Printf("  Skipped (duplicate): %d\n", sum.SkippedDuplicates)
	}
	if len(sum.Titles) > 0 {
		fmt.Println("  Titles:")
		shown := sum.Titles
		if len(shown) > 10 {
			shown = shown[:10]
		}
		for _, t := range shown {
			fmt.Printf("    - %s\n", t)
		}
		if len(sum.Titles) > 10 {
			fmt.Printf("    … and %d more\n", len(sum.Titles)-10)
		}
	}
}

// sessionAssociationsBackfill recomputes and updates tree metadata for
// already-imported Hermes sessions (WIRE-006). It reads the Hermes session
// source (the 6-hourly snapshot by default — GAP-077 — or an explicit --db
// file), computes association metadata (parent/children/delegation goals +
// title-parsed project/task/commit), looks up the matching Canopy tree by
// metadata->>'session_id', and replaces the tree's metadata JSON.
//
// Safe to re-run: a second invocation produces identical metadata (the
// session source is read-only, and the computation is deterministic).
// Sessions whose trees don't exist in Canopy are silently skipped.
//
// Usage: canopyd session associations-backfill [--db path] [--snapshot-dir dir] [--dry-run]
func sessionAssociationsBackfill(args []string) {
	fs := flag.NewFlagSet("session associations-backfill", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	src := newSessionSourceFlags(fs)
	dryRun := fs.Bool("dry-run", false, "print what would be updated without writing")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	owner := defaultOwnerID
	if v := os.Getenv("CANOPY_OWNER_ID"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid CANOPY_OWNER_ID %q: %v\n", v, err)
			os.Exit(1)
		}
		owner = id
	}
	_ = owner // not used for metadata updates, but kept for consistency

	ctx := context.Background()

	reader, source, err := src.open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = reader.Close() }()
	fmt.Printf("Source: %s\n", source)

	sessions, err := reader.ListSessions(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: list sessions: %v\n", err)
		os.Exit(1)
	}
	delegations, _ := reader.ListDelegations(ctx) // best-effort
	idx := session.BuildSessionIndex(sessions, delegations)

	// Canopy services.
	cfg := config.FromEnv()
	database, err := db.New(ctx, db.PoolConfig{DSN: cfg.DSN()})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: connect to Canopy database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	treeSvc := service.NewTreeService(database.Trees, database.Nodes, database.Edges, database.Pool)

	// Build a lookup of all session IDs → tree IDs by querying Canopy.
	// We batch the session IDs to resolve them to trees.
	allSessionIDs := make([]string, 0, len(sessions))
	for _, s := range sessions {
		allSessionIDs = append(allSessionIDs, s.ID)
	}
	treeLookup, err := treeSvc.GetTreesBySessionIDs(ctx, allSessionIDs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: resolve trees by session ids: %v\n", err)
		os.Exit(1)
	}

	var updated, skipped, notFound int
	for _, s := range sessions {
		tree, ok := treeLookup[s.ID]
		if !ok || tree == nil {
			notFound++
			continue
		}
		assoc := session.ComputeAssociations(s, idx)
		meta := session.NewTreeMetadata(s.ID, assoc)
		metaJSON, err := meta.Marshal()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: marshal metadata for %s: %v\n", s.ID, err)
			skipped++
			continue
		}

		if *dryRun {
			updated++
			fmt.Printf("  [dry-run] would update tree %s (session %s)\n", tree.ID, s.ID)
			continue
		}

		if err := treeSvc.UpdateTreeMetadata(ctx, tree.ID, metaJSON); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: update metadata for tree %s: %v\n", tree.ID, err)
			skipped++
			continue
		}
		updated++
	}

	if *dryRun {
		fmt.Println("Associations backfill dry run — nothing was written.")
	} else {
		fmt.Println("Associations backfill complete.")
	}
	fmt.Printf("  Trees updated:    %d\n", updated)
	if skipped > 0 {
		fmt.Printf("  Skipped (error):  %d\n", skipped)
	}
	fmt.Printf("  Sessions skipped (no tree): %d\n", notFound)
}

// sessionBrowse prints what Canopy would read from the session source: the
// session list (default) or one session's messages (--session). It is
// read-only in both directions — it never touches the Canopy database, and the
// source is opened read-only (GAP-077's browse surface for the snapshot
// reader).
//
// Usage: canopyd session browse [--db path | --snapshot-dir dir] [--limit N] [--session ID]
func sessionBrowse(args []string) {
	os.Exit(sessionBrowseE(args))
}

// sessionBrowseE is sessionBrowse without the process exit so tests can assert
// exit codes and capture output.
func sessionBrowseE(args []string) int {
	fs := flag.NewFlagSet("session browse", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	src := newSessionSourceFlags(fs)
	limit := fs.Int("limit", 20, "maximum number of sessions to list (0 = all)")
	sessionID := fs.String("session", "", "show this session's messages instead of the session list")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	ctx := context.Background()
	reader, source, err := src.open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	defer func() { _ = reader.Close() }()
	fmt.Printf("Source: %s\n", source)

	sessions, err := reader.ListSessions(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: list sessions: %v\n", err)
		return 1
	}
	if *sessionID != "" {
		return browseSessionMessages(ctx, reader, sessions, *sessionID)
	}
	return browseSessionList(ctx, reader, sessions, *limit)
}

// browseSessionList prints the session list with per-session message counts.
func browseSessionList(ctx context.Context, reader *session.Reader, sessions []session.Session, limit int) int {
	shown := sessions
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	fmt.Printf("Sessions: %d of %d\n", len(shown), len(sessions))
	if len(shown) == 0 {
		fmt.Println("  (none)")
		return 0
	}
	for _, s := range shown {
		msgs, err := reader.ListMessages(ctx, s.ID)
		count := "?"
		if err == nil {
			count = strconv.Itoa(len(msgs))
		}
		fmt.Printf("  %s  %s  %-10s  msgs=%-5s  %s\n",
			s.ID,
			s.StartedAt.Format(time.RFC3339),
			browseOrDash(s.Source),
			count,
			browseOneLine(s.Title, 60))
	}
	return 0
}

// browseSessionMessages prints one session's messages.
func browseSessionMessages(ctx context.Context, reader *session.Reader, sessions []session.Session, sessionID string) int {
	var found *session.Session
	for i := range sessions {
		if sessions[i].ID == sessionID {
			found = &sessions[i]
			break
		}
	}
	if found == nil {
		fmt.Fprintf(os.Stderr, "Error: session %q not found in the session source\n", sessionID)
		return 1
	}
	fmt.Printf("Session %s — %s (source=%s, model=%s, started=%s)\n",
		found.ID, browseOrDash(found.Title), browseOrDash(found.Source),
		browseOrDash(found.Model), found.StartedAt.Format(time.RFC3339))
	msgs, err := reader.ListMessages(ctx, sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: list messages for %s: %v\n", sessionID, err)
		return 1
	}
	fmt.Printf("Messages: %d\n", len(msgs))
	for _, m := range msgs {
		tool := ""
		if m.ToolName != "" {
			tool = " (" + m.ToolName + ")"
		}
		fmt.Printf("  %s  %-9s%s  %s\n",
			m.Timestamp.Format(time.RFC3339), m.Role, tool, browseOneLine(m.Content, 200))
	}
	return 0
}

// browseOrDash renders an empty field as "-" so columns stay aligned.
func browseOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return strings.TrimSpace(s)
}

// browseOneLine flattens a value to a single truncated line.
func browseOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "-"
	}
	if max > 0 && len(s) > max {
		runes := []rune(s)
		if len(runes) > max {
			return string(runes[:max]) + "…"
		}
	}
	return s
}
