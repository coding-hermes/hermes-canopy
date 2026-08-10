// Package service — topic detection engine.
// Implements SPEC-TM-02 §3 (signals), §4 (algorithm), §8.1 (types).
//
// The engine evaluates three signal classes (explicit, implicit, structural)
// for each newly persisted node and orchestrates proposal generation. The
// implicit signal uses a deterministic keyword/entity/intent fallback instead
// of embeddings — an Analyzer interface seam allows a future agent/embedding
// implementation to be plugged in without changing the orchestration layer.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

// ── Detection types (SPEC-TM-02 §8.1) ───────────────────────────────────

// DetectionConfig holds the per-tree topic-detection settings.
type DetectionConfig struct {
	AutoCreate          bool   `json:"auto_create"`
	AlwaysAsk           bool   `json:"always_ask"`
	DetectionLevel      string `json:"detection_level"` // off | explicit_only | full
	MinMessagesPerTopic int    `json:"min_messages_per_topic"`
	ProposalCooldown    int    `json:"proposal_cooldown"`
}

// DefaultDetectionConfig returns the spec defaults (§7):
// AlwaysAsk=true, AutoCreate=false, DetectionLevel="full",
// MinMessagesPerTopic=3, ProposalCooldown=10.
func DefaultDetectionConfig() DetectionConfig {
	return DetectionConfig{
		AutoCreate:          false,
		AlwaysAsk:           true,
		DetectionLevel:      DetectionLevelFull,
		MinMessagesPerTopic: 3,
		ProposalCooldown:    10,
	}
}

// Detection level constants.
const (
	DetectionLevelOff         = "off"
	DetectionLevelExplicitOnly = "explicit_only"
	DetectionLevelFull        = "full"
)

// DetectionType enumerates the signal classes.
type DetectionType string

const (
	DetectionExplicit   DetectionType = "explicit"
	DetectionImplicit   DetectionType = "implicit"
	DetectionStructural DetectionType = "structural"
)

// Signal is the result of evaluating one signal class for a node.
type Signal struct {
	Type       DetectionType `json:"type"`
	Confidence float32       `json:"confidence"`
	RootNodeID uuid.UUID     `json:"root_node_id"`
	SubjectKey string        `json:"subject_key"`
	Evidence   []string      `json:"evidence"`
}

// ── Explicit signal patterns (SPEC-TM-02 §3.1) ──────────────────────────

// explicitPatterns match intent phrases. Group "subject" captures the
// requested subject after the intent phrase.
var explicitPatterns = []*regexp.Regexp{
	// "make/turn/mark this/that/it into/as a topic"
	regexp.MustCompile(`(?i)\b(?:make|turn|mark)\s+(?:this|that|it)\s+(?:into|as)\s+a\s+topic\b`),
	// "make/turn/mark this/that/it a topic" (without into/as)
	regexp.MustCompile(`(?i)\b(?:make|turn|mark)\s+(?:this|that|it)\s+a\s+topic\b`),
	// "create/start/open/new a topic" + optional subject
	regexp.MustCompile(`(?i)\b(?:create|start|open|new)\s+(?:a\s+)?topic\b`),
	// "new topic about/on/for <subject>"
	regexp.MustCompile(`(?i)\bnew\s+topic\s+(?:about|on|for)\s+(?P<subject>.+)`),
	// "make/turn/mark this/that/it into/as a topic about <subject>"
	regexp.MustCompile(`(?i)\b(?:make|turn|mark)\s+(?:this|that|it)\s+(?:into|as)\s+a\s+topic\s+(?:about|on|for)\s+(?P<subject>.+)`),
}

// subjectPattern extracts the subject from a "new topic about X" phrase.
var subjectPattern = regexp.MustCompile(`(?i)\b(?:about|on|for)\s+(.+)`)

// DetectExplicit checks whether the node content contains an explicit topic
// request. Returns a Signal with confidence 1.0 if matched, nil otherwise.
// The subjectKey is extracted from the content if present; otherwise it
// falls back to the node ID (title will be generated from context).
func DetectExplicit(node db.Node) *Signal {
	content := sanitizeForMatching(node.Content)
	for _, pat := range explicitPatterns {
		if pat.MatchString(content) {
			subject := extractSubject(content)
			sk := subject
			if sk == "" {
				sk = node.ID.String()
			}
			return &Signal{
				Type:       DetectionExplicit,
				Confidence: 1.0,
				RootNodeID: node.ID,
				SubjectKey: sk,
				Evidence:   []string{fmt.Sprintf("explicit pattern matched: %s", pat.String())},
			}
		}
	}
	return nil
}

