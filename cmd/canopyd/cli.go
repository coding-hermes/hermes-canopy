package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultServerURL = "http://localhost:8091"

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
	Content  string  `json:"content,omitempty"`
}

type graphEdgeSummary struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	EdgeType string `json:"edge_type"`
}

// --- CLI entry point -----------------------------------------------------------

// knownSubcommands maps top-level subcommands to their handler.
var knownSubcommands = map[string]struct{}{
	"tree":             {},
	"session":          {},
	"topic":            {},
	"card":             {},
	"db":               {},
	"context-accuracy": {},
}

// exitUnknownSubcommand is the exit code for CLI misuse: an argument that is
// neither a known subcommand nor a flag. It is distinct from 1 (a command that
// ran and failed) so a script can tell "you typed it wrong" from "it failed".
const exitUnknownSubcommand = 2

// runCLI detects the subcommand from args and dispatches to the appropriate handler.
// It should be called when os.Args[1] matches a known subcommand.
func runCLI() {
	if len(os.Args) < 2 {
		printCLIUsage()
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
	case "card":
		runCardCmd(os.Args[2:])
	case "db":
		runDBCmd(os.Args[2:])
	case "context-accuracy":
		runContextAccuracyCmd(os.Args[2:])
	default:
		os.Exit(refuseUnknownSubcommand(sub))
	}
}

// printCLIUsage documents the top-level CLI surface: every subcommand a reader
// can actually run. main() prints it for an unrecognised first argument, and
// runCLI prints it when it is somehow reached with no subcommand.
func printCLIUsage() {
	fmt.Fprintf(os.Stderr, "Usage: canopyd <subcommand> [args...]\n")
	fmt.Fprintf(os.Stderr, "Subcommands:\n")
	fmt.Fprintf(os.Stderr, "  tree create <name> [--content <text>]  Create a new tree (root message required)\n")
	fmt.Fprintf(os.Stderr, "  tree list                 List all trees\n")
	fmt.Fprintf(os.Stderr, "  tree delete <id>          Delete a tree\n")
	fmt.Fprintf(os.Stderr, "  tree navigate <id>        Print tree structure as indented text\n")
	fmt.Fprintf(os.Stderr, "  session browse [flags]    List sessions from the Hermes state snapshot (6-hourly, read-only)\n")
	fmt.Fprintf(os.Stderr, "  session import [flags]    Import Hermes sessions from the state snapshot into trees\n")
	fmt.Fprintf(os.Stderr, "  session associations-backfill [flags]  Recompute association metadata for imported sessions\n")
	fmt.Fprintf(os.Stderr, "  topic <subcmd> [flags]    Topic detection: detect, proposals, config\n")
	fmt.Fprintf(os.Stderr, "  card export [flags]       Export the local card stores as deterministic JSONL (docs/CARD_EXPORT.md)\n")
	fmt.Fprintf(os.Stderr, "  db export-sqlite [flags] Copy core graph data from PostgreSQL into a new SQLite file\n")
	fmt.Fprintf(os.Stderr, "  context-accuracy [flags] Measure ancestry/topic selection accuracy\n")
	fmt.Fprintf(os.Stderr, "  serve [flags]             Start the API server (default mode; env-only config)\n")
}

// refuseUnknownSubcommand reports an unrecognised first argument and returns
// the exit code main() must use. It never starts a server: a typo used to fall
// through to server mode and boot canopyd against the live database
// (DF-HERMES-CANOPY-10).
func refuseUnknownSubcommand(arg string) int {
	fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", arg)
	printCLIUsage()
	return exitUnknownSubcommand
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
	fmt.Fprintf(os.Stderr, "\nThese commands are HTTP clients of a running canopyd and target\n")
	fmt.Fprintf(os.Stderr, "CANOPY_SERVER_URL (default %s). HTTP_ADDR and DB_* configure the\n", defaultServerURL)
	fmt.Fprintf(os.Stderr, "server process; set CANOPY_SERVER_URL to the API you mean to change.\n")
}

// --- HTTP helpers --------------------------------------------------------------

