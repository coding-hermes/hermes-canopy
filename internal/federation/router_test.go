package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

func routeFixtures(t *testing.T) (*PGProfileRouter, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	t.Setenv("CANOPY_REQUIRE_DB", "1")
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	ownerID, profileID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, display_name) VALUES ($1,$2,'Route Owner')`, ownerID, "route-"+ownerID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO profiles (id, owner_id, profile_type, name, display_name) VALUES ($1,$2,'hermes-profile',$3,'Route Profile')`, profileID, ownerID, "route-"+profileID.String()); err != nil {
		t.Fatal(err)
	}
	tree, err := db.NewPGTreeRepo(pool).Create(ctx, &db.Tree{Title: "Route Tree", OwnerID: profileID})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := NewPGRepository(pool).Create(ctx, &FederationPeer{ServerURL: "https://route-" + uuid.NewString() + ".example", SigningKeyFP: "sha256:test", State: PeerConnected, TreeID: tree.ID, CreatedBy: profileID})
	if err != nil {
		t.Fatal(err)
	}
	return NewPGProfileRouter(pool), profileID, tree.ID, peer.ID
}

func TestProfileRouterCRUDAndValidation(t *testing.T) {
	router, profileID, treeID, peerID := routeFixtures(t)
	ctx := context.Background()
	if _, err := router.Create(ctx, &Route{ProfileID: profileID, RouteType: RouteRemote}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("remote without peer error = %v", err)
	}
	if _, err := router.Create(ctx, &Route{ProfileID: profileID, RouteType: RouteLocal, PeerID: &peerID}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("local with peer error = %v", err)
	}
	route, err := router.Create(ctx, &Route{ProfileID: profileID, TreeID: &treeID, RouteType: RouteLocal, Priority: 2})
	if err != nil {
		t.Fatal(err)
	}
	routes, err := router.List(ctx, &profileID, &treeID)
	if err != nil || len(routes) != 1 {
		t.Fatalf("List = %v, %v", routes, err)
	}
	remote := RouteRemote
	priority := 9
	peer := &peerID
	updated, err := router.Update(ctx, route.ID, RouteUpdate{PeerID: &peer, RouteType: &remote, Priority: &priority})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RouteType != RouteRemote || updated.Priority != 9 || updated.PeerID == nil || *updated.PeerID != peerID {
		t.Fatalf("updated = %+v", updated)
	}
	if err := router.Delete(ctx, route.ID); err != nil {
		t.Fatal(err)
	}
	if err := router.Delete(ctx, route.ID); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

func TestProfileRouterResolvePriorityAndCreatedAtTieBreak(t *testing.T) {
	router, profileID, treeID, peerID := routeFixtures(t)
	ctx := context.Background()
	global, err := router.Create(ctx, &Route{ProfileID: profileID, RouteType: RouteLocal, Priority: 10})
	if err != nil {
		t.Fatal(err)
	}
	specific, err := router.Create(ctx, &Route{ProfileID: profileID, TreeID: &treeID, PeerID: &peerID, RouteType: RouteRemote, Priority: 20})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := router.Resolve(ctx, profileID, treeID)
	if err != nil || resolved.ID != specific.ID {
		t.Fatalf("priority resolve = %+v, %v", resolved, err)
	}
	if _, err := router.pool.Exec(ctx, `UPDATE profile_routes SET priority=20, created_at=$2 WHERE id=$1`, global.ID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	resolved, err = router.Resolve(ctx, profileID, treeID)
	if err != nil || resolved.ID != global.ID {
		t.Fatalf("tie resolve = %+v, %v", resolved, err)
	}
	if _, err := router.Resolve(ctx, uuid.New(), treeID); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("missing resolve = %v", err)
	}
}
