// Unit tests for the multi-reference selection token, the §9.4 error
// catalog, and the §6.2 budget arithmetic. No database is required.
package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// fixedClock returns a mutable clock so a test can move time forward without
// sleeping.
func fixedClock(start time.Time) (func() time.Time, func(time.Duration)) {
	current := start
	return func() time.Time { return current }, func(d time.Duration) { current = current.Add(d) }
}

func TestReferenceSelectionSigner_RoundTrip(t *testing.T) {
	clock, _ := fixedClock(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC))
	signer := NewReferenceSelectionSigner("test-secret", clock)
	treeID, requester, primary := uuid.New(), uuid.New(), uuid.New()
	other := uuid.New()

	token, err := signer.Sign(ReferenceSelectionClaims{
		TreeID:          treeID,
		RequesterID:     requester,
		PrimarySourceID: primary,
		SourceIDs:       []uuid.UUID{primary, other},
		SourceHashes:    []string{"a", "b"},
		Budget:          8192,
	})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if got := token[:7]; got != "mrs.v1." {
		t.Fatalf("token prefix = %q, want mrs.v1.", got)
	}

	claims, err := signer.Verify(token, requester)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if claims.TreeID != treeID || claims.PrimarySourceID != primary || claims.RequesterID != requester {
		t.Fatalf("claims round-trip mismatch: %+v", claims)
	}
	if len(claims.SourceIDs) != 2 || claims.SourceIDs[0] != primary {
		t.Fatalf("canonical order not preserved: %+v", claims.SourceIDs)
	}
	if claims.Budget != 8192 {
		t.Fatalf("budget = %d, want 8192", claims.Budget)
	}
	// Five-minute lifetime (§4.3).
	if want := clock().Add(ReferenceSelectionTTL).Unix(); claims.ExpiresAt != want {
		t.Fatalf("exp = %d, want %d", claims.ExpiresAt, want)
	}
}

func TestReferenceSelectionSigner_Expired(t *testing.T) {
	clock, advance := fixedClock(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC))
	signer := NewReferenceSelectionSigner("test-secret", clock)
	requester := uuid.New()

	token, err := signer.Sign(ReferenceSelectionClaims{
		TreeID:          uuid.New(),
		RequesterID:     requester,
		PrimarySourceID: uuid.New(),
		SourceIDs:       []uuid.UUID{uuid.New(), uuid.New()},
	})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if _, err := signer.Verify(token, requester); err != nil {
		t.Fatalf("fresh token rejected: %v", err)
	}

	// Still inside the five-minute window.
	advance(ReferenceSelectionTTL - time.Second)
	if _, err := signer.Verify(token, requester); err != nil {
		t.Fatalf("token rejected before expiry: %v", err)
	}

	// Past the window (§15 scenario 34, §9.4 REFERENCE_SELECTION_TOKEN_EXPIRED).
	advance(2 * time.Second)
	_, err = signer.Verify(token, requester)
	if !errors.Is(err, ErrReferenceSelectionTokenExpired) {
		t.Fatalf("expired token error = %v, want ErrReferenceSelectionTokenExpired", err)
	}
	apiErr, ok := ReferenceErrorFrom(err)
	if !ok || apiErr.Status != 410 || apiErr.Code != "REFERENCE_SELECTION_TOKEN_EXPIRED" {
		t.Fatalf("expired token envelope = %+v, want 410 REFERENCE_SELECTION_TOKEN_EXPIRED", apiErr)
	}
}

func TestReferenceSelectionSigner_RejectsTamperingAndForeignCallers(t *testing.T) {
	clock, _ := fixedClock(time.Now())
	signer := NewReferenceSelectionSigner("test-secret", clock)
	requester := uuid.New()

	token, err := signer.Sign(ReferenceSelectionClaims{
		TreeID:          uuid.New(),
		RequesterID:     requester,
		PrimarySourceID: uuid.New(),
		SourceIDs:       []uuid.UUID{uuid.New(), uuid.New()},
	})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	cases := []struct {
		name  string
		token string
		call  uuid.UUID
	}{
		{"tampered signature", token[:len(token)-2] + "AA", requester},
		{"empty token", "", requester},
		{"wrong prefix", "xyz.v1.a.b", requester},
		{"missing signature", "mrs.v1.abc", requester},
		{"different caller", token, uuid.New()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := signer.Verify(tc.token, tc.call)
			if !errors.Is(err, ErrReferenceSelectionTokenInvalid) {
				t.Fatalf("Verify() error = %v, want ErrReferenceSelectionTokenInvalid", err)
			}
			apiErr, ok := ReferenceErrorFrom(err)
			if !ok || apiErr.Status != 400 || apiErr.Code != "REFERENCE_SELECTION_TOKEN_INVALID" {
				t.Fatalf("envelope = %+v, want 400 REFERENCE_SELECTION_TOKEN_INVALID", apiErr)
			}
		})
	}
}

