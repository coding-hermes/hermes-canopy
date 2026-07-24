<!--
  ⚠️  BOARD FORMAT — coding-hermes-model-router v1.3 (2026-07-24)
  All tasks MUST use matrix format: | ID | Task | Pri | Cpx | Deps | Tags | Model | Reasoning | Fallback |
  Before editing this file, load the skill: skill_view(name='coding-hermes-model-router')
  Validate: python3 ~/.hermes/scripts/validate-board-format.py .coding-hermes/tasks.md
  NEVER remove the matrix header row or NEVER-DONE / E2E-001 fixtures.
-->

# Hermes Canopy — Model Router Task Matrix

> **Core purpose:** Hermes-native knowledge canopy — collaborative tree-structured knowledge with multi-agent approval, offline-first CRDT sync, MLS encryption, and plugin-based extension cards. Canvas for agent-visible memory.
> **Language:** Go (backend) + TypeScript/React (frontend) | **CI:** GitHub Actions
> **Status:** Phase 4 backend complete (BE-01→BE-18). BE-12a scaffold done (docker-compose + testutil + uuidv7 fix). BE-12b→f integration suite next.
> **DuckBrain:** hermes-canopy namespace (populated tick 2026-07-24-16-07 — status, bugs, tasks, architecture, CI)

## Active Tasks