// extractSubject pulls the subject phrase from content matching "about/on/for X".
func extractSubject(content string) string {
	m := subjectPattern.FindStringSubmatch(content)
	if len(m) < 2 {
		return ""
	}
	s := cleanSubject(m[1])
	return s
}

// cleanSubject strips trailing punctuation, #references, and whitespace.
func cleanSubject(s string) string {
	s = strings.TrimSpace(s)
	// Strip trailing #references (e.g. "#topic-slug").
	if idx := strings.Index(s, "#"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	// Strip trailing punctuation.
	s = strings.TrimRight(s, ".,!?;:")
	return strings.TrimSpace(s)
}

// sanitizeForMatching normalizes content for case-insensitive, punctuation-
// tolerant matching while preserving the original for subject extraction.
func sanitizeForMatching(content string) string {
	return strings.TrimSpace(content)
}

// ── Implicit signal (SPEC-TM-02 §3.2) ────────────────────────────────────

// Analyzer is the interface seam for plugging a semantic analysis backend
// (embeddings or LLM agent). The default implementation (KeywordAnalyzer)
// is a deterministic keyword/entity/intent fallback.
type Analyzer interface {
	// Analyze computes the semantic distance [0,1] and subject overlap [0,1]
	// between the current message window and the previous window.
	// Returns (distance, overlap, error). If analysis is unavailable, the
	// fallback should return (0, 1, nil) — no shift detected.
	Analyze(current, previous []db.Node) (distance, overlap float64, err error)
}

// KeywordAnalyzer is the deterministic fallback Analyzer. It tokenizes
// messages into lowercase word sets (excluding stopwords and code blocks),
// computes Jaccard distance for semantic_distance and overlap ratio for
// subject_overlap. Code blocks are tokenized separately by language/symbols.
type KeywordAnalyzer struct{}

// NewKeywordAnalyzer returns a default deterministic analyzer.
func NewKeywordAnalyzer() *KeywordAnalyzer { return &KeywordAnalyzer{} }

// commonStopwords are high-frequency words excluded from keyword comparison.
var commonStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "should": true, "could": true,
	"may": true, "might": true, "must": true, "shall": true, "can": true,
	"of": true, "in": true, "on": true, "at": true, "to": true, "for": true,
	"with": true, "by": true, "from": true, "as": true, "into": true,
	"about": true, "like": true, "through": true, "after": true, "over": true,
	"between": true, "out": true, "against": true, "during": true,
	"without": true, "within": true, "and": true, "or": true, "but": true,
	"if": true, "then": true, "else": true, "when": true, "where": true,
	"why": true, "how": true, "all": true, "any": true, "both": true,
	"each": true, "few": true, "more": true, "most": true, "other": true,
	"some": true, "such": true, "no": true, "not": true, "only": true,
	"own": true, "same": true, "so": true, "than": true, "too": true,
	"very": true, "just": true, "this": true, "that": true, "these": true,
	"those": true, "i": true, "you": true, "he": true, "she": true,
	"it": true, "we": true, "they": true, "me": true, "him": true,
	"her": true, "us": true, "them": true, "my": true, "your": true,
	"his": true, "its": true, "our": true, "their": true,
}

// tokenize extracts meaningful keyword tokens from a set of nodes.
// Code blocks are separated and tokenized by language/symbols/file-paths.
func tokenize(nodes []db.Node) (keywords, codeTokens map[string]int) {
	keywords = make(map[string]int)
	codeTokens = make(map[string]int)
	for _, n := range nodes {
		text, code := splitCodeBlocks(n.Content)
		for _, w := range strings.Fields(strings.ToLower(text)) {
			w = strings.Trim(w, ".,!?;:\"'()[]{}")
			if w == "" || len(w) < 2 || commonStopwords[w] {
				continue
			}
			keywords[w]++
		}
		// Code tokens: language identifiers, symbols, file paths.
		for _, line := range code {
			for _, w := range strings.Fields(line) {
				w = strings.ToLower(strings.TrimSpace(w))
				if w == "" {
					continue
				}
				codeTokens[w]++
			}
		}
	}
	return keywords, codeTokens
}

