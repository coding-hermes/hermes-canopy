package service

import (
	"strings"
	"testing"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/collaboration"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"My Project Tree", "my-project-tree"},
		{"  Leading and trailing  ", "leading-and-trailing"},
		{"UPPER CASE", "upper-case"},
		{"dash-ed name", "dash-ed-name"},
		{"under_score", "under-score"},
		{"multi   spaces", "multi-spaces"},
		{"", "workspace"},
		{"!!!", "workspace"},
		{"Café Naïve", "caf-nave"}, // non-ASCII dropped, adjacent letters merge
		{"-leading-dash", "leading-dash"},
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRandomSuffix(t *testing.T) {
	for _, n := range []int{1, 4, 8} {
		s := randomSuffix(n)
		if len(s) != n {
			t.Errorf("randomSuffix(%d) length = %d, want %d", n, len(s), n)
		}
		for _, r := range s {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789", r) {
				t.Errorf("randomSuffix(%d) contains invalid char %q", n, r)
			}
		}
	}
	// Two draws must differ (collision probability is negligible).
	if randomSuffix(8) == randomSuffix(8) {
		t.Error("randomSuffix(8) returned identical values twice")
	}
}

func TestHashTokenRoundTrip(t *testing.T) {
	token := "abc123XYZ_-"
	h1 := hashToken(token)
	h2 := hashToken(token)
	if h1 != h2 {
		t.Errorf("hashToken not deterministic: %q vs %q", h1, h2)
	}
	if len(h1) != 64 { // SHA-256 hex
		t.Errorf("hashToken length = %d, want 64", len(h1))
	}
	if hashToken(token) == hashToken(token+"x") {
		t.Error("hashToken collision on different inputs")
	}
}

func TestInvitationTTL(t *testing.T) {
	// invitationTTL must be 7 days per the worker brief (spec default).
	if invitationTTL != 7*24*time.Hour {
		t.Errorf("invitationTTL = %v, want 168h", invitationTTL)
	}
}

func TestDefaultApprovalTTL(t *testing.T) {
	// SPEC-FTR-01 §2 decision 12: 5-minute default (300 seconds).
	if DefaultApprovalTTL != 300 {
		t.Errorf("DefaultApprovalTTL = %d, want 300", DefaultApprovalTTL)
	}
}

func TestRoleConstantsMatchCollaboration(t *testing.T) {
	// The service layer must use the collaboration package's role values.
	if int(collaboration.RoleViewer) != 0 || int(collaboration.RoleEditor) != 1 || int(collaboration.RoleAdmin) != 2 {
		t.Error("collaboration role constants out of order")
	}
}