// serverOnlyEnvVars names the environment variables that configure the canopyd
// SERVER process — its HTTP listen address and its PostgreSQL connection. They
// describe where a server runs, never where an API can be reached from a
// client, so a CLI run with one of them set and no CANOPY_SERVER_URL is
// ambiguous: the CLI used to fall back to defaultServerURL, which silently
// wrote into whatever instance happened to be live on :8091
// (DF-HERMES-CANOPY-6). Only the NAMES are ever reported — DB_PASSWORD would
// leak if values were echoed.
var serverOnlyEnvVars = []string{
	"HTTP_ADDR",
	"CANOPY_DB_URL",
	"DB_HOST",
	"DB_PORT",
	"DB_USER",
	"DB_PASSWORD",
	"DB_NAME",
	"DB_SCHEMA",
	"DB_SSLMODE",
}

// ErrCLITargetUnresolved wraps every resolveServerURL failure so callers (and
// tests) can distinguish a target-configuration refusal from a transport error
// without string matching.
var ErrCLITargetUnresolved = errors.New("unresolved CLI API target")

// cliTargetError is a target refusal carrying an actionable, possibly
// multi-line message. It unwraps to ErrCLITargetUnresolved, so callers match it
// with errors.Is instead of inspecting the text.
type cliTargetError struct{ msg string }

func (e *cliTargetError) Error() string { return e.msg }

func (e *cliTargetError) Unwrap() error { return ErrCLITargetUnresolved }

// cliTargetRefusal builds a refusal whose lines are joined for stderr.
func cliTargetRefusal(lines ...string) error {
	return &cliTargetError{msg: ErrCLITargetUnresolved.Error() + ": " + strings.Join(lines, "\n")}
}

// resolveServerURL resolves the API base URL the CLI must talk to. Rules:
//
//   - An explicit non-empty CANOPY_SERVER_URL wins — even when DB_*/HTTP_ADDR
//     are set; that is the documented isolation path. It must be a valid
//     absolute http(s) URL with a host, and a malformed explicit value fails
//     closed instead of falling back to the default target.
//   - With no explicit value, any server-only setting (HTTP_ADDR or
//     DB_* / CANOPY_DB_URL) is refused: the CLI is an HTTP client and cannot
//     derive an API address from a listen address or a database DSN.
//   - With no relevant configuration at all, the historical default is kept.
//
// It never exits and never contacts the network: help paths and callers that
// want to render their own message stay in control of the process.
func resolveServerURL(getenv func(string) string) (string, error) {
	if raw := strings.TrimSpace(getenv("CANOPY_SERVER_URL")); raw != "" {
		target := strings.TrimRight(raw, "/")
		if err := validateServerURL(target); err != nil {
			return "", cliTargetRefusal(
				fmt.Sprintf("invalid CANOPY_SERVER_URL %s: %v", strconv.Quote(redactURLCredentials(raw)), err),
				fmt.Sprintf("the CLI needs an absolute http(s) URL with a host (e.g. CANOPY_SERVER_URL=%s) and refuses to fall back to %s, which would target whichever instance is live there", defaultServerURL, defaultServerURL),
			)
		}
		return target, nil
	}

	if set := serverOnlyEnvSet(getenv); len(set) > 0 {
		return "", cliTargetRefusal(
			fmt.Sprintf("ambiguous API target — %s set without CANOPY_SERVER_URL", strings.Join(set, ", ")),
			"The canopyd CLI is an HTTP client: it talks to a running canopyd API server and cannot derive that address from HTTP_ADDR (a listen address for `canopyd serve`) or from DB_*/CANOPY_DB_URL (PostgreSQL settings).",
			fmt.Sprintf("Set CANOPY_SERVER_URL to the API base URL of the instance you mean to target, e.g. `CANOPY_SERVER_URL=%s canopyd tree list`.", defaultServerURL),
			"To isolate a scratch instance, start a second canopyd with its own HTTP_ADDR and DB_* settings, then point CANOPY_SERVER_URL at it (see README \"CLI\").",
		)
	}

	return defaultServerURL, nil
}

// validateServerURL rejects anything that is not an absolute http(s) URL with
// a host. The parse error is deliberately not propagated: url.Parse echoes the
// offending URL text, which may contain credentials. Hostname() (not Host) is
// checked so a host-less ":8091" is refused too.
func validateServerURL(target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return errors.New("not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme %q is neither http nor https", u.Scheme)
	}
	if u.Hostname() == "" {
		return errors.New("missing host")
	}
	return nil
}

