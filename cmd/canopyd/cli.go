package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const defaultServerURL = "http://localhost:8080"

// --- API response types -------------------------------------------------------

type apiErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type treeCreateRequest struct {
	Title       string                 `json:"title"`
	Description string                 `json:"description,omitempty"`
	RootMessage *treeCreateRootMessage `json:"rootMessage,omitempty"`
}

// treeCreateRootMessage is the camelCase rootMessage object the tree-create
// API requires (see internal/handler/tree_handler.go). The handler decodes
// camelCase JSON — never send snake_case keys in this request (GAP-042).
type treeCreateRootMessage struct {
	Content       string `json:"content"`
	ContentFormat string `json:"contentFormat,omitempty"`
	NodeType      string `json:"nodeType,omitempty"`
}

type treeCreateResponse struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	RootNodeID string `json:"root_node_id"`
	CreatedAt  string `json:"created_at"`
}

type listTreesResponse struct {
	Trees      []treeSummary `json:"trees"`
	Pagination struct {
		HasMore bool `json:"hasMore"`
		Total   int  `json:"total"`
	} `json:"pagination"`
}

type treeSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
}

type treeDetail struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	RootNodeID string `json:"root_node_id"`
	CreatedAt  string `json:"created_at"`
}

type graphQueryResult struct {
	Nodes []graphNodeSummary `json:"nodes"`
	Edges []graphEdgeSummary `json:"edges"`
}

type graphNodeSummary struct {
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id,omitempty"`
	Type     string  `json:"type"`
	Depth    int     `json:"depth"`
}

type graphEdgeSummary struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	EdgeType string `json:"edge_type"`
}

// --- CLI entry point -----------------------------------------------------------

// knownSubcommands maps top-level subcommands to their handler.
var knownSubcommands = map[string]struct{}{
	"tree":    {},
	"session": {},
	"topic":   {},
}

// runCLI detects the subcommand from args and dispatches to the appropriate handler.
// It should be called when os.Args[1] matches a known subcommand.
func runCLI() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: canopyd <subcommand> [args...]\n")
		fmt.Fprintf(os.Stderr, "Subcommands:\n")
		fmt.Fprintf(os.Stderr, "  tree create <name> [--content <text>]  Create a new tree (root message required)\n")
		fmt.Fprintf(os.Stderr, "  tree list                 List all trees\n")
		fmt.Fprintf(os.Stderr, "  tree delete <id>          Delete a tree\n")
		fmt.Fprintf(os.Stderr, "  tree navigate <id>        Print tree structure as indented text\n")
		fmt.Fprintf(os.Stderr, "  session import [flags]    Import Hermes sessions from state.db into trees\n")
		fmt.Fprintf(os.Stderr, "  topic <subcmd> [flags]    Topic detection: detect, proposals, config\n")
		fmt.Fprintf(os.Stderr, "  serve [flags]             Start the API server (default mode; env-only config)\n")
		os.Exit(1)
	}

	sub := os.Args[1]
	switch sub {
	case "tree":
		runTreeCmd(os.Args[2:])
	case "session":
		runSessionCmd(os.Args[2:])
	case "topic":
		runTopicCmd(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", sub)
		fmt.Fprintf(os.Stderr, "Run 'canopyd' without arguments to see usage.\n")
		os.Exit(1)
	}
}

// runTreeCmd dispatches tree sub-subcommands.
func runTreeCmd(args []string) {
	os.Exit(runTreeCmdE(args))
}