| ID | Task | Pri | Cpx | Deps | Tags | Model | Lvl | Fallback |
|----|------|-----|-----|------|------|-------|-----|----------|
| **Phase 4: Backend** | | | | | | | | |
| ✅ BE-12a | Integration test framework scaffolded & verified (docker-compose PG port 5437, migration runner, SkipIfNoDB, TruncateAll — uuidv7() bug fixed, table name mismatches corrected: tree_snapshots not snapshots, profile_route not profile_routes. All 2 integration tests PASS) | High | 3 | BE-11d | ++testing, ++infra, +docker | DeepSeek V4 Flash | Medium | Step 3.7 Flash |
| BE-12b | API-level integration: tree, node, edge CRUD via real HTTP + DB | High | 4 | BE-12a | ++testing, ++api-use, ++backend | DeepSeek V4 Pro | Medium | GLM-5.2 |
| BE-12c | Auth & approval integration: JWT flow, user creation, approval lifecycle | High | 3 | BE-12a | ++testing, ++security, ++auth | DeepSeek V4 Pro | Medium | GLM-5.2 |
| BE-12d | MLS integration: group creation, membership, encryption via real DB | High | 4 | BE-10d, BE-12a | ++testing, ++security, ++encryption | GLM-5.2 | High | DeepSeek V4 Pro |
| BE-12e | Transport integration: SSE hub, connection lifecycle, rate limiting | Medium | 3 | BE-09d, BE-12a | ++testing, ++sse, ++transport | DeepSeek V4 Pro | Medium | Step 3.7 Flash |
| BE-12f | GitHub Actions CI workflow with PostgreSQL service container | Medium | 2 | BE-12a | ++infra, ++ci | DeepSeek V4 Flash | Low | Step 3.7 Flash |
| ✅ BE-13a | Fix missing workspaces table migration — P0 blocking | Critical | 2 | — | ++debugging, ++sql | DeepSeek V4 Pro | Medium | GLM-5.2 |
| ✅ BE-13b | Fix canopy_app role migration — P0 blocking | Critical | 2 | — | ++debugging, ++sql | DeepSeek V4 Pro | Medium | GLM-5.2 |
| ✅ BE-13c | Fix now() in index predicate (PATCHED — verified) | Medium | 1 | — | ++sql, ++testing | DeepSeek V4 Flash | Minimal | Step 3.7 Flash |
|| ✅ BE-14 | Implement /api/topics endpoints (full CRUD: repo + service + handler + migration + parseIntParam fix + server wiring) | High | 4 | BE-04 | ++backend, ++api, ++code-generation | DeepSeek V4 Pro | High | GLM-5.2 |
||| ✅ BE-15 | Implement /api/cards endpoints (SQLite-backed card subsystem: internal/card/ package, handler, wiring) | High | 4 | BE-04 | ++backend, ++api, ++code-generation | DeepSeek V4 Pro | High | GLM-5.2 |
|| ✅ BE-16 | Implement /api/graph endpoints (GraphService impl: subtree, ancestors, stats over nodes/edges) | High | 4 | BE-04 | ++backend, ++api, ++code-generation | GLM-5.2 | High | DeepSeek V4 Pro |
|| ✅ BE-17 | Wire extractActorID to JWT claims (returns uuid.Nil — auth blocked) | Critical | 3 | BE-07 | ++security, ++auth, ++backend | DeepSeek V4 Pro | High | GPT-5.6 Sol |
|| ✅ BE-18 | Wire SSE broadcast in node_service.go (Create, Update, SoftDelete) | Medium | 2 | BE-05 | ++backend, ++sse | DeepSeek V4 Flash | Medium | Step 3.7 Flash |
| **Phase 5: Frontend** | | | | | | | | |
| FE-01 | Project scaffold (Vite + React + TypeScript + Tailwind) | High | 2 | — | ++frontend, ++typescript, ++scaffold | DeepSeek V4 Flash | Medium | Hy3 |
| FE-02 | Tree data store (Yjs CRDT + React Flow integration) | High | 5 | FE-01 | ++frontend, ++crdt, ++typescript | DeepSeek V4 Pro | High | GLM-5.2 |
| FE-03 | Tree rendering engine (React Flow + d3-hierarchy layout + Canvas fallback) | High | 5 | FE-02 | ++frontend, ++visualization, ++react | DeepSeek V4 Pro | High | GLM-5.2 |
| FE-04 | Navigation system (pan, zoom, search, breadcrumbs, minimap) | Medium | 3 | FE-03 | ++frontend, ++ui, ++react | Hy3 | Medium | DeepSeek V4 Flash |
| FE-05 | Message composer (rich text, file attachments, agent context pinning) | High | 3 | FE-01 | ++frontend, ++ui, ++react | Hy3 | Medium | DeepSeek V4 Pro |
| FE-06 | Approval panel (pending items, approve/deny, diff view, audit trail) | Medium | 3 | FE-01, BE-07 | ++frontend, ++ui, ++react | DeepSeek V4 Pro | Medium | GLM-5.2 |
| FE-07 | Multi-user features (presence, cursors, permissions, share dialog) | Medium | 4 | FE-02 | ++frontend, ++multi-user, ++crdt | DeepSeek V4 Pro | High | GLM-5.2 |
| FE-08 | Agent context visualization (thinking cards, iteration cards, search results) | Medium | 4 | SPEC-PL-04, FE-05 | ++frontend, ++ui, ++react | Hy3 | Medium | DeepSeek V4 Pro |
| FE-09 | Offline mode (Service Worker + y-indexeddb + Background Sync) | Low | 5 | FE-02 | ++frontend, ++offline, ++service-worker | DeepSeek V4 Pro | High | GPT-5.6 Sol |
| FE-10 | Accessibility (WCAG 2.1 AA, keyboard nav, screen reader) | Medium | 3 | FE-03 | ++frontend, ++accessibility, ++ui | Hy3 | Medium | DeepSeek V4 Flash |
| FE-11 | Frontend integration tests (Playwright + vitest) | Medium | 3 | FE-03 | ++testing, ++frontend, ++e2e | Step 3.7 Flash | Medium | DeepSeek V4 Flash |
| **Phase 6: Integration** | | | | | | | | |
| INT-01 | End-to-end tree flow (create → edit → merge → approve) | High | 4 | BE-12b, FE-03 | ++testing, ++e2e, ++integration | Step 3.7 Flash | High | DeepSeek V4 Pro |
| INT-02 | Multi-user integration (2+ users, concurrent edits, CRDT merge) | Medium | 4 | FE-07, BE-07 | ++testing, ++multi-user, ++crdt | DeepSeek V4 Pro | High | GLM-5.2 |
| INT-03 | Multi-profile integration (switch profiles, isolated trees, routing) | Low | 3 | BE-08 | ++testing, ++multi-profile | DeepSeek V4 Pro | Medium | Step 3.7 Flash |
| INT-04 | Offline sync integration (offline → edit → reconnect → merge) | Low | 5 | FE-09 | ++testing, ++offline, ++sync | DeepSeek V4 Pro | High | GPT-5.6 Sol |
| INT-05 | Performance baseline (render 2000 nodes, 50 concurrent SSE, latency p99) | Medium | 3 | INT-01 | ++performance, ++benchmark | DeepSeek V4 Pro | Medium | GLM-5.2 |
| INT-06 | CLI wiring (hermes canopy tree — create/list/delete/navigate) | Low | 2 | BE-04 | ++cli, ++terminal | DeepSeek V4 Flash | Low | Step 3.7 Flash |
| **Phase 7: Testing** | | | | | | | | |
| TEST-01 | Unit test coverage (target 80%+ backend, 70%+ frontend) | Medium | 3 | BE-12b, FE-03 | ++testing, ++coverage | Step 3.7 Flash | Medium | DeepSeek V4 Pro |
| TEST-02 | Integration test suite (docker-compose, full API surface) | Medium | 4 | BE-12f, INT-01 | ++testing, ++integration | Step 3.7 Flash | Medium | DeepSeek V4 Pro |
| TEST-03 | Chaos & resilience (kill backend, network partition, DB outage) | Low | 4 | INT-01 | ++testing, ++chaos, ++resilience | DeepSeek V4 Pro | High | GLM-5.2 |
| TEST-04 | Security audit (MLS key rotation, JWT expiry, auth bypass attempts) | Medium | 4 | BE-10d, BE-07 | ++testing, ++security, ++audit | GLM-5.2 | High | GPT-5.6 Sol |
| TEST-05 | Accessibility audit (axe-core, manual screen reader, keyboard-only) | Low | 3 | FE-10 | ++testing, ++accessibility | Step 3.7 Flash | Medium | DeepSeek V4 Flash |
| **Phase 8: Deployment** | | | | | | | | |
| DEPLOY-01 | Docker + Compose + WebUI Native Binary | High | 3 | BE-12f, FE-03 | ++infra, ++docker, ++deploy | DeepSeek V4 Flash | Medium | Step 3.7 Flash |
| DEPLOY-02 | Observability (Prometheus + Grafana + structured logging + traces) | Medium | 3 | BE-05 | ++observability, ++monitoring | DeepSeek V4 Pro | Medium | GLM-5.2 |
| DEPLOY-03 | CI/CD (GitHub Actions: test → build → deploy → smoke test) | Medium | 3 | BE-12f | ++infra, ++ci | DeepSeek V4 Flash | Medium | Step 3.7 Flash |
| DEPLOY-04 | Documentation (README, API docs, deploy guide, architecture overview) | Low | 2 | — | ++documentation | DeepSeek V4 Flash | Low | GPT-5.6 Terra |
| DEPLOY-05 | Migration plan (existing Hermes data → canopy trees) | Low | 3 | BE-04 | ++planning, ++migration | DeepSeek V4 Pro | Medium | GLM-5.2 |
| **Phase 9: Distribution** | | | | | | | | |
| DIST-01 | Multi-tenant + Multi-transport isolation | Low | 4 | BE-09d | ++multi-tenant, ++transport | DeepSeek V4 Pro | High | GLM-5.2 |
| DIST-02 | Self-host guide (single binary, env vars, TLS, backup) | Low | 2 | DEPLOY-01 | ++documentation | DeepSeek V4 Flash | Low | GPT-5.6 Terra |
| DIST-03 | Open source readiness (LICENSE, CONTRIBUTING, CoC, issue templates) | Low | 1 | — | ++documentation | DeepSeek V4 Flash | Minimal | GPT-5.6 Terra |
| **Continuous** | | | | | | | | |
| INFRA-001 | Fix tick storm: cooldown < tick_timeout (mitigated, needs root fix) | Critical | 1 | — | — | ADMIN — scheduler-level guard | — | — |
| E2E-001 | E2E Testing Tick (self-improving loop) 🔁 Recurring every 5-10 ticks | High | 4 | server running | ++browser, ++screenshots, ++verification | GPT-5.6 Luna | High | Step 3.7 Flash |
| NEVER-DONE | 11-point audit sweep | High | 2 | — | ++code-review, +testing | DeepSeek V4 Pro | Medium | GLM-5.2 |

