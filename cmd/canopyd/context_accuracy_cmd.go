package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/card"
	"github.com/coding-hermes/hermes-canopy/internal/config"
	ctxpkg "github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/ctxaccuracy"
	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// runContextAccuracyCmd starts the context-selection accuracy command.
func runContextAccuracyCmd(args []string) {
	os.Exit(runContextAccuracyCmdE(args))
}

// runContextAccuracyCmdE returns 0 for a measured score, 1 for a measurement
// or threshold failure, and 2 for invalid command-line usage.
func runContextAccuracyCmdE(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		printContextAccuracyUsage()
		return 0
	}

	fs := flag.NewFlagSet("context-accuracy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = printContextAccuracyUsage
	sample := fs.Int("sample", 25, "number of target nodes to measure")
	jsonOutput := fs.Bool("json", false, "emit the score as JSON")
	minAccuracy := fs.Float64("min-accuracy", -1, "minimum accuracy ratio, from 0.0 to 1.0")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "Error: unexpected argument %q\n", fs.Arg(0))
		printContextAccuracyUsage()
		return 2
	}
	if *sample < 1 {
		fmt.Fprintln(os.Stderr, "Error: --sample must be at least 1")
		return 2
	}
	if *minAccuracy != -1 && (*minAccuracy < 0 || *minAccuracy > 1) {
		fmt.Fprintln(os.Stderr, "Error: --min-accuracy must be between 0.0 and 1.0")
		return 2
	}

	ctx := context.Background()
	cfg := config.FromEnv()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid configuration: %v\n", err)
		return 1
	}
	if cfg.DBDriver != "postgres" {
		fmt.Fprintln(os.Stderr, "Error: context accuracy requires the configured PostgreSQL database")
		return 1
	}
	database, err := db.New(ctx, db.PoolConfig{DSN: cfg.DSN()})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: connect to Canopy database: %v\n", err)
		return 1
	}
	defer database.Close()

	deriver := ctxaccuracy.NewGoldenSetDeriver(&pgAccuracyGraphReader{pool: database.Pool})
	golden, err := deriver.Derive(ctx, *sample)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: derive context-accuracy golden set: %v\n", err)
		return 1
	}
	if len(golden) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no active target nodes available for context-accuracy measurement")
		return 1
	}

	cards := card.NewCardDBManager(card.DataDir())
	defer func() { _ = cards.Close() }()
	compiler := ctxpkg.NewCompiler(
		database.Nodes,
		database.Topics,
		cards,
		ctxpkg.NewTokenEstimator(),
		cfg.ContextMaxRefs,
	)
	compiled := make([]*ctxpkg.CompiledContext, 0, len(golden))
	for _, entry := range golden {
		result, err := compiler.Compile(ctx, ctxpkg.CompileRequest{
			TreeID:       entry.TreeID,
			NodeID:       entry.NodeID,
			TokenBudget:  cfg.ContextDefaultBudget,
			MaxAncestors: cfg.ContextMaxAncestors,
			ResolveRefs:  true,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: compile target %s: %v\n", entry.NodeID, err)
			return 1
		}
		compiled = append(compiled, result)
	}

	score := ctxaccuracy.ScoreGolden(golden, compiled)
	if *jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(score); err != nil {
			fmt.Fprintf(os.Stderr, "Error: encode score: %v\n", err)
			return 1
		}
	} else {
		printContextAccuracyScore(score)
	}
	if *minAccuracy >= 0 && score.Accuracy < *minAccuracy {
		return 1
	}
	return 0
}

func printContextAccuracyUsage() {
	fmt.Fprintln(os.Stderr, "Usage: canopyd context-accuracy --sample N [--json] [--min-accuracy P]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Measure ancestry and topic selection against an independent graph-derived golden set.")
	fmt.Fprintln(os.Stderr, "  --sample N          target nodes to measure (default 25; must be at least 1)")
	fmt.Fprintln(os.Stderr, "  --json              emit the Score struct as JSON")
	fmt.Fprintln(os.Stderr, "  --min-accuracy P    fail below ratio P, from 0.0 to 1.0")
}

func printContextAccuracyScore(score ctxaccuracy.Score) {
	fmt.Printf("SELECTION_ACCURACY=%.2f%%\n", score.Accuracy*100)
	fmt.Printf("entries=%d node_hit=%d node_miss=%d node_extra=%d topic_hit=%d topic_miss=%d\n",
		score.Total, score.NodeHit, score.NodeMiss, score.NodeExtra, score.TopicHit, score.TopicMiss)
	if len(score.PerEntry) == 0 {
		fmt.Println("misses=none")
		return
	}
	for _, entry := range score.PerEntry {
		fmt.Printf("MISS tree=%s node=%s missing=%v extra=%v\n", entry.TreeID, entry.NodeID, entry.Missing, entry.Extra)
	}
}

// pgAccuracyGraphReader reads the graph directly. Its parent query follows
// active graph edges rather than nodes.parent_id, keeping golden derivation
// independent from the compiler's NodeReader.GetAncestors path.
type pgAccuracyGraphReader struct {
	pool *pgxpool.Pool
}

func (r *pgAccuracyGraphReader) SampleTargets(ctx context.Context, sample int) ([]ctxaccuracy.Target, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT tree_id, id
		FROM nodes
		WHERE deleted_at IS NULL
		ORDER BY sequence_num ASC, id ASC
		LIMIT $1`, sample)
	if err != nil {
		return nil, fmt.Errorf("sample nodes: %w", err)
	}
	defer rows.Close()
	var targets []ctxaccuracy.Target
	for rows.Next() {
		var target ctxaccuracy.Target
		if err := rows.Scan(&target.TreeID, &target.NodeID); err != nil {
			return nil, fmt.Errorf("scan sample node: %w", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read sample nodes: %w", err)
	}
	return targets, nil
}

func (r *pgAccuracyGraphReader) IncomingParents(ctx context.Context, nodeID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT e.source_id
		FROM edges e
		JOIN nodes n ON n.id = e.source_id
		WHERE e.target_id = $1
		  AND e.deleted_at IS NULL
		  AND n.deleted_at IS NULL
		  AND e.edge_type IN ('reply', 'fork', 'synthesis')
		ORDER BY e.sequence_num ASC, e.source_id ASC`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("read graph parents: %w", err)
	}
	defer rows.Close()
	var parents []uuid.UUID
	for rows.Next() {
		var parent uuid.UUID
		if err := rows.Scan(&parent); err != nil {
			return nil, fmt.Errorf("scan graph parent: %w", err)
		}
		parents = append(parents, parent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read graph parents: %w", err)
	}
	return parents, nil
}

func (r *pgAccuracyGraphReader) TopicIDs(ctx context.Context, nodeID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT topic_id
		FROM (
			SELECT DISTINCT t.id AS topic_id
			FROM topics t
			JOIN topic_member_nodes tmn ON tmn.topic_id = t.id
			WHERE tmn.node_id = $1 AND t.deleted_at IS NULL
			UNION
			SELECT DISTINCT t.id AS topic_id
			FROM topics t
			JOIN node_resolved_refs nrr ON nrr.topic_id = t.id
			WHERE nrr.node_id = $1 AND t.deleted_at IS NULL
		) topic_ids
		ORDER BY topic_id`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("read graph topics: %w", err)
	}
	defer rows.Close()
	var topics []uuid.UUID
	for rows.Next() {
		var topic uuid.UUID
		if err := rows.Scan(&topic); err != nil {
			return nil, fmt.Errorf("scan graph topic: %w", err)
		}
		topics = append(topics, topic)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read graph topics: %w", err)
	}
	return topics, nil
}
