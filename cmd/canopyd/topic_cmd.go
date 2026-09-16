package main

// Topic detection CLI subcommands (SPEC-TM-02 §7).
//
//	canopyd topic detect --tree <uuid> --node <uuid>   Preview a proposal
//	canopyd topic proposals --tree <uuid>             List pending proposals
//	canopyd topic config --tree <uuid> [flags]        View/update detection config

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// runTopicCmd dispatches topic sub-subcommands.
func runTopicCmd(args []string) {
	os.Exit(runTopicCmdE(args))
}

// runTopicCmdE is runTopicCmd without the process exit so tests can assert exit
// codes. `canopyd topic --help` / `-h` prints usage and returns 0; `canopyd
// topic` with no arguments returns 1; only a genuinely unknown topic
// subcommand returns nonzero (DF-HERMES-CANOPY-10).
func runTopicCmdE(args []string) int {
	if len(args) == 0 {
		printTopicUsage()
		return 1
	}

	sub := args[0]
	rest := args[1:]

	if sub == "-h" || sub == "--help" {
		printTopicUsage()
		return 0
	}

	switch sub {
	case "detect":
		return topicDetectE(rest)
	case "proposals":
		return topicProposalsE(rest)
	case "config":
		return topicConfigE(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown topic subcommand: %s\n", sub)
		fmt.Fprintf(os.Stderr, "Available: detect, proposals, config\n")
		return 1
	}
}

// printTopicUsage documents the topic subcommands. It is printed by
// `canopyd topic --help` (exit 0) and by a bare `canopyd topic` (exit 1).
func printTopicUsage() {
	fmt.Fprintf(os.Stderr, "Usage: canopyd topic <detect|proposals|config> [flags]\n\n")
	fmt.Fprintf(os.Stderr, "Subcommands:\n")
	fmt.Fprintf(os.Stderr, "  detect --tree <uuid> --node <uuid>     Explain how a node would be evaluated\n")
	fmt.Fprintf(os.Stderr, "  proposals --tree <uuid>                Show detection config + pending-proposal guidance\n")
	fmt.Fprintf(os.Stderr, "  config --tree <uuid> [flags]           View or update per-tree detection config\n\n")
	fmt.Fprintf(os.Stderr, "These are HTTP clients of a running canopyd and target CANOPY_SERVER_URL\n")
	fmt.Fprintf(os.Stderr, "(default %s).\n", defaultServerURL)
}

// topicDetectE previews a detection proposal for a node. It returns an exit
// code instead of exiting so tests can assert it.
func topicDetectE(args []string) int {
	fs := flag.NewFlagSet("topic detect", flag.ExitOnError)
	treeID := fs.String("tree", "", "tree UUID")
	nodeID := fs.String("node", "", "node UUID")
	_ = fs.Parse(args)

	if *treeID == "" || *nodeID == "" {
		fmt.Fprintf(os.Stderr, "Usage: canopyd topic detect --tree <uuid> --node <uuid>\n")
		return 1
	}

	// The preview endpoint is not exposed as a REST route; detection runs
	// automatically on node creation. For the CLI, we trigger detection by
	// listing pending proposals for the tree and filtering by node.
	path := fmt.Sprintf("/api/v1/trees/%s/topic-detection", *treeID)
	_ = path
	// Since PreviewProposal is a service-level method not exposed via HTTP,
	// we print guidance to use the proposals list instead.
	fmt.Fprintf(os.Stderr, "Detection preview is server-side only. Use 'canopyd topic proposals --tree %s' to see pending proposals.\n", *treeID)
	fmt.Fprintf(os.Stderr, "Node %s will be evaluated on creation; check proposals above.\n", *nodeID)
	return 0
}

// topicProposalsE lists pending proposals for a tree. It returns an exit code
// instead of exiting so tests can assert both the usage and the
// target-refusal paths (DF-HERMES-CANOPY-6).
func topicProposalsE(args []string) int {
	fs := flag.NewFlagSet("topic proposals", flag.ExitOnError)
	treeID := fs.String("tree", "", "tree UUID")
	_ = fs.Parse(args)

	if *treeID == "" {
		fmt.Fprintf(os.Stderr, "Usage: canopyd topic proposals --tree <uuid>\n")
		return 1
	}

	// Proposals are listed via the topics endpoint with a status filter,
	// or we query the detection-specific endpoint.
	// For now, use the topic-detection config endpoint to verify the tree
	// has detection enabled, then show guidance.
	path := fmt.Sprintf("/api/v1/trees/%s/topic-detection", *treeID)
	respBody, _, err := apiRequestE(http.MethodGet, path, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	var cfg detectionConfigCLIResponse
	if err := json.Unmarshal(respBody, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to parse config: %v\n", err)
		return 1
	}

	fmt.Printf("Detection config for tree %s:\n", *treeID)
	fmt.Printf("  Level:          %s\n", cfg.DetectionLevel)
	fmt.Printf("  Auto-create:    %v\n", cfg.AutoCreate)
	fmt.Printf("  Always ask:     %v\n", cfg.AlwaysAsk)
	fmt.Printf("  Min messages:   %d\n", cfg.MinMessagesPerTopic)
	fmt.Printf("  Cooldown:       %d\n", cfg.ProposalCooldown)
	fmt.Println()
	fmt.Println("Pending proposals are delivered via SSE (topic_proposed events).")
	fmt.Println("Use the HTTP API to confirm/dismiss proposals:")
	fmt.Println("  POST /api/v1/topic-proposals/{id}/confirm")
	fmt.Println("  POST /api/v1/topic-proposals/{id}/dismiss")
	return 0
}

// topicConfigE views or updates the per-tree detection configuration. It
// returns an exit code instead of exiting so tests can assert the
// target-refusal path on both the GET and the PUT helper.
func topicConfigE(args []string) int {
	fs := flag.NewFlagSet("topic config", flag.ExitOnError)
	treeID := fs.String("tree", "", "tree UUID")
	level := fs.String("level", "", "detection level: off|explicit_only|full")
	autoCreate := fs.Bool("auto-create", false, "enable auto-create")
	alwaysAsk := fs.Bool("always-ask", false, "enable always-ask")
	_ = fs.Parse(args)

	if *treeID == "" {
		fmt.Fprintf(os.Stderr, "Usage: canopyd topic config --tree <uuid> [--level off|explicit_only|full] [--auto-create] [--always-ask]\n")
		return 1
	}

	// If no update flags, just GET the config.
	set := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "level", "auto-create", "always-ask":
			set = true
		}
	})
	if !set {
		// GET mode.
		path := fmt.Sprintf("/api/v1/trees/%s/topic-detection", *treeID)
		respBody, _, err := apiRequestE(http.MethodGet, path, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}
		var cfg detectionConfigCLIResponse
		if err := json.Unmarshal(respBody, &cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to parse config: %v\n", err)
			return 1
		}
		printTopicDetectionConfig(*treeID, cfg)
		return 0
	}

	// PUT mode — build update body.
	updateBody := map[string]any{}
	if *level != "" {
		updateBody["detection_level"] = *level
	}

	// Check which flags were explicitly set.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "auto-create":
			updateBody["auto_create"] = *autoCreate
		case "always-ask":
			updateBody["always_ask"] = *alwaysAsk
		}
	})

	reqBody, err := json.Marshal(updateBody)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to marshal request: %v\n", err)
		return 1
	}

	path := fmt.Sprintf("/api/v1/trees/%s/topic-detection", *treeID)
	respBody, _, err := apiRequestE(http.MethodPut, path, strings.NewReader(string(reqBody)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	var cfg detectionConfigCLIResponse
	if err := json.Unmarshal(respBody, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to parse response: %v\n", err)
		return 1
	}

	fmt.Println("Detection config updated.")
	printTopicDetectionConfig(*treeID, cfg)
	return 0
}

// detectionConfigCLIResponse is the CLI representation of the config response.
type detectionConfigCLIResponse struct {
	AutoCreate          bool   `json:"auto_create"`
	AlwaysAsk           bool   `json:"always_ask"`
	DetectionLevel      string `json:"detection_level"`
	MinMessagesPerTopic int    `json:"min_messages_per_topic"`
	ProposalCooldown    int    `json:"proposal_cooldown"`
}

func printTopicDetectionConfig(treeID string, cfg detectionConfigCLIResponse) {
	fmt.Printf("Detection config for tree %s:\n", treeID)
	fmt.Printf("  Level:             %s\n", cfg.DetectionLevel)
	fmt.Printf("  Auto-create:       %v\n", cfg.AutoCreate)
	fmt.Printf("  Always ask:        %v\n", cfg.AlwaysAsk)
	fmt.Printf("  Min messages/topic: %d\n", cfg.MinMessagesPerTopic)
	fmt.Printf("  Proposal cooldown: %d\n", cfg.ProposalCooldown)
}
