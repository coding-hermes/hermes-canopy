package main

// Card export CLI subcommand (GAP-083 phase 1).
//
//	canopyd card export [--data-dir <dir>] [--out <file>] [--snapshot-dir <dir>]
//
// Unlike the tree/session/topic subcommands this one is NOT an HTTP client of a
// running canopyd: it reads the local per-type card SQLite stores directly (the
// same stores the server writes) and emits deterministic JSONL — see
// internal/cardexport and docs/CARD_EXPORT.md.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/card"
	"github.com/coding-hermes/hermes-canopy/internal/cardexport"
)

// cardExportNow is the clock behind the snapshot filename: an injection point so
// a test can pin the date instead of racing midnight, and so the name is derived
// from ONE reading of the clock rather than two.
var cardExportNow = func() time.Time { return time.Now() }

// runCardCmd dispatches card sub-subcommands.
func runCardCmd(args []string) {
	os.Exit(runCardCmdE(args))
}

// runCardCmdE is runCardCmd without the process exit so tests can assert exit
// codes. `canopyd card --help` prints usage and returns 0; a bare `canopyd card`
// prints usage and returns 1; an unknown card subcommand returns 1, matching the
// tree/topic subcommands.
func runCardCmdE(args []string) int {
	if len(args) == 0 {
		printCardUsage()
		return 1
	}

	sub := args[0]
	rest := args[1:]

	if sub == "-h" || sub == "--help" {
		printCardUsage()
		return 0
	}

	switch sub {
	case "export":
		return cardExportE(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown card subcommand: %s\n", sub)
		fmt.Fprintf(os.Stderr, "Available: export\n")
		return 1
	}
}

// printCardUsage documents the card subcommands. It is printed by
// `canopyd card --help` (exit 0) and by a bare `canopyd card` (exit 1).
func printCardUsage() {
	fmt.Fprintf(os.Stderr, "Usage: canopyd card <subcommand> [flags]\n\n")
	fmt.Fprintf(os.Stderr, "Subcommands:\n")
	fmt.Fprintf(os.Stderr, "  export [flags]            Export the local card stores as deterministic JSONL\n\n")
	fmt.Fprintf(os.Stderr, "card is the exception to the CLI's HTTP-client subcommands: `card export`\n")
	fmt.Fprintf(os.Stderr, "reads the per-type SQLite card stores directly, in-process, and never\n")
	fmt.Fprintf(os.Stderr, "contacts CANOPY_SERVER_URL or PostgreSQL. It opens every store read-only.\n")
	fmt.Fprintf(os.Stderr, "See docs/CARD_EXPORT.md.\n")
}

// printCardExportUsage documents `canopyd card export`. It is the FlagSet's own
// usage function, so an unknown flag prints the flag error followed by this text
// and exits 2, while -h/--help prints it and exits 0.
func printCardExportUsage() {
	fmt.Fprintf(os.Stderr, "Usage: canopyd card export [flags]\n\n")
	fmt.Fprintf(os.Stderr, "Writes one compact JSON object per card, one line each, ordered by\n")
	fmt.Fprintf(os.Stderr, "(card_type, id) with each card's events ordered by sequence:\n\n")
	fmt.Fprintf(os.Stderr, "  {\"record\":\"card\",\"card_type\":\"compact\",\"card\":{...},\"events\":[{...}]}\n\n")
	fmt.Fprintf(os.Stderr, "The export carries no timestamp, hostname or version, so two exports of an\n")
	fmt.Fprintf(os.Stderr, "unchanged store are byte-identical and diff cleanly in git.\n\n")
	fmt.Fprintf(os.Stderr, "Flags:\n")
	fmt.Fprintf(os.Stderr, "  --data-dir <dir>      Card data directory (default %s)\n", card.DataDir())
	fmt.Fprintf(os.Stderr, "  --out <file>          Write the export to <file> instead of stdout\n")
	fmt.Fprintf(os.Stderr, "  --snapshot-dir <dir>  Write <dir>/cards-<YYYY-MM-DD>.jsonl (UTC), atomically\n")
	fmt.Fprintf(os.Stderr, "  -h, --help            Print this help and exit\n\n")
	fmt.Fprintf(os.Stderr, "Every store is opened read-only: nothing in the data directory is written,\n")
	fmt.Fprintf(os.Stderr, "and a store missing its cards table is an error rather than an empty export.\n")
	fmt.Fprintf(os.Stderr, "--out and --snapshot-dir are mutually exclusive.\n")
}

// cardExportE implements `canopyd card export` and returns its exit code
// (0 success, 1 the command ran and failed, 2 CLI misuse).
func cardExportE(args []string) int {
	fs := flag.NewFlagSet("card export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = printCardExportUsage

	dataDirFlag := fs.String("data-dir", "", "card data directory (default: $CANOPY_CARD_DATA_DIR or ~/.hermes/canopy/cards)")
	outFlag := fs.String("out", "", "write the export to this file instead of stdout")
	snapshotDirFlag := fs.String("snapshot-dir", "", "write <dir>/cards-<YYYY-MM-DD>.jsonl atomically")

	if err := fs.Parse(args); err != nil {
		// fs.Usage has already printed the usage text: for -h/--help that is the
		// whole output and the exit code is 0, for anything else it is CLI misuse.
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitUnknownSubcommand
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "Error: unexpected argument %q\n", fs.Arg(0))
		printCardExportUsage()
		return exitUnknownSubcommand
	}
	if *outFlag != "" && *snapshotDirFlag != "" {
		fmt.Fprintln(os.Stderr, "Error: --out and --snapshot-dir are mutually exclusive — a snapshot must be written atomically, and one of them would have to win silently")
		printCardExportUsage()
		return exitUnknownSubcommand
	}

	// Resolved per call, never cached: the card package reads
	// CANOPY_CARD_DATA_DIR on every DataDir() call, so a scratch instance can
	// redirect the store after the process started.
	dataDir := *dataDirFlag
	if dataDir == "" {
		dataDir = card.DataDir()
	}

	if *snapshotDirFlag != "" {
		return cardExportToSnapshot(dataDir, *snapshotDirFlag)
	}
	return cardExportToWriter(dataDir, *outFlag)
}

// cardExportToWriter writes the export to stdout, or to a file when out is set.
// `--out` receives exactly the bytes stdout would have carried — the same
// Options, the same writer interface, no filename in the stream.
func cardExportToWriter(dataDir, out string) int {
	var file *os.File
	w := io.Writer(os.Stdout)
	if out != "" {
		f, err := os.Create(out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: create %s: %v\n", out, err)
			return 1
		}
		file, w = f, f
	}

	res, err := cardexport.Export(context.Background(), cardexport.Options{DataDir: dataDir, Out: w})
	if file != nil {
		if cerr := file.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}
	if err != nil {
		// A half-written export is worse than none: a reader cannot tell it from
		// a complete one. The file was created by this run, so it is removed.
		if file != nil {
			_ = os.Remove(out)
			fmt.Fprintf(os.Stderr, "Error: %v (removed the partial %s)\n", err, out)
			return 1
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	reportCardExport(res)
	return 0
}

// cardExportToSnapshot writes the export to <dir>/cards-<YYYY-MM-DD>.jsonl
// (UTC date) through a `*.tmp` file in the same directory, renamed into place on
// success: a reader either sees the previous file or the complete new one, never
// a partial export. Nothing is left behind — a failed write removes its own tmp
// file, and the target directory is never created.
func cardExportToSnapshot(dataDir, snapshotDir string) int {
	path := cardSnapshotPath(snapshotDir, cardExportNow())
	tmp := path + ".tmp"

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: create %s: %v (does the snapshot directory exist?)\n", tmp, err)
		return 1
	}

	res, err := cardexport.Export(context.Background(), cardexport.Options{DataDir: dataDir, Out: f})
	if cerr := f.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		fmt.Fprintf(os.Stderr, "Error: %v (no snapshot written)\n", err)
		return 1
	}

	reportCardExport(res)
	fmt.Println(path)
	return 0
}

// cardSnapshotPath is the snapshot target for the UTC day of now.
func cardSnapshotPath(dir string, now time.Time) string {
	return filepath.Join(dir, "cards-"+now.UTC().Format("2006-01-02")+".jsonl")
}

// reportCardExport prints the human summary on stderr, where it can never be
// mistaken for export bytes: stdout carries the JSONL (or the snapshot path) and
// nothing else. Counts the format cannot represent — an event with no card, a
// timestamp no layout matched — are reported here rather than dropped silently.
func reportCardExport(res cardexport.Result) {
	fmt.Fprintf(os.Stderr, "canopyd: exported %d card(s), %d event(s) from %d database(s), %d bytes\n",
		res.Cards, res.Events, res.Files, res.Bytes)
	if res.OrphanEvents > 0 {
		fmt.Fprintf(os.Stderr, "Warning: %d event(s) reference a card that does not exist in their database and were not exported\n", res.OrphanEvents)
	}
	if res.UnparsedTimestamps > 0 {
		fmt.Fprintf(os.Stderr, "Warning: %d timestamp value(s) matched no known layout and were exported as the zero time\n", res.UnparsedTimestamps)
	}
}
