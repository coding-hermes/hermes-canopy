package ctxaccuracy

import (
	stdctx "context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	ctx "github.com/coding-hermes/hermes-canopy/internal/context"
)

type fixtureGraph struct {
	targets     []Target
	parents     map[uuid.UUID][]uuid.UUID
	edgeParents map[uuid.UUID][]uuid.UUID
	topics      map[uuid.UUID][]uuid.UUID
}

func (f fixtureGraph) SampleTargets(_ stdctx.Context, sample int) ([]Target, error) {
	if sample > len(f.targets) {
		sample = len(f.targets)
	}
	return f.targets[:sample], nil
}

func (f fixtureGraph) ParentIDParent(_ stdctx.Context, nodeID uuid.UUID) (uuid.UUID, error) {
	parents := f.parents[nodeID]
	if len(parents) == 0 {
		return uuid.Nil, nil
	}
	return parents[0], nil
}

func (f fixtureGraph) EdgeParents(_ stdctx.Context, nodeID uuid.UUID) ([]uuid.UUID, error) {
	if f.edgeParents != nil {
		return f.edgeParents[nodeID], nil
	}
	return f.parents[nodeID], nil
}

func (f fixtureGraph) TopicIDs(_ stdctx.Context, nodeID uuid.UUID) ([]uuid.UUID, error) {
	return f.topics[nodeID], nil
}

func fixture() (fixtureGraph, []GoldenEntry, []*ctx.CompiledContext) {
	tree := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	root := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	child := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	leaf := uuid.MustParse("00000000-0000-0000-0000-000000000004")
	branch := uuid.MustParse("00000000-0000-0000-0000-000000000005")
	topicA := uuid.MustParse("00000000-0000-0000-0000-000000000006")
	topicB := uuid.MustParse("00000000-0000-0000-0000-000000000007")

	graph := fixtureGraph{
		targets: []Target{{TreeID: tree, NodeID: leaf}, {TreeID: tree, NodeID: branch}},
		parents: map[uuid.UUID][]uuid.UUID{
			leaf:   {child},
			child:  {root},
			branch: {root},
		},
		topics: map[uuid.UUID][]uuid.UUID{
			leaf:   {topicA, topicB},
			branch: {topicB},
		},
	}
	golden := []GoldenEntry{
		{TreeID: tree, NodeID: leaf, ExpectedNodes: []uuid.UUID{leaf, child, root}, ExpectedTopics: []uuid.UUID{topicA, topicB}},
		{TreeID: tree, NodeID: branch, ExpectedNodes: []uuid.UUID{branch, root}, ExpectedTopics: []uuid.UUID{topicB}},
	}
	compiled := []*ctx.CompiledContext{
		{Manifest: &ctx.Manifest{
			Ancestry:   []ctx.ManifestItem{{ID: leaf, Kind: "node"}, {ID: child, Kind: "node"}, {ID: root, Kind: "node"}},
			References: []ctx.ManifestItem{{ID: topicA, Kind: "topic"}, {ID: topicB, Kind: "topic"}},
		}},
		{Manifest: &ctx.Manifest{
			Ancestry:   []ctx.ManifestItem{{ID: branch, Kind: "node"}, {ID: root, Kind: "node"}},
			References: []ctx.ManifestItem{{ID: topicB, Kind: "topic"}},
		}},
	}
	return graph, golden, compiled
}

func TestGoldenSet_DerivesParentIDChain(t *testing.T) {
	graph, want, _ := fixture()
	got, err := NewGoldenSetDeriver(graph).Derive(stdctx.Background(), 2)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].TreeID != want[i].TreeID || got[i].NodeID != want[i].NodeID {
			t.Fatalf("entry %d identity = %v/%v, want %v/%v", i, got[i].TreeID, got[i].NodeID, want[i].TreeID, want[i].NodeID)
		}
		if fmt.Sprint(got[i].ExpectedNodes) != fmt.Sprint(want[i].ExpectedNodes) {
			t.Errorf("entry %d nodes = %v, want %v", i, got[i].ExpectedNodes, want[i].ExpectedNodes)
		}
	}
}

func TestGoldenSet_Fixture_ScoresExactly(t *testing.T) {
	_, golden, compiled := fixture()
	score := ScoreGolden(golden, compiled)
	if score.Total != 2 || score.NodeHit != 5 || score.NodeMiss != 0 || score.NodeExtra != 0 || score.TopicHit != 3 || score.TopicMiss != 0 {
		t.Fatalf("unexpected score: %+v", score)
	}
	if score.Accuracy != 1 {
		t.Fatalf("accuracy = %f, want 1", score.Accuracy)
	}
	if score.RootsSampled != 0 || score.NonrootSampled != 2 || score.DepthScored != 2 || score.EdgeOnlyAncestors != 0 || score.Warning != "" {
		t.Fatalf("unexpected sample composition: %+v", score)
	}
	if len(score.PerEntry) != 0 {
		t.Fatalf("happy path PerEntry = %+v, want empty", score.PerEntry)
	}
}

