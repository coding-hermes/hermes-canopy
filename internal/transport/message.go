package transport

import (
	"encoding/json"
	"time"
)

// --- Message (SPEC-FTR-04 §3.2) --------------------------------------------

// Message is the universal sync message across all transports.
type Message struct {
	Opcode    Opcode          `json:"op" cbor:"0"`
	TreeID    string          `json:"tree" cbor:"1"`
	Sequence  uint64          `json:"seq" cbor:"2"`
	Timestamp int64           `json:"ts" cbor:"3"`
	Payload   json.RawMessage `json:"data" cbor:"4"`
	Origin    string          `json:"origin" cbor:"5"`
}

// --- Opcode (SPEC-FTR-04 §3.2) ---------------------------------------------

// Opcode enumerates all tree sync operations.
type Opcode uint8

const (
	OpTreeCreate     Opcode = 0x01
	OpNodeAdd        Opcode = 0x02
	OpNodeUpdate     Opcode = 0x03
	OpNodeDelete     Opcode = 0x04
	OpEdgeAdd        Opcode = 0x05
	OpEdgeRemove     Opcode = 0x06
	OpApprovalChange Opcode = 0x07
	OpUserJoin       Opcode = 0x08
	OpUserLeave      Opcode = 0x09
	OpTreeSnapshot   Opcode = 0x0A
	OpTreeDelta      Opcode = 0x0B
	OpHeartbeat      Opcode = 0x0C
	OpAck            Opcode = 0x0D
)

// String returns the human-readable opcode name.
func (o Opcode) String() string {
	switch o {
	case OpTreeCreate:
		return "tree_create"
	case OpNodeAdd:
		return "node_add"
	case OpNodeUpdate:
		return "node_update"
	case OpNodeDelete:
		return "node_delete"
	case OpEdgeAdd:
		return "edge_add"
	case OpEdgeRemove:
		return "edge_remove"
	case OpApprovalChange:
		return "approval_change"
	case OpUserJoin:
		return "user_join"
	case OpUserLeave:
		return "user_leave"
	case OpTreeSnapshot:
		return "tree_snapshot"
	case OpTreeDelta:
		return "tree_delta"
	case OpHeartbeat:
		return "heartbeat"
	case OpAck:
		return "ack"
	default:
		return "unknown"
	}
}

// --- Per-opcode payload types (SPEC-FTR-04 §3.2 / T1.8 §4.2) ----------------

// TreeCreatePayload is sent when a new tree is created.
type TreeCreatePayload struct {
	OwnerID     string    `json:"owner_id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// NodeAddPayload carries a new node added to a tree.
type NodeAddPayload struct {
	NodeID        string    `json:"node_id"`
	ParentID      string    `json:"parent_id,omitempty"`
	AuthorID      string    `json:"author_id"`
	Content       string    `json:"content"`
	ContentFormat string    `json:"content_format"`
	NodeType      string    `json:"node_type"`
	SequenceNum   int64     `json:"sequence_num"`
	CreatedAt     time.Time `json:"created_at"`
}

// NodeUpdatePayload carries a mutation to an existing node's content.
type NodeUpdatePayload struct {
	NodeID        string    `json:"node_id"`
	Content       string    `json:"content"`
	ContentFormat string    `json:"content_format,omitempty"`
	EditedAt      time.Time `json:"edited_at"`
}

// NodeDeletePayload signals removal of a node (soft-delete).
type NodeDeletePayload struct {
	NodeID    string    `json:"node_id"`
	DeletedAt time.Time `json:"deleted_at"`
}

// EdgeAddPayload carries a new edge between two nodes.
type EdgeAddPayload struct {
	EdgeID   string `json:"edge_id"`
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	EdgeType string `json:"edge_type"`
}

// EdgeRemovePayload signals removal of an edge.
type EdgeRemovePayload struct {
	EdgeID string `json:"edge_id"`
}

// ApprovalChangePayload carries an approval state transition.
type ApprovalChangePayload struct {
	ApprovalID string `json:"approval_id"`
	NodeID     string `json:"node_id"`
	Status     string `json:"status"`
	DecidedBy  string `json:"decided_by,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// UserJoinPayload signals a user or profile joining a tree session.
type UserJoinPayload struct {
	UserID    string `json:"user_id"`
	ProfileID string `json:"profile_id,omitempty"`
	Role      string `json:"role,omitempty"`
}

// UserLeavePayload signals a user or profile leaving a tree session.
type UserLeavePayload struct {
	UserID    string `json:"user_id"`
	ProfileID string `json:"profile_id,omitempty"`
}

// TreeSnapshotPayload carries a full-tree snapshot for initial sync or
// recovery. SnapshotData is opaque (hash-verified, MLS-encrypted in
// production).
type TreeSnapshotPayload struct {
	SnapshotID   string          `json:"snapshot_id"`
	Hash         string          `json:"hash"`
	NodeCount    int             `json:"node_count"`
	EdgeCount    int             `json:"edge_count"`
	SnapshotData json.RawMessage `json:"snapshot_data"`
}

// TreeDeltaPayload carries an incremental delta for resynchronisation.
type TreeDeltaPayload struct {
	FromHash     string          `json:"from_hash"`
	ToHash       string          `json:"to_hash"`
	AddedNodes   []string        `json:"added_nodes,omitempty"`
	RemovedNodes []string        `json:"removed_nodes,omitempty"`
	AddedEdges   []string        `json:"added_edges,omitempty"`
	RemovedEdges []string        `json:"removed_edges,omitempty"`
	DeltaData    json.RawMessage `json:"delta_data"`
}

// HeartbeatPayload is the keepalive message exchanged on idle connections.
type HeartbeatPayload struct {
	PeerID    string `json:"peer_id"`
	Timestamp int64  `json:"timestamp"`
}

// AckPayload acknowledges receipt of messages up to the given sequence.
type AckPayload struct {
	AckSequence uint64 `json:"ack_sequence"`
}
