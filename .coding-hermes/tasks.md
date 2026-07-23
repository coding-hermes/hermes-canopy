# Hermes Canopy — Task Board

|||> **Status:** Phase 1 ✅ (9/9) | Phase 2 ✅ (4/4) | **Phase 3 — API Specs ✅ (7/7)** | **Phase 3b — Topic Specs ✅ (5/5)** | **Phase 3c — Plugin & Card Specs ✅ (6/6)** | **Phase 3d — Post-MVP ✅ (7/7)** | **Phase 4 — Backend: BE-01 ✅ BE-02 ✅ BE-03 ✅ → BE-04 next**
||> **Foreman:** deepseek-v4-flash @ deepseek-foreman  
||> **Last tick:** BE-03 complete — 968-line TreeService (aa0c31a). 16 types, 16 error sentinels, atomic CreateTree tx, ListTrees pagination, GetTree with stats, DeleteTree. Wired to HTTP via TreeHandler.
|> **DuckBrain:** hermes-canopy namespace (23+ entries)

---

## Phase 1: Architecture & Research Validation

**Goal:** Validate stack decisions, research existing solutions, confirm no showstoppers. Output: confirmed architecture document with rationale.

- [x] **T1.1 — Transport Research: SSE vs WebSocket vs NATS** ✅ COMPLETE 2026-07-19
  **Decision: SSE (HTTP/2) primary + NATS backend + WebSocket as future bidirectional fallback.**

- [x] **T1.2 — CRDT Library Evaluation: Yjs vs Automerge** ✅ COMPLETE 2026-07-20
  **Decision: Yjs — 18KB gzipped, pure JS, no WASM, granular observe() for tree re-rendering, 920K/wk downloads.**

- [x] **T1.3 — Tree Visualization Research** ✅ COMPLETE 2026-07-20
  **Decision: React Flow (@xyflow/react v12) primary + d3-hierarchy layout engine + Canvas fallback for >2000 nodes.**

- [x] **T1.4 — Offline-Stack Research** ✅ COMPLETE 2026-07-20
  **Decision: Service Worker (Workbox v7) + y-indexeddb + Custom SSE Provider + Background Sync queue. No SQLite WASM in MVP. Delta Chat as optional post-MVP relay.**

- [x] **T1.5 — Approval UX Research** ✅ COMPLETE 2026-07-20
  **Decision: GitHub triage panel + Linear notification discipline + Google Docs per-item granularity.**

- [x] **T1.6 — WebUI Native App Evaluation** ✅ COMPLETE 2026-07-20
  **Decision: Wails v3 (post-MVP) + Go embed (MVP).**

- [x] **T1.7 — Security Protocol: MLS-Only Architecture** ✅ COMPLETE 2026-07-20

- [x] **T1.8 — Multi-Transport Architecture Design** ✅ COMPLETE 2026-07-20
  **Commit: 8706036**

- [x] **T1.9 — Confirmed Architecture Document** ✅ COMPLETE 2026-07-20
  **Commit: b8e170d**

---

## Phase 2: Data Model Specs

- [x] **SPEC-DM-01 — Tree Node & Edge DDL** ✅ COMPLETE 2026-07-20
  **Commit: 09fa6d1**

- [x] **SPEC-DM-02 — Tree Snapshot & Delta Model** ✅ COMPLETE 2026-07-20
  **Commit: f7d3f6f**

- [x] **SPEC-DM-03 — Approval & Audit Trail DDL** ✅ COMPLETE 2026-07-20
  **Commit: 6caafc6**

- [x] **SPEC-DM-04 — User & Profile Model** ✅ COMPLETE 2026-07-20

---

## Phase 3: API Specs

- [x] **SPEC-API-01 — SSE Event Stream Spec** ✅ COMPLETE 2026-07-20
  **Commit: 6d6c8b4**

- [x] **SPEC-API-02 — Tree CRUD Endpoints** ✅ COMPLETE 2026-07-20
  **Commit: 4f24622**

- [x] **SPEC-API-03 — Node CRUD Endpoints** ✅ COMPLETE 2026-07-20
  **Commit: 5e65fc6**

- [x] **SPEC-API-04 — Merge & Navigation Endpoints** ✅ COMPLETE 2026-07-20
  **Commit: cb18965**

- [x] **SPEC-API-05 — Approval Endpoints** ✅ COMPLETE 2026-07-20
  **Commit: 0e15a03**

