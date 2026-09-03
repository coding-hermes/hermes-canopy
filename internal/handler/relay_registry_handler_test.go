package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/relay"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type relayRegistryStub struct {
	registerErr error
	deleteErr   error
	relays      []relay.RelayNodeInfo
}

func (s *relayRegistryStub) RegisterInstance(context.Context, *relay.RegisterRequest) (*relay.RegisterResponse, error) {
	if s.registerErr != nil {
		return nil, s.registerErr
	}
	return &relay.RegisterResponse{InstanceID: uuid.New(), RelaySecret: "secret", CreatedAt: time.Now()}, nil
}
func (s *relayRegistryStub) GetAvailableRelays(context.Context, uuid.UUID) ([]relay.RelayNodeInfo, error) {
	return s.relays, nil
}
func (s *relayRegistryStub) DeleteInstance(context.Context, uuid.UUID) error { return s.deleteErr }

func relayRegistryRoutes(stub *relayRegistryStub) http.Handler {
	r := chi.NewRouter()
	r.Mount("/api/v1/relays", NewRelayRegistryHandler(stub).Routes())
	return r
}

func asRelayAdmin(req *http.Request) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), roleContextKey{}, "admin"))
}

func TestRelayRegistryHandlerRegister(t *testing.T) {
	body, _ := json.Marshal(relay.RegisterRequest{TenantID: uuid.New(), PublicKey: make([]byte, 32), ListenAddr: "tcp://relay:9443", Tier: "pro", ProvisioningToken: "token"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/relays/instances", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	relayRegistryRoutes(&relayRegistryStub{}).ServeHTTP(rr, asRelayAdmin(req))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestRelayRegistryHandlerBadProvisioningToken(t *testing.T) {
	body, _ := json.Marshal(relay.RegisterRequest{TenantID: uuid.New(), PublicKey: make([]byte, 32), ListenAddr: "tcp://relay:9443", Tier: "pro", ProvisioningToken: "bad"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/relays/instances", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	relayRegistryRoutes(&relayRegistryStub{registerErr: relay.ErrProvisioningTokenInvalid}).ServeHTTP(rr, asRelayAdmin(req))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestRelayRegistryHandlerUnknownInstance(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/relays/instances/"+uuid.NewString(), nil)
	rr := httptest.NewRecorder()
	relayRegistryRoutes(&relayRegistryStub{deleteErr: relay.ErrInstanceNotFound}).ServeHTTP(rr, asRelayAdmin(req))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestRelayRegistryHandlerDiscoveryShape(t *testing.T) {
	tenantID, instanceID, userID := uuid.New(), uuid.New(), uuid.New()
	stub := &relayRegistryStub{relays: []relay.RelayNodeInfo{{InstanceID: instanceID, ListenAddr: "tcp://relay:9443", Load: .25, Region: "us-east-1"}}}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": userID.String(), "tenant_id": tenantID.String(), "exp": time.Now().Add(time.Hour).Unix()})
	raw, err := token.SignedString([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/relays", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rr := httptest.NewRecorder()
	AuthMiddleware("secret")(relayRegistryRoutes(stub)).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		TenantID uuid.UUID             `json:"tenant_id"`
		Relays   []relay.RelayNodeInfo `json:"relays"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || got.TenantID != tenantID || len(got.Relays) != 1 || got.Relays[0].InstanceID != instanceID {
		t.Fatalf("response=%s err=%v", rr.Body.String(), err)
	}
}
