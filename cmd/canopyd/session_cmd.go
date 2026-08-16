package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/session"
)

// defaultOwnerID is the dev-user UUID used when CANOPY_OWNER_ID is unset.
var defaultOwnerID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// runSessionCmd dispatches session sub-subcommands.
func runSessionCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: canopyd session <import> [flags...]\n")
		os.Exit(1)
	}
	switch args[0] {
	case "import":
		sessionImport(args[1:])
	case "associations-backfill":
		sessionAssociationsBackfill(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown session subcommand: %s\n", args[0])
		fmt.Fprintf(os.Stderr, "Available: import, associations-backfill\n")
		os.Exit(1)
	}
}

// sessionImport imports new Hermes sessions from state.db into Canopy
// trees (WIRE-003). It runs in-process: state.db is opened read-only, the
// Canopy services are built against the same PostgreSQL pool/config the
// server uses, and the import is incremental — a watermark file under
// ~/.canopy/ records the last imported session so re-runs never duplicate.
func sessionImport(args []string) {
	fs := flag.NewFlagSet("session import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dbPath := fs.String("db", "", "path to Hermes state.db (default $HOME/.hermes/state.db)")
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
	if *dbPath == "" {
		*dbPath = filepath.Join(home, ".hermes", "state.db")
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

	reader, err := session.OpenReader(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = reader.Close() }()

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
// already-imported Hermes sessions (WIRE-006). It reads sessions +
// delegations from state.db, computes association metadata (parent/
// children/delegation goals + title-parsed project/task/commit), looks
// up the matching Canopy tree by metadata->>'session_id', and replaces
// the tree's metadata JSON.
//
// Safe to re-run: a second invocation produces identical metadata (the
// session store is read-only, and the computation is deterministic).
// Sessions whose trees don't exist in Canopy are silently skipped.
//
// Usage: canopyd session associations-backfill [--db path] [--dry-run]
func sessionAssociationsBackfill(args []string) {
	fs := flag.NewFlagSet("session associations-backfill", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dbPath := fs.String("db", "", "path to Hermes state.db (default $HOME/.hermes/state.db)")
	dryRun := fs.Bool("dry-run", false, "print what would be updated without writing")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: home directory: %v\n", err)
		os.Exit(1)
	}
	if *dbPath == "" {
		*dbPath = filepath.Join(home, ".hermes", "state.db")
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

	reader, err := session.OpenReader(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = reader.Close() }()

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