- [x] **SPEC-API-06 — Multi-User & Profile Endpoints** ✅ COMPLETE 2026-07-20
  **Commit: 45e3fab**

- [x] **SPEC-API-07 — Error Catalog** ✅ COMPLETE 2026-07-21
  **Commit: 4074b71**

---

## Phase 3b: Topic Management Specs

- [x] **SPEC-TM-01 — Topic Data Model** ✅ COMPLETE 2026-07-21
  **Commit: d2a8168**

- [x] **SPEC-TM-02 — Auto-Topic Detection** ✅ 2026-07-21 (8bce2c0)

- [x] **SPEC-TM-03 — Topic Search & One-Button Context** ✅ 2026-07-21 (f866d0d)

- [x] **SPEC-TM-04 — #Reference Resolution** ✅ 2026-07-21 (e8a14d3)

- [x] **SPEC-TM-05 — Topic Lifecycle & Sidebar** ✅ 2026-07-21 (66beab0)

---

## Phase 3c: Plugin & App Card Specs

**Goal:** Exact specs for JS plugin system, embedded app cards, calendar integration, file viewers. Worker reads these specs and produces correct plugin/card layer.

**Dependencies:** Phase 2 complete (files, apps, and plugins are all tree-addressable).

- [x] **SPEC-PL-01 — JS Plugin System**
  Plugin format: single JS file with manifest (name, version, description, permissions, render_type). Registration: agent sends JS file as message, user clicks "Install", plugin loaded into renderer. Hot-reload: plugin updates instantly propagate to all connected devices (desktop, web, mobile). Sandbox: plugins run in isolated iframe/WebWorker with limited API surface. Permissions: file_access, network, notifications, calendar_read, calendar_write. Plugin registry: namespace to prevent conflicts.
  **Commit: caff298**

|- [x] **SPEC-PL-02 — Built-in File Viewers**
|  **Commit: c7bfa8b**
  Native viewers for: PDF (pdf.js), images (lightbox + zoom), code (Monaco Editor with syntax highlighting), CSV/spreadsheet (handsontable or similar), Markdown (rendered with GFM), JSON (collapsible tree view), audio/video (HTML5 player). File attachment model: attach by reference (already in Hermes filesystem → single canonical copy) or by upload (new file → stored in Hermes). Agent can open/view any file in the knowledge base.

- [x] **SPEC-PL-03 — App Card System + Database-per-Card** ✅ COMPLETE 2026-07-22
  **Commit: cc41acf** — 65KB, 1,121 lines, 16 sections, 3 Mermaid diagrams.
  Card model: {id, app_id, card_type: 'compact'|'expanded'|'iteration', data: JSON, actions: [{label, handler}], created_at, context_hash}. Agent renders cards based on context. Database-per-Card: each type gets own SQLite at `~/.hermes/canopy/cards/{type}.db`. Cards table + events table. REST API: GET/POST/PATCH `/api/cards/{type}/{id}`. Local-first with server sync.

|- [x] **SPEC-PL-04 — Dynamic Thinking Interface (Iteration Cards)** ✅ COMPLETE 2026-07-22
|  **Commit: 10ab311** — 1,530 lines, 43 design decisions, 38 test scenarios, 3 Mermaid diagrams.
|  Five iteration card subtypes: Search (live results + user relevance feedback), Code Exec (stdout/stderr streaming + cancel), File Read (highlight regions), Thinking (collapsible reasoning steps), Tool Call (gated approve/deny). Event flow: agent → SSE → card renderer → user feedback → agent. Feedback bridge with 30s ack timeout. Cancel with SIGTERM/SIGKILL escalation. Agent crash recovery preserved via SQLite durability.

- [x] **SPEC-PL-05 — Calendar Integration** ✅ COMPLETE 2026-07-22
  Calendar viewer card: month/week/day views, event cards with title/time/description/location. Multiple calendar sources: Google Calendar (OAuth), iCloud (CalDAV), local (.ics files in Hermes knowledge base). Agent can: create events, modify events, check availability, propose times. Calendar ↔ auto-responder: agent knows when you're busy and tells others. Calendar ↔ status: "I'm in a meeting until 3" → agent auto-sets busy status.

