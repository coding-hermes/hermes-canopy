package federation

// ClockComparison describes the causal relationship between two vector clocks.
type ClockComparison int

const (
	Before ClockComparison = iota
	After
	Equal
	Concurrent
)

// VectorClock is a set of monotonically increasing counters keyed by peer ID.
type VectorClock map[string]int64

func (c VectorClock) Increment(peerID string) {
	c[peerID]++
}

func (c VectorClock) Merge(other VectorClock) VectorClock {
	merged := make(VectorClock, len(c)+len(other))
	for peer, counter := range c {
		merged[peer] = counter
	}
	for peer, counter := range other {
		if counter > merged[peer] {
			merged[peer] = counter
		}
	}
	return merged
}

func (c VectorClock) Compare(other VectorClock) ClockComparison {
	less, greater := false, false
	for peer, counter := range c {
		if counter < other[peer] {
			less = true
		} else if counter > other[peer] {
			greater = true
		}
	}
	for peer, counter := range other {
		if _, ok := c[peer]; !ok && counter > 0 {
			less = true
		}
	}
	switch {
	case less && greater:
		return Concurrent
	case less:
		return Before
	case greater:
		return After
	default:
		return Equal
	}
}
