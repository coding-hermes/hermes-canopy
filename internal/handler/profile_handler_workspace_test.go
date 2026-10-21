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

// TestDF94ProfileBodyErrorMessages is the integration regression test for
// DF-HERMES-CANOPY-94. Before the fix (commit 2b2bc573), SetActiveProfile
// answered EVERY decodeJSON failure with the flat message "request body must
// be valid JSON" — even when the body WAS valid JSON whose only fault was an
// unknown field or a wrong-typed value, which misled API clients into
// re-sending the same bytes. The fix routes decode errors through
// invalidBodyMessage: parseable JSON with a schema problem names the
// offending field; only genuinely malformed JSON keeps the generic validity
// message.
//
// The arms drive the production handler over the real chi mount against the
// real PGProfileRouter (not a stub): the unknown-field and type-mismatch arms
// deliberately use a nonexistent workspace so that if decode were skipped or
// mis-ordered, the request would surface as the router's 404
// WORKSPACE_NOT_FOUND instead of the decode-path 400 — the INVALID_BODY code
// in those arms is itself proof the decode path ran.
//
// ch:trace row=DF-HERMES-CANOPY-94 spec=docs/API.md#profiles-POST wave=none:serial test=internal/handler/profile_handler_workspace_test.go::TestDF94ProfileBodyErrorMessages doc=none:no-doc-change prompt=/tmp/hermes-canopy-DF-HERMES-CANOPY-94-brief.txt evidence=internal/handler/profile_handler_workspace_test.go witness=none:local-pg-integration verdict=none:pending-judge commit=none:pending-foreman-trace memory=none:not-written
func TestDF94ProfileBodyErrorMessages(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	testutil.TruncateAll(t, pool)
	ctx := context.Background()
	router := hermes.NewPGProfileRouter(pool, testTokenKey)

	// The documented valid body (docs/API.md — Set Active Profile request).
	const validBody = `{"profile_name":"dev-hermes","profile_token":"hprof_dev_token"}`

	// df94DecodeError POSTs body to the workspace profile route and returns
	// the decoded error envelope.
	df94DecodeError := func(body string) apiErrorBody {
		t.Helper()
		rr := serveProfileRequest(t, router, uuid.New(), http.MethodPost, "/", bytes.NewBufferString(body))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
		}
		var got apiErrorBody
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode response %q: %v", rr.Body.String(), err)
		}
		if got.Error.Code != "INVALID_BODY" {
			t.Fatalf("error code = %q, want INVALID_BODY: %s", got.Error.Code, rr.Body.String())
		}
		return got
	}

	t.Run("malformed json still says request body must be valid JSON", func(t *testing.T) {
		got := df94DecodeError(`{"profile_name": `)
		if got.Error.Message != "request body must be valid JSON" {
			t.Fatalf("message = %q, want %q", got.Error.Message, "request body must be valid JSON")
		}
	})

	t.Run("unknown field names the field and is not the generic message", func(t *testing.T) {
		got := df94DecodeError(`{"profile_name":"dev-hermes","profile_token":"hprof_dev_token","nonsense_field":true}`)
		if !strings.Contains(got.Error.Message, "unknown field") || !strings.Contains(got.Error.Message, "nonsense_field") {
			t.Fatalf("message %q must name the unknown field nonsense_field", got.Error.Message)
		}
		// The misleading pre-fix message must be gone for parseable JSON.
		if strings.Contains(got.Error.Message, "must be valid JSON") {
			t.Fatalf("message %q still claims the body is not valid JSON", got.Error.Message)
		}
	})

	t.Run("type mismatch names the field", func(t *testing.T) {
		got := df94DecodeError(`{"profile_name":12345,"profile_token":"hprof_dev_token"}`)
		if !strings.Contains(got.Error.Message, "profile_name") || !strings.Contains(got.Error.Message, "invalid value") {
			t.Fatalf("message %q must name the field profile_name and the value/type problem", got.Error.Message)
		}
		if strings.Contains(got.Error.Message, "must be valid JSON") {
			t.Fatalf("message %q still claims the body is not valid JSON", got.Error.Message)
		}
	})

	t.Run("documented valid body creates a readable mapping", func(t *testing.T) {
		workspaceID := uuid.New()
		if _, err := pool.Exec(ctx,
			`INSERT INTO workspaces (id, name, slug, description) VALUES ($1, 'W-df94', 'w-df94', '')`,
			workspaceID); err != nil {
			t.Fatalf("seed workspace: %v", err)
		}

		rr := serveProfileRequest(t, router, workspaceID, http.MethodPost, "/", bytes.NewBufferString(validBody))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
		}
		var mapping profileMappingResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &mapping); err != nil {
			t.Fatalf("decode response %q: %v", rr.Body.String(), err)
		}
		if mapping.ProfileName != "dev-hermes" || !mapping.IsActive || mapping.WorkspaceID != workspaceID {
			t.Fatalf("mapping = (%q, isActive=%v, workspace=%s), want (\"dev-hermes\", true, %s)",
				mapping.ProfileName, mapping.IsActive, mapping.WorkspaceID, workspaceID)
		}

		// The created mapping must be readable back via GET .../profiles/active.
		active := serveProfileRequest(t, router, workspaceID, http.MethodGet, "/active", nil)
		if active.Code != http.StatusOK {
			t.Fatalf("GET active status = %d, want %d: %s", active.Code, http.StatusOK, active.Body.String())
		}
		var activeMapping profileMappingResponse
		if err := json.Unmarshal(active.Body.Bytes(), &activeMapping); err != nil {
			t.Fatalf("decode active response %q: %v", active.Body.String(), err)
		}
		if activeMapping.ProfileName != "dev-hermes" {
			t.Fatalf("read-back profile_name = %q, want \"dev-hermes\"", activeMapping.ProfileName)
		}
	})

	t.Run("rejected bodies did not create any mapping", func(t *testing.T) {
		// The three error arms used nonexistent workspaces; had any decode
		// accepted the body, a mapping row would exist and GET active would
		// have returned 200. Every error arm must have left no mapping.
		for _, arm := range []string{"malformed", "unknown field", "type mismatch"} {
			rr := serveProfileRequest(t, router, uuid.New(), http.MethodGet, "/active", nil)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("%s: GET active status = %d, want %d PROFILE_NOT_FOUND: %s",
					arm, rr.Code, http.StatusNotFound, rr.Body.String())
			}
			var got apiErrorBody
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("%s: decode response: %v", arm, err)
			}
			if got.Error.Code != "PROFILE_NOT_FOUND" {
				t.Fatalf("%s: error code = %q, want PROFILE_NOT_FOUND", arm, got.Error.Code)
			}
		}
	})
}