## Completed (Phases 1-4, Migration Fixes, JWT Wiring)

All specs + backend implementation complete. 17 backend tasks (BE-01→BE-11d + BE-13a/b/c + BE-17), 29 specs across Phases 1-3d.

| Phase | Purpose | Key outcomes |
|-------|---------|--------------|
| P1: Architecture | Research & validation (SSE, Yjs, React Flow, MLS, offline stack) | 9 specs, confirmed architecture |
| P2: Data Model | Tree node/edge DDL, snapshot/delta model, approval & audit trail | 4 spec files |
| P3: API Specs | SSE event stream, tree/node/edge CRUD, merge, approval, profile, errors | 7 spec files |
| P3b: Topics | Topic data model, auto-detection, search, #reference, lifecycle | 5 spec files |
| P3c: Plugins/Cards | JS plugin system, file viewers, app cards, iteration cards, calendar, multi-ref | 6 spec files |
| P3d: Post-MVP | Multi-user collaboration, federated agents, MLS encryption, multi-transport, SaaS relay, native packaging, gateway integration | 7 spec files |
| P4: Backend | Go gateway — scaffold, DB layer, tree/node/edge services, SSE hub, sync engine, auth/approval, profile routing, multi-transport, MLS encryption, middleware | 15 tasks (BE-01→BE-11d, BE-13a/b/c), ~15K lines |
| P0 Fixes | Migration gaps & JWT wiring: workspaces table, canopy_app role, now() predicate, extractActorID -> UserIDFromContext | 4 migration files, 1 handler fix |

