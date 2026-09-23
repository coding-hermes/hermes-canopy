// Package service provides the business logic for Canopy import/export.
// ExportService serialises a conversation tree (with all nodes and edges)
// to a portable JSON format and imports trees from that same format.
//
// The wire format mirrors the db.Node/db.Edge types so downstream
// consumers can round-trip without loss. Import assigns new UUIDs to
// every entity and remaps edge references accordingly.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/reference"
)

// --- Error sentinels -------------------------------------------------------

var (
	ErrExportNotFound          = errors.New("export service: tree not found")
	ErrExportInvalidJSON       = errors.New("export service: invalid import payload")
	ErrExportMissingTree       = errors.New("export service: import payload missing tree")
	ErrExportMissingRootNode   = errors.New("export service: import payload missing root node")
	ErrExportInvalidRootNode   = errors.New("export service: import payload root node not in nodes list")
	ErrExportEdgeNodeNotFound  = errors.New("export service: edge references node not in import payload")
	ErrExportTopicNodeNotFound = errors.New("export service: topic references node not in import payload")
	ErrExportTopicNotFound     = errors.New("export service: resolved reference topic not in import payload")
	ErrExportRefNodeNotFound   = errors.New("export service: resolved reference node not in import payload")
)

// --- Wire types ------------------------------------------------------------

// ExportData is the top-level serialisation envelope for a conversation tree.
// It holds the tree metadata, all nodes, edges, topics, and resolved
// references so a complete conversation DAG can be reconstructed from a
// single JSON document. Version 2 adds topics and resolved_refs.
type ExportData struct {
	Tree         ExportTree              `json:"tree"`
	Nodes        []db.Node               `json:"nodes"`
	Edges        []db.Edge               `json:"edges"`
	Topics       []TopicWire             `json:"topics,omitempty"`
	ResolvedRefs []ResolvedReferenceWire `json:"resolved_refs,omitempty"`
	Version      int                     `json:"version"`
	ExportedAt   time.Time               `json:"exportedAt"`
}

// TopicWire is the snake_case export representation of a topic. Deleted
// topics are not returned by TopicRepo.GetByTree; active and archived topics
// are both included.
type TopicWire struct {
	ID            uuid.UUID  `json:"id"`
	TreeID        uuid.UUID  `json:"tree_id"`
	RootNodeID    uuid.UUID  `json:"root_node_id"`
	Title         string     `json:"title"`
	Description   string     `json:"description"`
	Slug          string     `json:"slug"`
	ParentTopicID *uuid.UUID `json:"parent_topic_id,omitempty"`
	Status        string     `json:"status"`
	TopicTags     []string   `json:"topic_tags"`
	NodeCount     int32      `json:"node_count"`
	CreatedAt     time.Time  `json:"created_at"`
	ArchivedAt    *time.Time `json:"archived_at,omitempty"`
}

// ResolvedReferenceWire is the snake_case export representation of one
// node_resolved_refs row. NodeID and TopicID are remapped during import.
type ResolvedReferenceWire struct {
	ID          uuid.UUID `json:"id"`
	NodeID      uuid.UUID `json:"node_id"`
	TreeID      uuid.UUID `json:"tree_id"`
	TopicID     uuid.UUID `json:"topic_id"`
	RawRef      string    `json:"raw_ref"`
	Slug        string    `json:"slug"`
	ResolvedAt  time.Time `json:"resolved_at"`
	ResolvedBy  uuid.UUID `json:"resolved_by"`
	ContextHash string    `json:"context_hash"`
}