// serverOnlyEnvSet returns the names (never the values) of the server-only
// configuration variables that are set in getenv.
func serverOnlyEnvSet(getenv func(string) string) []string {
	var set []string
	for _, name := range serverOnlyEnvVars {
		if strings.TrimSpace(getenv(name)) != "" {
			set = append(set, name)
		}
	}
	return set
}

// redactURLCredentials replaces the password in the userinfo of a URL-shaped
// string with "***" so an error message can quote the value without exposing a
// secret. Only the password is masked: the username and host stay readable for
// diagnosis, matching how net/http renders a failed request in its own
// url.Error (stripPassword), so both halves of one message agree. A bare
// userinfo with no ":" (an opaque token used as a username) is masked whole —
// there is no way to tell a token apart from a password.
func redactURLCredentials(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		return raw
	}
	rest := raw[i+3:]
	at := strings.IndexByte(rest, '@')
	if at < 0 || strings.Contains(rest[:at], "/") {
		return raw
	}
	masked := "***"
	if colon := strings.IndexByte(rest[:at], ':'); colon >= 0 {
		masked = rest[:colon+1] + "***"
	}
	return raw[:i+3] + masked + rest[at:]
}

// authHeader returns an Authorization: Bearer header value if CANOPY_TOKEN is
// set, or empty string otherwise (with a warning to stderr).
func authHeader() string {
	tok := os.Getenv("CANOPY_TOKEN")
	if tok == "" {
		fmt.Fprintln(os.Stderr, "Warning: CANOPY_TOKEN is not set; mint a dev JWT (README section \"Authentication (dev mode)\", sub-section \"Direct API access\": sign HS256 with JWT_SECRET, default dev-secret-change-me, sub=00000000-0000-0000-0000-000000000001) and export CANOPY_TOKEN (continuing without auth)")
		return ""
	}
	return "Bearer " + tok
}