func TestReferenceSelectionSigner_Unconfigured(t *testing.T) {
	signer := NewReferenceSelectionSigner("", time.Now)
	_, err := signer.Verify("mrs.v1.whatever.sig", uuid.New())
	if !errors.Is(err, ErrReferenceSelectionUnconfigured) {
		t.Fatalf("Verify() error = %v, want ErrReferenceSelectionUnconfigured", err)
	}
	// The tree service refuses the preflight outright when no signer is wired.
	svc := &TreeServiceImpl{}
	_, err = svc.ValidateReferenceSelection(context.Background(), uuid.New(), ReferenceSelectionInput{
		SourceNodeIDs: []uuid.UUID{uuid.New(), uuid.New()},
	})
	if !errors.Is(err, ErrReferenceSelectionUnconfigured) {
		t.Fatalf("ValidateReferenceSelection() error = %v, want ErrReferenceSelectionUnconfigured", err)
	}
}

// TestReferenceErrorCatalog pins every §9.4 code/status pair this phase emits.
func TestReferenceErrorCatalog(t *testing.T) {
	cases := []struct {
		sentinel error
		code     string
		status   int
	}{
		{ErrReferenceSourceCountTooLow, "REFERENCE_SOURCE_COUNT_TOO_LOW", 400},
		{ErrReferenceSourceCountTooHigh, "REFERENCE_SOURCE_COUNT_TOO_HIGH", 400},
		{ErrReferenceSourceDuplicate, "REFERENCE_SOURCE_DUPLICATE", 400},
		{ErrReferenceSourceInvalid, "REFERENCE_SOURCE_INVALID", 400},
		{ErrReferenceSourceNotFound, "REFERENCE_SOURCE_NOT_FOUND", 404},
		{ErrReferenceSourceDeleted, "REFERENCE_SOURCE_DELETED", 410},
		{ErrReferenceSourceSystemForbidden, "REFERENCE_SOURCE_SYSTEM_FORBIDDEN", 400},
		{ErrReferenceTreeMismatch, "REFERENCE_TREE_MISMATCH", 400},
		{ErrReferencePrimaryNotSelected, "REFERENCE_PRIMARY_NOT_SELECTED", 400},
		{ErrReferenceContextBudgetExceeded, "REFERENCE_CONTEXT_BUDGET_EXCEEDED", 422},
		{ErrReferenceSelectionTokenInvalid, "REFERENCE_SELECTION_TOKEN_INVALID", 400},
		{ErrReferenceSelectionTokenExpired, "REFERENCE_SELECTION_TOKEN_EXPIRED", 410},
		{ErrReferenceSelectionStale, "REFERENCE_SELECTION_STALE", 409},
		{ErrReferenceParentInvariant, "REFERENCE_PARENT_INVARIANT", 409},
		{ErrReferenceRequestIDConflict, "REFERENCE_REQUEST_ID_CONFLICT", 409},
	}
	for _, tc := range cases {
		err := NewReferenceAPIError(tc.sentinel, "")
		apiErr, ok := ReferenceErrorFrom(err)
		if !ok {
			t.Fatalf("%v: not a *ReferenceAPIError", tc.sentinel)
		}
		if apiErr.Code != tc.code || apiErr.Status != tc.status {
			t.Errorf("catalog mismatch: got %d %s, want %d %s", apiErr.Status, apiErr.Code, tc.status, tc.code)
		}
		if !errors.Is(err, tc.sentinel) {
			t.Errorf("%s: errors.Is no longer matches the sentinel", tc.code)
		}
		if apiErr.Message == "" {
			t.Errorf("%s: catalog message is empty", tc.code)
		}
	}
}

