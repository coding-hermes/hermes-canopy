package handler

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/federation"
)

type relayRepo struct {
	mu   sync.Mutex
	peer *federation.FederationPeer
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