// ExportTree carries the tree-level metadata that is included in the export.
type ExportTree struct {
	Title       string    `json:"title"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	RootNodeID  uuid.UUID `json:"rootNodeId"`
}

// ExportResult is returned by ImportTree so callers can locate the
// newly-created tree.
type ExportResult struct {
	TreeID           uuid.UUID `json:"treeId"`
	RootNodeID       uuid.UUID `json:"rootNodeId"`
	NodeCount        int       `json:"nodeCount"`
	EdgeCount        int       `json:"edgeCount"`
	TopicCount       int       `json:"topicCount"`
	ResolvedRefCount int       `json:"resolvedRefCount"`
}

// --- Service interface + implementation ------------------------------------

// ExportService defines the import/export contract.
type ExportService interface {
	ExportTree(ctx context.Context, treeID uuid.UUID) (*ExportData, error)
	ImportTree(ctx context.Context, input *ExportData, ownerID uuid.UUID) (*ExportResult, error)
}

// ExportServiceImpl is the pgx-backed implementation of ExportService.
type ExportServiceImpl struct {
	treeRepo      db.TreeRepo
	nodeRepo      db.NodeRepo
	edgeRepo      db.EdgeRepo
	topicRepo     db.TopicRepo
	referenceRepo reference.ReferenceRepo
	pool          *pgxpool.Pool
	now           func() time.Time
}

// NewExportService wires the repositories + pool into an ExportServiceImpl.
func NewExportService(treeRepo db.TreeRepo, nodeRepo db.NodeRepo, edgeRepo db.EdgeRepo, pool *pgxpool.Pool) *ExportServiceImpl {
	return &ExportServiceImpl{
		treeRepo: treeRepo,
		nodeRepo: nodeRepo,
		edgeRepo: edgeRepo,
		pool:     pool,
		now:      time.Now,
	}
}

// WithTopicReferences wires the topic and resolved-reference repositories.
// It is separate from the legacy constructor so existing callers that do not
// need the additive export fields remain source-compatible.
func (s *ExportServiceImpl) WithTopicReferences(topicRepo db.TopicRepo, referenceRepo reference.ReferenceRepo) *ExportServiceImpl {
	s.topicRepo = topicRepo
	s.referenceRepo = referenceRepo
	return s
}

// --- ExportTree -------------------------------------------------------------

// ExportTree fetches the tree metadata, all active nodes, and all active
// edges for the given tree and packs them into an ExportData envelope.
// A tree that exists but has no nodes returns an export with empty
// slices (still valid — the importer handles this gracefully).
func (s *ExportServiceImpl) ExportTree(ctx context.Context, treeID uuid.UUID) (*ExportData, error) {
	t, err := s.treeRepo.GetByID(ctx, treeID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, ErrExportNotFound
		}
		return nil, fmt.Errorf("%w: %v", ErrDatabaseUnavailable, err)
	}

	nodes, err := s.nodeRepo.GetByTree(ctx, treeID)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch nodes: %v", ErrDatabaseUnavailable, err)
	}

	edges, err := s.edgeRepo.GetByTree(ctx, treeID)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch edges: %v", ErrDatabaseUnavailable, err)
	}

	var topics []TopicWire
	if s.topicRepo != nil {
		rows, topicErr := s.topicRepo.GetByTree(ctx, treeID, "")
		if topicErr != nil {
			return nil, fmt.Errorf("%w: fetch topics: %v", ErrDatabaseUnavailable, topicErr)
		}
		topics = make([]TopicWire, 0, len(rows))
		for _, topic := range rows {
			topics = append(topics, topicToWire(topic))
		}
	}

	var resolvedRefs []ResolvedReferenceWire
	if s.referenceRepo != nil && len(nodes) > 0 {
		nodeIDs := make([]uuid.UUID, 0, len(nodes))
		for _, node := range nodes {
			nodeIDs = append(nodeIDs, node.ID)
		}
		rows, refErr := s.referenceRepo.GetResolvedRefsForTree(ctx, treeID, nodeIDs)
		if refErr != nil {
			return nil, fmt.Errorf("%w: fetch resolved references: %v", ErrDatabaseUnavailable, refErr)
		}
		resolvedRefs = make([]ResolvedReferenceWire, 0, len(rows))
		for _, link := range rows {
			resolvedRefs = append(resolvedRefs, resolvedRefToWire(link))
		}
	}

	rootNodeID := uuid.Nil
	if t.RootNodeID != nil {
		rootNodeID = *t.RootNodeID
	}

	return &ExportData{
		Tree: ExportTree{
			Title:       t.Title,
			Description: t.Description,
			CreatedAt:   t.CreatedAt,
			RootNodeID:  rootNodeID,
		},
		Nodes:        nodes,
		Edges:        edges,
		Topics:       topics,
		ResolvedRefs: resolvedRefs,
		Version:      2,
		ExportedAt:   s.now().UTC(),
	}, nil
}

// --- ImportTree -------------------------------------------------------------

// ImportTree creates a new tree (with a new owner) from the export
// payload. All entities receive fresh UUIDs; a mapping table is
// maintained so edge source/target references are correctly remapped.
// The entire import runs inside a single database transaction.
func (s *ExportServiceImpl) ImportTree(ctx context.Context, input *ExportData, ownerID uuid.UUID) (*ExportResult, error) {
	if input == nil {
		return nil, ErrExportInvalidJSON
	}
	if input.Tree.Title == "" {
		return nil, ErrExportMissingTree
	}
	if len(input.Nodes) == 0 {
		return nil, ErrExportMissingRootNode
	}

	// Build a set of node IDs present in the payload so we can validate
	// edges before doing any database work.
	nodeIDSet := make(map[uuid.UUID]bool, len(input.Nodes))
	for _, n := range input.Nodes {
		nodeIDSet[n.ID] = true
	}

	// Verify root node exists in the nodes list.
	if !nodeIDSet[input.Tree.RootNodeID] {
		return nil, ErrExportInvalidRootNode
	}

	// Validate all edge references.
	for _, e := range input.Edges {
		if !nodeIDSet[e.SourceID] || !nodeIDSet[e.TargetID] {
			return nil, ErrExportEdgeNodeNotFound
		}
	}

	// Validate topic roots/parents and resolved-reference node/topic IDs when
	// present. Older version-1 payloads leave these additive slices empty.
	topicIDSet := make(map[uuid.UUID]bool, len(input.Topics))
	for _, topic := range input.Topics {
		if !nodeIDSet[topic.RootNodeID] {
			return nil, ErrExportTopicNodeNotFound
		}
		topicIDSet[topic.ID] = true
	}
	for _, topic := range input.Topics {
		if topic.ParentTopicID != nil && !topicIDSet[*topic.ParentTopicID] {
			return nil, ErrExportTopicNotFound
		}
	}
	for _, ref := range input.ResolvedRefs {
		if !nodeIDSet[ref.NodeID] {
			return nil, ErrExportRefNodeNotFound
		}
		if !topicIDSet[ref.TopicID] {
			return nil, ErrExportTopicNotFound
		}
	}

	if s.pool == nil {
		return nil, ErrDatabaseUnavailable
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: begin tx: %v", ErrDatabaseUnavailable, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Create the tree row.
	newTreeID := uuid.New()
	now := s.now()

	_, err = tx.Exec(ctx,
		`INSERT INTO trees (id, owner_id, title, description, root_node_id)
		 VALUES ($1, $2, $3, $4, NULL)`,
		newTreeID, ownerID, input.Tree.Title, input.Tree.Description,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: insert tree: %v", ErrDatabaseUnavailable, err)
	}

	// 2. Create tree_members row (owner).
	if _, err := tx.Exec(ctx,
		`INSERT INTO tree_members (tree_id, user_id, role, joined_at)
		 VALUES ($1, $2, $3, $4)`,
		newTreeID, ownerID, "owner", now,
	); err != nil && !isUndefinedTable(err) {
		return nil, fmt.Errorf("%w: insert tree_members: %v", ErrDatabaseUnavailable, err)
	}

	// 3. Remap node IDs: old-id → new-id.
	idMap := make(map[uuid.UUID]uuid.UUID, len(input.Nodes))
	for _, n := range input.Nodes {
		idMap[n.ID] = uuid.New()
	}

	// 4. Insert all nodes.
	for _, n := range input.Nodes {
		newID := idMap[n.ID]
		var parentID *uuid.UUID
		if n.ParentID != nil {
			if mapped, ok := idMap[*n.ParentID]; ok {
				parentID = &mapped
			}
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO nodes (id, tree_id, parent_id, author_id, content,
			 content_format, node_type, metadata)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, COALESCE($8, '{}'::jsonb))`,
			newID, newTreeID, parentID, n.AuthorID, n.Content,
			n.ContentFormat, n.NodeType, n.Metadata,
		)
		if err != nil {
			return nil, fmt.Errorf("%w: insert node: %v", ErrDatabaseUnavailable, err)
		}
	}

	// 5. Backfill root_node_id.
	newRootNodeID := idMap[input.Tree.RootNodeID]
	if _, err := tx.Exec(ctx,
		`UPDATE trees SET root_node_id = $2, edited_at = $3 WHERE id = $1`,
		newTreeID, newRootNodeID, now,
	); err != nil {
		return nil, fmt.Errorf("%w: set root_node_id: %v", ErrDatabaseUnavailable, err)
	}

	// 6. Insert all edges with remapped source/target.
	for i, e := range input.Edges {
		newSourceID := idMap[e.SourceID]
		newTargetID := idMap[e.TargetID]
		sequence := e.SequenceNum
		if sequence == 0 {
			// Older payloads may omit sequence_num; keep those imports
			// deterministic while preserving exported values when present.
			sequence = int64(i + 1)
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO edges (tree_id, source_id, target_id, edge_type, sequence_num, metadata)
			 VALUES ($1, $2, $3, $4, $5, COALESCE($6, '{}'::jsonb))`,
			newTreeID, newSourceID, newTargetID, e.EdgeType, sequence, e.Metadata,
		)
		if err != nil {
			return nil, fmt.Errorf("%w: insert edge: %v", ErrDatabaseUnavailable, err)
		}
	}

	// 7. Insert topics in two passes so parent_topic_id ordering in the
	// payload does not matter. IDs and root node IDs are remapped.
	topicIDMap := make(map[uuid.UUID]uuid.UUID, len(input.Topics))
	for _, topic := range input.Topics {
		topicIDMap[topic.ID] = uuid.New()
		_, err := tx.Exec(ctx,
			`INSERT INTO topics (id, tree_id, root_node_id, title, description,
			 slug, parent_topic_id, status, topic_tags, node_count, created_at, archived_at)
			 VALUES ($1, $2, $3, $4, $5, $6, NULL, $7, $8, $9, $10, $11)`,
			topicIDMap[topic.ID], newTreeID, idMap[topic.RootNodeID], topic.Title,
			topic.Description, topic.Slug, topic.Status, topic.TopicTags,
			topic.NodeCount, topic.CreatedAt, topic.ArchivedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("%w: insert topic: %v", ErrDatabaseUnavailable, err)
		}
	}
	for _, topic := range input.Topics {
		if topic.ParentTopicID == nil {
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE topics SET parent_topic_id = $2 WHERE id = $1`,
			topicIDMap[topic.ID], topicIDMap[*topic.ParentTopicID]); err != nil {
			return nil, fmt.Errorf("%w: set topic parent: %v", ErrDatabaseUnavailable, err)
		}
	}

	// 8. Insert resolved references in one transaction. Prefer the original
	// resolved_by profile when it exists; imports performed by another user
	// fall back to that user's newest profile.
	for _, ref := range input.ResolvedRefs {
		_, err := tx.Exec(ctx,
			`INSERT INTO node_resolved_refs
				(node_id, tree_id, topic_id, raw_ref, slug, resolved_at, resolved_by, context_hash)
			 VALUES ($1, $2, $3, $4, $5, $6,
				COALESCE((SELECT id FROM profiles WHERE id = $7),
				         (SELECT id FROM profiles WHERE owner_id = $8 ORDER BY created_at DESC LIMIT 1)),
				$9)`,
			idMap[ref.NodeID], newTreeID, topicIDMap[ref.TopicID], ref.RawRef,
			ref.Slug, ref.ResolvedAt, ref.ResolvedBy, ownerID, ref.ContextHash,
		)
		if err != nil {
			return nil, fmt.Errorf("%w: insert resolved reference: %v", ErrDatabaseUnavailable, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit: %v", ErrDatabaseUnavailable, err)
	}

	return &ExportResult{
		TreeID:           newTreeID,
		RootNodeID:       newRootNodeID,
		NodeCount:        len(input.Nodes),
		EdgeCount:        len(input.Edges),
		TopicCount:       len(input.Topics),
		ResolvedRefCount: len(input.ResolvedRefs),
	}, nil
}

func topicToWire(topic db.Topic) TopicWire {
	return TopicWire{
		ID: topic.ID, TreeID: topic.TreeID, RootNodeID: topic.RootNodeID,
		Title: topic.Title, Description: topic.Description, Slug: topic.Slug,
		ParentTopicID: topic.ParentTopicID, Status: topic.Status,
		TopicTags: topic.TopicTags, NodeCount: topic.NodeCount,
		CreatedAt: topic.CreatedAt, ArchivedAt: topic.ArchivedAt,
	}
}

func resolvedRefToWire(link reference.ResolvedReferenceLink) ResolvedReferenceWire {
	return ResolvedReferenceWire{
		ID: link.ID, NodeID: link.NodeID, TreeID: link.TreeID, TopicID: link.TopicID,
		RawRef: link.RawRef, Slug: link.Slug, ResolvedAt: link.ResolvedAt,
		ResolvedBy: link.ResolvedBy, ContextHash: link.ContextHash,
	}
}
