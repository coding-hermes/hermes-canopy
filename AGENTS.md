# Hermes Canopy

Tree-native Hermes frontend — a chat/collaboration surface built for human-agent collaboration, not human-to-human chat.

## Problem

Existing chat apps (ChatGPT, Claude, etc.) are linear scroll logs built for human-to-human conversation. They fail at:
- Multi-session projects with context connection
- Deep exploration without polluting the main thread
- Multi-user collaboration with shared context
- Visible context management
- Approval workflows ("I'm thinking out loud" vs "execute this")

## Solution

A spatial, tree-native interface where the agent's memory IS the tree. Messages branch. Context is visible, navigable, shareable, and gated.

### Core Concepts
1. **Tree Memory** — conversation is a navigable tree, not linear log. Every message can branch.
2. **Three-Level Navigation** — Macro (full tree), Branch (drill into thread), Merge (side-by-side branches)
3. **Multi-User with Approval Gates** — friends join threads, agent listens but only acts on owner-approved input
4. **Multi-Profile** — different Hermes profiles (coding, creative, research) participate in same tree
5. **Offline-First** — delta sync, SSE transport, progressive loading, bandwidth-aware

## Architecture Decisions (DuckBrain)
- Namespace: `hermes-canopy`
- Language/framework: TBD (research phase)
- Transport: SSE over WebSockets (offline resilience)
- Storage: Local-first with CRDTs (Yjs/Automerge)
- Visual paradigm: Outliner/mind-map, not chat log

## Specs
See `specs/` directory. Each spec follows coding-hermes-specs format (exact interfaces, DDL, error paths, wiring).

## Tasks
See `.coding-hermes/tasks.md` for the autonomous development board.