// TestReferenceBudgetArithmetic covers the §6.2 budget rules.
func TestReferenceBudgetArithmetic(t *testing.T) {
	// reference_budget = min(floor(P*0.50), 16384)
	if got := referenceAvailableBudget(16384); got != 8192 {
		t.Errorf("available(16384) = %d, want 8192", got)
	}
	if got := referenceAvailableBudget(100000); got != 16384 {
		t.Errorf("available(100000) = %d, want the 16384 cap", got)
	}
	if got := referenceAvailableBudget(0); got != referenceDefaultProfileBudget/2 {
		t.Errorf("available(0) = %d, want the default budget half", got)
	}

	sources := []referenceSource{
		{ID: uuid.New(), Content: "short source"},
		{ID: uuid.New(), Content: "another short source"},
	}
	preview, available, err := referenceBudgetPreview(sources, 16384)
	if err != nil {
		t.Fatalf("referenceBudgetPreview() error = %v", err)
	}
	if available != 8192 || preview.MinimumRequiredTokens != 512 || !preview.Fits {
		t.Fatalf("unexpected preview: %+v (available %d)", preview, available)
	}

	// A budget that cannot reserve 256 tokens per source is rejected with 422
	// rather than silently omitting a source (§15 scenario 16).
	_, _, err = referenceBudgetPreview(sources, 100)
	if !errors.Is(err, ErrReferenceContextBudgetExceeded) {
		t.Fatalf("underbudget error = %v, want ErrReferenceContextBudgetExceeded", err)
	}
	if apiErr, ok := ReferenceErrorFrom(err); !ok || apiErr.Status != 422 {
		t.Fatalf("underbudget envelope = %+v, want 422", apiErr)
	}

	// Twenty sources need 5,120 tokens (§15 scenario 2).
	twenty := make([]referenceSource, 20)
	for i := range twenty {
		twenty[i] = referenceSource{ID: uuid.New(), Content: "x"}
	}
	if _, _, err := referenceBudgetPreview(twenty, 16384); err != nil {
		t.Fatalf("twenty sources must fit an 8192-token selected-source budget: %v", err)
	}
}

// TestIsSyntheticMergePoint pins the §8.1 all-same-branch rule.
func TestIsSyntheticMergePoint(t *testing.T) {
	shared := uuid.New()
	span := &BranchSpanMetadata{
		CommonAncestorID: uuid.New(),
		SourceBranches: []ReferenceBranchSource{
			{SourceID: uuid.New(), BranchRootID: shared},
			{SourceID: uuid.New(), BranchRootID: shared},
		},
	}
	if IsSyntheticMergePoint(span) {
		t.Fatal("identical branch roots must not be a synthetic merge point")
	}
	span.SourceBranches[1].BranchRootID = uuid.New()
	if !IsSyntheticMergePoint(span) {
		t.Fatal("divergent branch roots must be a synthetic merge point")
	}
	if IsSyntheticMergePoint(nil) {
		t.Fatal("a nil span is not a merge point")
	}
}

// TestReferenceRepoErrorMapping proves the repository's typed reference
// errors reach the §9.4 catalog rather than a generic 500.
func TestReferenceRepoErrorMapping(t *testing.T) {
	cases := []struct {
		in   error
		want error
	}{
		{db.ErrReferenceParentInvariant, ErrReferenceParentInvariant},
		{db.ErrMultipleParents, ErrReferenceParentInvariant},
		{db.ErrSystemNodeParentForbidden, ErrReferenceParentInvariant},
		{db.ErrReferenceTargetType, ErrReferenceParentInvariant},
		{db.ErrReferenceSourceCount, ErrReferenceParentInvariant},
		{db.ErrReferenceSourceDuplicate, ErrReferenceParentInvariant},
	}
	for _, tc := range cases {
		err := referenceRepoError(tc.in)
		if !errors.Is(err, tc.want) {
			t.Errorf("referenceRepoError(%v) = %v, want %v", tc.in, err, tc.want)
		}
		if _, ok := ReferenceErrorFrom(err); !ok {
			t.Errorf("referenceRepoError(%v) did not produce a catalog error", tc.in)
		}
	}
}
