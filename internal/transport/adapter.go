// Package transport defines the abstract transport interface for Canopy.
//
// Every transport (SSE, WebRTC, NATS, Redis, Relay) implements the
// TransportAdapter interface defined here. The sync engine and
// ConnectionManager import only this package; they never import
// transport-specific libraries.
//
// SPEC-FTR-04 — Multi-Transport Architecture is the authoritative reference.
package transport

// This file exists so the package doc comment is co-located with the
// package declaration. Concrete type definitions are split across:
//   - transport.go     — TransportAdapter, Connection, ConnectOptions, enums, errors
//   - message.go       — Message, Opcode, per-opcode payload types
//   - selector.go      — TransportSelector, DeploymentMode, NetworkTopology
//   - connection_manager.go — ConnectionManager, MessageQueue, RateLimiter
//   - sse_adapter.go   — SSEAdapter (primary MVP implementation)
//   - stub_adapters.go — NATS/WebRTC/Redis/Relay stubs
//   - events.go        — SSE event helpers for transport status/error/degradation
