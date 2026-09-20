// Package ctxaccuracy measures context-selection accuracy against the ancestry
// contract used by the context compiler.
//
// GoldenSetDeriver walks the nodes.parent_id chain used by NodeReader.GetAncestors.
// It also records ancestry reachable only through active graph edges as diagnostic
// detail; those edge-only nodes are not part of the compiler's expected set.
package ctxaccuracy

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"

	ctx "github.com/coding-hermes/hermes-canopy/internal/context"
)

// GoldenEntry is the graph-derived expectation for one sampled target node.
type GoldenEntry struct {
	TreeID            uuid.UUID   `json:"treeId"`
	NodeID            uuid.UUID   `json:"nodeId"`
	ExpectedNodes     []uuid.UUID `json:"expectedNodes"`
	ExpectedTopics    []uuid.UUID `json:"expectedTopics"`
	IsRoot            bool        `json:"isRoot,omitempty"`
	RootFallback      bool        `json:"rootFallback,omitempty"`
	EdgeOnlyAncestors []uuid.UUID `json:"edgeOnlyAncestors,omitempty"`
}

// GoldenDeriver derives one graph-backed golden entry per sampled target node.
type GoldenDeriver interface {
	// Derive returns one golden entry per sampled target node.
	Derive(ctx context.Context, sample int) ([]GoldenEntry, error)
}

// Target identifies a node selected for measurement.
type Target struct {
	TreeID       uuid.UUID
	NodeID       uuid.UUID
	IsRoot       bool
	RootFallback bool
}

// GraphReader is the graph-data seam used by GoldenSetDeriver.
// ParentIDParent returns the node's live parent_id parent, which is the
// ancestry mechanism used by the context compiler. EdgeParents is diagnostic
// only: it returns active incoming graph-edge sources so edge-only ancestry can
// be reported without turning it into a compiler miss.
// TopicIDs must return the active topic membership/reference set for nodeID.
type GraphReader interface {
	SampleTargets(ctx context.Context, sample int) ([]Target, error)
	ParentIDParent(ctx context.Context, nodeID uuid.UUID) (uuid.UUID, error)
	EdgeParents(ctx context.Context, nodeID uuid.UUID) ([]uuid.UUID, error)
	TopicIDs(ctx context.Context, nodeID uuid.UUID) ([]uuid.UUID, error)
}

// GoldenSetDeriver derives the ancestry contract used by the compiler and
// records the edge-only graph superset as diagnostic detail.
type GoldenSetDeriver struct {
	reader GraphReader
}

// NewGoldenSetDeriver constructs a graph-backed golden deriver.
func NewGoldenSetDeriver(reader GraphReader) *GoldenSetDeriver {
	return &GoldenSetDeriver{reader: reader}
}

// Derive samples targets and follows each target's parent_id chain. Active
// incoming edges are walked separately only to identify edge-only ancestors.
// Cycles are tolerated defensively by visited sets; malformed graph data cannot
// make the measurement loop forever.
func (d *GoldenSetDeriver) Derive(ctx context.Context, sample int) ([]GoldenEntry, error) {
	if sample < 1 {
		return nil, errors.New("ctxaccuracy: sample must be at least 1")
	}
	if d == nil || d.reader == nil {
		return nil, errors.New("ctxaccuracy: nil graph reader")
	}
	targets, err := d.reader.SampleTargets(ctx, sample)
	if err != nil {
		return nil, fmt.Errorf("ctxaccuracy: sample targets: %w", err)
	}
	if len(targets) > sample {
		targets = targets[:sample]
	}

	entries := make([]GoldenEntry, 0, len(targets))
	for _, target := range targets {
		nodes, err := d.walkParentID(ctx, target.NodeID)
		if err != nil {
			return nil, fmt.Errorf("ctxaccuracy: derive parent_id ancestry for %s: %w", target.NodeID, err)
		}
		edgeNodes, err := d.walkEdges(ctx, target.NodeID)
		if err != nil {
			return nil, fmt.Errorf("ctxaccuracy: derive edge diagnostics for %s: %w", target.NodeID, err)
		}
		topics, err := d.reader.TopicIDs(ctx, target.NodeID)
		if err != nil {
			return nil, fmt.Errorf("ctxaccuracy: derive topics for %s: %w", target.NodeID, err)
		}
		expected := unique(nodes)
		edgeOnly := difference(unique(edgeNodes), expected)
		entries = append(entries, GoldenEntry{
			TreeID:            target.TreeID,
			NodeID:            target.NodeID,
			ExpectedNodes:     expected,
			ExpectedTopics:    unique(topics),
			IsRoot:            target.IsRoot,
			RootFallback:      target.RootFallback,
			EdgeOnlyAncestors: edgeOnly,
		})
	}
	return entries, nil
}