// splitCodeBlocks separates fenced code blocks (```...```) from prose text.
// Returns (prose, codeLines).
func splitCodeBlocks(content string) (string, []string) {
	var prose strings.Builder
	var code []string
	lines := strings.Split(content, "\n")
	inCode := false
	var codeBlock strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inCode {
				code = append(code, codeBlock.String())
				codeBlock.Reset()
				inCode = false
			} else {
				inCode = true
			}
			continue
		}
		if inCode {
			codeBlock.WriteString(line + "\n")
		} else {
			prose.WriteString(line + "\n")
		}
	}
	if inCode {
		code = append(code, codeBlock.String())
	}
	return prose.String(), code
}

// jaccardDistance computes 1 - |A∩B| / |A∪B| for two sets.
func jaccardDistance(a, b map[string]int) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	intersection := 0
	for k := range a {
		if _, ok := b[k]; ok {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return 1.0 - float64(intersection)/float64(union)
}

// overlapRatio computes |A∩B| / min(|A|,|B|) — how much of the smaller set
// is shared.
func overlapRatio(a, b map[string]int) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for k := range a {
		if _, ok := b[k]; ok {
			intersection++
		}
	}
	minSize := len(a)
	if len(b) < minSize {
		minSize = len(b)
	}
	return float64(intersection) / float64(minSize)
}

// Analyze implements the Analyzer interface with keyword/entity fallback.
func (a *KeywordAnalyzer) Analyze(current, previous []db.Node) (float64, float64, error) {
	if len(current) == 0 || len(previous) == 0 {
		return 0, 1, nil // no shift detectable
	}
	curKw, curCode := tokenize(current)
	prevKw, prevCode := tokenize(previous)

	distance := jaccardDistance(curKw, prevKw)
	overlap := overlapRatio(curKw, prevKw)

	// If code tokens dominate and differ heavily, boost distance.
	if len(curCode) > 0 || len(prevCode) > 0 {
		codeDist := jaccardDistance(curCode, prevCode)
		// Blend: code divergence contributes to semantic distance.
		distance = math.Max(distance, codeDist*0.5)
	}

	return distance, overlap, nil
}

// DetectImplicit evaluates the implicit signal using the Analyzer. Per §3.2:
//   - current = newest 3-5 consecutive messages
//   - previous = preceding 10 messages
//   - qualifies when distance >= 0.70 AND overlap <= 0.30 AND count >= max(3, MinMessagesPerTopic)
//   - confidence ladder: 3 msgs >= 0.85, 4 msgs >= 0.75, 5 msgs >= 0.65
//
// Returns nil if thresholds are not met.
func DetectImplicit(current, previous []db.Node, cfg DetectionConfig, analyzer Analyzer) *Signal {
	if analyzer == nil {
		analyzer = NewKeywordAnalyzer()
	}
	minCount := 3
	if cfg.MinMessagesPerTopic > minCount {
		minCount = cfg.MinMessagesPerTopic
	}
	if len(current) < minCount {
		return nil
	}

	// Cap current window at 5.
	cur := current
	if len(cur) > 5 {
		cur = cur[len(cur)-5:]
	}

	distance, overlap, err := analyzer.Analyze(cur, previous)
	if err != nil || distance < 0.70 || overlap > 0.30 {
		return nil
	}

	// Confidence ladder based on message count.
	var confidence float32
	switch {
	case len(cur) >= 5:
		confidence = 0.65
	case len(cur) >= 4:
		confidence = 0.75
	default:
		confidence = 0.85
	}

	// Root is the oldest node in the current window (the shift start).
	root := cur[0]
	subjectKey := deriveSubjectKey(cur)

	return &Signal{
		Type:       DetectionImplicit,
		Confidence: confidence,
		RootNodeID: root.ID,
		SubjectKey: subjectKey,
		Evidence: []string{
			fmt.Sprintf("semantic_distance=%.2f (threshold>=0.70)", distance),
			fmt.Sprintf("subject_overlap=%.2f (threshold<=0.30)", overlap),
			fmt.Sprintf("message_count=%d", len(cur)),
		},
	}
}

