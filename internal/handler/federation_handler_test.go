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
	ecdhPublicKey, _, err := federation.GenerateECDHKeyPair()
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
			body := map[string]string{"token": tt.token, "server_url": "https://canopy-a.example.com", "ecdhe_public_key": base64.StdEncoding.EncodeToString(ecdhPublicKey), "signing_public_key": base64.StdEncoding.EncodeToString(svc.SigningPublicKey())}
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

	peer, err := svc.AcceptFederationLink(ctx, token, "https://canopy-a.example.com", ecdhPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeFederationLink(ctx, peer.ID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"token": token, "server_url": "https://canopy-a.example.com", "ecdhe_public_key": base64.StdEncoding.EncodeToString(ecdhPublicKey), "signing_public_key": base64.StdEncoding.EncodeToString(svc.SigningPublicKey())})
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

func TestFederationHandlerRouteEndpoints(t *testing.T) {
	t.Setenv("CANOPY_REQUIRE_DB", "1")
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	ownerID, profileID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, display_name) VALUES ($1,$2,'Route Handler Owner')`, ownerID, "route-handler-"+ownerID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO profiles (id, owner_id, profile_type, name, display_name) VALUES ($1,$2,'hermes-profile',$3,'Route Handler Profile')`, profileID, ownerID, "route-handler-"+profileID.String()); err != nil {
		t.Fatal(err)
	}
	tree, err := db.NewPGTreeRepo(pool).Create(ctx, &db.Tree{Title: "Route Handler Tree", OwnerID: profileID})
	if err != nil {
		t.Fatal(err)
	}

	svc := federation.NewService(federation.NewPGRepository(pool), []byte("route-handler-secret"), uuid.New(), "https://local.example")
	h := NewFederationHandler(svc, "https://local.example").WithProfileRouter(federation.NewPGProfileRouter(pool))
	r := chi.NewRouter()
	r.Mount("/api/v1/federation/routes", h.RouteRoutes())
	srv := httptest.NewServer(r)
	defer srv.Close()

	data, _ := json.Marshal(map[string]any{"profile_id": profileID, "tree_id": tree.ID, "route_type": "local", "priority": 3})
	resp, err := srv.Client().Post(srv.URL+"/api/v1/federation/routes/", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d", resp.StatusCode)
	}
	var created federation.Route
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	resp, err = srv.Client().Get(srv.URL + "/api/v1/federation/routes/?profile_id=" + profileID.String())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}

	patch, _ := json.Marshal(map[string]any{"priority": 8})
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/v1/federation/routes/"+created.ID.String(), bytes.NewReader(patch))
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH status = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/federation/routes/"+created.ID.String(), nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d", resp.StatusCode)
	}
}
