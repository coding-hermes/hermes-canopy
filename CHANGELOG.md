# Changelog

## [Unreleased]

### Phase 11 — Real-Time Wiring & Anti-Phantom Program (2026-08-08/09)
- Real-time sync (WIRE-001): `/api/v1/events` SSE + frontend EventSource + Yjs pushUpdate — de-stubbed BUG-024
- Context manifest panel (WIRE-002): token budget + ancestry from `/api/v1/context/{node_id}` in node detail UI
- Hermes session ingestion (WIRE-003): auto-import real sessions from `~/.hermes/state.db` into Canopy trees
- Real share + presence endpoints (WIRE-004): POST `/trees/{id}/share` + presence/leave — ShareDialog/usePresence de-stubbed
- Live backend status pill (WIRE-005): header label derived from `/health`
- Association layer (WIRE-006): session lineage (parent/child/delegation goals) + task/commit/project links in tree metadata, `GET /trees/{id}` Related + idempotent `associations-backfill` CLI
- Anti-phantom E2E regressions (TEST-REAL-001..003): two-context realtime sync, composer→canvas (would have caught BUG-032), context manifest UI — zero mocks, real wiring
- Workspace surface (SPEC-023-UI-001..004): SSE workspace channels, React workspace view, agent roster, PR review panel — unblocked helix UI-001..004 (GAP-017)
- Design-system overhaul (UI-01..UI-10): dark navy theme + tokens, topics sidebar rail, header upgrade, branching canvas (glow connectors, ghost nodes), node card redesign, composer bar, keyboard shortcuts, node-list hierarchy, visual-regression baseline (4 mockup goldens), sidebar consolidation
- Bug fixes: BUG-024 endpoint stubs, BUG-025 flat /nodes access control, BUG-029 root node 503, BUG-030 composer read-only, BUG-031 SSE goroutine leak, BUG-032 Yjs replica bridge, BUG-034 `set_content_hash` bytea crash (real-data), BUG-035 session import idempotency
- Docs/devx sweep (GAP-006..020): compose env var, `make run`, curl walkthrough, Vite proxy port, frontend bootstrap, `make test-short` without PG, README graph API routes, DB_PORT sync, Go 1.25+/PG 17 version drift, license alignment (MIT)
- Current test state (2026-08-09): vitest 583/583, Playwright 48/48, Go integration 46/46 (PG), judge-verified per task

### Phase 10 — Hardening (2026-07-28)
- Fixed MLS key reuse (BUG-013): domain-separated encryption/signing keys
- Fixed unsigned JWT acceptance (BUG-014): explicit alg:none rejection
- Fixed uuid.Nil sentinel author (BUG-015): JWT UserID extraction in all handlers
- Fixed cross-user access (BUG-016): owner-only access checks on tree/node handlers
- Fixed a11y heading hierarchy (BUG-017): proper h1→h2 page structure
- Fixed a11y color contrast (BUG-018): 4.5:1 minimum ratio compliance
- Fixed input validation gaps (BUG-019): content-length enforcement
- Fixed error message leakage (BUG-020): generic error responses, server-side logging
- Fixed config mismatch (BUG-021): CANOPY_DB_URL env var support
- Fixed integration test FK violation (BUG-022): consistent sentinel user IDs

### Phase 7-9 — Testing, Deployment, Distribution
- Unit test coverage at 40.7% (card 70.8%, config 74.1%, mls 80.1%)
- Integration test suite: 23 tests, all PASS
- Chaos & resilience tests: 5/6 PASS
- Security audit: 17 tests, 9 vulnerabilities found and fixed
- Accessibility audit: all 7 pages audited, 20 violations fixed
- Docker support: 52.4MB production image
- Observability: Prometheus + Grafana + structured logging
- CI/CD: GitHub Actions with golangci-lint, gitleaks
- Multi-tenant isolation + WebSocket transport
- Self-host guide, open source readiness (MIT)

### Phase 6 — Integration
- End-to-end tree flow: create → edit → merge → approve
- Multi-user integration: concurrent edits, CRDT merge, presence (aspirational at the time — the real-time surfaces were stubs until WIRE-004 landed 2026-08-09)
- Multi-profile integration: switching, isolation, routing
- Offline sync: 7-step flow with snapshot capture and delta computation
- Performance baseline: 2000 nodes in 49.9s, p99=440µs
- CLI wiring: hermes canopy tree → create/list/delete/navigate

### Phase 5 — Frontend
- React + TypeScript + Vite + Tailwind scaffold
- Yjs CRDT tree store with SSE sync provider
- React Flow canvas with d3-hierarchy layout
- Navigation: pan, zoom, search, breadcrumbs, minimap
- Message composer with rich text and file attachments
- Approval panel with diff view and audit trail
- Multi-user presence, cursors, permissions
- Agent context visualization cards
- Offline mode: Service Worker + IndexedDB + Background Sync
- WCAG 2.1 AA accessibility
- Playwright E2E tests: 48/48 PASS (2026-08-09)

### Phase 4 — Backend
- Go gateway with PostgreSQL, SSE hub, sync engine
- Tree/node/edge CRUD with approval workflows
- MLS encryption group management
- Topics, Cards (SQLite), Graph (subtree/ancestors/stats)
- JWT authentication with dev mode auto-injection
- Multi-transport: SSE, WebSocket, NATS/RTC/Redis stubs

## Versioning

This project follows [Semantic Versioning](https://semver.org/). The current development version is pre-1.0.
