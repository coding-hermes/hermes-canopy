package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/gateway"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
	"github.com/google/uuid"
)

func TestGatewayOutputSinkPersistsReplyAndSourceMetadata(t *testing.T) {
	testutil.SkipIfNoDB(t)
	ctx := context.Background()
	pool := testutil.NewSharedIntegrationPool(t)

	if _, err := db.EnsureDevJWTUser(ctx, pool); err != nil {
		t.Fatalf("EnsureDevJWTUser: %v", err)
	}
	if _, err := db.EnsureDevWorkspaceProfile(ctx, pool); err != nil {
		t.Fatalf("EnsureDevWorkspaceProfile: %v", err)
	}
	var devProfileID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM profiles WHERE owner_id = $1 AND name = $2 AND deleted_at IS NULL`,
		db.DevJWTUserID, db.DevProfileName).Scan(&devProfileID); err != nil {
		t.Fatalf("load dev profile: %v", err)
	}

	ownerID := uuid.MustParse(db.DevJWTUserID)
	treeSvc := service.NewTreeService(
		db.NewPGTreeRepo(pool),
		db.NewPGNodeRepo(pool), db.NewPGEdgeRepo(pool), pool,
	)
	tree, err := treeSvc.CreateTree(ctx, service.CreateTreeParams{
		OwnerID:       ownerID,
		Title:         "Gateway output sink integration",
		RootContent:   "source",
		ContentFormat: service.FormatMarkdown,
		NodeType:      service.NodeTypeMessage,
	})
	if err != nil {
		t.Fatalf("CreateTree: %v", err)
	}
	nodeSvc := service.NewNodeService(db.NewPGNodeRepo(pool), db.NewPGEdgeRepo(pool), pool, nil)
	existing := json.RawMessage(`{"keep":"me"}`)
	if _, err := nodeSvc.Update(ctx, tree.RootNodeID, service.UpdateNodeInput{Metadata: &existing}); err != nil {
		t.Fatalf("seed source metadata: %v", err)
	}

	sink := newGatewayOutputSink(nodeSvc, pool)
	if sink == nil {
		t.Fatal("newGatewayOutputSink returned nil for production dependencies")
	}
	input := gateway.PersistRunOutputInput{
		RunID:        "run_integration",
		SourceNodeID: tree.RootNodeID.String(),
		Output:       "gateway answer",
	}
	if err := sink.PersistRunOutput(ctx, input); err != nil {
		t.Fatalf("PersistRunOutput: %v", err)
	}

	nodes, err := nodeSvc.ListByTree(ctx, tree.ID)
	if err != nil {
		t.Fatalf("ListByTree: %v", err)
	}
	var reply *service.NodeDetail
	for i := range nodes {
		if nodes[i].ParentID != nil && *nodes[i].ParentID == tree.RootNodeID {
			reply = &nodes[i]
			break
		}
	}
	if reply == nil {
		t.Fatalf("reply node missing from normal node list: %+v", nodes)
	}
	if reply.Content != input.Output || reply.AuthorID != devProfileID {
		t.Fatalf("reply = %+v, want content and author %s", reply, devProfileID)
	}
	var replyMetadata map[string]string
	if err := json.Unmarshal(reply.Metadata, &replyMetadata); err != nil {
		t.Fatalf("decode reply metadata: %v", err)
	}
	if replyMetadata["run_id"] != input.RunID || replyMetadata["origin"] != "gateway_run" {
		t.Fatalf("reply metadata = %#v", replyMetadata)
	}

	source, err := nodeSvc.GetByID(ctx, tree.RootNodeID)
	if err != nil {
		t.Fatalf("GetByID source: %v", err)
	}
	var sourceMetadata map[string]json.RawMessage
	if err := json.Unmarshal(source.Metadata, &sourceMetadata); err != nil {
		t.Fatalf("decode source metadata: %v", err)
	}
	var keep string
	if err := json.Unmarshal(sourceMetadata["keep"], &keep); err != nil || keep != "me" {
		t.Fatalf("source existing metadata was not preserved: %#v", sourceMetadata)
	}
	var lastRun map[string]string
	if err := json.Unmarshal(sourceMetadata["last_gateway_run"], &lastRun); err != nil {
		t.Fatalf("decode source run metadata: %v", err)
	}
	if lastRun["run_id"] != input.RunID || lastRun["completed_at"] == "" {
		t.Fatalf("source last_gateway_run = %#v", lastRun)
	}
}
