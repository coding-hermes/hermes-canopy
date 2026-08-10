package session

import (
	"encoding/json"
	"sort"
)

// DelegationGoal is a compact representation of one delegation's goal,
// suitable for embedding in tree metadata JSON.
type DelegationGoal struct {
	DelegationID string `json:"delegation_id"`
	Goal         string `json:"goal"`
}

// Associations holds the session-lineage metadata computed for one
// session. Every field is optional; empty values mean "no association
// found" and are omitted from the metadata JSON.
type Associations struct {
	ParentSessionID  string            `json:"parent_session_id,omitempty"`
	ChildSessionIDs  []string          `json:"child_session_ids,omitempty"`
	DelegationGoals  []DelegationGoal  `json:"delegation_goals,omitempty"`
	Project          string            `json:"project,omitempty"`
	BoardTask        string            `json:"board_task,omitempty"`
	CommitHash       string            `json:"commit_hash,omitempty"`
}

// SessionIndex is a pre-computed lookup structure that maps session IDs
// to their children and delegation goals. It is built once from the full
// session + delegation lists, then queried per-session in O(1).
type SessionIndex struct {
	// children maps a session ID to the IDs of sessions whose
	// parent_session_id == this ID.
	children map[string][]string

	// delegations maps an origin session ID to its delegation goals.
	delegations map[string][]DelegationGoal
}

// BuildSessionIndex constructs a lookup index from the full session and
// delegation lists. The session list provides the parent_session_id for
// the reverse-lookup (child→parent); the delegation list provides the
// per-origin goals.
func BuildSessionIndex(sessions []Session, delegations []Delegation) *SessionIndex {
	idx := &SessionIndex{
		children:    make(map[string][]string),
		delegations: make(map[string][]DelegationGoal),
	}

	// Build children map: for each session, record it under its parent.
	for _, s := range sessions {
		if s.ParentSessionID != "" {
			idx.children[s.ParentSessionID] = append(idx.children[s.ParentSessionID], s.ID)
		}
	}

	// Build delegations map: group by origin session.
	for _, d := range delegations {
		if d.OriginSession == "" {
			continue
		}
		idx.delegations[d.OriginSession] = append(idx.delegations[d.OriginSession], DelegationGoal{
			DelegationID: d.DelegationID,
			Goal:         d.TaskGoal,
		})
	}

	return idx
}

// ComputeAssociations computes the full association set for one session.
// It uses the SessionIndex for children and delegation lookups, the
// session's own ParentSessionID for the parent, and ParseTitle for
// project/board_task/commit_hash extraction.
//
// This function is best-effort — it never returns an error. Missing
// associations simply produce empty fields in the result.
func ComputeAssociations(s Session, idx *SessionIndex) Associations {
	var assoc Associations

	// Parent — from the session row itself.
	assoc.ParentSessionID = s.ParentSessionID

	// Children — reverse lookup from the index.
	if idx != nil {
		if children, ok := idx.children[s.ID]; ok {
			// Sort for deterministic output.
			sorted := make([]string, len(children))
			copy(sorted, children)
			sort.Strings(sorted)
			assoc.ChildSessionIDs = sorted
		}

		// Delegation goals — from the index.
		if goals, ok := idx.delegations[s.ID]; ok {
			assoc.DelegationGoals = goals
		}
	}

	// Title parsing — project, board task, commit hash.
	info := ParseTitle(s.Title)
	assoc.Project = info.Project
	assoc.BoardTask = info.BoardTask
	assoc.CommitHash = info.CommitHash

	return assoc
}

// TreeMetadata combines the base session_id with associations into a
// single JSON map suitable for trees.metadata. The "session_id" key is
// ALWAYS present (ImportedBefore depends on it). Association keys are
// only included when they have meaningful values.
type TreeMetadata struct {
	SessionID       string            `json:"session_id"`
	ParentSessionID string            `json:"parent_session_id,omitempty"`
	ChildSessionIDs []string          `json:"child_session_ids,omitempty"`
	DelegationGoals []DelegationGoal  `json:"delegation_goals,omitempty"`
	Project         string            `json:"project,omitempty"`
	BoardTask       string            `json:"board_task,omitempty"`
	CommitHash      string            `json:"commit_hash,omitempty"`
}

// NewTreeMetadata builds a TreeMetadata from a session ID and its
// computed associations. The session_id is always set; association
// fields are carried over (omitempty handles the empty cases).
func NewTreeMetadata(sessionID string, assoc Associations) TreeMetadata {
	return TreeMetadata{
		SessionID:       sessionID,
		ParentSessionID: assoc.ParentSessionID,
		ChildSessionIDs: assoc.ChildSessionIDs,
		DelegationGoals: assoc.DelegationGoals,
		Project:         assoc.Project,
		BoardTask:       assoc.BoardTask,
		CommitHash:      assoc.CommitHash,
	}
}

// Marshal serializes TreeMetadata to JSON for the trees.metadata column.
func (m TreeMetadata) Marshal() (json.RawMessage, error) {
	return json.Marshal(m)
}

// ParseTreeMetadata deserializes tree metadata JSON into TreeMetadata.
// It is tolerant of the legacy format (just {"session_id": "..."}) and
// the new format with associations. Unknown keys are ignored.
func ParseTreeMetadata(raw json.RawMessage) (TreeMetadata, error) {
	var m TreeMetadata
	if len(raw) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	return m, nil
}