// runTreeCmdE is runTreeCmd without the process exit so tests can assert
// exit codes. `canopyd tree --help` prints usage and returns 0 (GAP-042);
// `canopyd tree` with no arguments prints usage and returns 1.
func runTreeCmdE(args []string) int {
	if len(args) == 0 {
		printTreeUsage()
		return 1
	}
	if args[0] == "-h" || args[0] == "--help" {
		printTreeUsage()
		return 0
	}

	sub := args[0]
	rest := args[1:]

	switch sub {
	case "create":
		return treeCreateE(rest)
	case "list":
		return treeListE(rest)
	case "delete":
		return treeDeleteE(rest)
	case "navigate":
		return treeNavigateE(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown tree subcommand: %s\n", sub)
		fmt.Fprintf(os.Stderr, "Available: create, list, delete, navigate\n")
		return 1
	}
}

// printTreeUsage documents the tree subcommands.
func printTreeUsage() {
	fmt.Fprintf(os.Stderr, "Usage: canopyd tree <create|list|delete|navigate> [args...]\n\n")
	fmt.Fprintf(os.Stderr, "Subcommands:\n")
	fmt.Fprintf(os.Stderr, "  create <name> [--content <text>]  Create a new tree (root message required)\n")
	fmt.Fprintf(os.Stderr, "  list                              List all trees\n")
	fmt.Fprintf(os.Stderr, "  delete <id>                       Delete a tree\n")
	fmt.Fprintf(os.Stderr, "  navigate <id>                     Print tree structure as indented text\n")
}

// --- HTTP helpers --------------------------------------------------------------

// serverURL reads CANOPY_SERVER_URL from the environment, defaulting to
// http://localhost:8080.
func serverURL() string {
	if u := os.Getenv("CANOPY_SERVER_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return defaultServerURL
}

// authHeader returns an Authorization: Bearer header value if CANOPY_TOKEN is
// set, or empty string otherwise (with a warning to stderr).
func authHeader() string {
	tok := os.Getenv("CANOPY_TOKEN")
	if tok == "" {
		fmt.Fprintln(os.Stderr, "Warning: CANOPY_TOKEN not set — sending requests without auth (dev mode)")
		return ""
	}
	return "Bearer " + tok
}

// httpClient is a shared HTTP client with a reasonable timeout.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// apiRequest makes an HTTP request to the canopyd API and returns the parsed
// response. On 4xx/5xx, it prints the error from the response body and exits.
// On success, it returns the raw body bytes for the caller to unmarshal.
func apiRequest(method, path string, body io.Reader) ([]byte, int) {
	url := serverURL() + path

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to build request: %v\n", err)
		os.Exit(1)
	}

	req.Header.Set("Content-Type", "application/json")
	if ah := authHeader(); ah != "" {
		req.Header.Set("Authorization", ah)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to reach server at %s: %v\n", serverURL(), err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to read response: %v\n", err)
		os.Exit(1)
	}

	if resp.StatusCode >= 400 {
		var apiErr apiErrorResponse
		if err := json.Unmarshal(respBody, &apiErr); err == nil && apiErr.Error.Code != "" {
			fmt.Fprintf(os.Stderr, "Error: [%s] %s\n", apiErr.Error.Code, apiErr.Error.Message)
		} else {
			// Fallback: print status and raw body (trimmed).
			raw := strings.TrimSpace(string(respBody))
			if len(raw) > 500 {
				raw = raw[:500] + "..."
			}
			fmt.Fprintf(os.Stderr, "Error: HTTP %d\n%s\n", resp.StatusCode, raw)
		}
		os.Exit(1)
	}

	return respBody, resp.StatusCode
}

// --- Tree subcommands ----------------------------------------------------------

// parseTreeCreateArgs scans args for the tree name positional and the
// --content/--message flag. Flags may appear before or after the name. It
// returns the parsed name and content, whether help was requested, and an
// error for unknown flags or missing flag values. Extra positional arguments
// beyond the name are ignored (matching prior behavior).
func parseTreeCreateArgs(args []string) (name, content string, help bool, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			help = true
		case a == "--content" || a == "--message":
			if i+1 >= len(args) {
				return "", "", false, fmt.Errorf("flag %s requires a value", a)
			}
			i++
			content = args[i]
		case strings.HasPrefix(a, "--content="):
			content = strings.TrimPrefix(a, "--content=")
		case strings.HasPrefix(a, "--message="):
			content = strings.TrimPrefix(a, "--message=")
		case strings.HasPrefix(a, "-") && a != "-":
			return "", "", false, fmt.Errorf("unknown flag: %s", a)
		default:
			if name == "" {
				name = a
			}
		}
	}
	return name, content, help, nil
}

// printTreeCreateUsage documents the tree create command. Called for --help
// (exit 0) and for usage errors (exit 1).
func printTreeCreateUsage() {
	fmt.Fprintf(os.Stderr, "Usage: canopyd tree create <name> [--content <text>]\n\n")
	fmt.Fprintf(os.Stderr, "Creates a new tree. The API requires a root message, so\n")
	fmt.Fprintf(os.Stderr, "--content is mandatory (GAP-042).\n\n")
	fmt.Fprintf(os.Stderr, "Flags:\n")
	fmt.Fprintf(os.Stderr, "  --content <text>   Root message content (required)\n")
	fmt.Fprintf(os.Stderr, "  --message <text>   Alias for --content\n")
	fmt.Fprintf(os.Stderr, "  -h, --help         Print this help and exit\n")
}

// buildTreeCreateBody marshals the tree-create request body. The API handler
// decodes camelCase JSON (see internal/handler/tree_handler.go) and requires
// rootMessage.content, so rootMessage is included whenever content is set.
func buildTreeCreateBody(name, content string) ([]byte, error) {
	req := treeCreateRequest{
		Title: name,
	}
	if content != "" {
		req.RootMessage = &treeCreateRootMessage{
			Content:       content,
			ContentFormat: "markdown",
			NodeType:      "message",
		}
	}
	return json.Marshal(req)
}