- [x] **SPEC-PL-06 — Multi-Message Reference Model** ✅ COMPLETE 2026-07-22
  **Commit: d72ccbe** — 67KB, 1,155 lines, 35 design decisions, 35 edge cases, 40 test scenarios.
  Data model extension: a node can have MULTIPLE parent edges (not just one). Multi-reference reply: select N messages → reply → new node with N parent edges of type 'reference'. Visual: colored edges from each source converge into reply. Context: agent sees all referenced messages as unified input. Conflict: if referenced messages are in different branches, the reply node is a synthetic merge point showing which context contributed what.

---

## Phase 3d: Post-MVP Architecture Specs

- [x] **SPEC-FTR-01 — Multi-User Collaboration & Approval Model** ✅ COMPLETE 2026-07-22
  **Commit: 37fe758** — 620 lines, 11 sections, 20 design decisions, Go interfaces, SSE events, API endpoints, security model.
- [x] **SPEC-FTR-02 — Federated Multi-Agent Architecture** ✅ COMPLETE 2026-07-22
  **Commit: 29b61a6** — 482 lines, 11 sections, 20 design decisions, FTL transport protocol, ECDH encryption, profile routing, SSE relay, API endpoints, 3 Go interfaces, 3 DDL tables, 12 edge cases, 14 test scenarios, 7-phase implementation plan.
- [x] **SPEC-FTR-03 — MLS Encryption Model** ✅ COMPLETE 2026-07-22
  **Commit: 4aad8b5** — 589 lines, 42KB, 11 sections, 20 design decisions, 2 Mermaid diagrams, 16 edge cases, 16 test scenarios, 13 security considerations.
- [x] **SPEC-FTR-04 — Multi-Transport Architecture** ✅ COMPLETE 2026-07-22
  **Commit: 3669bef** — 647 lines, 46KB, 10 sections
- [x] **SPEC-FTR-05 — Self-Hosted & SaaS Relay Architecture** ✅ COMPLETE 2026-07-22
  **Commit: 206944f** — 852 lines, 59KB, 12 sections, 20 design decisions, 3 Mermaid diagrams, 4 Go interfaces, 4 DDL tables, 6 API endpoints, 12 edge cases, 14 test scenarios.
|- [x] **SPEC-FTR-06 — WebUI Native Packaging & Distribution** ✅ COMPLETE 2026-07-22
|  **Commit: 1a2a8f6** — 758 lines, 45KB, 12 sections, 20 design decisions, 3 Go interfaces (PackageManager, Updater, InstallerBuilder), JSON Schema (latest.json), full CI/CD workflow YAML, 14 edge cases, 20 test scenarios, 6 security considerations, 5-phase implementation plan.
||- [x] **SPEC-FTR-07 — Hermes Agent Gateway Integration** ✅ COMPLETE 2026-07-23
|  **Commit: c9d489c** — 828 lines, 49KB, 8 sections, 20 design decisions, 6 Go interfaces, 13 API endpoints, 17 edge cases, 3 DDL tables. HermesClient, EventTranslator, ProfileRouter, SkillBridge, AgentSessionManager interfaces. canopyd↔Hermes API contract with SSE relay, profile routing, tool→Card translation, session continuity, and degraded-mode fallback.

---

## Phase 4: Backend (Go Gateway)

- [x] **BE-01 — Project Scaffold** ✅ COMPLETE 2026-07-23
  **Commit: d04f0c3** — 1,245 lines across 21 files: Go module (chi/pgx/migrate/uuid/zerolog), 4 DDL migrations (extensions→trees→nodes→edges), cmd/canopyd/main.go with -ldflags version injection, Makefile (build/test/vet/lint/build-embed×5), CI workflow, Dockerfile, .gitignore, full data layer (models, NodeRepo, TreeRepo, EdgeRepo) per SPEC-DM-01 interfaces. EdgeRepo: 327 lines (408f5c6). Total: 1,572 lines scaffold.
|- [x] **BE-02 — Database Layer** ✅ COMPLETE 2026-07-23
  **Commits: 408f5c6 (EdgeRepo: 327 lines), c4d4ce9 (DB struct + pool), 26f450b (Migrate, PoolConfig, migrations pkg). Models, NodeRepo (376 lines), TreeRepo (220 lines), EdgeRepo (327 lines), db.go with pool/migration runner, 4 DDL migrations, standalone migrations/ Go package. Full pgx impl per SPEC-DM-01 §4.**