func (d *GoldenSetDeriver) walkParentID(ctx context.Context, target uuid.UUID) ([]uuid.UUID, error) {
	seen := map[uuid.UUID]bool{target: true}
	nodes := []uuid.UUID{target}
	current := target
	for {
		parent, err := d.reader.ParentIDParent(ctx, current)
		if err != nil {
			return nil, err
		}
		if parent == uuid.Nil || seen[parent] {
			return nodes, nil
		}
		seen[parent] = true
		nodes = append(nodes, parent)
		current = parent
	}
}

func (d *GoldenSetDeriver) walkEdges(ctx context.Context, target uuid.UUID) ([]uuid.UUID, error) {
	seen := map[uuid.UUID]bool{target: true}
	queue := []uuid.UUID{target}
	for head := 0; head < len(queue); head++ {
		parents, err := d.reader.EdgeParents(ctx, queue[head])
		if err != nil {
			return nil, err
		}
		for _, parent := range parents {
			if parent == uuid.Nil || seen[parent] {
				continue
			}
			seen[parent] = true
			queue = append(queue, parent)
		}
	}
	return queue, nil
}

// EntryScore describes selection misses and extras for one golden entry.
type EntryScore struct {
	TreeID            uuid.UUID   `json:"treeId"`
	NodeID            uuid.UUID   `json:"nodeId"`
	Missing           []uuid.UUID `json:"missing"`
	Extra             []uuid.UUID `json:"extra"`
	MissingNodes      []uuid.UUID `json:"missingNodes"`
	MissingTopics     []uuid.UUID `json:"missingTopics"`
	ExtraNodes        []uuid.UUID `json:"extraNodes"`
	ExtraTopics       []uuid.UUID `json:"extraTopics"`
	EdgeOnlyAncestors []uuid.UUID `json:"edgeOnlyAncestors,omitempty"`
}

// Score is the pooled score for a golden set and its compiled manifests.
type Score struct {
	Total             int          `json:"total"`
	NodeHit           int          `json:"nodeHit"`
	NodeMiss          int          `json:"nodeMiss"`
	NodeExtra         int          `json:"nodeExtra"`
	TopicHit          int          `json:"topicHit"`
	TopicMiss         int          `json:"topicMiss"`
	Accuracy          float64      `json:"accuracy"`
	RootsSampled      int          `json:"rootsSampled"`
	NonrootSampled    int          `json:"nonrootSampled"`
	DepthScored       int          `json:"depthScored"`
	EdgeOnlyAncestors int          `json:"edgeOnlyAncestors"`
	RootFallback      bool         `json:"rootFallback"`
	Warning           string       `json:"warning,omitempty"`
	PerEntry          []EntryScore `json:"perEntry"`
}