// treeCreateE implements `canopyd tree create`. It returns an exit code
// instead of calling os.Exit so tests can exercise the help and validation
// paths without spawning processes or hitting the API.
func treeCreateE(args []string) int {
	name, content, help, err := parseTreeCreateArgs(args)
	if help {
		printTreeCreateUsage()
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		printTreeCreateUsage()
		return 1
	}
	if name == "" {
		printTreeCreateUsage()
		return 1
	}
	if content == "" {
		fmt.Fprintf(os.Stderr, "Error: tree create requires a root message — pass --content <text> (the API rejects trees without a root message).\n")
		fmt.Fprintf(os.Stderr, "Usage: canopyd tree create <name> --content <text>\n")
		return 1
	}

	reqBody, err := buildTreeCreateBody(name, content)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to marshal request: %v\n", err)
		return 1
	}

	respBody, _ := apiRequest(http.MethodPost, "/api/v1/trees", bytes.NewReader(reqBody))

	var tree treeCreateResponse
	if err := json.Unmarshal(respBody, &tree); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to parse response: %v\n", err)
		return 1
	}

	fmt.Printf("Tree created successfully.\n")
	fmt.Printf("  ID:          %s\n", tree.ID)
	fmt.Printf("  Title:       %s\n", tree.Title)
	fmt.Printf("  Root Node:   %s\n", tree.RootNodeID)
	return 0
}

func treeListE(args []string) int {
	if wantsHelp(args) {
		fmt.Fprintf(os.Stderr, "Usage: canopyd tree list\n")
		return 0
	}
	respBody, _ := apiRequest(http.MethodGet, "/api/v1/trees", nil)

	var list listTreesResponse
	if err := json.Unmarshal(respBody, &list); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to parse response: %v\n", err)
		return 1
	}

	if len(list.Trees) == 0 {
		fmt.Println("No trees found.")
		return 0
	}

	// Print table header.
	fmt.Printf("%-38s  %-30s  %s\n", "ID", "NAME", "CREATED")
	fmt.Println(strings.Repeat("-", 100))

	for _, t := range list.Trees {
		created := formatTimestamp(t.CreatedAt)
		fmt.Printf("%-38s  %-30s  %s\n", t.ID, truncate(t.Title, 30), created)
	}

	fmt.Printf("\n%d tree(s) total\n", list.Pagination.Total)
	return 0
}

func treeDeleteE(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: canopyd tree delete <id>\n")
		return 1
	}
	if wantsHelp(args) {
		fmt.Fprintf(os.Stderr, "Usage: canopyd tree delete <id>\n")
		return 0
	}
	id := args[0]

	_, status := apiRequest(http.MethodDelete, "/api/v1/trees/"+id, nil)

	if status == http.StatusNoContent {
		fmt.Printf("Tree %s deleted successfully.\n", id)
	} else {
		fmt.Printf("Tree %s deleted.\n", id)
	}
	return 0
}

func treeNavigateE(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: canopyd tree navigate <id>\n")
		return 1
	}
	if wantsHelp(args) {
		fmt.Fprintf(os.Stderr, "Usage: canopyd tree navigate <id>\n")
		return 0
	}
	treeID := args[0]

	// Step 1: Fetch tree details to get root_node_id.
	respBody, _ := apiRequest(http.MethodGet, "/api/v1/trees/"+treeID, nil)

	var detail treeDetail
	if err := json.Unmarshal(respBody, &detail); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to parse tree detail: %v\n", err)
		os.Exit(1)
	}

	if detail.RootNodeID == "" {
		fmt.Fprintf(os.Stderr, "Error: tree has no root node\n")
		os.Exit(1)
	}

	// Step 2: Fetch subtree.
	subtreePath := fmt.Sprintf("/api/v1/graph/trees/%s/subtree/%s", treeID, detail.RootNodeID)
	respBody, _ = apiRequest(http.MethodGet, subtreePath, nil)

	var result graphQueryResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to parse subtree: %v\n", err)
		os.Exit(1)
	}

	if len(result.Nodes) == 0 {
		fmt.Println("(empty tree)")
		return 0
	}

	fmt.Printf("Tree: %s\n\n", detail.Title)

	// Build a child map for tree printing.
	children := make(map[string][]graphNodeSummary)
	nodeMap := make(map[string]graphNodeSummary)
	for _, n := range result.Nodes {
		nodeMap[n.ID] = n
		pid := ""
		if n.ParentID != nil {
			pid = *n.ParentID
		}
		children[pid] = append(children[pid], n)
	}

	// Sort children of each parent by ID for stable output.
	for k := range children {
		sort.Slice(children[k], func(i, j int) bool {
			return children[k][i].ID < children[k][j].ID
		})
	}

	// Find root nodes (those with no parent or whose parent is not in the set).
	rootNodes := children[""]
	if len(rootNodes) == 0 {
		// Fallback: find a node that is referenced as a source but not as
		// a target, or just use the first node.
		rootNodes = result.Nodes[:1]
	}

	for _, root := range rootNodes {
		printNode(root, children, "", true)
	}
	return 0
}

