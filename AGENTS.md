# Hermes Canopy — Canopy OS

Graph-native collaboration surface for human-agent work. Every message is a node in a DAG. Every model call has a visible context manifest. Every Card is a graph node with structured data.

## First Customer
Technical power users working with AI agents across multi-session projects who need to resume work in <30 seconds without manually reconstructing context.

## Core Concepts
1. **Conversation DAG** — Messages are nodes. Edges are reply, fork, or synthesis. Multi-parent nodes synthesize from multiple sources.
2. **Context Compiler** — Transparently assembles a budgeted, auditable context for every model call. Visible manifest shows exactly what was sent.
3. **View Modes** — Graph Overview, Thread Focus, Synthesis View. Fluid transitions, not hierarchy levels.
4. **Cards** — Graph nodes with structured data and interactive behavior. Three built-in in MVP: compact, expanded, iteration.
5. **Topics** — Named, searchable subgraphs with #references. Context compiler resolves references per budget rules.

## Architecture
- **Backend:** Go (canopyd) — single binary, built-in HTTP server
- **Frontend:** React + TypeScript + Vite — PWA with Service Worker
- **Graph DB:** PostgreSQL (authoritative today) + Yjs/IndexedDB (local replica). Ruling 2026-09-16: SQLite-first (`modernc.org/sqlite`, pure Go, WAL) is the declared direction — tracked as board row GAP-076, **not landed**.
- **Card DB:** per-type SQLite databases (`modernc.org/sqlite`, CGo-free) under `~/.hermes/canopy/cards/`, overridable with `CANOPY_CARD_DATA_DIR`. `internal/card/duckdb/` is ARCHIVED — cgo-only, zero importers repo-wide. Cards do not use DuckDB and there is no card JSONL path.
- **Transport:** SSE (server→client) + HTTP POST (client→server)
- **Encryption:** MLS group encryption for workspace collaboration has SHIPPED (SPEC-FTR-03, mounted at `/api/v1/workspaces/{workspace_id}/mls`). Local node/edge/card data at rest is still unencrypted — this is not end-to-end encryption of all data.
- **Plugin Sandbox:** Sandboxed iframes + CSP + capability-scoped APIs
- **Deployment:** `canopyd serve` + local PostgreSQL + PWA in browser

## MVP Scope
Single-user, desktop-first PWA + local server. Branch from any message. Multi-node synthesis. Searchable topics with #references. Visible context manifest + token budget. Three Cards (compact, expanded, iteration). Import/export. Basic plugin sandbox.

## Shipped beyond the MVP framing
Workspace CRUD, profiles, workspace channels, membership checks and MLS group encryption (SPEC-FTR-01/03) are live behind auth. The primary UX is still desktop-first single-user, but the multi-user surfaces are no longer "deferred".

## Deferred (Post-MVP)
The full multi-user collaboration UX and multi-user CRDTs (the workspace/profile/channel/MLS surfaces listed above have shipped), approval gates, arbitrary JS plugins, multi-agent federation, all deployment modes beyond local server.

## Terminology (Post-Review)
- **DAG** (data model), **tree** (UI metaphor) — not interchangeable
- **View modes** — not "levels"
- **Context compiler** — not "tree IS memory"
- **Activity trace** — not "chain-of-thought"
- **Synthesis node** — not "merge"
- **Sandboxed iframes + CSP** — not "shadow DOM" for security

## Commit Rules
- Every commit MUST include `Co-authored-by: Alexis Okuwa <wojonstech@gmail.com>`
- A `.gitmessage` template is configured in the repo — `git commit` auto-includes the co-author
- Never commit secrets, tokens, or passwords

## Specs
See `specs/` directory.

## Tasks
See `.coding-hermes/board/tasks.jsonl` (JSONL canonical).

## Vision
See `vision-brief.html` — Product Vision & Architecture Brief v2.0 with 4 embedded mockups.
