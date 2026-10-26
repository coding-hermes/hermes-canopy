package main

import "errors"

// errSQLiteFullBootRefused is the fail-closed reason `canopyd serve` reports once
// CANOPY_DB_DRIVER=sqlite has passed the core-graph self-check (store opened,
// embedded migrations applied, schema parity verified).
//
// The sqlite backend is a wave-1 core-graph store: the full server still needs
// PostgreSQL-only repositories (relay, events/snapshots, approvals, MLS,
// topics, transport, plugins, file viewer, federation, profile routing), so a
// full boot on sqlite is refused deliberately — see SQLITE-PIVOT.md.
//
// It is named (rather than an inline string in main) so the refusal is
// greppable and so `canopyd config check` and its tests can state the serve
// outcome from the same text the server actually fails with. `config check`
// validating sqlite is NOT a promise that serve boots on it.
var errSQLiteFullBootRefused = errors.New("SQLite core-graph boot succeeded, but full canopyd boot is refused: PostgreSQL-only repository surfaces remain (relay, events/snapshots, approvals, MLS, topics, transport, plugins, file viewer, federation, and profile routing)")
