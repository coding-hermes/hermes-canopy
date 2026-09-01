package federation

import "testing"

func TestVectorClockCompare(t *testing.T) {
	tests := []struct {
		name  string
		left  VectorClock
		right VectorClock
		want  ClockComparison
	}{
		{"equal", VectorClock{"a": 1}, VectorClock{"a": 1}, Equal},
		{"before", VectorClock{"a": 1}, VectorClock{"a": 2, "b": 1}, Before},
		{"after", VectorClock{"a": 3, "b": 1}, VectorClock{"a": 2}, After},
		{"concurrent", VectorClock{"a": 2}, VectorClock{"b": 2}, Concurrent},
		{"missing zero equals", VectorClock{"a": 1}, VectorClock{"a": 1, "b": 0}, Equal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.left.Compare(tt.right); got != tt.want {
				t.Fatalf("Compare() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVectorClockIncrementAndMerge(t *testing.T) {
	clock := VectorClock{"a": 1}
	clock.Increment("a")
	clock.Increment("b")
	merged := clock.Merge(VectorClock{"a": 1, "b": 4, "c": 2})
	if clock["a"] != 2 || merged["a"] != 2 || merged["b"] != 4 || merged["c"] != 2 {
		t.Fatalf("clock = %v, merged = %v", clock, merged)
	}
}
