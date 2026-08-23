// UI-LIVE-001 tests: session-import fields on TreeSummary. The
// fillSessionFields / sourceFromDescription helpers are the single
// chokepoint every list path flows through (legacy + keyset both call
// treeToSummary), so these cover the contract for all of them.
package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

func TestFillSessionFields_ImportedTreeWithMetadata(t *testing.T) {
	sum := TreeSummary{}
	meta := []byte(`{
		"session_id": "sess_abc",
		"parent_session_id": "sess_parent",
		"source": "telegram",
		"project": "hermes-canopy"
	}`)
	fillSessionFields(&sum, meta, "Imported Hermes session sess_abc · model=deepseek · source=cli · started=2026-08-01T00:00:00Z")

	if sum.SessionID != "sess_abc" {
		t.Errorf("SessionID = %q, want sess_abc", sum.SessionID)
	}
	if sum.ParentSessionID != "sess_parent" {
		t.Errorf("ParentSessionID = %q, want sess_parent", sum.ParentSessionID)
	}
	// metadata source wins over description-parse fallback
	if sum.Source != "telegram" {
		t.Errorf("Source = %q, want telegram (metadata wins)", sum.Source)
	}
}

func TestFillSessionFields_DescriptionParseFallback(t *testing.T) {
	// Legacy imports carry source only in the description string.
	sum := TreeSummary{}
	meta := []byte(`{"session_id": "sess_legacy"}`)
	desc := "Imported Hermes session sess_legacy · model=glm-5.2 · source=kimi · started=2026-08-01T12:34:56Z"
	fillSessionFields(&sum, meta, desc)

	if sum.SessionID != "sess_legacy" {
		t.Errorf("SessionID = %q, want sess_legacy", sum.SessionID)
	}
	if sum.Source != "kimi" {
		t.Errorf("Source = %q, want kimi (description fallback)", sum.Source)
	}
	if sum.ParentSessionID != "" {
		t.Errorf("ParentSessionID = %q, want empty", sum.ParentSessionID)
	}
}

func TestFillSessionFields_PlainTreeOmitted(t *testing.T) {
	// No metadata at all → everything empty; omitempty drops the keys.
	sum := TreeSummary{}
	fillSessionFields(&sum, nil, "A plain workspace tree")
	if sum.SessionID != "" || sum.Source != "" || sum.ParentSessionID != "" {
		t.Errorf("plain tree got session fields: %+v", sum)
	}

	wire, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(wire, &back); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"session_id", "parent_session_id", "source"} {
		if _, present := back[key]; present {
			t.Errorf("plain tree JSON has %q key: %s", key, wire)
		}
	}
}

func TestFillSessionFields_MalformedMetadataFailsClosed(t *testing.T) {
	sum := TreeSummary{}
	fillSessionFields(&sum, []byte(`{"session_id": `), "Imported Hermes session x · source=cli · started=t")
	if sum.SessionID != "" || sum.Source != "" {
		t.Errorf("malformed metadata leaked fields: %+v", sum)
	}
}

func TestFillSessionFields_MetadataWithoutSessionID(t *testing.T) {
	// Metadata that parses but carries no session_id is not an imported
	// tree — matches extractRelated's gate. A "source=" token in the
	// description must NOT pull a tree without a session id into the
	// session bucket.
	sum := TreeSummary{}
	fillSessionFields(&sum, []byte(`{"project": "x"}`), "source=telegram · decoy")
	if sum.SessionID != "" || sum.Source != "" {
		t.Errorf("non-session metadata leaked fields: %+v", sum)
	}
}

func TestSourceFromDescription(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "canonical import format",
			in:   "Imported Hermes session abc · model=m1 · source=webui · started=2026-08-22T10:00:00Z",
			want: "webui",
		},
		{
			name: "no source segment",
			in:   "Imported Hermes session abc · model=m1 · started=2026-08-22T10:00:00Z",
			want: "",
		},
		{
			name: "trailing source segment",
			in:   "model=m1 · source=kanban",
			want: "kanban",
		},
		{
			name: "empty value before separator",
			in:   "a=b · source= · started=x",
			want: "",
		},
		{
			name: "value with spaces survives until separator",
			in:   "x · source=my custom thing · started=y",
			want: "my custom thing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sourceFromDescription(tc.in)
			if got != tc.want {
				t.Errorf("sourceFromDescription(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// End-to-end through the public mapping entrypoint: an imported tree
// gets the fields, a plain tree does not — proving treeToSummary (and
// therefore both ListTrees paths + GetTree) expose them.
func TestTreeToSummary_SessionFields(t *testing.T) {
	id := uuid.New()
	now := time.Now()

	imported := treeToSummary(db.Tree{
		ID:          id,
		Title:       "Imported session",
		Description: "Imported Hermes session s1 · source=cli · started=z",
		Metadata:    []byte(`{"session_id":"s1","parent_session_id":"p9","source":"cron"}`),
		CreatedAt:   now,
	})
	if imported.SessionID != "s1" || imported.ParentSessionID != "p9" || imported.Source != "cron" {
		t.Errorf("imported summary fields wrong: %+v", imported)
	}

	plain := treeToSummary(db.Tree{ID: id, Title: "Manual tree", CreatedAt: now})
	if plain.SessionID != "" || plain.Source != "" || plain.ParentSessionID != "" {
		t.Errorf("plain summary leaked fields: %+v", plain)
	}
}
