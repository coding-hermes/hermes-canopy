package handler

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/federation"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

type relayRepo struct {
	mu   sync.Mutex
	peer *federation.FederationPeer
}

type resolveSpy struct {
	called bool
	route  *federation.Route
}

func (s *resolveSpy) Create(context.Context, *federation.Route) (*federation.Route, error) {
	return nil, nil
}
func (s *resolveSpy) List(context.Context, *uuid.UUID, *uuid.UUID) ([]federation.Route, error) {
	return nil, nil
}
func (s *resolveSpy) Update(context.Context, uuid.UUID, federation.RouteUpdate) (*federation.Route, error) {
	return nil, nil
}
func (s *resolveSpy) Delete(context.Context, uuid.UUID) error { return nil }
func (s *resolveSpy) Resolve(context.Context, uuid.UUID, uuid.UUID) (*federation.Route, error) {
	s.called = true
	return s.route, nil
}

func (r *relayRepo) Create(_ context.Context, p *federation.FederationPeer) (*federation.FederationPeer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := *p
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	c.CreatedAt = time.Now()
	r.peer = &c
	return &c, nil
}

func TestPostEventConsultsProfileRouter(t *testing.T) {
	peerID, treeID, profileID := uuid.New(), uuid.New(), uuid.New()
	senderRepo, receiverRepo := &relayRepo{}, &relayRepo{}
	sender := federation.NewService(senderRepo, []byte("dispatch-sender"), uuid.New(), "http://sender.local")
	receiver := federation.NewService(receiverRepo, []byte("dispatch-receiver"), uuid.New(), "http://receiver.local")
	_, _ = senderRepo.Create(context.Background(), &federation.FederationPeer{ID: peerID, TreeID: treeID, SigningKeyFP: fingerprintForTest(receiver.SigningPublicKey()), SigningPublicKey: receiver.SigningPublicKey(), State: federation.PeerConnected})
	_, _ = receiverRepo.Create(context.Background(), &federation.FederationPeer{ID: peerID, TreeID: treeID, SigningKeyFP: fingerprintForTest(sender.SigningPublicKey()), SigningPublicKey: sender.SigningPublicKey(), State: federation.PeerConnected})
	aPub, aPriv, _ := federation.GenerateECDHKeyPair()
	bPub, bPriv, _ := federation.GenerateECDHKeyPair()
	_ = sender.EstablishSession(peerID, aPriv, bPub)
	_ = receiver.EstablishSession(peerID, bPriv, aPub)
	token, _ := sender.GenerateToken(profileID, treeID)
	envelope, err := sender.EncryptEnvelope(context.Background(), peerID, profileID, 1, "remote_node_added", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	spy := &resolveSpy{route: &federation.Route{ProfileID: profileID, TreeID: &treeID, RouteType: federation.RouteLocal}}
	h := NewFederationHandler(receiver, "http://receiver.local").WithProfileRouter(spy)
	body, _ := json.Marshal(envelope)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/federation/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.PostEvent(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !spy.called {
		t.Fatal("Resolve was not consulted")
	}
}
func (r *relayRepo) UpsertAccepted(ctx context.Context, p *federation.FederationPeer) (*federation.FederationPeer, error) {
	if r.peer != nil {
		p.ID = r.peer.ID
	}
	return r.Create(ctx, p)
}
func (r *relayRepo) Get(_ context.Context, id uuid.UUID) (*federation.FederationPeer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.peer == nil || r.peer.ID != id {
		return nil, federation.ErrFederationNotFound
	}
	c := *r.peer
	return &c, nil
}
func (r *relayRepo) FindByServerTree(_ context.Context, u string, id uuid.UUID) (*federation.FederationPeer, error) {
	if r.peer != nil && r.peer.ServerURL == u && r.peer.TreeID == id {
		c := *r.peer
		return &c, nil
	}
	return nil, federation.ErrFederationNotFound
}
func (r *relayRepo) List(context.Context, *uuid.UUID, bool) ([]*federation.FederationPeer, error) {
	if r.peer == nil {
		return nil, nil
	}
	return []*federation.FederationPeer{r.peer}, nil
}
func (r *relayRepo) SetState(context.Context, uuid.UUID, federation.PeerState, *time.Time, *string) error {
	return nil
}

func TestFederationTwoInstanceRelay(t *testing.T) {
	peerID, treeID, profileID := uuid.New(), uuid.New(), uuid.New()
	senderRepo, receiverRepo := &relayRepo{}, &relayRepo{}
	sender := federation.NewService(senderRepo, []byte("sender-secret"), uuid.New(), "http://sender.local")
	receiver := federation.NewService(receiverRepo, []byte("receiver-secret"), uuid.New(), "http://receiver.local")
	_, _ = senderRepo.Create(context.Background(), &federation.FederationPeer{ID: peerID, TreeID: treeID, ServerURL: "http://receiver.local", SigningKeyFP: fingerprintForTest(receiver.SigningPublicKey()), SigningPublicKey: receiver.SigningPublicKey(), State: federation.PeerConnected})
	_, _ = receiverRepo.Create(context.Background(), &federation.FederationPeer{ID: peerID, TreeID: treeID, ServerURL: "http://sender.local", SigningKeyFP: fingerprintForTest(sender.SigningPublicKey()), SigningPublicKey: sender.SigningPublicKey(), State: federation.PeerConnected})
	aPub, aPriv, _ := federation.GenerateECDHKeyPair()
	bPub, bPriv, _ := federation.GenerateECDHKeyPair()
	if err := sender.EstablishSession(peerID, aPriv, bPub); err != nil {
		t.Fatal(err)
	}
	if err := receiver.EstablishSession(peerID, bPriv, aPub); err != nil {
		t.Fatal(err)
	}
	token, err := sender.GenerateToken(profileID, treeID)
	if err != nil {
		t.Fatal(err)
	}

	receiverHandler := NewFederationHandler(receiver, "http://receiver.local")
	receiverMux := http.NewServeMux()
	receiverMux.HandleFunc("GET /api/v1/federation/events", receiverHandler.Events)
	receiverMux.HandleFunc("POST /api/v1/federation/events", receiverHandler.PostEvent)
	receiverServer := httptest.NewServer(receiverMux)
	defer receiverServer.Close()

	senderServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		envelope, err := sender.EncryptEnvelope(r.Context(), peerID, profileID, 1, "remote_node_added", []byte(`{"node_id":"n1"}`))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		body, _ := json.Marshal(envelope)
		req, _ := http.NewRequest(http.MethodPost, receiverServer.URL+"/api/v1/federation/events", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := receiverServer.Client().Do(req)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
	}))
	defer senderServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, receiverServer.URL+"/api/v1/federation/events?peer_id="+peerID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	stream, err := receiverServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	resp, err := senderServer.Client().Get(senderServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status = %d", resp.StatusCode)
	}
	line, err := bufio.NewReader(stream.Body).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "event: ftl_event") {
		t.Fatalf("SSE line = %q", line)
	}
}

