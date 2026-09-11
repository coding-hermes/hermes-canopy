// Package relay implements the Canopy store-and-forward relay: an encrypted
// frame protocol (handshake, key rotation, CBOR payloads), a multi-tenant
// session hub with per-tenant rate limiting, and the client used by peers to
// poll and push envelopes over TLS.
package relay
