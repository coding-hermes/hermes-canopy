package main

import (
	"encoding/json"
	"io"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestStripServerFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"no args", nil, nil},
		{"plain subcommand", []string{"tree", "list"}, []string{"tree", "list"}},
		{"server flag before subcommand", []string{"-version", "tree", "list"}, []string{"tree", "list"}},
		{
			"session import flags preserved",
			[]string{"session", "import", "--db", "/tmp/state.db", "--limit", "5", "--dry-run"},
			[]string{"session", "import", "--db", "/tmp/state.db", "--limit", "5", "--dry-run"},
		},
		{
			"mixed leading flags stop at first token",
			[]string{"-version", "-addr", ":9999", "tree", "create", "x"},
			[]string{":9999", "tree", "create", "x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripServerFlags(tt.args)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("stripServerFlags(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestIsSubcommand(t *testing.T) {
	for _, sub := range []string{"tree", "session"} {
		if !isSubcommand(sub) {
			t.Errorf("isSubcommand(%q) = false, want true", sub)
		}
	}
	for _, non := range []string{"-version", "import", "create"} {
		if isSubcommand(non) {
			t.Errorf("isSubcommand(%q) = true, want false", non)
		}
	}
}

func TestWantsServeHelp(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"no args", nil, false},
		{"plain serve", []string{}, false},
		{"short help", []string{"-h"}, true},
		{"long help", []string{"--help"}, true},
		{"help after other args", []string{"-version", "--help"}, true},
		{"non-help flags", []string{"-version"}, false},
		{"subcommand args are not help", []string{"tree", "list"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wantsServeHelp(tt.args); got != tt.want {
				t.Errorf("wantsServeHelp(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestCLIDefaultServerURL(t *testing.T) {
	t.Setenv("CANOPY_SERVER_URL", "")
	if got := serverURL(); got != "http://localhost:8091" {
		t.Fatalf("serverURL() = %q, want http://localhost:8091", got)
	}
}

func TestMissingTokenHint(t *testing.T) {
	t.Setenv("CANOPY_TOKEN", "")
	_, out := captureStderr(t, func() int {
		if got := authHeader(); got != "" {
			t.Fatalf("authHeader() = %q, want empty", got)
		}
		return 0
	})
	if !strings.Contains(out, "CANOPY_TOKEN") || !strings.Contains(out, "canopyd serve") {
		t.Fatalf("missing-token hint lacks env var or dev-token source: %q", out)
	}
}

// captureStderr runs f with os.Stderr redirected to a pipe and returns the
// exit code f produced and everything f wrote to stderr.
func captureStderr(t *testing.T, f func() int) (int, string) {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	code := f()
	_ = w.Close()
	data, _ := io.ReadAll(r)
	return code, string(data)
}

func TestBuildTreeCreateBodyWithContent(t *testing.T) {
	body, err := buildTreeCreateBody("GAP-042 smoke test", "hello")
	if err != nil {
		t.Fatalf("buildTreeCreateBody: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got["title"] != "GAP-042 smoke test" {
		t.Errorf("title = %v, want %q", got["title"], "GAP-042 smoke test")
	}

	// The rootMessage object must be present with camelCase keys (the
	// handler decodes camelCase JSON — see internal/handler/tree_handler.go).
	root, ok := got["rootMessage"].(map[string]any)
	if !ok {
		t.Fatalf("rootMessage missing or wrong type: %#v", got["rootMessage"])
	}
	if root["content"] != "hello" {
		t.Errorf("rootMessage.content = %v, want hello", root["content"])
	}
	if root["contentFormat"] != "markdown" {
		t.Errorf("rootMessage.contentFormat = %v, want markdown", root["contentFormat"])
	}
	if root["nodeType"] != "message" {
		t.Errorf("rootMessage.nodeType = %v, want message", root["nodeType"])
	}

	// No snake_case keys anywhere in the body.
	if _, ok := got["root_message"]; ok {
		t.Errorf("body contains snake_case root_message key")
	}
	if _, ok := root["content_format"]; ok {
		t.Errorf("rootMessage contains snake_case content_format key")
	}
	if _, ok := root["node_type"]; ok {
		t.Errorf("rootMessage contains snake_case node_type key")
	}
}

func TestBuildTreeCreateBodyNoContent(t *testing.T) {
	body, err := buildTreeCreateBody("No content", "")
	if err != nil {
		t.Fatalf("buildTreeCreateBody: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got["title"] != "No content" {
		t.Errorf("title = %v, want %q", got["title"], "No content")
	}
	if _, ok := got["rootMessage"]; ok {
		t.Errorf("rootMessage present when content is empty")
	}
}

func TestParseTreeCreateArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantName    string
		wantContent string
		wantHelp    bool
		wantErr     bool
	}{
		{"no args", nil, "", "", false, false},
		{"name only", []string{"My Tree"}, "My Tree", "", false, false},
		{"name then content", []string{"My Tree", "--content", "hello"}, "My Tree", "hello", false, false},
		{"content then name", []string{"--content", "hello", "My Tree"}, "My Tree", "hello", false, false},
		{"message alias", []string{"My Tree", "--message", "hi"}, "My Tree", "hi", false, false},
		{"content equals form", []string{"My Tree", "--content=hello"}, "My Tree", "hello", false, false},
		{"message equals form", []string{"--message=hi", "My Tree"}, "My Tree", "hi", false, false},
		{"help flag", []string{"--help"}, "", "", true, false},
		{"short help", []string{"-h"}, "", "", true, false},
		{"help with name", []string{"My Tree", "--help"}, "My Tree", "", true, false},
		{"help and content", []string{"--content", "x", "My Tree", "--help"}, "My Tree", "x", true, false},
		{"missing value", []string{"My Tree", "--content"}, "", "", false, true},
		{"unknown flag", []string{"My Tree", "--bogus"}, "", "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, content, help, err := parseTreeCreateArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseTreeCreateArgs(%v) err = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
			if name != tt.wantName {
				t.Errorf("parseTreeCreateArgs(%v) name = %q, want %q", tt.args, name, tt.wantName)
			}
			if content != tt.wantContent {
				t.Errorf("parseTreeCreateArgs(%v) content = %q, want %q", tt.args, content, tt.wantContent)
			}
			if help != tt.wantHelp {
				t.Errorf("parseTreeCreateArgs(%v) help = %v, want %v", tt.args, help, tt.wantHelp)
			}
		})
	}
}

// TestTreeCreateHelpExitZeroNoAPI asserts `tree create --help` / `-h` print
// the create usage and return exit code 0. The help path returns before any
// HTTP call, so no API request can be attempted (GAP-042).
func TestTreeCreateHelpExitZeroNoAPI(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		code, out := captureStderr(t, func() int { return runTreeCmdE(append([]string{"create"}, args...)) })
		if code != 0 {
			t.Errorf("runTreeCmdE(create %v) exit = %d, want 0", args, code)
		}
		if !strings.Contains(out, "Usage: canopyd tree create") {
			t.Errorf("runTreeCmdE(create %v) stderr missing create usage: %q", args, out)
		}
		if strings.Contains(out, "Error:") {
			t.Errorf("runTreeCmdE(create %v) stderr contains an error: %q", args, out)
		}
	}
}

// TestTreeCommandDispatchHelp asserts `canopyd tree --help` / `-h` print the
// tree usage and exit 0 instead of "unknown tree subcommand: --help".
func TestTreeCommandDispatchHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		code, out := captureStderr(t, func() int { return runTreeCmdE(args) })
		if code != 0 {
			t.Errorf("runTreeCmdE(%v) exit = %d, want 0", args, code)
		}
		if !strings.Contains(out, "Usage: canopyd tree <create|list|delete|navigate>") {
			t.Errorf("runTreeCmdE(%v) stderr missing tree usage: %q", args, out)
		}
		if strings.Contains(out, "unknown tree subcommand") {
			t.Errorf("runTreeCmdE(%v) stderr reports unknown subcommand: %q", args, out)
		}
	}
}

// TestTreeSiblingSubcommandHelp asserts list/delete/navigate also handle
// --help by printing usage and exiting 0 (GAP-042).
func TestTreeSiblingSubcommandHelp(t *testing.T) {
	tests := []struct {
		args    []string
		wantUse string
	}{
		{[]string{"list", "--help"}, "Usage: canopyd tree list"},
		{[]string{"delete", "--help"}, "Usage: canopyd tree delete"},
		{[]string{"navigate", "--help"}, "Usage: canopyd tree navigate"},
	}
	for _, tt := range tests {
		code, out := captureStderr(t, func() int { return runTreeCmdE(tt.args) })
		if code != 0 {
			t.Errorf("runTreeCmdE(%v) exit = %d, want 0", tt.args, code)
		}
		if !strings.Contains(out, tt.wantUse) {
			t.Errorf("runTreeCmdE(%v) stderr missing %q: %q", tt.args, tt.wantUse, out)
		}
	}
}

// TestTreeCreateMissingContent asserts `tree create <name>` without
// --content prints a clear usage error and exits 1 without hitting the API.
func TestTreeCreateMissingContent(t *testing.T) {
	code, out := captureStderr(t, func() int { return runTreeCmdE([]string{"create", "My Tree"}) })
	if code != 1 {
		t.Errorf("runTreeCmdE(create My Tree) exit = %d, want 1", code)
	}
	if !strings.Contains(out, "--content") {
		t.Errorf("runTreeCmdE(create My Tree) stderr does not mention --content: %q", out)
	}
}

// TestTreeCreateNoArgsPrintsUsage asserts `tree create` with no arguments
// keeps printing usage and exits 1.
func TestTreeCreateNoArgsPrintsUsage(t *testing.T) {
	code, out := captureStderr(t, func() int { return runTreeCmdE([]string{"create"}) })
	if code != 1 {
		t.Errorf("runTreeCmdE(create) exit = %d, want 1", code)
	}
	if !strings.Contains(out, "Usage: canopyd tree create") {
		t.Errorf("runTreeCmdE(create) stderr missing create usage: %q", out)
	}
}

// TestTreeUnknownSubcommandStillErrors asserts the unknown-subcommand path
// still exits 1 (behavior preserved).
func TestTreeUnknownSubcommandStillErrors(t *testing.T) {
	code, out := captureStderr(t, func() int { return runTreeCmdE([]string{"bogus"}) })
	if code != 1 {
		t.Errorf("runTreeCmdE(bogus) exit = %d, want 1", code)
	}
	if !strings.Contains(out, "unknown tree subcommand: bogus") {
		t.Errorf("runTreeCmdE(bogus) stderr missing unknown-subcommand error: %q", out)
	}
}

// --- GAP-045: tree navigate hierarchy, snippets, unique labels --------------

// testNode builds a graphNodeSummary with the given parent link ("" = root).
func testNode(id, parent, content string) graphNodeSummary {
	n := graphNodeSummary{
		ID:      id,
		Type:    "message",
		Content: content,
	}
	if parent != "" {
		p := parent
		n.ParentID = &p
	}
	return n
}

// TestRenderTreeIndentationAndConnectors asserts a 3-level tree renders
// with connectors and depth-visible indentation (GAP-045).
func TestRenderTreeIndentationAndConnectors(t *testing.T) {
	nodes := []graphNodeSummary{
		testNode("10000000-0000-4000-8000-00000000000a", "", "root content"),
		testNode("20000000-0000-4000-8000-00000000000b", "10000000-0000-4000-8000-00000000000a", "child A content"),
		testNode("30000000-0000-4000-8000-00000000000c", "10000000-0000-4000-8000-00000000000a", "child B content"),
		testNode("40000000-0000-4000-8000-00000000000d", "20000000-0000-4000-8000-00000000000b", "grandchild content"),
	}
	lines := renderTree(nodes)
	want := []string{
		"[10000000] message — root content",
		"├── [20000000] message — child A content",
		"│   └── [40000000] message — grandchild content",
		"└── [30000000] message — child B content",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("renderTree mismatch:\n got: %q\nwant: %q", lines, want)
	}
}

// TestRenderTreeUniqueLabels asserts nodes sharing an 8-char prefix get
// disambiguated labels while non-colliding nodes keep the short form.
func TestRenderTreeUniqueLabels(t *testing.T) {
	rootID := "abcd1234-1111-4000-8000-000000000001"
	childID := "abcd1234-2222-4000-8000-000000000002"
	otherID := "ffffffff-3333-4000-8000-000000000003"
	nodes := []graphNodeSummary{
		testNode(rootID, "", "root"),
		testNode(childID, rootID, "child"),
		testNode(otherID, rootID, "other"),
	}
	lines := renderTree(nodes)

	// Extract row labels and check uniqueness across all rows.
	var labels []string
	labelRe := regexp.MustCompile(`\[([0-9a-f]+)\]`)
	for _, line := range lines {
		m := labelRe.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("line %q has no [label]", line)
		}
		labels = append(labels, m[1])
	}
	seen := map[string]bool{}
	for _, l := range labels {
		if seen[l] {
			t.Errorf("duplicate row label %q in %v", l, labels)
		}
		seen[l] = true
	}

	// Root and child share the 8-char prefix "abcd1234" → extended to 9.
	if !strings.Contains(lines[0], "[abcd12341]") {
		t.Errorf("root label not disambiguated: %q", lines[0])
	}
	if !strings.Contains(lines[1], "[abcd12342]") {
		t.Errorf("child label not disambiguated: %q", lines[1])
	}
	// Non-colliding node keeps the 8-char short form.
	if !strings.Contains(lines[2], "[ffffffff]") {
		t.Errorf("non-colliding node should keep 8-char label: %q", lines[2])
	}
}

// TestRenderTreeSnippetTruncation asserts long content is truncated to
// snippetMaxLen runes, newlines collapse to spaces, and empty content has
// no snippet separator.
func TestRenderTreeSnippetTruncation(t *testing.T) {
	long := "🚀 This is a very long message body that keeps going and going well beyond the sixty character preview limit so it must be truncated"
	nodes := []graphNodeSummary{
		testNode("10000000-0000-4000-8000-00000000000a", "", long),
		testNode("20000000-0000-4000-8000-00000000000b", "10000000-0000-4000-8000-00000000000a", "line one\nline two\n\nline four"),
		testNode("30000000-0000-4000-8000-00000000000c", "10000000-0000-4000-8000-00000000000a", ""),
	}
	lines := renderTree(nodes)

	prefix := "[10000000] message — "
	if !strings.HasPrefix(lines[0], prefix) {
		t.Fatalf("line 0 missing snippet prefix: %q", lines[0])
	}
	snip := strings.TrimPrefix(lines[0], prefix)
	if !strings.HasSuffix(snip, "…") {
		t.Errorf("long content not truncated: %q", snip)
	}
	if got := len([]rune(snip)); got != snippetMaxLen {
		t.Errorf("snippet length = %d runes, want %d", got, snippetMaxLen)
	}
	if !strings.Contains(lines[1], "line one line two line four") {
		t.Errorf("newlines not collapsed to spaces: %q", lines[1])
	}
	if strings.Contains(lines[2], " — ") {
		t.Errorf("empty content should have no snippet separator: %q", lines[2])
	}
}

// TestRenderTreeEmpty asserts an empty node set renders no rows.
func TestRenderTreeEmpty(t *testing.T) {
	if lines := renderTree(nil); lines != nil {
		t.Errorf("renderTree(nil) = %v, want nil", lines)
	}
}

// TestUniqueLabels asserts collision-free labels: colliding IDs extend
// beyond 8 chars, distinct IDs keep the short form.
func TestUniqueLabels(t *testing.T) {
	a := "abcd1234-aaaa-4000-8000-000000000001"
	b := "abcd1234-bbbb-4000-8000-000000000002"
	c := "12345678-cccc-4000-8000-000000000003"
	d := "abcd1234-cccc-4000-8000-000000000004" // collides with a and b too
	labels := uniqueLabels([]string{a, b, c, d})

	seen := map[string]string{}
	for _, id := range []string{a, b, c, d} {
		l := labels[id]
		if l == "" {
			t.Fatalf("no label for %s", id)
		}
		if prev, ok := seen[l]; ok {
			t.Errorf("label %q shared by %s and %s", l, prev, id)
		}
		seen[l] = id
	}
	if labels[c] != "12345678" {
		t.Errorf("non-colliding ID label = %q, want %q", labels[c], "12345678")
	}
	for _, id := range []string{a, b, d} {
		if len(labels[id]) <= 8 {
			t.Errorf("colliding ID %s label %q not extended beyond 8 chars", id, labels[id])
		}
	}
}

// TestUniqueLabelsIdenticalIDsTerminate asserts identical IDs fall back to
// the full UUID without an infinite loop.
func TestUniqueLabelsIdenticalIDsTerminate(t *testing.T) {
	id := "abcd1234-aaaa-4000-8000-000000000001"
	labels := uniqueLabels([]string{id, id})
	if labels[id] != id {
		t.Errorf("identical IDs should fall back to the full UUID, got %q", labels[id])
	}
}

// TestSnippet asserts whitespace collapsing and rune-safe truncation.
func TestSnippet(t *testing.T) {
	tests := []struct {
		name    string
		content string
		maxLen  int
		want    string
	}{
		{"empty", "", 60, ""},
		{"whitespace only", " \n	 ", 60, ""},
		{"short unchanged", "hi there", 60, "hi there"},
		{"whitespace collapsed", "a\n\n  b	c", 60, "a b c"},
		{"exact fit", "12345", 5, "12345"},
		{"truncated", "123456789", 5, "1234…"},
		{"rune-safe truncation", "héllo wörld", 6, "héllo…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := snippet(tt.content, tt.maxLen); got != tt.want {
				t.Errorf("snippet(%q, %d) = %q, want %q", tt.content, tt.maxLen, got, tt.want)
			}
		})
	}
}
