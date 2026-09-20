package main

import (
	"strings"
	"testing"
)

func TestContextAccuracyCommandUsageAndValidation(t *testing.T) {
	if !isSubcommand("context-accuracy") {
		t.Fatal("context-accuracy is not a registered top-level subcommand")
	}

	tests := []struct {
		name string
		args []string
		want int
		text string
	}{
		{name: "help", args: []string{"--help"}, want: 0, text: "Usage: canopyd context-accuracy"},
		{name: "sample zero", args: []string{"--sample", "0"}, want: 2, text: "--sample must be at least 1"},
		{name: "threshold outside range", args: []string{"--min-accuracy", "2"}, want: 2, text: "--min-accuracy must be between"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, output := captureStderr(t, func() int { return runContextAccuracyCmdE(tt.args) })
			if code != tt.want {
				t.Fatalf("exit = %d, want %d; output=%q", code, tt.want, output)
			}
			if !strings.Contains(output, tt.text) {
				t.Fatalf("output %q does not contain %q", output, tt.text)
			}
		})
	}
}
