// Unit-level contract for repairFailed's actionable error message
// (QA-HERMES-CANOPY-30): no database needed. Lives in the internal test
// package because repairFailed is deliberately unexported.
package db

import (
	"errors"
	"strings"
	"testing"
)

// TestRepairFailedMessageFormat pins the exact actionable-error contract
// of repairFailed: the dirty version, the "not auto-repaired" phrasing,
// the copy-pasteable manual recipe, and the re-run instruction must all
// be present.
func TestRepairFailedMessageFormat(t *testing.T) {
	err := repairFailed(4, 4, errors.New("boom"))
	for _, want := range []string{
		"db: migrate: dirty version 4 could not be auto-repaired",
		"boom",
		"UPDATE schema_migrations SET dirty=false WHERE version=4",
		"re-run canopyd",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("repairFailed error missing %q:\n%v", want, err)
		}
	}
}
