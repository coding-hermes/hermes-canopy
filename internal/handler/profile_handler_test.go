// Copyright 2026 Hermes Agent. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/hermes"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// fixturePool seeds a temp profiles row and returns its id.
func fixtureProfile(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	display := "fixture-profile"
	_, err := pool.Exec(t.Context(),
		`INSERT INTO profiles (id, owner_id, profile_type, name, display_name, description, config_json, can_auto_respond, context_window_size, is_public)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (id) DO NOTHING`,
		id, userID, "hermes-profile",
		"fixture-profile", display, nil,
		`{}`, false, 32768, false,
	)
	if err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	return id
}

// buildUnscopedServer returns a chi router with only the unscoped profile
// routes (POST /profiles) wired.
func buildUnscopedServer(t *testing.T, pool *pgxpool.Pool) *chi.Mux {
	t.Helper()
	encryptionKey := make([]byte, 32)
	for i := range encryptionKey {
		encryptionKey[i] = byte(i)
	}
	router := hermes.NewPGProfileRouter(pool, encryptionKey)
	h := NewProfileHandler(router).WithProfileRepo(db.NewPGProfileRepo(pool))

	r := chi.NewRouter()
	r.Mount("/", h.UnscopedRoutes())
	return r
}

// ---------------------------------------------------------------------------
// Tests: CreateProfile — happy path
// ---------------------------------------------------------------------------

func TestCreateProfile_Returns201WithProfileDocument(t *testing.T) {
	pool := setupProfileTestDB(t)
	userID := seedUser(t, pool)
	r := buildUnscopedServer(t, pool)

	body := map[string]any{
		"name":                "my-profile",
		"token":               "hermes-gateway-token-12345",
		"display_name":        "My Profile",
		"description":         "A test profile",
		"can_auto_respond":    true,
		"context_window_size": 8192,
		"is_public":           false,
	}
	payload, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("Bearer", "hermes-gateway-token-12345")

	// We need auth — use AuthMiddleware
	// For this test we bypass auth since CreateProfile extracts userID from context
	// but the handler already checks for uuid.Nil. Let's inject a real context.
	ctx := context.WithValue(req.Context(), userIDContextKey{}, userID)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp profileDocument
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp.Name != "my-profile" {
		t.Errorf("expected name=my-profile, got %s", resp.Name)
	}
	if resp.DisplayName != "My Profile" {
		t.Errorf("expected display_name=My Profile, got %s", resp.DisplayName)
	}
	if resp.IsPublic {
		t.Error("expected is_public=false")
	}
}

func TestCreateProfile_DisplayNameDefaultsToName(t *testing.T) {
	pool := setupProfileTestDB(t)
	userID := seedUser(t, pool)
	r := buildUnscopedServer(t, pool)

	body := map[string]any{
		"name":  "simple-name",
		"token": "hermes-gateway-token-12345",
	}
	payload, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), userIDContextKey{}, userID)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp profileDocument
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.DisplayName != "simple-name" {
		t.Errorf("expected display_name=simple-name (defaulted from name), got %s", resp.DisplayName)
	}
}

// ---------------------------------------------------------------------------
// Tests: CreateProfile — validation failures
// ---------------------------------------------------------------------------

func TestCreateProfile_BlankName_Returns400(t *testing.T) {
	pool := setupProfileTestDB(t)
	userID := seedUser(t, pool)
	r := buildUnscopedServer(t, pool)

	body := map[string]any{
		"token": "hermes-gateway-token-12345",
	}
	payload, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), userIDContextKey{}, userID)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var errResp map[string]any
	json.Unmarshal(w.Body.Bytes(), &errResp)
	if code, ok := errResp["error"].(map[string]any)["code"]; !ok || code != "INVALID_PROFILE_NAME" {
		t.Errorf("expected INVALID_PROFILE_NAME code, got %v", errResp)
	}
}

func TestCreateProfile_TooLongName_Returns400(t *testing.T) {
	pool := setupProfileTestDB(t)
	userID := seedUser(t, pool)
	r := buildUnscopedServer(t, pool)

	body := map[string]any{
		"name":  "this-name-is-way-too-long-and-exceeds-the-sixty-four-character-limit-for-profiles",
		"token": "hermes-gateway-token-12345",
	}
	payload, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), userIDContextKey{}, userID)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateProfile_InvalidNameChars_Returns400(t *testing.T) {
	pool := setupProfileTestDB(t)
	userID := seedUser(t, pool)
	r := buildUnscopedServer(t, pool)

	body := map[string]any{
		"name":  "bad name with spaces & symbols!",
		"token": "hermes-gateway-token-12345",
	}
	payload, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), userIDContextKey{}, userID)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateProfile_BlankToken_Returns400(t *testing.T) {
	pool := setupProfileTestDB(t)
	userID := seedUser(t, pool)
	r := buildUnscopedServer(t, pool)

	body := map[string]any{
		"name": "valid-name",
	}
	payload, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), userIDContextKey{}, userID)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var errResp map[string]any
	json.Unmarshal(w.Body.Bytes(), &errResp)
	if code, ok := errResp["error"].(map[string]any)["code"]; !ok || code != "VALIDATION_ERROR" {
		t.Errorf("expected VALIDATION_ERROR code, got %v", errResp)
	}
}

