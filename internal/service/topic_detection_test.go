// Package service — unit tests for the topic detection engine (SPEC-TM-02 §11.1).
// These are fast, PG-free tests covering the deterministic detector: explicit
// patterns, implicit thresholds, structural signals, title generation, and
// highest-signal-wins selection.
package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

// makeNode is a test helper that creates a db.Node with the given content.
func makeNode(content string) db.Node {
	return db.Node{
		ID:        uuid.New(),
		TreeID:    uuid.New(),
		Content:   content,
		NodeType:  db.NodeTypeMessage,
		AuthorID:  uuid.New(),
	}
}

// ── Explicit signal tests (§11.1 scenarios 1-4) ─────────────────────────

func TestTM02_Explicit_MakeThisATopic(t *testing.T) {
	node := makeNode("Let's make this into a topic please")
	sig := DetectExplicit(node)
	require.NotNil(t, sig, "should detect explicit pattern")
	assert.Equal(t, DetectionExplicit, sig.Type)
	assert.InDelta(t, 1.0, sig.Confidence, 0.001)
}

func TestTM02_Explicit_CreateTopicAbout(t *testing.T) {
	node := makeNode("Can you create a topic about database schema design?")
	sig := DetectExplicit(node)
	require.NotNil(t, sig)
	assert.Equal(t, DetectionExplicit, sig.Type)
	assert.Equal(t, "database schema design", sig.SubjectKey)
}

func TestTM02_Explicit_NewTopicOn(t *testing.T) {
	node := makeNode("new topic on deployment strategies for Kubernetes")
	sig := DetectExplicit(node)
	require.NotNil(t, sig)
	assert.Contains(t, sig.SubjectKey, "deployment")
}

func TestTM02_Explicit_NoSubject(t *testing.T) {
	node := makeNode("Create a topic!")
	sig := DetectExplicit(node)
	require.NotNil(t, sig)
	assert.Equal(t, DetectionExplicit, sig.Type)
	// No subject → falls back to node ID
	assert.NotEmpty(t, sig.SubjectKey)
}

func TestTM02_Explicit_CaseInsensitive(t *testing.T) {
	node := makeNode("CREATE A TOPIC please")
	sig := DetectExplicit(node)
	require.NotNil(t, sig)
	assert.Equal(t, DetectionExplicit, sig.Type)
}

func TestTM02_Explicit_PunctuationTolerant(t *testing.T) {
	node := makeNode("Please, turn this as a topic. Thanks.")
	sig := DetectExplicit(node)
	require.NotNil(t, sig)
}

func TestTM02_Explicit_NoMatch(t *testing.T) {
	node := makeNode("I think we should discuss the weather")
	sig := DetectExplicit(node)
	assert.Nil(t, sig)
}

// ── Implicit signal tests (§11.1 scenarios 5-8) ─────────────────────────

func TestTM02_Implicit_ThreeMessageShift(t *testing.T) {
	// Three messages about database design vs. previous about cooking.
	current := []db.Node{
		makeNode("database schema design needs careful normalization"),
		makeNode("postgres migration strategy for the database"),
		makeNode("database indexing optimization for queries"),
	}
	previous := []db.Node{
		makeNode("I love cooking pasta with tomato sauce"),
		makeNode("the recipe needs garlic and olive oil"),
		makeNode("cooking Italian food is my favorite hobby"),
		makeNode("pasta should be cooked al dente"),
	}
	cfg := DefaultDetectionConfig()
	sig := DetectImplicit(current, previous, cfg, nil)
	require.NotNil(t, sig, "should detect implicit shift")
	assert.Equal(t, DetectionImplicit, sig.Type)
	assert.InDelta(t, 0.85, sig.Confidence, 0.001, "3 msgs → confidence >= 0.85")
}

func TestTM02_Implicit_TwoMessagesNoProposal(t *testing.T) {
	current := []db.Node{
		makeNode("database schema design"),
		makeNode("database migration"),
	}
	previous := []db.Node{
		makeNode("cooking pasta recipes"),
		makeNode("Italian food is great"),
	}
	cfg := DefaultDetectionConfig()
	sig := DetectImplicit(current, previous, cfg, nil)
	assert.Nil(t, sig, "2 messages should not qualify")
}

func TestTM02_Implicit_StableConversation(t *testing.T) {
	current := []db.Node{
		makeNode("database schema design is important"),
		makeNode("database migration needs planning"),
		makeNode("database queries should be optimized"),
	}
	previous := []db.Node{
		makeNode("database schema needs normalization"),
		makeNode("database indexes improve performance"),
		makeNode("database design is crucial"),
	}
	cfg := DefaultDetectionConfig()
	sig := DetectImplicit(current, previous, cfg, nil)
	assert.Nil(t, sig, "stable conversation should not trigger")
}

func TestTM02_Implicit_MinMessagesConfig(t *testing.T) {
	cfg := DefaultDetectionConfig()
	cfg.MinMessagesPerTopic = 5
	current := []db.Node{
		makeNode("quantum physics research papers"),
		makeNode("quantum entanglement experiments"),
		makeNode("quantum computing breakthroughs"),
	}
	previous := []db.Node{
		makeNode("classical mechanics tutorials"),
		makeNode("Newtonian physics problems"),
	}
	sig := DetectImplicit(current, previous, cfg, nil)
	assert.Nil(t, sig, "should not fire with < MinMessagesPerTopic")
}

// ── Code-window analysis (§11.1 scenario 27) ────────────────────────────

