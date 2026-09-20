package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	fmt.Printf("roots_sampled=%d nonroot_sampled=%d depth_scored=%d edge_only_ancestors=%d root_fallback=%t\n",
		score.RootsSampled, score.NonrootSampled, score.DepthScored, score.EdgeOnlyAncestors, score.RootFallback)
	if score.Warning != "" {
		fmt.Println(score.Warning)
	}
	if len(score.PerEntry) == 0 {
		fmt.Println("misses=none")
		return
	}
	for _, entry := range score.PerEntry {
		fmt.Printf("MISS tree=%s node=%s missing=%v extra=%v edge_only_ancestors=%v\n", entry.TreeID, entry.NodeID, entry.Missing, entry.Extra, entry.EdgeOnlyAncestors)
	}
}

// pgAccuracyGraphReader reads the graph directly. Golden ancestry follows
// nodes.parent_id, matching PGNodeRepo.GetAncestors. EdgeParents is retained
// as a separate diagnostic walk for GAP-073 multi-parent/edge-only paths.
type pgAccuracyGraphReader struct {
	pool *pgxpool.Pool
}

func (r *pgAccuracyGraphReader) SampleTargets(ctx context.Context, sample int) ([]ctxaccuracy.Target, error) {
	rows, err := r.pool.Query(ctx, `
		WITH RECURSIVE ancestry(target_id, ancestor_id, parent_id, depth) AS (
			SELECT n.id, p.id, p.parent_id, 1
			FROM nodes n
			JOIN nodes p ON p.id = n.parent_id
			WHERE n.deleted_at IS NULL
			  AND p.deleted_at IS NULL
			  AND n.parent_id IS NOT NULL
			  AND n.id <> p.id
			UNION ALL
			SELECT a.target_id, p.id, p.parent_id, a.depth + 1
			FROM ancestry a
			JOIN nodes p ON p.id = a.parent_id
			WHERE p.deleted_at IS NULL
			  AND a.depth < 10000
		),
		eligible AS (
			SELECT DISTINCT n.tree_id, n.id, n.sequence_num
			FROM nodes n
			JOIN ancestry a ON a.target_id = n.id
			WHERE n.deleted_at IS NULL
		),
		ranked AS (
			SELECT tree_id, id, sequence_num,
			       row_number() OVER (
				       PARTITION BY tree_id
				       ORDER BY sequence_num DESC, id DESC
			       ) AS tree_rank
			FROM eligible
		)
		SELECT tree_id, id, false, false
		FROM ranked
		ORDER BY tree_rank ASC, sequence_num DESC, id ASC
		LIMIT $1`, sample)
	if err != nil {
		return nil, fmt.Errorf("sample non-root nodes: %w", err)
	}
	defer rows.Close()
	targets, err := scanAccuracyTargets(rows)
	if err != nil {
		return nil, err
	}
	if len(targets) >= sample {
		return targets, nil
	}

	remaining := sample - len(targets)
	rootRows, err := r.pool.Query(ctx, `
		WITH ranked AS (
			SELECT tree_id, id, sequence_num,
			       row_number() OVER (
				       PARTITION BY tree_id
				       ORDER BY sequence_num ASC, id ASC
			       ) AS tree_rank
			FROM nodes
			WHERE deleted_at IS NULL AND parent_id IS NULL
		)
		SELECT tree_id, id, true, true
		FROM ranked
		ORDER BY tree_rank ASC, sequence_num ASC, id ASC
		LIMIT $1`, remaining)
	if err != nil {
		return nil, fmt.Errorf("fill sample with root nodes: %w", err)
	}
	defer rootRows.Close()
	roots, err := scanAccuracyTargets(rootRows)
	if err != nil {
		return nil, err
	}
	return append(targets, roots...), nil
}

func scanAccuracyTargets(rows pgx.Rows) ([]ctxaccuracy.Target, error) {
	var targets []ctxaccuracy.Target
	for rows.Next() {
		var target ctxaccuracy.Target
		if err := rows.Scan(&target.TreeID, &target.NodeID, &target.IsRoot, &target.RootFallback); err != nil {
			return nil, fmt.Errorf("scan sample node: %w", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read sample nodes: %w", err)
	}
	return targets, nil
}

func (r *pgAccuracyGraphReader) ParentIDParent(ctx context.Context, nodeID uuid.UUID) (uuid.UUID, error) {
	var parent *uuid.UUID
	err := r.pool.QueryRow(ctx, `
		SELECT parent_id
		FROM nodes
		WHERE id = $1 AND deleted_at IS NULL`, nodeID).Scan(&parent)
	if errors.Is(err, pgx.ErrNoRows) || parent == nil {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("read parent_id parent: %w", err)
	}
	return *parent, nil
}

func (r *pgAccuracyGraphReader) EdgeParents(ctx context.Context, nodeID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT e.source_id
		FROM edges e
		JOIN nodes n ON n.id = e.source_id
		WHERE e.target_id = $1
		  AND e.deleted_at IS NULL
		  AND n.deleted_at IS NULL
		  AND e.source_id <> e.target_id
		  AND e.edge_type IN ('reply', 'fork', 'synthesis')
		ORDER BY e.sequence_num ASC, e.source_id ASC`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("read edge parents: %w", err)
	}
	defer rows.Close()
	var parents []uuid.UUID
	for rows.Next() {
		var parent uuid.UUID
		if err := rows.Scan(&parent); err != nil {
			return nil, fmt.Errorf("scan edge parent: %w", err)
		}
		parents = append(parents, parent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read edge parents: %w", err)
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
