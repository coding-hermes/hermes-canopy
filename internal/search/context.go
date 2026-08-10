// Package search — context compiler.
// Implements the compileMultiTopicContext and contextHash logic from
// SPEC-TM-03 §4.5. The merged text uses boundary markers so the agent
// can distinguish which nodes belong to which topic.
package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// compileMultiTopicContext assembles the merged context for agent injection.
// This is the core of the one-button context feature.
func compileMultiTopicContext(contexts []TopicContext, globalMaxNodes int) *MultiTopicContext {
	var merged MultiTopicContext
	merged.Topics = contexts

	var sb strings.Builder
	totalNodes := 0
	truncated := false

	for _, tc := range contexts {
		if totalNodes >= globalMaxNodes {
			truncated = true
			break
		}

		// Topic boundary marker
		fmt.Fprintf(&sb, "\n--- topic boundary: %s (id: %s) ---\n", tc.Slug, tc.TopicID)
		fmt.Fprintf(&sb, "Topic: %s\n", tc.Title)
		fmt.Fprintf(&sb, "Root node: %s\n", tc.RootNodeID)
		fmt.Fprintf(&sb, "Total nodes in topic: %d\n", tc.TotalNodes)
		fmt.Fprintf(&sb, "Nodes included: %d\n\n", len(tc.Nodes))

		budget := globalMaxNodes - totalNodes
		included := 0
		for _, node := range tc.Nodes {
			if included >= budget {
				truncated = true
				break
			}
			sb.WriteString(formatNodeForContext(node))
			sb.WriteString("\n")
			included++
			totalNodes++
		}

		if included < len(tc.Nodes) {
			fmt.Fprintf(&sb, "\n[... %d more nodes in topic %s — truncated by context budget]\n",
				tc.TotalNodes-included, tc.Slug)
		}
	}

	if truncated {
		fmt.Fprintf(&sb, "\n[CONTEXT WARNING: %d topics requested, total nodes exceed budget. Some nodes omitted. Consider re-injecting with fewer topics or higher max_nodes.]\n", len(contexts))
	}

	merged.MergedText = sb.String()
	merged.TotalNodes = totalNodes
	merged.Truncated = truncated
	return &merged
}

// formatNodeForContext renders a single node into the merged context text.
func formatNodeForContext(node ContextNode) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- node %s (author: %s) ---\n", node.ID, node.AuthorID)
	content := stripMarkdown(node.Content)
	sb.WriteString(content)
	return sb.String()
}

// stripMarkdown removes common markdown formatting characters for plain-text rendering.
func stripMarkdown(s string) string {
	replacer := strings.NewReplacer(
		"#", "", "**", "", "*", "", "`", "",
		"[", "", "]", "", "(", "", ")", "",
		"> ", "", "- ", "",
	)
	return strings.TrimSpace(replacer.Replace(s))
}

// contextHash computes a deterministic SHA-256 hash of node IDs.
// Nodes are sorted by a stable key for deterministic output.
func contextHash(nodes []ContextNode) string {
	sorted := make([]ContextNode, len(nodes))
	copy(sorted, nodes)
	sort.Slice(sorted, func(i, j int) bool {
		return nodeOrderKey(sorted[i]) < nodeOrderKey(sorted[j])
	})

	h := sha256.New()
	for _, n := range sorted {
		h.Write(n.ID[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// nodeOrderKey returns a sort key for deterministic node ordering.
func nodeOrderKey(n ContextNode) string {
	return fmt.Sprintf("%s_%s", n.CreatedAt.UTC().Format(time.RFC3339Nano), n.ID.String())
}

// resolveMaxNodes returns the effective per-topic max with defaults.
func resolveMaxNodes(m int) int {
	if m <= 0 {
		return DefaultMaxNodes
	}
	if m > 10000 {
		return 10000
	}
	return m
}

// formatRelativeTime returns a human-readable relative time string.
func formatRelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	}
}

// truncateSnippet caps a snippet at maxLen characters, breaking at word boundary.
func truncateSnippet(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	cut := maxLen
	for cut > 0 && s[cut] != ' ' {
		cut--
	}
	if cut == 0 {
		cut = maxLen
	}
	return s[:cut] + "..."
}

// --- Injection pipeline ----------------------------------------------------

// injectResult holds the per-topic context plus SSE event data.
type injectResult struct {
	context      TopicContext
	topicID      uuid.UUID
	nodeCount    int
	totalInScope int
}

// CompileInjectContext is called by the service to produce the response.
func CompileInjectContext(ctx context.Context, repo TopicSearchRepo, treeID uuid.UUID, req InjectContextRequest) (*MultiTopicContext, []injectResult, error) {
	maxNodesPerTopic := resolveMaxNodes(req.MaxNodes)

	var contexts []TopicContext
	var results []injectResult
	totalRequestedNodes := 0

	for _, topicID := range req.TopicIDs {
		topic, err := repo.GetTopicForInject(ctx, topicID)
		if err != nil {
			return nil, nil, fmt.Errorf("get topic %s: %w", topicID, err)
		}
		if topic.Status == "deleted" {
			return nil, nil, ErrTopicDeleted
		}
		if topic.Status == "archived" {
			return nil, nil, ErrTopicArchived
		}

		nodes, totalCount, hasMore, err := repo.GetTopicNodes(ctx, topicID, maxNodesPerTopic)
		if err != nil {
			return nil, nil, fmt.Errorf("get nodes for topic %s: %w", topicID, err)
		}

		totalRequestedNodes += totalCount

		tc := TopicContext{
			TopicID:     topic.ID,
			Title:       topic.Title,
			Slug:        topic.Slug,
			RootNodeID:  topic.RootNodeID,
			Nodes:       nodes,
			TotalNodes:  totalCount,
			HasMore:     hasMore,
			ContextHash: contextHash(nodes),
		}
		contexts = append(contexts, tc)
		results = append(results, injectResult{
			context:      tc,
			topicID:      topic.ID,
			nodeCount:    len(nodes),
			totalInScope: totalCount,
		})
	}

	if totalRequestedNodes > GlobalMaxNodes {
		return nil, nil, fmt.Errorf("%w: requested %d, max %d", ErrContextTooLarge, totalRequestedNodes, GlobalMaxNodes)
	}

	merged := compileMultiTopicContext(contexts, GlobalMaxNodes)
	return merged, results, nil
}
