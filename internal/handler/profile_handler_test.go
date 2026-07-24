package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/hermes"
)

type profileRouterStub struct {
	active       *hermes.ProfileMapping
	profiles     []hermes.ProfileMapping
	setWorkspace uuid.UUID
	setName      string
	setToken     string
	removedName  string
	err          error
}

func (s *profileRouterStub) GetActiveProfile(context.Context, uuid.UUID) (*hermes.ProfileMapping, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.active, nil
}

func (s *profileRouterStub) SetActiveProfile(_ context.Context, workspaceID uuid.UUID, profileName, profileToken string) error {
	if s.err != nil {
		return s.err
	}
	s.setWorkspace = workspaceID
	s.setName = profileName
	s.setToken = profileToken
	return nil
}

func (s *profileRouterStub) ListProfiles(context.Context, uuid.UUID) ([]hermes.ProfileMapping, error) {
	return s.profiles, s.err
}

func (s *profileRouterStub) RemoveProfile(_ context.Context, _ uuid.UUID, profileName string) error {
	if s.err != nil {
		return s.err
	}
	s.removedName = profileName
	return nil
}

func (s *profileRouterStub) GetProfileToken(context.Context, uuid.UUID, string) (string, error) {
	return "", s.err
}

func (s *profileRouterStub) ListAvailableProfiles(context.Context) ([]hermes.AvailableProfile, error) {
	return nil, s.err
}

func TestProfileHandlerListProfilesUsesCamelCaseAndHidesToken(t *testing.T) {
	workspaceID := uuid.New()
	stub := &profileRouterStub{profiles: []hermes.ProfileMapping{{
		WorkspaceID:           workspaceID,
		ProfileName:           "coding",
		DisplayName:           "Coding",
		IsActive:              true,
		ProfileTokenEncrypted: []byte("secret-ciphertext"),
		MappedAt:              time.Unix(1, 0).UTC(),
		LastUsedAt:            time.Unix(2, 0).UTC(),
	}}}

	rr := serveProfileRequest(t, stub, workspaceID, http.MethodGet, "/", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	profiles := body["profiles"].([]any)
	profile := profiles[0].(map[string]any)
	if profile["workspaceId"] != workspaceID.String() {
		t.Fatalf("workspaceId = %v, want %s", profile["workspaceId"], workspaceID)
	}
	if _, ok := profile["workspace_id"]; ok {
		t.Fatal("response contains snake_case workspace_id")
	}
	if _, ok := profile["profileTokenEncrypted"]; ok {
		t.Fatal("response serialized encrypted profile token")
	}
}

func TestProfileHandlerSetActiveProfile(t *testing.T) {
	workspaceID := uuid.New()
	stub := &profileRouterStub{active: &hermes.ProfileMapping{
		WorkspaceID: workspaceID,
		ProfileName: "coding",
		DisplayName: "coding",
		IsActive:    true,
		MappedAt:    time.Now().UTC(),
		LastUsedAt:  time.Now().UTC(),
	}}
	body := bytes.NewBufferString(`{"profile_name":"coding","profile_token":"hprof_123"}`)

	rr := serveProfileRequest(t, stub, workspaceID, http.MethodPost, "/", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if stub.setWorkspace != workspaceID || stub.setName != "coding" || stub.setToken != "hprof_123" {
		t.Fatalf("SetActiveProfile args = (%s, %q, %q)", stub.setWorkspace, stub.setName, stub.setToken)
	}
}

func TestProfileHandlerNotFoundUsesEstablishedErrorShape(t *testing.T) {
	workspaceID := uuid.New()
	stub := &profileRouterStub{err: hermes.ErrNoProfileMapping}

	rr := serveProfileRequest(t, stub, workspaceID, http.MethodGet, "/active", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusNotFound, rr.Body.String())
	}
	var body apiErrorBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "PROFILE_NOT_FOUND" {
		t.Fatalf("error code = %q, want PROFILE_NOT_FOUND", body.Error.Code)
	}
}

func TestProfileHandlerRejectsInvalidWorkspaceID(t *testing.T) {
	stub := &profileRouterStub{err: errors.New("must not be called")}
	r := chi.NewRouter()
	r.Mount("/api/v1/workspaces/{workspace_id}/profiles", NewProfileHandler(stub).Routes())

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/not-a-uuid/profiles/", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func serveProfileRequest(t *testing.T, stub hermes.ProfileRouter, workspaceID uuid.UUID, method, suffix string, body *bytes.Buffer) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Mount("/api/v1/workspaces/{workspace_id}/profiles", NewProfileHandler(stub).Routes())
	var reqBody *bytes.Buffer
	if body == nil {
		reqBody = bytes.NewBuffer(nil)
	} else {
		reqBody = body
	}
	req := httptest.NewRequest(method, "/api/v1/workspaces/"+workspaceID.String()+"/profiles"+suffix, reqBody)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}