func TestCreateProfile_ContextWindowOutOfBounds_Returns400(t *testing.T) {
	pool := setupProfileTestDB(t)
	userID := seedUser(t, pool)
	r := buildUnscopedServer(t, pool)

	// Below minimum
	body := map[string]any{
		"name":                "test-profile",
		"token":               "hermes-gateway-token-12345",
		"context_window_size": 100, // < 1024
	}
	payload, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), userIDContextKey{}, userID)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("below-min: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// Above maximum
	body2 := map[string]any{
		"name":                "test-profile",
		"token":               "hermes-gateway-token-12345",
		"context_window_size": 9999999, // > 2097152
	}
	payload2, _ := json.Marshal(body2)

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/profiles", bytes.NewReader(payload2))
	req2.Header.Set("Content-Type", "application/json")
	req2 = req2.WithContext(ctx)

	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("above-max: expected 400, got %d: %s", w2.Code, w2.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Tests: CreateProfile — errors
// ---------------------------------------------------------------------------

func TestCreateProfile_DuplicateName_Returns409(t *testing.T) {
	pool := setupProfileTestDB(t)
	userID := seedUser(t, pool)

	// Create first profile
	firstID := fixtureProfile(t, pool, userID)
	_ = firstID

	r := buildUnscopedServer(t, pool)

	body := map[string]any{
		"name":  "fixture-profile", // same name as fixture
		"token": "hermes-gateway-token-12345",
	}
	payload, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/profiles", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), userIDContextKey{}, userID)
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}

	var errResp map[string]any
	json.Unmarshal(w.Body.Bytes(), &errResp)
	if code, ok := errResp["error"].(map[string]any)["code"]; !ok || code != "DUPLICATE_PROFILE_NAME" {
		t.Errorf("expected DUPLICATE_PROFILE_NAME code, got %v", errResp)
	}
}

func TestCreateProfile_NoRepo_Returns503(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	r.Mount("/", NewProfileHandler(nil).UnscopedRoutes())

	body := map[string]any{
		"name":  "test",
		"token": "token",
	}
	payload, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), userIDContextKey{}, uuid.New())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateProfile_Unauthorized_Returns401(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	r.Mount("/", NewProfileHandler(nil).UnscopedRoutes())

	body := map[string]any{
		"name":  "test",
		"token": "token",
	}
	payload, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

// serveProfileRequest exercises the existing workspace-scoped profile routes.
// Keep this helper alongside the original workspace handler tests so the
// unscoped profile additions do not remove their coverage seam.
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

// profileRouterStub preserves the unit-test seam used by the workspace routes.
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
	s.setWorkspace, s.setName, s.setToken = workspaceID, profileName, profileToken
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// setupProfileTestDB creates a temp PG test database with profiles table.
func setupProfileTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := newTestDB(t, []string{
		`CREATE TABLE IF NOT EXISTS profiles (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			profile_type TEXT NOT NULL DEFAULT 'hermes-profile',
			name TEXT NOT NULL,
			display_name TEXT NOT NULL DEFAULT '',
			description TEXT,
			config_json JSONB NOT NULL DEFAULT '{}',
			can_auto_respond BOOLEAN NOT NULL DEFAULT false,
			context_window_size INTEGER NOT NULL DEFAULT 32768,
			is_public BOOLEAN NOT NULL DEFAULT false,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE (owner_id, name)
		)`,
	})
	return pool
}

func seedUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := pool.Exec(t.Context(),
		`INSERT INTO users (id, hermes_user_id, display_name) VALUES ($1, $2, $3)
		 ON CONFLICT (id) DO NOTHING`,
		id, "test-user", "Test User")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

var userIDKey struct{}

func newTestDB(t *testing.T, createSQL []string) *pgxpool.Pool {
	t.Helper()
	dsn := "postgresql://localhost:5432/canopy_test?sslmode=disable"
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Skipf("no PG available: %v", err)
	}
	if err := pool.Ping(t.Context()); err != nil {
		pool.Close()
		t.Skipf("PG unreachable: %v", err)
	}
	for _, sql := range createSQL {
		if _, err := pool.Exec(t.Context(), sql); err != nil {
			pool.Close()
			t.Fatalf("CREATE TABLE: %v", err)
		}
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}
