package context

// --- TokenEstimator implementation ------------------------------------------

// charTokenEstimator implements TokenEstimator with a deterministic
// 4-chars-per-token rule. ceil(len([]rune(s)) / 4).
type charTokenEstimator struct{}

// NewTokenEstimator returns a deterministic estimator:
// ceil(len([]rune(s)) / 4) — a conservative 4-chars-per-token rule.
// No external dependency; deterministic across runs (SPEC-TM-004 determinism
// requirement).
func NewTokenEstimator() TokenEstimator {
	return &charTokenEstimator{}
}

// Estimate returns the token count for s using a simple 4-chars-per-token rule.
// The formula is ceil(runeCount / 4). An empty string yields 0.
func (e *charTokenEstimator) Estimate(s string) int {
	rc := len([]rune(s))
	if rc == 0 {
		return 0
	}
	return (rc + 3) / 4 // ceil division
}