// httpClient is a shared HTTP client with a reasonable timeout.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// apiRequestE makes an HTTP request to the canopyd API and returns the parsed
// response body plus its status code. Every failure — an unresolvable or
// invalid CLI target, request build, transport, body read, and HTTP >= 400
// (whose error payload is rendered exactly as before) — is returned as an
// error so callers report it and return a nonzero exit code of their own.
func apiRequestE(method, path string, body io.Reader) ([]byte, int, error) {
	target, err := resolveServerURL(os.Getenv)
	if err != nil {
		return nil, 0, err
	}

	req, err := http.NewRequest(method, target+path, body)
	if err != nil {
		// url.Parse echoes the offending URL in its error, so the target is
		// redacted here too — a parse error must not be the one path that
		// prints a password (DF-HERMES-CANOPY-6).
		return nil, 0, fmt.Errorf("failed to build request for %s: %w", redactURLCredentials(target), err)
	}

	req.Header.Set("Content-Type", "application/json")
	if ah := authHeader(); ah != "" {
		req.Header.Set("Authorization", ah)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		// The resolved target may carry userinfo; Go's own url.Error redacts it
		// (net/http stripPassword) but this prefix must not print it verbatim.
		return nil, 0, fmt.Errorf("failed to reach server at %s: %w", redactURLCredentials(target), err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr apiErrorResponse
		if err := json.Unmarshal(respBody, &apiErr); err == nil && apiErr.Error.Code != "" {
			return nil, resp.StatusCode, fmt.Errorf("[%s] %s", apiErr.Error.Code, apiErr.Error.Message)
		}
		// Fallback: print status and raw body (trimmed).
		raw := strings.TrimSpace(string(respBody))
		if len(raw) > 500 {
			raw = raw[:500] + "..."
		}
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d\n%s", resp.StatusCode, raw)
	}

	return respBody, resp.StatusCode, nil
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

	respBody, _, err := apiRequestE(http.MethodPost, "/api/v1/trees", bytes.NewReader(reqBody))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

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
	respBody, _, err := apiRequestE(http.MethodGet, "/api/v1/trees", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

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

	_, status, err := apiRequestE(http.MethodDelete, "/api/v1/trees/"+id, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

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
	respBody, _, err := apiRequestE(http.MethodGet, "/api/v1/trees/"+treeID, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	var detail treeDetail
	if err := json.Unmarshal(respBody, &detail); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to parse tree detail: %v\n", err)
		return 1
	}

	if detail.RootNodeID == "" {
		fmt.Fprintf(os.Stderr, "Error: tree has no root node\n")
		return 1
	}

	// Step 2: Fetch subtree.
	subtreePath := fmt.Sprintf("/api/v1/graph/trees/%s/subtree/%s", treeID, detail.RootNodeID)
	respBody, _, err = apiRequestE(http.MethodGet, subtreePath, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	var result graphQueryResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to parse subtree: %v\n", err)
		return 1
	}

	if len(result.Nodes) == 0 {
		fmt.Println("(empty tree)")
		return 0
	}

	fmt.Printf("Tree: %s\n\n", detail.Title)

	for _, line := range renderTree(result.Nodes) {
		fmt.Println(line)
	}
	return 0
}

// snippetMaxLen is the maximum length of a content preview in tree rows.
const snippetMaxLen = 60

// renderTree renders nodes as an indented tree: children are nested under
// their parent with tree connectors, each row shows a unique short-ID
// label, the node type, and a content snippet (GAP-045).
func renderTree(nodes []graphNodeSummary) []string {
	if len(nodes) == 0 {
		return nil
	}

	// Build a child map for tree printing.
	children := make(map[string][]graphNodeSummary)
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
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

	labels := uniqueLabels(ids)

	// Find root nodes (those with no parent or whose parent is not in the set).
	rootNodes := children[""]
	if len(rootNodes) == 0 {
		// Fallback: find a node that is referenced as a source but not as
		// a target, or just use the first node.
		rootNodes = nodes[:1]
	}

	var lines []string
	for _, root := range rootNodes {
		lines = append(lines, renderNodeLines(root, children, labels, "", true, true)...)
	}
	return lines
}

// renderNodeLines recursively renders a node and its children as indented
// tree rows with connectors (├── / └── / │). isRoot suppresses the
// connector and indentation for the top-level node.
func renderNodeLines(node graphNodeSummary, children map[string][]graphNodeSummary, labels map[string]string, prefix string, isLast, isRoot bool) []string {
	var connector string
	if isRoot {
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
	rowLabel := labels[node.ID]
	if rowLabel == "" {
		rowLabel = shortID(node.ID)
	}
	line := fmt.Sprintf("%s%s[%s] %s", prefix, connector, rowLabel, label)
	if snip := snippet(node.Content, snippetMaxLen); snip != "" {
		line += " — " + snip
	}

	lines := []string{line}
	kids := children[node.ID]
	for i, child := range kids {
		last := i == len(kids)-1
		childPrefix := prefix
		if !isRoot {
			if isLast {
				childPrefix += "    "
			} else {
				childPrefix += "│   "
			}
		}
		lines = append(lines, renderNodeLines(child, children, labels, childPrefix, last, false)...)
	}
	return lines
}

// uniqueLabels computes a display label for every ID such that no two
// labels collide: start with the 8-char short form and extend one
// non-hyphen character at a time until every label is unique (full UUID
// as last resort).
func uniqueLabels(ids []string) map[string]string {
	labels := make(map[string]string, len(ids))
	for _, id := range ids {
		labels[id] = shortID(id)
	}
	for {
		groups := make(map[string][]string)
		for _, id := range ids {
			groups[labels[id]] = append(groups[labels[id]], id)
		}
		extended := false
		for _, group := range groups {
			if len(group) < 2 {
				continue
			}
			for _, id := range group {
				next := extendLabel(id, labels[id])
				if next != labels[id] {
					labels[id] = next
					extended = true
				}
			}
		}
		if !extended {
			break
		}
	}
	return labels
}

// extendLabel returns label extended by one non-hyphen character from id
// (UUID separators are skipped so labels stay compact hex), or label
// unchanged when it already covers every non-hyphen character of id.
func extendLabel(id, label string) string {
	if len(label) >= len(id) {
		return label
	}
	// Rebuild the label from id's non-hyphen characters, taking one more
	// than label currently has.
	var sb strings.Builder
	for _, c := range id {
		if c == '-' {
			continue
		}
		sb.WriteRune(c)
		if sb.Len() > len(label) {
			return sb.String()
		}
	}
	// No more non-hyphen characters left: fall back to the full UUID.
	return id
}

// snippet returns a single-line preview of content: runs of whitespace
// (including newlines) collapse to single spaces, and the result is
// truncated to maxLen characters with "…" appended when needed. Empty or
// whitespace-only content yields "".
func snippet(content string, maxLen int) string {
	s := strings.Join(strings.Fields(content), " ")
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
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
// It also lists the CLI subcommands: without them a reader of `canopyd --help`
// never learns that the binary has a command line at all (DF-HERMES-CANOPY-10).
func printServerUsage() {
	fmt.Fprintf(os.Stderr, "Usage: canopyd [serve] [flags]\n\n")
	fmt.Fprintf(os.Stderr, "Server mode (default). Configuration is environment-based — see\n")
	fmt.Fprintf(os.Stderr, "docs/INTEGRATION.md §4 and README \"Environment Variables\".\n\n")
	fmt.Fprintf(os.Stderr, "Subcommands:\n")
	fmt.Fprintf(os.Stderr, "  serve [flags]             Start the API server (default mode)\n")
	fmt.Fprintf(os.Stderr, "  tree <subcmd> [args...]   Tree CRUD from the command line (see `canopyd tree --help`)\n")
	fmt.Fprintf(os.Stderr, "  session <subcmd> [flags]  Browse/import Hermes sessions from the state snapshot (see `canopyd session --help`)\n")
	fmt.Fprintf(os.Stderr, "  topic <subcmd> [flags]    Topic detection proposals + config (see `canopyd topic --help`)\n")
	fmt.Fprintf(os.Stderr, "  db export-sqlite [flags] Copy core graph data from PostgreSQL into a new SQLite file\n\n")
	fmt.Fprintf(os.Stderr, "The tree/session/topic subcommands are HTTP clients of a running canopyd;\n")
	fmt.Fprintf(os.Stderr, "they target CANOPY_SERVER_URL (default %s).\n\n", defaultServerURL)
	fmt.Fprintf(os.Stderr, "Flags:\n")
	fmt.Fprintf(os.Stderr, "  -version                print version and exit\n")
	fmt.Fprintf(os.Stderr, "  -relay-mode             air_gapped, self_hosted, or saas (default air_gapped)\n")
	fmt.Fprintf(os.Stderr, "  -relay-listen           relay listener address\n")
	fmt.Fprintf(os.Stderr, "  -relay-connect          upstream relay address\n")
	fmt.Fprintf(os.Stderr, "  -max-relay-sessions     maximum concurrent sessions\n")
	fmt.Fprintf(os.Stderr, "  -relay-heartbeat        heartbeat interval\n")
	fmt.Fprintf(os.Stderr, "  -relay-drain-timeout    graceful drain timeout\n")
	fmt.Fprintf(os.Stderr, "  -relay-tls-enabled      enable TLS for relay connections\n")
	fmt.Fprintf(os.Stderr, "  -relay-tls-cert         TLS certificate file path\n")
	fmt.Fprintf(os.Stderr, "  -relay-tls-key          TLS key file path\n")
	fmt.Fprintf(os.Stderr, "  -relay-tls-ca           TLS CA certificate file path\n")
	fmt.Fprintf(os.Stderr, "  -relay-tls-mutual       require mutual TLS\n")
	fmt.Fprintf(os.Stderr, "  -h, --help  print this help and exit\n\n")
	fmt.Fprintf(os.Stderr, "Key environment variables:\n")
	fmt.Fprintf(os.Stderr, "  HTTP_ADDR       listen address (default :8080)\n")
	fmt.Fprintf(os.Stderr, "  CANOPY_DB_URL   postgres:// DSN (overrides all DB_* fields)\n")
	fmt.Fprintf(os.Stderr, "  CANOPY_DB_DRIVER postgres (default) or sqlite\n")
	fmt.Fprintf(os.Stderr, "  CANOPY_SQLITE_PATH SQLite file path (default ~/.canopy/canopy.sqlite)\n")
	fmt.Fprintf(os.Stderr, "  DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME, DB_SCHEMA, DB_SSLMODE\n")
	fmt.Fprintf(os.Stderr, "  JWT_SECRET      HS256 signing secret (default dev-secret-change-me)\n")
	fmt.Fprintf(os.Stderr, "  LOG_LEVEL, LOG_FORMAT, METRICS_ENABLED, CORS_ORIGIN\n")
}