func fingerprintForTest(key []byte) string {
	sum := sha256.Sum256(key)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type outageTransport struct {
	mu      sync.Mutex
	online  bool
	handler http.Handler
}

func (t *outageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	online, target := t.online, t.handler
	t.mu.Unlock()
	if !online {
		return nil, errors.New("simulated peer outage")
	}
	recorder := httptest.NewRecorder()
	target.ServeHTTP(recorder, req)
	return recorder.Result(), nil
}

// TestFederationRelayQueueThenReplayAcrossOutage uses two separately migrated
// PostgreSQL instances and an in-process HTTP transport so CANOPY_REQUIRE_DB=1
// fails loudly when the required :5437 database is unavailable.
func TestFederationRelayQueueThenReplayAcrossOutage(t *testing.T) {
	t.Setenv("CANOPY_REQUIRE_DB", "1")
	senderPool := testutil.NewIntegrationPool(t)
	receiverPool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	peerID, treeID, profileID := uuid.New(), uuid.New(), uuid.New()
	seed := func(pool *pgxpool.Pool) {
		ownerID := uuid.New()
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,hermes_user_id,display_name) VALUES($1,$2,'Relay Owner')`, ownerID, "relay-"+ownerID.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO profiles(id,owner_id,profile_type,name,display_name) VALUES($1,$2,'hermes-profile',$3,'Relay Profile')`, profileID, ownerID, "relay-"+profileID.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO trees(id,owner_id,title) VALUES($1,$2,'Relay Tree')`, treeID, profileID); err != nil {
			t.Fatal(err)
		}
	}
	seed(senderPool)
	seed(receiverPool)
	sender := federation.NewService(federation.NewPGRepository(senderPool), []byte("relay-a"), uuid.New(), "http://sender.local")
	receiver := federation.NewService(federation.NewPGRepository(receiverPool), []byte("relay-b"), uuid.New(), "http://receiver.local")
	insertPeer := func(pool *pgxpool.Pool, url, fp string, key []byte) {
		_, err := pool.Exec(ctx, `INSERT INTO federation_peers(id,server_url,signing_key_fp,signing_public_key,state,tree_id,created_by) VALUES($1,$2,$3,$4,2,$5,$6)`, peerID, url, fp, key, treeID, profileID)
		if err != nil {
			t.Fatal(err)
		}
	}
	insertPeer(senderPool, "http://receiver.local", fingerprintForTest(receiver.SigningPublicKey()), receiver.SigningPublicKey())
	insertPeer(receiverPool, "http://sender.local", fingerprintForTest(sender.SigningPublicKey()), sender.SigningPublicKey())
	aPub, aPriv, _ := federation.GenerateECDHKeyPair()
	bPub, bPriv, _ := federation.GenerateECDHKeyPair()
	if err := sender.EstablishSession(peerID, aPriv, bPub); err != nil {
		t.Fatal(err)
	}
	if err := receiver.EstablishSession(peerID, bPriv, aPub); err != nil {
		t.Fatal(err)
	}

	receiverHandler := NewFederationHandler(receiver, "http://receiver.local")
	transport := &outageTransport{handler: http.HandlerFunc(receiverHandler.PostEvent)}
	senderRelay := federation.NewRelayService(federation.NewPGRelayRepository(senderPool), federation.NewPGRepository(senderPool), &http.Client{Transport: transport}, func(_ context.Context, _ *federation.FederationPeer) (string, error) {
		return sender.GenerateToken(profileID, treeID)
	})
	envelope, err := sender.EncryptEnvelope(ctx, peerID, profileID, 1, "remote_node_added", []byte(`{"node_id":"offline"}`))
	if err != nil {
		t.Fatal(err)
	}
	event, err := senderRelay.Enqueue(ctx, peerID, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := senderRelay.DeliverPending(ctx, peerID); err == nil {
		t.Fatal("delivery during outage unexpectedly succeeded")
	}

	transport.mu.Lock()
	transport.online = true
	transport.mu.Unlock()
	if err := senderRelay.DeliverPending(ctx, peerID); err != nil {
		t.Fatal(err)
	}
	// Force at-least-once redelivery; receiver receipt must suppress it.
	if _, err := senderPool.Exec(ctx, `UPDATE federation_events SET status='failed' WHERE event_id=$1`, event.EventID); err != nil {
		t.Fatal(err)
	}
	if err := senderRelay.DeliverPending(ctx, peerID); err != nil {
		t.Fatal(err)
	}
	var receipts int
	if err := receiverPool.QueryRow(ctx, `SELECT count(*) FROM federation_event_receipts WHERE event_id=$1`, event.EventID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 {
		t.Fatalf("receipts = %d, want exactly 1", receipts)
	}
}
