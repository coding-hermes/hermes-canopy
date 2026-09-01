package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/federation"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

func TestFederationHandlerHandshake(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	ownerID, profileID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, display_name) VALUES ($1,$2,'Federation Owner')`, ownerID, "fed-"+ownerID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO profiles (id, owner_id, profile_type, name, display_name) VALUES ($1,$2,'hermes-profile','fed-profile','Federation Profile')`, profileID, ownerID); err != nil {
		t.Fatal(err)
	}
	tree, err := db.NewPGTreeRepo(pool).Create(ctx, &db.Tree{Title: "Federation Tree", OwnerID: profileID})
	if err != nil {
		t.Fatal(err)
	}

	secret := []byte("federation-handler-secret")
	svc := federation.NewService(federation.NewPGRepository(pool), secret, uuid.New(), "https://canopy-a.example.com")
	h := NewFederationHandler(svc, "https://canopy-b.example.com")
	r := chi.NewRouter()
	r.With(FederationAuthMiddleware("jwt-secret", svc)).Post("/api/v1/federation/handshake", h.Handshake)
	srv := httptest.NewServer(r)
	defer srv.Close()

	token, err := svc.GenerateToken(profileID, tree.ID)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		token      string
		wantStatus int
	}{
		{name: "happy path", token: token, wantStatus: http.StatusOK},
		{name: "bad token", token: token + "bad", wantStatus: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := map[string]string{"token": tt.token, "server_url": "https://canopy-a.example.com", "ecdhe_public_key": base64.StdEncoding.EncodeToString([]byte("x25519-public-key"))}
			data, _ := json.Marshal(body)
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/federation/handshake", bytes.NewReader(data))
			req.Header.Set("Authorization", "Bearer "+tt.token)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK {
				var got federationHandshakeResponse
				if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				if got.PeerID == uuid.Nil || got.ServerURL != "https://canopy-b.example.com" {
					t.Fatalf("response = %+v", got)
				}
			}
		})
	}

	peer, err := svc.AcceptFederationLink(ctx, token, "https://canopy-a.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeFederationLink(ctx, peer.ID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"token": token, "server_url": "https://canopy-a.example.com", "ecdhe_public_key": ""})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/federation/handshake", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("revoked handshake status = %d, want 410", resp.StatusCode)
	}
}
