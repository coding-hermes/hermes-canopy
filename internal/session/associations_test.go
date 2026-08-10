package session

import (
	"encoding/json"
	"testing"
)

func TestBuildSessionIndex_Children(t *testing.T) {
	sessions := []Session{
		{ID: "parent", Source: "cli", Title: "Parent"},
		{ID: "child_a", Source: "subagent", Title: "A", ParentSessionID: "parent"},
		{ID: "child_b", Source: "subagent", Title: "B", ParentSessionID: "parent"},
		{ID: "orphan", Source: "cli", Title: "Orphan"},
	}
	idx := BuildSessionIndex(sessions, nil)

	if children, ok := idx.children["parent"]; !ok || len(children) != 2 {
		t.Fatalf("idx.children[parent] = %v (ok=%v), want 2 children", children, ok)
	}
	if _, ok := idx.children["orphan"]; ok {
		t.Error("idx.children[orphan] should not exist")
	}
}

func TestBuildSessionIndex_Delegations(t *testing.T) {
	delegations := []Delegation{
		{DelegationID: "d1", OriginSession: "sess_a", State: "completed", TaskGoal: "Fix BUG-034"},
		{DelegationID: "d2", OriginSession: "sess_a", State: "error", TaskGoal: "Run E2E"},
		{DelegationID: "d3", OriginSession: "sess_b", State: "completed", TaskGoal: "Deploy"},
	}
	idx := BuildSessionIndex(nil, delegations)

	goalsA, ok := idx.delegations["sess_a"]
	if !ok || len(goalsA) != 2 {
		t.Fatalf("delegations[sess_a] = %v (ok=%v), want 2 goals", goalsA, ok)
	}
	if goalsA[0].Goal != "Fix BUG-034" {
		t.Errorf("goalsA[0].Goal = %q", goalsA[0].Goal)
	}
}

func TestComputeAssociations_Full(t *testing.T) {
	sessions := []Session{
		{ID: "parent", Source: "cli", Title: "Scheduler tick"},
		{ID: "child", Source: "subagent", Title: "hermes-canopy-duckbrain-sync · Aug 09", ParentSessionID: "parent"},
	}
	delegations := []Delegation{
		{DelegationID: "deleg_1", OriginSession: "parent", State: "completed", TaskGoal: "Run E2E suite"},
	}
	idx := BuildSessionIndex(sessions, delegations)

	// Parent session: has children + delegation goals.
	assoc := ComputeAssociations(sessions[0], idx)
	if assoc.ChildSessionIDs == nil || len(assoc.ChildSessionIDs) != 1 {
		t.Fatalf("parent ChildSessionIDs = %v, want [child]", assoc.ChildSessionIDs)
	}
	if assoc.ChildSessionIDs[0] != "child" {
		t.Errorf("ChildSessionIDs[0] = %q, want child", assoc.ChildSessionIDs[0])
	}
	if len(assoc.DelegationGoals) != 1 {
		t.Fatalf("parent DelegationGoals = %v, want 1", assoc.DelegationGoals)
	}
	if assoc.DelegationGoals[0].DelegationID != "deleg_1" {
		t.Errorf("DelegationGoals[0].DelegationID = %q", assoc.DelegationGoals[0].DelegationID)
	}
}

func TestComputeAssociations_Child(t *testing.T) {
	sessions := []Session{
		{ID: "parent", Source: "cli", Title: "Scheduler tick"},
		{ID: "child", Source: "subagent", Title: "hermes-canopy: Fix WIRE-006 in a1b2c3d", ParentSessionID: "parent"},
	}
	idx := BuildSessionIndex(sessions, nil)

	// Child session: has parent, no children, title-parsed fields.
	assoc := ComputeAssociations(sessions[1], idx)
	if assoc.ParentSessionID != "parent" {
		t.Errorf("child ParentSessionID = %q, want parent", assoc.ParentSessionID)
	}
	if len(assoc.ChildSessionIDs) != 0 {
		t.Errorf("child ChildSessionIDs = %v, want empty", assoc.ChildSessionIDs)
	}
	if assoc.Project != "hermes-canopy" {
		t.Errorf("child Project = %q, want hermes-canopy", assoc.Project)
	}
	if assoc.BoardTask != "WIRE-006" {
		t.Errorf("child BoardTask = %q, want WIRE-006", assoc.BoardTask)
	}
	if assoc.CommitHash != "a1b2c3d" {
		t.Errorf("child CommitHash = %q, want a1b2c3d", assoc.CommitHash)
	}
}

func TestComputeAssociations_NoAssociations(t *testing.T) {
	s := Session{ID: "loner", Source: "cli", Title: "Documenting things"}
	idx := BuildSessionIndex(nil, nil)

	assoc := ComputeAssociations(s, idx)
	if assoc.ParentSessionID != "" {
		t.Errorf("ParentSessionID = %q, want empty", assoc.ParentSessionID)
	}
	if assoc.ChildSessionIDs != nil {
		t.Errorf("ChildSessionIDs = %v, want nil", assoc.ChildSessionIDs)
	}
	if assoc.DelegationGoals != nil {
		t.Errorf("DelegationGoals = %v, want nil", assoc.DelegationGoals)
	}
}

func TestNewTreeMetadata_MarshalRoundTrip(t *testing.T) {
	assoc := Associations{
		ParentSessionID: "parent_1",
		ChildSessionIDs: []string{"child_a", "child_b"},
		DelegationGoals: []DelegationGoal{
			{DelegationID: "deleg_1", Goal: "Fix BUG-034"},
		},
		Project:    "hermes-canopy",
		BoardTask:  "BUG-034",
		CommitHash: "a1b2c3d",
	}
	meta := NewTreeMetadata("sess_42", assoc)

	raw, err := meta.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// session_id is always present.
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["session_id"] != "sess_42" {
		t.Errorf("session_id = %v, want sess_42", m["session_id"])
	}
	if m["parent_session_id"] != "parent_1" {
		t.Errorf("parent_session_id = %v", m["parent_session_id"])
	}
	if m["project"] != "hermes-canopy" {
		t.Errorf("project = %v", m["project"])
	}

	// Round-trip: parse back.
	parsed, err := ParseTreeMetadata(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.SessionID != "sess_42" {
		t.Errorf("parsed.SessionID = %q", parsed.SessionID)
	}
	if parsed.ParentSessionID != "parent_1" {
		t.Errorf("parsed.ParentSessionID = %q", parsed.ParentSessionID)
	}
}

func TestNewTreeMetadata_LegacyCompat(t *testing.T) {
	// Legacy metadata: just {"session_id": "old"}.
	legacy := json.RawMessage(`{"session_id": "old_sess"}`)
	parsed, err := ParseTreeMetadata(legacy)
	if err != nil {
		t.Fatalf("parse legacy: %v", err)
	}
	if parsed.SessionID != "old_sess" {
		t.Errorf("parsed.SessionID = %q, want old_sess", parsed.SessionID)
	}
	if parsed.ParentSessionID != "" || parsed.Project != "" {
		t.Errorf("legacy fields should be empty: %+v", parsed)
	}
}

func TestNewTreeMetadata_OmitEmpty(t *testing.T) {
	// Empty associations: only session_id should survive.
	meta := NewTreeMetadata("sess_only", Associations{})
	raw, err := meta.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(m) != 1 {
		t.Errorf("metadata has %d keys, want 1 (session_id only): %+v", len(m), m)
	}
	if m["session_id"] != "sess_only" {
		t.Errorf("session_id = %v", m["session_id"])
	}
}
