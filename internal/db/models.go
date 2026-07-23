// Package db provides the PostgreSQL data layer for Canopy: types,
// repository interfaces, and pgxpool connection management.
//
// Migrations live under ../../migrations and are applied by Migrate (see
// db.go) using golang-migrate's iofs source. Down migrations are paired
// with each up migration but are NOT exposed via this package — rewind
// is performed exclusively by the `make migrate-down` target, which uses
// the migrate CLI directly against the DSN.
package db

import (
	"time"

	"github.com/google/uuid"
)

// ContentFormat enumerates the supported node content formats. Mirrors
// the CHECK constraint defined in 000003_nodes.up.sql.
const (
	ContentFormatMarkdown = "markdown"
	ContentFormatPlain    = "plain"
	ContentFormatRich     = "rich"
)

// NodeType enumerates the kinds of nodes a conversation DAG can hold.
const (
	NodeTypeMessage   = "message"
	NodeTypeSynthesis = "synthesis"
	NodeTypeSystem    = "system"
)

// EdgeType enumerates the kinds of directed edges between nodes.
const (
	EdgeTypeReply     = "reply"
	EdgeTypeFork      = "fork"
	EdgeTypeSynthesis = "synthesis"
	EdgeTypeReference = "reference"
)

// Node represents a single message in a conversation tree. Maps to the
// nodes table. JSON tags match the wire format used by SPEC-API-03.
type Node struct {
	ID            uuid.UUID  `db:"id"             json:"id"`
	TreeID        uuid.UUID  `db:"tree_id"        json:"treeId"`
	ParentID      *uuid.UUID `db:"parent_id"      json:"parentId"`
	AuthorID      uuid.UUID  `db:"author_id"      json:"authorId"`
	Content       string     `db:"content"        json:"content"`
	ContentFormat string     `db:"content_format" json:"contentFormat"`
	NodeType      string     `db:"node_type"      json:"nodeType"`
	SequenceNum   int64      `db:"sequence_num"   json:"sequenceNum"`
	Metadata      []byte     `db:"metadata"       json:"metadata"`
	CreatedAt     time.Time  `db:"created_at"     json:"createdAt"`
	EditedAt      *time.Time `db:"edited_at"      json:"editedAt"`
	DeletedAt     *time.Time `db:"deleted_at"     json:"deletedAt"`
}

// Edge represents a typed directed edge between two nodes. Maps to the
// edges table.
type Edge struct {
	ID          uuid.UUID  `db:"id"           json:"id"`
	TreeID      uuid.UUID  `db:"tree_id"      json:"treeId"`
	SourceID    uuid.UUID  `db:"source_id"    json:"sourceId"`
	TargetID    uuid.UUID  `db:"target_id"    json:"targetId"`
	EdgeType    string     `db:"edge_type"    json:"edgeType"`
	SequenceNum int64      `db:"sequence_num" json:"sequenceNum"`
	Metadata    []byte     `db:"metadata"     json:"metadata"`
	CreatedAt   time.Time  `db:"created_at"   json:"createdAt"`
	DeletedAt   *time.Time `db:"deleted_at"   json:"deletedAt"`
}

// Tree represents a conversation tree container. Maps to the trees
// table.
type Tree struct {
	ID          uuid.UUID  `db:"id"            json:"id"`
	OwnerID     uuid.UUID  `db:"owner_id"      json:"ownerId"`
	Title       string     `db:"title"         json:"title"`
	Description string     `db:"description"   json:"description"`
	RootNodeID  *uuid.UUID `db:"root_node_id"  json:"rootNodeId"`
	Metadata    []byte     `db:"metadata"      json:"metadata"`
	CreatedAt   time.Time  `db:"created_at"    json:"createdAt"`
	EditedAt    *time.Time `db:"edited_at"     json:"editedAt"`
	DeletedAt   *time.Time `db:"deleted_at"    json:"deletedAt"`
}

// TreeSnapshot represents a point-in-time hash-verified state of a tree.
// Maps to the tree_snapshots table (migration 000005) and is defined in
// SPEC-DM-02 §4.2.
type TreeSnapshot struct {
	ID           uuid.UUID `db:"id"            json:"id"`
	TreeID       uuid.UUID `db:"tree_id"       json:"treeId"`
	ParentHash   *string   `db:"parent_hash"   json:"parentHash"`
	Hash         string    `db:"hash"          json:"hash"`
	NodeCount    int       `db:"node_count"    json:"nodeCount"`
	EdgeCount    int       `db:"edge_count"    json:"edgeCount"`
	SnapshotData []byte    `db:"snapshot_data" json:"snapshotData"`
	CreatedAt    time.Time `db:"created_at"    json:"createdAt"`
}

// TreeEvent represents a single change event in a tree.
// Maps to the tree_events table (migration 000007) and is defined in
// SPEC-DM-02 §4.2.
type TreeEvent struct {
	ID          uuid.UUID  `db:"id"            json:"id"`
	TreeID      uuid.UUID  `db:"tree_id"       json:"treeId"`
	SnapshotID  *uuid.UUID `db:"snapshot_id"   json:"snapshotId"`
	EventType   string     `db:"event_type"    json:"eventType"`
	NodeID      *uuid.UUID `db:"node_id"       json:"nodeId"`
	EdgeID      *uuid.UUID `db:"edge_id"       json:"edgeId"`
	Payload     []byte     `db:"payload"       json:"payload"`
	SequenceNum int64      `db:"sequence_num"  json:"sequenceNum"`
	CreatedAt   time.Time  `db:"created_at"    json:"createdAt"`
}

// NodeCounts provides aggregate counts for a tree, returned by
// NodeRepo.GetCounts. All counts are pure SQL aggregates; nothing
// here is application-derived.
type NodeCounts struct {
	TreeID      uuid.UUID `json:"treeId"`
	TotalNodes  int64     `json:"totalNodes"`
	ActiveNodes int64     `json:"activeNodes"`
	TotalEdges  int64     `json:"totalEdges"`
	ActiveEdges int64     `json:"activeEdges"`
	MaxDepth    int       `json:"maxDepth"`
}