- [x] **BE-03 — Tree Service** ✅ COMPLETE 2026-07-23
  **Commit: aa0c31a** — 968-line TreeService with spec-compliant interface, 16 types, 16 error sentinels, atomic CreateTree tx, ListTrees pagination, GetTree with stats, DeleteTree. Wired to HTTP server via TreeHandler.
  **Details:** 1,155 lines added. Full TreeService with CreateTree (atomic tree+root-node tx), ListTrees (pagination/sort/search), GetTree, UpdateTree (partial), DeleteTree (soft). HTTP handlers with camelCase JSON, UUID validation, limit clamping, structured error responses. Wired via chi router at /trees, DB init in main.go.
- [ ] **BE-04 — Node Service**
- [ ] **BE-05 — SSE Hub**
- [ ] **BE-06 — Sync Engine**
- [ ] **BE-07 — Auth & Approval Engine**
- [ ] **BE-08 — Profile Routing**
- [ ] **BE-09 — Transport Adapter Layer**
- [ ] **BE-10 — Encryption Layer (MLS-Only)**
- [ ] **BE-11 — HTTP Router & Middleware**
- [ ] **BE-12 — Backend Integration Tests**

---

## Phase 5: Frontend (TypeScript/React)

- [ ] **FE-01 — Project Scaffold**
- [ ] **FE-02 — Tree Data Store**
- [ ] **FE-03 — Tree Rendering Engine**
- [ ] **FE-04 — Navigation System**
- [ ] **FE-05 — Message Composer**
- [ ] **FE-06 — Approval Panel**
- [ ] **FE-07 — Multi-User Features**
- [ ] **FE-08 — Agent Context Visualization**
- [ ] **FE-09 — Offline Mode**
- [ ] **FE-10 — Accessibility**
- [ ] **FE-11 — Frontend Integration Tests**

---

## Phase 6: Integration & Wiring

- [ ] **INT-01 — End-to-End Tree Flow**
- [ ] **INT-02 — Multi-User Integration**
- [ ] **INT-03 — Multi-Profile Integration**
- [ ] **INT-04 — Offline Sync Integration**
- [ ] **INT-05 — Performance Baseline**
- [ ] **INT-06 — CLI Wiring**

---

## Phase 7: Testing & Hardening

- [ ] **TEST-01 — Unit Test Coverage**
- [ ] **TEST-02 — Integration Test Suite**
- [ ] **TEST-03 — Chaos & Resilience**
- [ ] **TEST-04 — Security Audit**
- [ ] **TEST-05 — Accessibility Audit**

---

## Phase 8: Production Deployment

- [ ] **DEPLOY-01 — Docker + Compose + WebUI Native Binary**
- [ ] **DEPLOY-02 — Observability**
- [ ] **DEPLOY-03 — CI/CD**
- [ ] **DEPLOY-04 — Documentation**
- [ ] **DEPLOY-05 — Migration Plan**

---

## Phase 9: Distribution & Multi-Tenant

- [ ] **DIST-01 — Multi-Tenant + Multi-Transport Isolation**
- [ ] **DIST-02 — Self-Host Guide**
- [ ] **DIST-03 — Open Source Readiness**

---

## Phase 10: Continuous Improvement

- [ ] **NEVER-DONE — Run coding-hermes-never-done 11-point audit**
  Load coding-hermes-never-done skill. Run ALL 11 checks: spec alignment, doc coverage, test gaps, package upgrades, pitfall hunt, performance audit, endpoint verification, CI/CD health, DuckBrain sync, code quality, middle-out wiring. Create a task for EVERY gap found. This task is never complete — the audit always finds something.

---

## Legend

| Marker | Meaning |
|--------|---------|
| `[ ]` | Not started |
| `[x]` | Complete (verified — spec written and committed) |
| **T1.x** | Phase 1 task |
| **SPEC-DM-xx** | Spec task (produces spec files) |
| **SPEC-API-xx** | API spec task |
| **SPEC-TM-xx** | Topic management spec task |
| **SPEC-PL-xx** | Plugin/card spec task |
| **SPEC-FTR-xx** | Post-MVP feature spec task |
| **BE-xx** | Backend implementation |
| **FE-xx** | Frontend implementation |
| **INT-xx** | Integration |
| **TEST-xx** | Testing |
| **DEPLOY-xx** | Deployment |
| **DIST-xx** | Distribution |
