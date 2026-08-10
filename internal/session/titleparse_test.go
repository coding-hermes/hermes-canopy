package session

import (
	"testing"
)

func TestParseTitle_BoardTask(t *testing.T) {
	tests := []struct {
		title string
		task  string
	}{
		{"Fix WIRE-006 in importer", "WIRE-006"},
		{"BUG-034 regression fix", "BUG-034"},
		{"Addresses GAP-001 and GAP-002", "GAP-001"}, // first match
		{"task TASK-123 description", "TASK-123"},
		{"REL-003 gate", "REL-003"},
		{"lowercase bug-034 should also match", "BUG-034"}, // normalized to uppercase
	}
	for _, tt := range tests {
		info := ParseTitle(tt.title)
		if info.BoardTask != tt.task {
			t.Errorf("ParseTitle(%q).BoardTask = %q, want %q", tt.title, info.BoardTask, tt.task)
		}
	}
}

func TestParseTitle_BoardTaskNegative(t *testing.T) {
	tests := []string{
		"Documenting the Kelly leverage cycle",
		"alpha",
		"H4F Hourly Health Check · Aug 09 19:12",
		"no board refs here",
	}
	for _, title := range tests {
		info := ParseTitle(title)
		if info.BoardTask != "" {
			t.Errorf("ParseTitle(%q).BoardTask = %q, want empty", title, info.BoardTask)
		}
	}
}

func TestParseTitle_CommitHash(t *testing.T) {
	tests := []struct {
		title string
		hash  string
	}{
		{"Fix in commit a1b2c3d", "a1b2c3d"},
		{"revert abc1234def5678", "abc1234def5678"},           // 12 chars
		{"long hash 0123456789abcdef0123456789abcdef01234567", "0123456789abcdef0123456789abcdef01234567"}, // 40 chars
	}
	for _, tt := range tests {
		info := ParseTitle(tt.title)
		if info.CommitHash != tt.hash {
			t.Errorf("ParseTitle(%q).CommitHash = %q, want %q", tt.title, info.CommitHash, tt.hash)
		}
	}
}

func TestParseTitle_CommitHashNegative(t *testing.T) {
	// Pure numbers should NOT match as commit hashes.
	info := ParseTitle("fix 1234567 issue")
	if info.CommitHash != "" {
		t.Errorf("ParseTitle(%q).CommitHash = %q, want empty (pure number)", "fix 1234567 issue", info.CommitHash)
	}
	// Short strings (<7 chars) should not match.
	info = ParseTitle("hash ab12cd")
	if info.CommitHash != "" {
		t.Errorf("ParseTitle(%q).CommitHash = %q, want empty (<7 chars)", "hash ab12cd", info.CommitHash)
	}
}

func TestParseTitle_Project(t *testing.T) {
	tests := []struct {
		title   string
		project string
	}{
		{"hermes-canopy-duckbrain-sync · Aug 09 03:15", "hermes-canopy"},
		{"9router-duckbrain-sync · Aug 09 03:20", "9router"},
		{"consensus-duckbrain-sync · Aug 08 03:09", "consensus"},
		{"speclang-duckbrain-sync · Aug 09 03:16", "speclang"},
		{"hivemind-duckbrain-sync · Aug 08 03:08", "hivemind"},
		{"helix-duckbrain-sync · Aug 08 03:09", "helix"},
		{"totalstack-shape-validator · Aug 09 18:54", "totalstack-shape-validator"},
	}
	for _, tt := range tests {
		info := ParseTitle(tt.title)
		if info.Project != tt.project {
			t.Errorf("ParseTitle(%q).Project = %q, want %q", tt.title, info.Project, tt.project)
		}
	}
}

func TestParseTitle_ProjectNegative(t *testing.T) {
	tests := []string{
		"Documenting the Kelly leverage cycle",
		"alpha",
		"alpha · Aug 09 18:41",
		"H4F Hourly Health Check · Aug 09 19:12",
		"Recovering the testing plan from Duckbrain",
	}
	for _, title := range tests {
		info := ParseTitle(title)
		if info.Project != "" {
			t.Errorf("ParseTitle(%q).Project = %q, want empty", title, info.Project)
		}
	}
}

func TestParseTitle_Combined(t *testing.T) {
	// A title can have multiple fields populated.
	info := ParseTitle("hermes-canopy: Fix BUG-034 in commit a1b2c3d")
	if info.Project != "hermes-canopy" {
		t.Errorf("Project = %q, want hermes-canopy", info.Project)
	}
	if info.BoardTask != "BUG-034" {
		t.Errorf("BoardTask = %q, want BUG-034", info.BoardTask)
	}
	if info.CommitHash != "a1b2c3d" {
		t.Errorf("CommitHash = %q, want a1b2c3d", info.CommitHash)
	}
}

func TestParseTitle_Empty(t *testing.T) {
	info := ParseTitle("")
	if info != (TitleInfo{}) {
		t.Errorf("ParseTitle(\"\") = %+v, want zero value", info)
	}
}

func TestParseTitle_NoMatch(t *testing.T) {
	info := ParseTitle("Just a regular conversation title with no patterns")
	if info != (TitleInfo{}) {
		t.Errorf("ParseTitle = %+v, want zero value", info)
	}
}
