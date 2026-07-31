# Changelog

## [Unreleased]

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
- Multi-user integration: concurrent edits, CRDT merge, presence
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
- Playwright E2E tests: 41/41 PASS

### Phase 4 — Backend
- Go gateway with PostgreSQL, SSE hub, sync engine
- Tree/node/edge CRUD with approval workflows
- MLS encryption group management
- Topics, Cards (SQLite), Graph (subtree/ancestors/stats)
- JWT authentication with dev mode auto-injection
- Multi-transport: SSE, WebSocket, NATS/RTC/Redis stubs

## Versioning

This project follows [Semantic Versioning](https://semver.org/). The current development version is pre-1.0.
