package main

import (
	"reflect"
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
