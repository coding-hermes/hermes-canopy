# Hermes Canopy — Canopy OS

Graph-native collaboration surface for human-agent work. Every message is a node in a DAG. Every model call has a visible context manifest. Every Card is a graph node with structured data.

## First Customer
Technical power users working with AI agents across multi-session projects who need to resume work in <30 seconds without manually reconstructing context.

## Core Concepts
1. **Conversation DAG** — Messages are nodes. Edges are reply, fork, or synthesis. Multi-parent nodes synthesize from multiple sources.
2. **Context Compiler** — Transparently assembles a budgeted, auditable context for every model call. Visible manifest shows exactly what was sent.
3. **View Modes** — Graph Overview, Thread Focus, Synthesis View. Fluid transitions, not hierarchy levels.
4. **Cards** — Graph nodes with structured data and interactive behavior. Three built-in in MVP: File, Task, Code.
5. **Topics** — Named, searchable subgraphs with #references. Context compiler resolves references per budget rules.

## Architecture
- **Backend:** Go (canopyd) — single binary, built-in HTTP server
- **Frontend:** React + TypeScript + Vite — PWA with Service Worker
- **Graph DB:** PostgreSQL (authoritative) + Yjs/IndexedDB (local replica)
- **Card DB:** DuckDB in-process + JSONL files (git-friendly)
- **Transport:** SSE (server→client) + HTTP POST (client→server)
- **Encryption:** None in MVP (local data). MLS post-MVP for multi-user.
- **Plugin Sandbox:** Sandboxed iframes + CSP + capability-scoped APIs
- **Deployment:** `canopyd serve` + local PostgreSQL + PWA in browser

## MVP Scope
Single-user, desktop-first PWA + local server. Branch from any message. Multi-node synthesis. Searchable topics with #references. Visible context manifest + token budget. Three Cards (File, Task, Code). Import/export. Basic plugin sandbox.

## Deferred (Post-MVP)
Multi-user collaboration, approval gates, arbitrary JS plugins, multi-agent federation, MLS encryption, multi-user CRDTs, all deployment modes beyond local server.

## Terminology (Post-Review)
- **DAG** (data model), **tree** (UI metaphor) — not interchangeable
- **View modes** — not "levels"
- **Context compiler** — not "tree IS memory"
- **Activity trace** — not "chain-of-thought"
- **Synthesis node** — not "merge"
- **Sandboxed iframes + CSP** — not "shadow DOM" for security

## Specs
See `specs/` directory.

## Tasks
See `.coding-hermes/tasks.md`.

## Vision
See `vision-brief.html` — Product Vision & Architecture Brief v2.0 with 4 embedded mockups.
