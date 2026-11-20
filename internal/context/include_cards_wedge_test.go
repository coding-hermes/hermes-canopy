package context

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
	"github.com/coding-hermes/hermes-canopy/internal/db"
)

func TestIncludeCardsNoWedge(t *testing.T) {
	mgr := card.NewCardDBManager(t.TempDir())
	nodeID := uuid.New()
	treeID := uuid.New()
	content := "reply with card"
	repo, err := mgr.Repository(card.CardTypeCompact)
	if err != nil {
		t.Fatal(err)
	}
	created, err := repo.Create(context.Background(), card.CreateCardInput{
		ID: uuid.New(), TreeID: treeID, NodeID: nodeID, AppID: "test", CardType: card.CardTypeCompact,
		Data: json.RawMessage(`{"title":"test"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	node := makeNode(nodeID, uuid.New(), content)
	node.TreeID = treeID
	nodes := &stubNodeReader{nodes: map[uuid.UUID]*db.Node{nodeID: node}, getAncFn: func(context.Context, uuid.UUID) ([]db.Node, error) { return []db.Node{*node}, nil }}
	compiler := NewCompiler(nodes, &stubTopicReader{}, mgr, NewTokenEstimator(), 5)
	done := make(chan error, 1)
	go func() {
		result, err := compiler.Compile(context.Background(), CompileRequest{NodeID: nodeID, TokenBudget: 8000, IncludeCards: true})
		if err == nil && len(result.Manifest.Cards) != 1 {
			err = errors.New("compiled card missing")
		}
		if err == nil {
			_, err = mgr.Repository(card.CardTypeCompact)
		}
		if err == nil {
			_, err = repo.Get(context.Background(), created.ID)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		if err := mgr.Close(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("includeCards or subsequent card GET wedged")
	}
}
