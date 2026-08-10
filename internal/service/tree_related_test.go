package service

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestExtractRelated_NilOnEmptyMetadata(t *testing.T) {
	svc := &TreeServiceImpl{}
	related := svc.extractRelated(nil, nil)
	if related != nil {
		t.Errorf("extractRelated(nil) = %+v, want nil", related)
	}
}

func TestExtractRelated_NilOnNoSessionID(t *testing.T) {
	svc := &TreeServiceImpl{}
	related := svc.extractRelated(nil, []byte(`{"foo": "bar"}`))
	if related != nil {
		t.Errorf("extractRelated = %+v, want nil (no session_id)", related)
	}
}

func TestExtractRelated_ScalarFields(t *testing.T) {
	svc := &TreeServiceImpl{}
	meta := []byte(`{
		"session_id": "sess_1",
		"project": "hermes-canopy",
		"board_task": "WIRE-006",
		"commit_hash": "a1b2c3d"
	}`)
	related := svc.extractRelated(nil, meta)
	if related == nil {
		t.Fatal("related = nil, want non-nil")
	}
	if related.Project == nil || *related.Project != "hermes-canopy" {
		t.Errorf("Project = %v, want hermes-canopy", related.Project)
	}
	if related.BoardTask == nil || *related.BoardTask != "WIRE-006" {
		t.Errorf("BoardTask = %v, want WIRE-006", related.BoardTask)
	}
	if related.CommitHash == nil || *related.CommitHash != "a1b2c3d" {
		t.Errorf("CommitHash = %v, want a1b2c3d", related.CommitHash)
	}
}

func TestExtractRelated_DelegationGoals(t *testing.T) {
	svc := &TreeServiceImpl{}
	meta := []byte(`{
		"session_id": "sess_1",
		"delegation_goals": [
			{"delegation_id": "deleg_1", "goal": "Fix BUG-034"},
			{"delegation_id": "deleg_2", "goal": "Run E2E"}
		]
	}`)
	related := svc.extractRelated(nil, meta)
	if related == nil {
		t.Fatal("related = nil")
	}
	if len(related.DelegationGoals) != 2 {
		t.Fatalf("DelegationGoals = %d, want 2", len(related.DelegationGoals))
	}
	if related.DelegationGoals[0].Goal != "Fix BUG-034" {
		t.Errorf("Goals[0] = %v", related.DelegationGoals[0])
	}
}

func TestExtractRelated_NilWhenNothingFound(t *testing.T) {
	// session_id present but no other fields → Related should be nil.
	svc := &TreeServiceImpl{}
	meta := []byte(`{"session_id": "sess_1"}`)
	related := svc.extractRelated(nil, meta)
	if related != nil {
		t.Errorf("related = %+v, want nil (nothing found)", related)
	}
}

func TestExtractRelated_ParentChildrenResolved(t *testing.T) {
	// Build a TreeServiceImpl with a pool is not feasible in unit tests
	// (requires a real PG). The parent/child resolution is covered by
	// the integration test. Here we just verify that without a pool,
	// scalar fields still work.
	svc := &TreeServiceImpl{} // pool is nil
	meta := []byte(`{
		"session_id": "sess_1",
		"parent_session_id": "parent_1",
		"child_session_ids": ["child_a"],
		"project": "consensus"
	}`)
	related := svc.extractRelated(nil, meta)
	if related == nil {
		t.Fatal("related = nil")
	}
	// Without pool, parent/children can't resolve — but scalar fields survive.
	if related.Project == nil || *related.Project != "consensus" {
		t.Errorf("Project = %v, want consensus", related.Project)
	}
}

// --- UpdateTreeMetadata tests (unit-level, no DB) ---------------------------

func TestUpdateTreeMetadata_NilUUID(t *testing.T) {
	svc := &TreeServiceImpl{}
	err := svc.UpdateTreeMetadata(nil, uuid.Nil, json.RawMessage(`{}`))
	if err != ErrTreeNotFound {
		t.Errorf("UpdateTreeMetadata(NilUUID) = %v, want ErrTreeNotFound", err)
	}
}
