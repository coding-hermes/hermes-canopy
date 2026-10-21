package handler

import (
	"errors"
	"testing"
)

// TestInvalidBodyMessage verifies that decodeJSON errors are translated
// into accurate client-facing messages — distinguishing malformed JSON
// from unknown-field / type-mismatch validation errors.
// Regression test for DF-HERMES-CANOPY-94.
func TestInvalidBodyMessage(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "json decode error returns generic message",
			err:      errors.New("invalid character 'o' looking for beginning of object key string"),
			expected: "request body must be valid JSON",
		},
		{
			name:     "unknown field error names the field",
			err:      errors.New("json: unknown field \"nonsense_field\""),
			expected: `request body contains an unknown field "nonsense_field"`,
		},
		{
			name:     "unknown field without quotes also handled",
			err:      errors.New("json: unknown field nonsense_field"),
			expected: "request body contains an unknown field nonsense_field",
		},
		{
			name:     "unknown field with contentFormat alias",
			err:      errors.New("json: unknown field contentFormat"),
			expected: "request body contains an unknown field contentFormat",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := invalidBodyMessage(tt.err)
			if got != tt.expected {
				t.Errorf("invalidBodyMessage() = %q, want %q", got, tt.expected)
			}
		})
	}
}