// ScoreGolden compares positional golden entries with compiled manifests.
// Missing compiled results count all expected IDs as misses; nil manifests are
// treated the same way. Accuracy is pooled over all expected nodes and topics,
// never averaged from per-entry ratios.
func ScoreGolden(entries []GoldenEntry, compiled []*ctx.CompiledContext) Score {
	score := Score{Total: len(entries)}
	for i, golden := range entries {
		if golden.IsRoot || len(golden.ExpectedNodes) <= 1 {
			score.RootsSampled++
		} else {
			score.NonrootSampled++
		}
		if len(golden.ExpectedNodes) > 1 {
			score.DepthScored++
		}
		if golden.RootFallback {
			score.RootFallback = true
		}
		edgeOnly := unique(golden.EdgeOnlyAncestors)
		score.EdgeOnlyAncestors += len(edgeOnly)

		var manifest *ctx.Manifest
		if i < len(compiled) && compiled[i] != nil {
			manifest = compiled[i].Manifest
		}

		expectedNodes := unique(golden.ExpectedNodes)
		expectedTopics := unique(golden.ExpectedTopics)
		actualNodes, actualTopics := manifestIDs(manifest)

		entry := EntryScore{TreeID: golden.TreeID, NodeID: golden.NodeID}
		for _, id := range expectedNodes {
			if actualNodes[id] {
				score.NodeHit++
			} else {
				score.NodeMiss++
				entry.MissingNodes = append(entry.MissingNodes, id)
			}
		}
		for _, id := range expectedTopics {
			if actualTopics[id] {
				score.TopicHit++
			} else {
				score.TopicMiss++
				entry.MissingTopics = append(entry.MissingTopics, id)
			}
		}
		for _, id := range sortedIDs(actualNodes) {
			if !contains(expectedNodes, id) {
				score.NodeExtra++
				entry.ExtraNodes = append(entry.ExtraNodes, id)
			}
		}
		for _, id := range sortedIDs(actualTopics) {
			if !contains(expectedTopics, id) {
				entry.ExtraTopics = append(entry.ExtraTopics, id)
			}
		}
		entry.Missing = append(entry.Missing, entry.MissingNodes...)
		entry.Missing = append(entry.Missing, entry.MissingTopics...)
		entry.Extra = append(entry.Extra, entry.ExtraNodes...)
		entry.Extra = append(entry.Extra, entry.ExtraTopics...)
		entry.EdgeOnlyAncestors = edgeOnly
		if len(entry.Missing) > 0 || len(entry.Extra) > 0 || len(edgeOnly) > 0 {
			score.PerEntry = append(score.PerEntry, entry)
		}
	}

	denominator := score.NodeHit + score.NodeMiss + score.TopicHit + score.TopicMiss
	if denominator > 0 {
		score.Accuracy = float64(score.NodeHit+score.TopicHit) / float64(denominator)
	}
	if score.Total > 0 && score.DepthScored == 0 && score.TopicHit+score.TopicMiss == 0 {
		score.Warning = "WARNING: sample not discriminating (no multi-node ancestry) — score is not meaningful"
	}
	return score
}

func manifestIDs(manifest *ctx.Manifest) (map[uuid.UUID]bool, map[uuid.UUID]bool) {
	nodes := make(map[uuid.UUID]bool)
	topics := make(map[uuid.UUID]bool)
	if manifest == nil {
		return nodes, topics
	}
	for _, item := range manifest.Ancestry {
		nodes[item.ID] = true
	}
	for _, item := range manifest.References {
		if item.Kind == "topic" {
			topics[item.ID] = true
		}
	}
	// Cards and multi-reference source items are deliberately not topic hits.
	// They are separate manifest sections and are outside this metric's scope.
	return nodes, topics
}

func unique(ids []uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if id != uuid.Nil && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func difference(ids, excluded []uuid.UUID) []uuid.UUID {
	blocked := make(map[uuid.UUID]bool, len(excluded))
	for _, id := range excluded {
		blocked[id] = true
	}
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !blocked[id] {
			out = append(out, id)
		}
	}
	return out
}

func sortedIDs(ids map[uuid.UUID]bool) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func contains(ids []uuid.UUID, want uuid.UUID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

var _ GoldenDeriver = (*GoldenSetDeriver)(nil)