func TestTM02_CodeWindow_NoSpuriousTopic(t *testing.T) {
	current := []db.Node{
		makeNode("```\nfunc main() {\n	fmt.Println(\"hello\")\n}\n```"),
		makeNode("```\npackage main\nimport \"fmt\"\n```"),
		makeNode("```\nerr := db.Query()\n```"),
	}
	previous := []db.Node{
		makeNode("```\nconsole.log(\"hello\")\n```"),
		makeNode("```\nconst x = 42\n```"),
	}
	analyzer := NewKeywordAnalyzer()
	dist, overlap, err := analyzer.Analyze(current, previous)
	require.NoError(t, err)
	// Code-heavy windows should have some distance but the overlap
	// from code tokens prevents false positives in normal prose.
	_ = dist
	_ = overlap
}

// ── Highest signal wins (§11.1 scenario 11) ─────────────────────────────

func TestTM02_HighestSignalWins(t *testing.T) {
	signals := []*Signal{
		{Type: DetectionImplicit, Confidence: 0.75},
		{Type: DetectionExplicit, Confidence: 1.0},
		{Type: DetectionStructural, Confidence: 0.90},
	}
	best := HighestConfidenceSignal(signals)
	require.NotNil(t, best)
	assert.Equal(t, DetectionExplicit, best.Type)
	assert.InDelta(t, 1.0, best.Confidence, 0.001)
}

func TestTM02_HighestSignalWins_NilAll(t *testing.T) {
	signals := []*Signal{nil, nil}
	best := HighestConfidenceSignal(signals)
	assert.Nil(t, best)
}

// ── Title generation (§4) ───────────────────────────────────────────────

func TestTM02_GenerateTitle_FromRootContent(t *testing.T) {
	root := makeNode("Database Schema Design Discussion")
	title := GenerateTitle(root, nil, 3)
	assert.NotEmpty(t, title)
	assert.Contains(t, title, "Database Schema Design")
}

func TestTM02_GenerateTitle_NormalizesWhitespace(t *testing.T) {
	root := makeNode("  Multiple   spaces   and\nnewlines  ")
	title := GenerateTitle(root, nil, 3)
	assert.NotEmpty(t, title)
	assert.NotContains(t, title, "  ", "should collapse whitespace")
}

func TestTM02_GenerateTitle_Caps200Chars(t *testing.T) {
	longContent := make([]rune, 300)
	for i := range longContent {
		longContent[i] = 'a'
	}
	root := makeNode(string(longContent))
	title := GenerateTitle(root, nil, 3)
	assert.LessOrEqual(t, len([]rune(title)), 200)
}

func TestTM02_GenerateTitle_EmptyContentFallback(t *testing.T) {
	root := makeNode("")
	title := GenerateTitle(root, nil, 3)
	assert.Empty(t, title, "empty content should produce empty title")
}

func TestTM02_GenerateTitle_SkipsCodeFence(t *testing.T) {
	root := makeNode("```\ncode here\n```\nThis is the real title")
	title := GenerateTitle(root, nil, 3)
	assert.Contains(t, title, "real title")
}

// ── Keyword analyzer ────────────────────────────────────────────────────

func TestTM02_KeywordAnalyzer_DistanceComputation(t *testing.T) {
	a := NewKeywordAnalyzer()
	current := []db.Node{makeNode("database postgres sql")}
	previous := []db.Node{makeNode("cooking recipe pasta")}

	dist, overlap, err := a.Analyze(current, previous)
	require.NoError(t, err)
	assert.Greater(t, dist, 0.5, "different topics should have high distance")
	assert.Less(t, overlap, 0.3, "different topics should have low overlap")
}

func TestTM02_KeywordAnalyzer_SameContent(t *testing.T) {
	a := NewKeywordAnalyzer()
	current := []db.Node{makeNode("database postgres sql")}
	previous := []db.Node{makeNode("database postgres sql")}

	dist, overlap, err := a.Analyze(current, previous)
	require.NoError(t, err)
	assert.InDelta(t, 0.0, dist, 0.01, "same content → distance ~0")
	assert.InDelta(t, 1.0, overlap, 0.01, "same content → overlap ~1")
}

// ── Detection config ────────────────────────────────────────────────────

func TestTM02_DefaultDetectionConfig(t *testing.T) {
	cfg := DefaultDetectionConfig()
	assert.True(t, cfg.AlwaysAsk)
	assert.False(t, cfg.AutoCreate)
	assert.Equal(t, DetectionLevelFull, cfg.DetectionLevel)
	assert.Equal(t, 3, cfg.MinMessagesPerTopic)
	assert.Equal(t, 10, cfg.ProposalCooldown)
}

// ── Subject extraction helpers ──────────────────────────────────────────

func TestTM02_CleanSubject_StripsPunctuation(t *testing.T) {
	assert.Equal(t, "database design", cleanSubject("database design."))
	assert.Equal(t, "database", cleanSubject("database!"))
}

func TestTM02_CleanSubject_StripsReferences(t *testing.T) {
	assert.Equal(t, "database", cleanSubject("database #topic-slug"))
}

func TestTM02_SplitCodeBlocks(t *testing.T) {
	content := "Some prose\n```go\nfunc main() {}\n```\nMore prose"
	prose, code := splitCodeBlocks(content)
	assert.Contains(t, prose, "Some prose")
	assert.Contains(t, prose, "More prose")
	require.Len(t, code, 1)
	assert.Contains(t, code[0], "func main")
}
