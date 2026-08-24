package collaboration

import (
	"testing"
)

func TestRoleString(t *testing.T) {
	cases := []struct {
		role Role
		want string
	}{
		{RoleViewer, "viewer"},
		{RoleEditor, "editor"},
		{RoleAdmin, "admin"},
		{Role(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.role.String(); got != c.want {
			t.Errorf("Role(%d).String() = %q, want %q", c.role, got, c.want)
		}
	}
}

func TestRoleOrdering(t *testing.T) {
	// The role constants must keep the spec ordering 0=viewer, 1=editor,
	// 2=admin (SPEC-FTR-01 §2 decision 5, §6.1 DDL comment).
	if RoleViewer != 0 || RoleEditor != 1 || RoleAdmin != 2 {
		t.Fatalf("role constants out of order: viewer=%d editor=%d admin=%d",
			RoleViewer, RoleEditor, RoleAdmin)
	}
}
