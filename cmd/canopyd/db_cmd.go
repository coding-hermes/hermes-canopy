package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	sqlitestore "github.com/coding-hermes/hermes-canopy/internal/db/sqlite"
)

// runDBCmd dispatches database maintenance subcommands.
func runDBCmd(args []string) {
	os.Exit(runDBCmdE(args))
}

// runDBCmdE returns 0 on success, 1 when the export ran and failed, and 2 for
// invalid command-line usage.
func runDBCmdE(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printDBUsage()
		if len(args) == 0 {
			return 1
		}
		return 0
	}
	if args[0] != "export-sqlite" {
		fmt.Fprintf(os.Stderr, "unknown db subcommand: %s\n", args[0])
		printDBUsage()
		return 2
	}
	return exportSQLiteE(args[1:])
}

func printDBUsage() {
	fmt.Fprintln(os.Stderr, "Usage: canopyd db export-sqlite --sqlite-path <file>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Copies the core graph tables (users, trees, nodes, edges) from the")
	fmt.Fprintln(os.Stderr, "configured PostgreSQL database into a new SQLite file.")
	fmt.Fprintln(os.Stderr, "PostgreSQL source resolves CANOPY_DB_URL or any non-empty DB_* variable; the built-in default is localhost:5432, while the project's development stack uses :5437.")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Flags:")
	fmt.Fprintln(os.Stderr, "  --sqlite-path <file>  Destination SQLite file (required)")
	fmt.Fprintln(os.Stderr, "  --dsn <file>          Alias for --sqlite-path")
}

func exportSQLiteE(args []string) int {
	fs := flag.NewFlagSet("db export-sqlite", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = printDBUsage
	path := fs.String("sqlite-path", "", "destination SQLite file")
	dsnAlias := fs.String("dsn", "", "destination SQLite file (alias for --sqlite-path)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "Error: unexpected argument %q\n", fs.Arg(0))
		printDBUsage()
		return 2
	}
	if *path != "" && *dsnAlias != "" && *path != *dsnAlias {
		fmt.Fprintln(os.Stderr, "Error: --sqlite-path and --dsn disagree")
		return 2
	}
	if *path == "" {
		*path = *dsnAlias
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "Error: --sqlite-path is required")
		printDBUsage()
		return 2
	}

	cfg := config.FromEnv()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid configuration: %v\n", err)
		return 2
	}
	target := config.DBTargetOf(cfg, os.Getenv)
	_, _ = fmt.Fprintf(os.Stdout, "%s\n", target.Describe())
	if err := sqlitestore.ExportPostgres(context.Background(), cfg.DSN(), *path); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(os.Stdout, "SQLite export complete: %s\n", *path)
	return 0
}