// printNode recursively prints a node and its children as an indented tree.
func printNode(node graphNodeSummary, children map[string][]graphNodeSummary, prefix string, isLast bool) {
	var connector string
	if prefix == "" {
		connector = ""
	} else if isLast {
		connector = "└── "
	} else {
		connector = "├── "
	}

	label := truncate(node.Type, 20)
	if label == "" {
		label = "node"
	}
	fmt.Printf("%s%s[%s] %s\n", prefix, connector, shortID(node.ID), label)

	kids := children[node.ID]
	for i, child := range kids {
		last := i == len(kids)-1
		var childPrefix string
		if prefix == "" {
			childPrefix = ""
		} else if isLast {
			childPrefix = prefix + "    "
		} else {
			childPrefix = prefix + "│   "
		}
		printNode(child, children, childPrefix, last)
	}
}

// --- Utility functions ---------------------------------------------------------

// shortID returns the first 8 characters of a UUID for compact display.
func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

// truncate returns s truncated to maxLen characters with "…" appended if needed.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}

// formatTimestamp converts an ISO 8601 / RFC 3339 timestamp into a short form.
// Returns the original string on parse failure.
func formatTimestamp(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		// Try without nanos.
		t, err = time.Parse(time.RFC3339, ts)
	}
	if err != nil {
		// Try a few more common formats.
		layouts := []string{
			"2006-01-02T15:04:05Z",
			"2006-01-02T15:04:05-07:00",
			"2006-01-02 15:04:05",
			"2006-01-02",
		}
		for _, l := range layouts {
			if t, err = time.Parse(l, ts); err == nil {
				break
			}
		}
	}
	if err != nil {
		return ts
	}
	return t.Format("2006-01-02 15:04")
}

// isSubcommand returns true if the given arg is a known top-level subcommand.
func isSubcommand(arg string) bool {
	_, ok := knownSubcommands[arg]
	return ok
}

// hasSubcommand checks os.Args for a known subcommand at position 1 (after
// the binary name). Returns true if one is found.
func hasSubcommand() bool {
	if len(os.Args) < 2 {
		return false
	}
	return isSubcommand(os.Args[1])
}

// stripServerFlags removes leading server flag arguments (e.g. -version)
// that appear BEFORE the subcommand token, so main() can route to CLI mode
// even when server flags precede the subcommand. Everything from the first
// non-flag token onward — including subcommand flags such as
// `session import --db …` — is preserved.
func stripServerFlags(args []string) []string {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") && !isSubcommand(args[i]) {
		i++
	}
	return args[i:]
}

// wantsHelp reports whether args contains a help flag (-h / --help).
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// wantsServeHelp reports whether the arguments following the `serve`
// subcommand request help (-h / --help). `canopyd serve --help` must print
// usage and exit 0 WITHOUT starting the server (GAP-033).
func wantsServeHelp(args []string) bool {
	return wantsHelp(args)
}

// printServerUsage documents the env-only server configuration. It is the
// custom flag.Usage for server mode and the output of `canopyd serve --help`.
func printServerUsage() {
	fmt.Fprintf(os.Stderr, "Usage: canopyd [serve] [flags]\n\n")
	fmt.Fprintf(os.Stderr, "Server mode (default). Configuration is environment-based — see\n")
	fmt.Fprintf(os.Stderr, "docs/INTEGRATION.md §4 and README \"Environment Variables\".\n\n")
	fmt.Fprintf(os.Stderr, "Flags:\n")
	fmt.Fprintf(os.Stderr, "  -version    print version and exit\n")
	fmt.Fprintf(os.Stderr, "  -h, --help  print this help and exit\n\n")
	fmt.Fprintf(os.Stderr, "Key environment variables:\n")
	fmt.Fprintf(os.Stderr, "  HTTP_ADDR       listen address (default :8080)\n")
	fmt.Fprintf(os.Stderr, "  CANOPY_DB_URL   postgres:// DSN (overrides all DB_* fields)\n")
	fmt.Fprintf(os.Stderr, "  DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME, DB_SCHEMA, DB_SSLMODE\n")
	fmt.Fprintf(os.Stderr, "  JWT_SECRET      HS256 signing secret (default dev-secret-change-me)\n")
	fmt.Fprintf(os.Stderr, "  LOG_LEVEL, LOG_FORMAT, METRICS_ENABLED, CORS_ORIGIN\n")
}

// Ensure net/http is used (compile-time check).
var _ = errors.New