// deriveSubjectKey builds a subject key from the top keywords of the current
// window. This is used for cooldown tracking so repeated shifts of the same
// subject are suppressed.
func deriveSubjectKey(nodes []db.Node) string {
	kw, _ := tokenize(nodes)
	if len(kw) == 0 {
		return nodes[0].ID.String()
	}
	// Pick the top keyword by frequency.
	var top string
	maxCount := 0
	for k, c := range kw {
		if c > maxCount || (c == maxCount && k < top) {
			top = k
			maxCount = c
		}
	}
	return top
}

// ── Structural signal (SPEC-TM-02 §3.3) ─────────────────────────────────

// DetectStructural checks whether the node was created via a fork edge.
// Requires the edgeRepo to check for fork edges targeting the node.
// Confidence: 0.90 for user fork, 0.80 for agent subtask (node_type).
// Returns nil if no structural signal is present or the node lacks
// subject-bearing content.
func DetectStructural(ctx context.Context, node db.Node, edgeRepo db.EdgeRepo) *Signal {
	if edgeRepo == nil {
		return nil
	}
	// Check if the node has subject-bearing content.
	content := strings.TrimSpace(node.Content)
	if content == "" {
		return nil
	}

	// We determine fork status by checking edges targeting this node.
	// A fork edge → structural signal.
	edges, err := edgeRepo.GetByTarget(ctx, node.ID)
	if err != nil {
		return nil
	}
	for _, e := range edges {
		if e.EdgeType == db.EdgeTypeFork {
			confidence := float32(0.90)
			// Agent subtask (synthesis node type).
			if node.NodeType == db.NodeTypeSynthesis {
				confidence = 0.80
			}
			return &Signal{
				Type:       DetectionStructural,
				Confidence: confidence,
				RootNodeID: node.ID,
				SubjectKey: deriveSubjectKey([]db.Node{node}),
				Evidence:   []string{"fork edge detected"},
			}
		}
	}
	return nil
}

// ── Proposal title generation (SPEC-TM-02 §4) ───────────────────────────

// GenerateTitle produces a deterministic title from the first N messages
// starting at the root node. Normalizes the first non-empty line(s) of the
// root node content, trimmed to 1-200 characters. Returns "" if no valid
// title can be derived (caller should reject).
func GenerateTitle(rootNode db.Node, contextNodes []db.Node, n int) string {
	if n <= 0 {
		n = 3
	}
	if n > 5 {
		n = 5
	}

	// Collect content from root + up to N-1 following nodes.
	contents := []string{rootNode.Content}
	for i := 0; i < len(contextNodes) && i < n-1; i++ {
		contents = append(contents, contextNodes[i].Content)
	}

	// Build a title from the first non-empty meaningful line.
	for _, c := range contents {
		line := firstMeaningfulLine(c)
		line = normalizeTitle(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// firstMeaningfulLine returns the first line of content that has
// non-whitespace text and is NOT inside a fenced code block.
func firstMeaningfulLine(content string) string {
	inCode := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return trimmed
	}
	return ""
}

// normalizeTitle trims, collapses whitespace, strips code blocks, and caps at 200 chars.
func normalizeTitle(s string) string {
	s = strings.TrimSpace(s)
	// Collapse whitespace runs.
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	// Strip markdown formatting remnants.
	s = strings.Trim(s, "*_`")
	if len([]rune(s)) > 200 {
		runes := []rune(s)
		s = string(runes[:200])
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return s
}

// ── Highest confidence selection (SPEC-TM-02 §4) ────────────────────────

// HighestConfidenceSignal returns the signal with the greatest confidence.
// Returns nil if no signals are present.
func HighestConfidenceSignal(signals []*Signal) *Signal {
	var best *Signal
	for _, s := range signals {
		if s == nil {
			continue
		}
		if best == nil || s.Confidence > best.Confidence {
			best = s
		}
	}
	return best
}

// ── Evidence serialization ──────────────────────────────────────────────

// MarshalEvidence serializes evidence strings to json.RawMessage for storage.
func MarshalEvidence(evidence []string) json.RawMessage {
	b, err := json.Marshal(evidence)
	if err != nil {
		return json.RawMessage(`[]`)
	}
	return b
}
