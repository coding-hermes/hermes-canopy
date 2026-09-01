package transport

import (
	"testing"
	"time"
)

func TestTransportHardeningTimeoutConstants(t *testing.T) {
	if OutboundRequestTimeout != 15*time.Second {
		t.Fatalf("outbound timeout = %v", OutboundRequestTimeout)
	}
	if ReconnectBackoffMax != 30*time.Second {
		t.Fatalf("reconnect cap = %v", ReconnectBackoffMax)
	}
}