func TestGoldenSet_Fixture_DroppedAncestryIsMiss(t *testing.T) {
	_, golden, compiled := fixture()
	compiled[0].Manifest.Ancestry = compiled[0].Manifest.Ancestry[0:1]
	score := ScoreGolden(golden, compiled)
	if score.NodeHit != 3 || score.NodeMiss != 2 || score.TopicHit != 3 || score.TopicMiss != 0 {
		t.Fatalf("unexpected dropped-node score: %+v", score)
	}
	if score.Accuracy != 6.0/8.0 {
		t.Fatalf("accuracy = %f, want 0.75", score.Accuracy)
	}
	if len(score.PerEntry) != 1 || len(score.PerEntry[0].Missing) != 2 {
		t.Fatalf("missing detail = %+v, want two node ids", score.PerEntry)
	}
	if score.PerEntry[0].Missing[0] != childID() || score.PerEntry[0].Missing[1] != rootID() {
		t.Fatalf("missing ids = %v, want child/root", score.PerEntry[0].Missing)
	}
}

func TestGoldenSet_EdgeOnlyAncestryIsDiagnosticNotMiss(t *testing.T) {
	graph, golden, compiled := fixture()
	edgeOnly := uuid.MustParse("00000000-0000-0000-0000-000000000008")
	graph.edgeParents = map[uuid.UUID][]uuid.UUID{
		golden[0].NodeID: {golden[0].ExpectedNodes[1], edgeOnly},
	}
	got, err := NewGoldenSetDeriver(graph).Derive(stdctx.Background(), 1)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if len(got[0].EdgeOnlyAncestors) != 1 || got[0].EdgeOnlyAncestors[0] != edgeOnly {
		t.Fatalf("edge-only ancestors = %v, want [%s]", got[0].EdgeOnlyAncestors, edgeOnly)
	}
	score := ScoreGolden(got, compiled[:1])
	if score.NodeMiss != 0 || score.EdgeOnlyAncestors != 1 || score.Accuracy != 1 {
		t.Fatalf("edge-only diagnostic changed score: %+v", score)
	}
}

func TestScoreGolden_RootOnlySampleWarns(t *testing.T) {
	root := rootID()
	entries := []GoldenEntry{{NodeID: root, ExpectedNodes: []uuid.UUID{root}}}
	compiled := []*ctx.CompiledContext{{Manifest: &ctx.Manifest{
		Ancestry: []ctx.ManifestItem{{ID: root, Kind: "node"}},
	}}}
	score := ScoreGolden(entries, compiled)
	if score.Accuracy != 1 {
		t.Fatalf("accuracy = %f, want 1 for the deliberately vacuous fixture", score.Accuracy)
	}
	if score.RootsSampled != 1 || score.NonrootSampled != 0 || score.DepthScored != 0 {
		t.Fatalf("unexpected root-only composition: %+v", score)
	}
	if score.Warning == "" {
		t.Fatal("root-only perfect score has no discrimination warning")
	}
}

func TestGoldenSet_MissingCompiledResultCountsAllExpected(t *testing.T) {
	_, golden, _ := fixture()
	score := ScoreGolden(golden[:1], nil)
	if score.NodeHit != 0 || score.NodeMiss != 3 || score.TopicHit != 0 || score.TopicMiss != 2 {
		t.Fatalf("unexpected missing-result score: %+v", score)
	}
	if score.Accuracy != 0 || len(score.PerEntry) != 1 {
		t.Fatalf("missing-result detail = %+v", score)
	}
}

func TestGoldenSet_Fixture_PrintsAccuracy(t *testing.T) {
	_, golden, compiled := fixture()
	score := ScoreGolden(golden, compiled)
	got := fmt.Sprintf("SELECTION_ACCURACY=%.2f%%", score.Accuracy*100)
	want := "SELECTION_ACCURACY=100.00%"
	if got != want {
		t.Fatalf("printed accuracy = %q, want %q", got, want)
	}
	fmt.Println(got)
}

func rootID() uuid.UUID  { return uuid.MustParse("00000000-0000-0000-0000-000000000002") }
func childID() uuid.UUID { return uuid.MustParse("00000000-0000-0000-0000-000000000003") }
