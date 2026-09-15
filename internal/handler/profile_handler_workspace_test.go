// GAP-071: a workspace id that does not exist must produce a 404
// WORKSPACE_NOT_FOUND, not a 500 INTERNAL_ERROR.
//
// Before the fix, PGProfileRouter.SetActiveProfile let the pgx FK
// violation (fk_profile_route_workspace) escape, writeRouterError mapped
// the unknown error to 500, and the documented "set active profile"
// walkthrough was unusable on a fresh database (no workspaces row existed
// and nothing created one). These tests drive the REAL PG router against
// a migrated database — a stub router cannot reproduce an FK violation.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/hermes"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// testTokenKey is a 32-byte AES-256 key (EncryptToken rejects any other
// length before the router ever reaches the database).
var testTokenKey = []byte("0123456789abcdef0123456789abcdef")

func TestGAP071SetActiveProfileMissingWorkspaceIs404(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	router := hermes.NewPGProfileRouter(pool, testTokenKey)
	missing := uuid.New()

	// Router layer: the sentinel, not a raw pgx FK error.
	err := router.SetActiveProfile(ctx, missing, "dev-hermes", "hprof_dev_token")
	if err == nil {
		t.Fatal("SetActiveProfile on a missing workspace returned nil error")
	}
	if !errors.Is(err, hermes.ErrWorkspaceNotFound) {
		t.Fatalf("SetActiveProfile error = %v, want ErrWorkspaceNotFound (raw pgx FK error leaked?)", err)
	}

	// HTTP layer: 404 WORKSPACE_NOT_FOUND naming the id, with no SQL text.
	rr := serveProfileRequest(t, router, missing, http.MethodPost, "/",
		bytes.NewBufferString(`{"profile_name":"dev-hermes","profile_token":"hprof_dev_token"}`))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusNotFound, rr.Body.String())
	}
	var body apiErrorBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rr.Body.String(), err)
	}
	if body.Error.Code != "WORKSPACE_NOT_FOUND" {
		t.Fatalf("error code = %q, want WORKSPACE_NOT_FOUND", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, missing.String()) {
		t.Fatalf("error message %q does not name the workspace id %s", body.Error.Message, missing)
	}
	for _, leak := range []string{"SQLSTATE", "fk_profile_route", "INSERT INTO", "23503"} {
		if strings.Contains(body.Error.Message, leak) {
			t.Fatalf("error message leaks raw SQL detail %q: %q", leak, body.Error.Message)
		}
	}
}

func TestGAP071SetActiveProfileExistingWorkspaceStillWorks(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	router := hermes.NewPGProfileRouter(pool, testTokenKey)

	// A real workspace row — the state GAP-071's dev provisioning creates.
	workspaceID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspaces (id, name, slug, description) VALUES ($1, 'W', 'w-gap071', '')`,
		workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	rr := serveProfileRequest(t, router, workspaceID, http.MethodPost, "/",
		bytes.NewBufferString(`{"profile_name":"dev-hermes","profile_token":"hprof_dev_token"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var mapping profileMappingResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &mapping); err != nil {
		t.Fatalf("decode response %q: %v", rr.Body.String(), err)
	}
	if mapping.ProfileName != "dev-hermes" || !mapping.IsActive {
		t.Fatalf("mapping = (%q, isActive=%v), want (\"dev-hermes\", true)", mapping.ProfileName, mapping.IsActive)
	}

	// The mapping is readable back through GET .../profiles/active.
	active := serveProfileRequest(t, router, workspaceID, http.MethodGet, "/active", nil)
	if active.Code != http.StatusOK {
		t.Fatalf("GET active status = %d, want %d: %s", active.Code, http.StatusOK, active.Body.String())
	}

	// GET .../profiles/active for a workspace with no mapping is unchanged
	// (404 PROFILE_NOT_FOUND) — the new branch must not swallow it.
	unmapped := serveProfileRequest(t, router, uuid.New(), http.MethodGet, "/active", nil)
	if unmapped.Code != http.StatusNotFound {
		t.Fatalf("unmapped GET active status = %d, want %d", unmapped.Code, http.StatusNotFound)
	}
	var unmappedBody apiErrorBody
	if err := json.Unmarshal(unmapped.Body.Bytes(), &unmappedBody); err != nil {
		t.Fatalf("decode unmapped response: %v", err)
	}
	if unmappedBody.Error.Code != "PROFILE_NOT_FOUND" {
		t.Fatalf("unmapped error code = %q, want PROFILE_NOT_FOUND", unmappedBody.Error.Code)
	}
}

// TestGAP071SetActiveProfileValidationUnchanged pins the 400 behaviours the
// new 404 branch must not disturb.
func TestGAP071SetActiveProfileValidationUnchanged(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	router := hermes.NewPGProfileRouter(pool, testTokenKey)

	cases := []struct {
		name string
		body string
		want string
	}{
		{"empty profile name", `{"profile_name":"  ","profile_token":"hprof"}`, "VALIDATION_ERROR"},
		{"empty token", `{"profile_name":"dev-hermes","profile_token":""}`, "VALIDATION_ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := serveProfileRequest(t, router, uuid.New(), http.MethodPost, "/", bytes.NewBufferString(tc.body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
			}
			var body apiErrorBody
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != tc.want {
				t.Fatalf("error code = %q, want %q", body.Error.Code, tc.want)
			}
		})
	}
}
