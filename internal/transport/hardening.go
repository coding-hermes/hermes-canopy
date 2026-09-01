package transport

import "time"

const (
	OutboundRequestTimeout = 15 * time.Second
	ReconnectBackoffMax    = 30 * time.Second
)