## Assumptions

- Go 1.23+ backend, TypeScript/React frontend (not yet scaffolded)
- PostgreSQL via docker-compose for integration tests
- Yjs CRDT with SSE transport for real-time sync
- MLS encryption (mls-rs) for group messaging
- React Flow + d3-hierarchy for tree rendering
- BE-13a/b/c and BE-17 resolved (migration fixes + JWT wiring deployed)
- INFRA-001 (tick storm) mitigated but root cause not fixed

## Routing Notes

- **Go backend tasks (BE-*):** DeepSeek V4 Pro primary for moderate complexity ($0.44/1M), GLM-5.2 for autonomous/SWE-bench tasks ($0.30/1M), V4 Flash for mechanical ($0.10/1M)
- **TypeScript/React frontend (FE-*):** Hy3 primary for UI/HTML/CSS (flat-rate), V4 Pro for complex state management, Step 3.7 Flash for tests ($0.09/1M)
- **Security-critical tasks (BE-13a/b, BE-17):** V4 Pro primary, escalate to GPT-5.6 Sol if auth architecture changes needed
- **Testing tasks (TEST-*, INT-*, E2E):** Step 3.7 Flash primary ($0.09/1M), Luna for browser/screenshots ($100/mo flat)
- **Spec/doc tasks (DEPLOY-04, DIST-02/03):** V4 Flash for mechanical docs, Terra for structured documentation

## Execution Order

|1. ✅ **P0 blockers resolved:** BE-13a → BE-13b → BE-13c → BE-17 ✅
|2. ✅ **BE-14 completed:** topic CRUD (repo, service, handler, migration, wiring). **BE-15/16 implemented:** cards (SQLite) + graph (subtree/ancestors/stats). **BE stubs deployed:** none remaining.
||3. ✅ **BE-18 completed:** SSE broadcast in node_service.go (Create, Update, SoftDelete)
||4. **BE integration:** BE-12a → BE-12b/BE-12c/BE-12d/BE-12e (parallel) → BE-12f
|5. **FE scaffold:** FE-01 → FE-02 → FE-03 (sequential — CRDT then rendering)
|6. **FE parallel:** FE-04/FE-05/FE-06/FE-07 (after FE-02)
|7. **Integration:** INT-01 (after BE-12b + FE-03) → INT-02/INT-03/INT-04/INT-05 (parallel)
|8. **Testing/Hardening:** TEST-01/TEST-02/TEST-03/TEST-04/TEST-05 (after INT-01)
|9. **Deploy:** DEPLOY-01 → DEPLOY-02/DEPLOY-03 (parallel) → DEPLOY-04/DEPLOY-05
|10. **Distribution:** DIST-01 → DIST-02/DIST-03

## Escalation Conditions

- BE-13a/13b migration fixes break existing data → CRITICAL, escalate to GPT-5.6 Sol
- BE-17 JWT extraction reveals auth architecture gap → escalate to GPT-5.6 Sol
- FE-02 CRDT integration fails with React Flow → escalate to V4 Pro High
- FE-09 offline mode complexity exceeds scope → reassess vs. post-MVP
- INFRA-001 tick storm reoccurs → escalate to Bane (scheduler root fix)
- Any security task (BE-17, TEST-04) reveals architectural vulnerability → CRITICAL, escalate to GPT-5.6 Sol Max
