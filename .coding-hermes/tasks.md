# Hermes Canopy — Task Board

> **Status:** Phase 1 ✅ (9/9) | Phase 2 ✅ (4/4) | **Phase 3 — API Specs ✅ (7/7)** | Active: Phase 3b (Topic Management Specs)
> **Foreman:** deepseek-v4-pro @ deepseek-foreman
> **DuckBrain:** hermes-canopy namespace (25 entries)
> **Last tick:** SPEC-API-07 complete — error catalog committed

---

## Phase 1: Architecture & Research Validation

**Goal:** Validate stack decisions, research existing solutions, confirm no showstoppers. Output: confirmed architecture document with rationale.

- [x] **T1.1 — Transport Research: SSE vs WebSocket vs NATS** ✅ COMPLETE 2026-07-19
  Research SSE reconnection behavior, browser support, polyfills. Compare with WebSocket sticky session requirements. Research NATS as message queue transport option. Document: max SSE connections per browser, reconnection backoff strategies, Last-Event-ID behavior across browsers. Output: specs/T1.1-transport-research.md.
  **Decision: SSE (HTTP/2) primary + NATS backend + WebSocket as future bidirectional fallback.**

- [x] **T1.2 — CRDT Library Evaluation: Yjs vs Automerge** ✅ COMPLETE 2026-07-20
  Compare Yjs and Automerge for tree-structured data: Y.Map vs Automerge's JSON CRDT. Measure: bundle size, WASM requirement, memory usage with 10K nodes, conflict resolution on concurrent branch creation. Output: specs/T1.2-crdt-evaluation.md.
  **Decision: Yjs — 18KB gzipped, pure JS, no WASM, granular observe() for tree re-rendering, 920K/wk downloads.**

- [x] **T1.3 — Tree Visualization Research** ✅ COMPLETE 2026-07-20
  Survey tree/outliner libraries: React Flow, D3.js tree layout, custom Canvas renderer. Evaluate: rendering 500+ nodes, zoom/pan performance, accessibility (keyboard nav), mobile touch support. Output: library comparison matrix.
  **Decision: React Flow (@xyflow/react v12) primary + d3-hierarchy layout engine + Canvas fallback for >2000 nodes.**

- [x] **T1.4 — Offline-Stack Research** ✅ COMPLETE 2026-07-20
  Research: Service Worker caching strategies (CacheFirst vs NetworkFirst for tree data), IndexedDB vs sql.js WASM for local persistence, CRDT sync over HTTP/SSE patterns. Test: Delta Chat protocol as transport fallback. Output: offline architecture doc.
  **Decision: Service Worker (Workbox v7) + y-indexeddb + Custom SSE Provider + Background Sync queue. No SQLite WASM in MVP. Delta Chat as optional post-MVP relay.**

- [x] **T1.5 — Approval UX Research** ✅ COMPLETE 2026-07-20
  Research existing approval/review UX patterns: GitHub PR review, Linear/Notion comment threads, Google Docs suggesting mode. Design: approval side panel mockups (pending count badge, message preview, approve/deny/reply-first, audit trail). Output: UX design doc with wireframes.
  **Decision: GitHub triage panel + Linear notification discipline + Google Docs per-item granularity. Output: specs/T1.5-approval-ux-research.md (24KB, 8 sections, wireframes, keyboard nav, mobile adaptation, data model impact).**

- [x] **T1.6 — WebUI Native App Evaluation** ✅ COMPLETE 2026-07-20
  Evaluate WebUI library (webui.me, github.com/webui-dev/webui) for native desktop packaging. Test: Go backend + React frontend in single binary, system WebView integration on Linux (GTK WebKit), macOS (WKWebView), Windows (Edge WebView2). Measure: binary size vs Electron, startup time, memory usage, WebView compatibility matrix. Output: recommendation for native app packaging.
  **Decision: Wails v3 (post-MVP) + Go embed (MVP). WebUI rejected — browser-chrome approach conflicts with Canopy's native collaboration surface requirements. Output: specs/T1.6-webui-evaluation.md (281 lines, 18 sections, 5 candidates evaluated, WebView compatibility matrix, architecture impact diagram).**

- [x] **T1.7 — Security Protocol: MLS-Only Architecture** ✅ COMPLETE 2026-07-20
  DECISION: MLS-only. No Signal Protocol. Rationale: every Canopy conversation is inherently multi-participant (user + agent + profiles + friends). MLS handles groups of 2 with minimal overhead. Single dependency (mls-rs via CGo) halves attack surface. Industry trajectory: MLS is RFC 9420, WhatsApp adopting. Signal is the gold standard for phone-to-phone messaging — Canopy is an agent collaboration OS, not a messaging app. Go implementation: CGo binding to mls-rs. Groups per tree and per topic. No protocol negotiation layer. Output: `specs/T1.7-mls-encryption.md` (14.6KB, 8 sections, Go interface, CGo binding diagram, threat model, alternatives analysis).

- [x] **T1.8 — Multi-Transport Architecture Design** ✅ COMPLETE 2026-07-20
  Design protocol-agnostic sync layer that abstracts over: SSE (HTTP/2), WebRTC P2P (STUN/TURN), NATS/Redis Streams (message queues), custom relay (self-hosted). Define transport adapter interface: Connect, Send, Receive, Disconnect. Design relay protocol: tree sync opcodes over any reliable channel. Output: specs/T1.8-multi-transport-architecture.md (54KB, 1254 lines, 9 sections, 2 Mermaid diagrams, 5 transport adapters, 13 opcodes, 7-mode selection matrix).
  **Commit: 8706036**

- [x] **T1.9 — Confirmed Architecture Document** ✅ COMPLETE 2026-07-20
  Synthesize T1.1–T1.8 into a single architecture document (563 lines, 12 sections, 3 Mermaid diagrams, decision registry, cost estimates, phase roadmap). Output: `specs/ARCHITECTURE.md`.
  **Commit: b8e170d**

**Blocks:** Nothing. All research tasks independent.

---

## Phase 2: Data Model Specs

**Goal:** Exact DDL, Go structs, TypeScript types, CRDT schema. Worker reading these specs produces correct data layer with zero questions.

**Dependencies:** Phase 1 complete (architecture decisions confirmed).

- [x] **SPEC-DM-01 — Tree Node & Edge DDL** ✅ COMPLETE 2026-07-20
  PostgreSQL DDL for nodes and edges tables. Include: UUIDv7 generation function, indexes (tree_id, parent_id, created_at), constraints (FKs, single-parent except merge nodes), soft-delete column, created_at/edited_at triggers. Go structs with sql tags, TypeScript interfaces with Zod validation. CRDT schema: Yjs Y.Map shape for nodes and edges.
  **Commit: 09fa6d1 — specs/SPEC-DM-01-tree-node-edge-ddl.md (762 lines, 12 sections, full DDL, Go interfaces, TS types + Zod, Yjs schema, error catalog, test scenarios)**

- [x] **SPEC-DM-02 — Tree Snapshot & Delta Model** ✅ COMPLETE 2026-07-20
  DDL for tree_snapshots table. SHA256 hash generation. Delta structure: added/removed/changed node lists, edge changes. Go: TreeSnapshot struct, ComputeDelta(from, to) signature. TypeScript: Snapshot type, applyDelta(snapshot, delta) function.
  **Commit: f7d3f6f — specs/SPEC-DM-02-tree-snapshot-delta-model.md (903 lines, 12 sections, full DDL, Go interfaces, TS types, SHA256 algorithm, delta computation, error catalog, test scenarios)**

- [x] **SPEC-DM-03 — Approval & Audit Trail DDL** ✅ COMPLETE 2026-07-20
  DDL for approvals and approval_rules tables. Approval states: pending, approved, denied, expired. Auto-approval rules: per-user, per-thread, per-profile. Audit trail: immutable log of all approval actions with full context. Go structs, TypeScript types.
  **Commit: 6caafc6 — specs/SPEC-DM-03-approval-audit-trail-ddl.md (1042 lines, 12 sections, full DDL, Go interfaces, TS types + Zod, approval FSM, auto-approval rule engine, audit trail architecture, error catalog, test scenarios)**

- [x] **SPEC-DM-04 — User & Profile Model** ✅ COMPLETE 2026-07-20
  DDL for users, profiles, tree_members, profile_invites. Profile types: human, hermes-profile. Permissions: owner, admin, member, viewer. Profile visibility: per-tree toggle. Go structs with auth integration points.

**Blocks:** Phase 1 (architecture decisions inform exact DDL types).

---

## Phase 3: API Specs

**Goal:** Exact REST + SSE endpoints, request/response schemas, error catalog, authentication. Worker reads these specs and produces correct handlers.

**Dependencies:** Phase 2 complete (data model types inform API shapes).

- [x] **SPEC-API-01 — SSE Event Stream Spec** ✅ COMPLETE 2026-07-20
  GET /trees/{tree_id}/events endpoint. Query params: ?since=<hash>, ?profiles=<csv>. Event types: node_added, node_updated, node_removed, edge_added, edge_removed, approval_changed, user_joined, user_left, tree_merged. Exact JSON shape per event. Reconnection: Last-Event-ID header behavior. Heartbeat interval. Max events per connection. Authentication: Bearer token validation at connection time.
  **Commit: 6d6c8b4 — specs/SPEC-API-01-sse-event-stream.md (1014 lines, 18 sections, 12 event types, Go SSEHub/SSEClient/SSEEventLog interfaces, 20 edge cases, 46 test scenarios, Mermaid sequence diagram)**

- [x] **SPEC-API-02 — Tree CRUD Endpoints** ✅ COMPLETE 2026-07-20
  GET/POST /trees, GET /trees/{tree_id}, DELETE /trees/{tree_id}. Request/response schemas. Pagination for GET /trees. Tree creation: initial root node auto-created. Tree deletion: soft-delete with retention period.
  **Commit: 4f24622 — specs/SPEC-API-02-tree-crud-endpoints.md (1251 lines, 17 sections, Go interfaces, TS types + Zod, 53 backend test scenarios, 10 frontend test scenarios, error catalog, Mermaid diagrams)**

- [x] **SPEC-API-03 — Node CRUD Endpoints** ✅ COMPLETE 2026-07-20
  POST /trees/{tree_id}/nodes, PATCH /nodes/{node_id}, DELETE /nodes/{node_id}, POST /nodes/{node_id}/reply, POST /nodes/{node_id}/fork. Request validation: content length limits, parent must exist, can't reply to deleted node. Response includes full node with computed fields (depth, child_count).
  **Commit: 5e65fc6 — specs/SPEC-API-03-node-crud-endpoints.md (1140 lines, 20 sections, Go interfaces, TS types + Zod, 23 error codes, 15 edge cases, 60 test scenarios, Mermaid sequence diagram)**

- [x] **SPEC-API-04 — Merge & Navigation Endpoints** ✅ COMPLETE 2026-07-20
  POST /trees/{tree_id}/merge (creates synthetic merge node). GET /trees/{tree_id}/path?from=X&to=Y (returns node path between two nodes). GET /trees/{tree_id}/subtree?root=X&depth=N (returns subtree). GET /trees/{tree_id}/compare?branch_a=X&branch_b=Y (returns node diff between branches).
  **Commit: cb18965 — specs/SPEC-API-04-merge-navigation-endpoints.md (1778 lines, 19 sections, 4 endpoints with full Go interfaces, TS types + Zod, 45 test scenarios, 4 Mermaid diagrams, 30+ error codes)**

- [x] **SPEC-API-05 — Approval Endpoints** ✅ COMPLETE 2026-07-20
  GET /approvals/pending, POST /approvals/{id}/approve, POST /approvals/{id}/deny, POST /approvals/rules, GET /approvals/history. Business logic: approval expiration after N days, auto-approval rule matching, conflict resolution (two rules match — most specific wins).
  **Commit: 0e15a03 — specs/SPEC-API-05-approval-endpoints.md (1760 lines, 23 sections, Go interfaces, TS types + Zod, 60 backend + 15 frontend test scenarios, 3 Mermaid diagrams, 30 edge cases, SSE event spec, rate limits)**

- [x] **SPEC-API-06 — Multi-User & Profile Endpoints** ✅ COMPLETE 2026-07-20
  POST /trees/{tree_id}/invite, GET /trees/{tree_id}/members, DELETE /trees/{tree_id}/members/{user_id}, GET /profiles, POST /trees/{tree_id}/profiles/{profile_id}/invite, PATCH /trees/{tree_id}/profiles/{profile_id}/visibility. Invite flow: generate invite link, accept via token, assign permissions.
  **Commit: 45e3fab — specs/SPEC-API-06-multi-user-profile-endpoints.md (1493 lines, 24 sections, 13 endpoints, Go interfaces, TS types + Zod, 40 backend + 15 integration + 10 frontend test scenarios, 3 Mermaid diagrams, 35+ error codes, invite token lifecycle)**

- [x] **SPEC-API-07 — Error Catalog** ✅ COMPLETE 2026-07-21
  Every error across all endpoints. HTTP status codes, error body format: {error: string, code: string, details?: object}. Error codes: TREE_NOT_FOUND, NODE_NOT_FOUND, INVALID_PARENT, NODE_DELETED, NOT_TREE_OWNER, NOT_TREE_MEMBER, APPROVAL_EXPIRED, PROFILE_OFFLINE, RATE_LIMITED, TREE_SIZE_EXCEEDED. Exact conditions that trigger each error.
  **Commit: 4074b71 — specs/SPEC-API-07-error-catalog.md (782 lines, 12 sections, 112+ error codes, Go interfaces, TS types + Zod, taxonomy by domain/status, 14 test scenarios, Mermaid diagram, error precedence rules, Phase 3b-3d forward compatibility)**

**Blocks:** Phase 2 (data model types inform request/response shapes).

---

## Phase 3b: Topic Management Specs

**Goal:** Exact topic system specs — auto-detection, #references, search, sidebar, one-button context. Worker reads these specs and produces correct topic management layer.

**Dependencies:** Phase 2 complete (tree data model — topics are tree branches).

- [x] **SPEC-TM-01 — Topic Data Model** ✅ COMPLETE 2026-07-21
  A topic IS a tree branch with metadata. Each topic: {id: UUIDv7, tree_id: UUID, root_node_id: UUID, title: string, description: string, parent_topic_id: UUID|null, status: active|archived|deleted, created_at, archived_at, topic_tags: string[]}. #Reference format: `#topic-slug` becomes clickable link with tooltip showing topic preview. Cross-topic references: nodes can reference nodes in other topics.
  **Commit: d2a8168**

- [x] **SPEC-TM-02 — Auto-Topic Detection** ✅ **2026-07-21 (8bce2c0)**
  Agent-side logic: as user converses, agent detects topic shifts. Signals: explicit ("make this a topic"), implicit (semantic shift over N messages), structural (user opens new subject). Agent proposes: "I think this is a new topic about X — create?" User can accept, reject, or name differently. Detection model: prompt engineering, not ML (initially). Contiguous messages with shared subject → same topic. Sharp semantic break → new topic proposal.

- [x] **SPEC-TM-03 — Topic Search & One-Button Context** ✅ **2026-07-21 (f866d0d)**
  Full-text search over topic titles, descriptions, and content. Index: PostgreSQL FTS (tsvector) or Meilisearch embedded. Search result: title + snippet + status + "Add to Context" button. One click → topic's tree is injected into agent's context window. Agent reads and parses the topic automatically — user doesn't need to open and read it. Search sidebar: persistent or toggleable, shows recent topics + search box.

- [x] **SPEC-TM-04 — #Reference Resolution** ✅ **2026-07-21 (e8a14d3)**
  Parsing: `#topic-name` in any message. Auto-complete while typing: as user types `#dat`, show `#database-schema`, `#data-model`, `#data-flow`. Click or tab to insert. At send time: replace with internal reference link. Agent-side: when it encounters a #reference in context, fetch that topic's tree and include it in the current turn's context window. Multiple references: each adds to context. Escalation: if too many references, agent warns "I can see 5 referenced topics — should I focus on specific ones?"

- [ ] **SPEC-TM-05 — Topic Lifecycle & Sidebar**
  Sidebar shows: active topics (sortable by recent activity, created, alphabetical). Archived topics (collapsible section). Search bar at top. Context menu per topic: rename, archive, delete, merge-with, split-from. Topic preview on hover: first N messages, participant count, last active time. Drag-and-drop reordering. Keyboard shortcuts: Ctrl+K to search topics, Ctrl+Shift+N new topic.

**Blocks:** Phase 2 (topics ARE tree branches).

---

## Phase 3c: Plugin & App Card Specs

**Goal:** Exact specs for JS plugin system, embedded app cards, calendar integration, file viewers. Worker reads these specs and produces correct plugin/card layer.

**Dependencies:** Phase 2 complete (files, apps, and plugins are all tree-addressable).

- [ ] **SPEC-PL-01 — JS Plugin System**
  Plugin format: single JS file with manifest (name, version, description, permissions, render_type). Registration: agent sends JS file as message, user clicks "Install", plugin loaded into renderer. Hot-reload: plugin updates instantly propagate to all connected devices (desktop, web, mobile). Sandbox: plugins run in isolated iframe/WebWorker with limited API surface. Permissions: file_access, network, notifications, calendar_read, calendar_write. Plugin registry: namespace to prevent conflicts.

- [ ] **SPEC-PL-02 — Built-in File Viewers**
  Native viewers for: PDF (pdf.js), images (lightbox + zoom), code (Monaco Editor with syntax highlighting), CSV/spreadsheet (handsontable or similar), Markdown (rendered with GFM), JSON (collapsible tree view), audio/video (HTML5 player). File attachment model: attach by reference (already in Hermes filesystem → single canonical copy) or by upload (new file → stored in Hermes). Agent can open/view any file in the knowledge base.

- [ ] **SPEC-PL-03 — App Card System + Database-per-Card**
  Card model: {id, app_id, card_type: 'compact'|'expanded'|'iteration', data: JSON, actions: [{label, handler}], created_at, context_hash}. Agent renders cards based on context — calendar events, weather, deployment status, task lists, monitoring dashboards, live searches, code execution, thinking steps. Cards are #referenceable like topics. Context parse: clicking a card injects its data into the agent's context. Card lifecycle: created by agent → displayed inline → collapsed to compact → dismissed.
  
  **Database-per-Card Architecture:** Each card type gets its own SQLite database at `~/.hermes/canopy/cards/{type}.db`. Standardized schema: `cards` table (id, card_type, title, data_json, status) + `events` table (id, card_id, event_type, payload_json, user_feedback, created_at). REST API: GET/POST/PATCH `/api/cards/{type}/{id}` — pgREST-like pattern over SQLite. Agent writes events as it works, UI reads via SSE, user interactions write back as `user_feedback`. Local-first: all card data lives in SQLite locally, syncs to server for multi-device.

- [ ] **SPEC-PL-04 — Dynamic Thinking Interface (Iteration Cards)**
  While agent is working, the interface is dynamic. User opens side panel → "show me the searches" → card pops up with live search results. Card shows: URLs searched, data retrieved, progress (3/5 complete). User can HIGHLIGHT specific results and give FEEDBACK: "this link is wrong" or "focus on this result." Feedback goes BACK to the model — like Hermes iterations but interactive. Iteration card types: search card (live results), code exec card (stdout/stderr streaming, cancel), file read card (contents with highlight), thinking card (chain-of-thought steps, collapsible), tool call card (what tool, params, result, approve/deny). Event flow: Agent process → events → card renderer + SQLite → user interaction → feedback events → back to agent. Each card is a bidirectional channel between human and agent thinking.

- [ ] **SPEC-PL-05 — Calendar Integration**
  Calendar viewer card: month/week/day views, event cards with title/time/description/location. Multiple calendar sources: Google Calendar (OAuth), iCloud (CalDAV), local (.ics files in Hermes knowledge base). Agent can: create events, modify events, check availability, propose times. Calendar ↔ auto-responder: agent knows when you're busy and tells others. Calendar ↔ status: "I'm in a meeting until 3" → agent auto-sets busy status.

- [ ] **SPEC-PL-06 — Multi-Message Reference Model**
  Data model extension: a node can have MULTIPLE parent edges (not just one). Multi-reference reply: select N messages → reply → new node with N parent edges of type 'reference'. Visual: colored edges from each source converge into reply. Context: agent sees all referenced messages as unified input. Conflict: if referenced messages are in different branches, the reply node is a synthetic merge point showing which context contributed what.

**Blocks:** Phase 2 (cards and plugins are nodes in the tree).

---

## Phase 3d: Post-MVP Architecture Specs

**Goal:** Design the full architecture now — multi-user, federation, encryption, multi-transport — so the MVP is built with these extensions in mind. The model understands the complete design. No refactoring walls later.

**Dependencies:** Phase 2 complete. These are design specs only — implementation deferred.

- [ ] **SPEC-FTR-01 — Multi-User Collaboration & Approval Model**
  Exact data model extensions for multi-user: user roles (owner/admin/member/viewer), per-node permissions, approval state machine (pending→approved→denied→expired), operation-level approvals (not message-level). Federation model: cross-server identity via Hermes profile tokens, shared tree access control, invite/accept flow. Public vs private reply determinism: permission-based, not model-chosen. Audit trail DDL for immutable approval log. This spec ensures the single-user MVP data model can extend to multi-user without migration pain.

- [ ] **SPEC-FTR-02 — Federated Multi-Agent Architecture**
  Cross-server agent participation: different Hermes instances federate into shared trees. Per-agent context isolation: each agent sees the shared graph through its own context compiler. Agent reply modes: public (tree-visible) and direct (owner-only channel). Profile routing: @mention routes context window to specific Hermes profile. Cost tracking per profile. Presence and availability per agent. Gateway routing: canopyd forwards to local Hermes OR remote Hermes gateway depending on profile ownership. This spec defines how Canopy becomes multi-agent without becoming a messaging app.

- [ ] **SPEC-FTR-03 — MLS Encryption Model**
  Complete MLS integration spec: per-tree MLS group creation, per-topic subgroup derivation, key rotation on member join/leave, forward secrecy guarantees. Server-side agent as authorized decrypting participant (visible to all group members). Search over encrypted data: agent decrypts → indexes → re-encrypts. Conflict with server-side topic detection resolved: agent is the authorized indexer. Key backup and recovery. This is designed upfront so the graph data model and API don't need retrofitting when encryption is added.

- [ ] **SPEC-FTR-04 — Multi-Transport Architecture**
  Transport adapter interface: Connect, Send, Receive, Disconnect, Health. Adapters: SSE (HTTP/2 server→client, POST client→server), WebRTC (P2P via pion/webrtc, STUN/TURN), NATS (message queue for reliable delivery). Relay protocol: tree sync opcodes over any transport. Connection manager: per-member transport tracking, graceful degradation (SSE fails → try WebRTC). Bandwidth detection and adaptive payload sizing. Offline queue: Background Sync for PWA, NATS persistence for server-side. This spec defines the transport abstraction that makes 7 deployment modes possible.

- [ ] **SPEC-FTR-05 — Self-Hosted & SaaS Relay Architecture**
  Relay deployment model: single canopyd binary as relay server. Multi-tenant isolation: database-per-tenant or DuckDB schema-per-tenant. Relay routing: clients connect to relay, relay routes messages between tree members. SaaS billing integration points. Resource limits per tenant. STUN/TURN server provisioning. Health check and monitoring endpoints. This spec defines how Canopy scales from single-user local to hosted multi-tenant.

- [ ] **SPEC-FTR-06 — WebUI Native Packaging & Distribution**
  WebUI integration: `webui.Show(window, "http://localhost:8080")` — one line of Go. Build targets: Linux (GTK WebKit), macOS (WKWebView), Windows (Edge WebView2). App signing and distribution: code signing, app store submission prep. Auto-update mechanism. This spec is short because WebUI is trivial — document the build pipeline and platform specifics.

- [ ] **SPEC-FTR-07 — Hermes Agent Gateway Integration**
  Exact API contract between canopyd and Hermes: model call request/response format, tool execution request/response, context assembly format, auth token forwarding, profile resolution, skill→card translation. Error handling: Hermes downtime, rate limiting, model unavailability. The canopyd→Hermes boundary is the most critical interface — this spec defines it precisely so both sides can be built independently.

**Blocks:** Phase 2. All post-MVP feature specs are design-only — implementation deferred but architecture is coherent from day one.

---

## Phase 4: Backend (Go Gateway)

**Goal:** Working SSE + REST gateway. Tests pass. Wired to CLI with serve command. Dockerfile.

**Dependencies:** Phase 3 complete (API specs).

- [ ] **BE-01 — Project Scaffold**
  Go module init, directory layout (cmd/canopyd, internal/{tree,sync,auth,approval,profile}, pkg/crdt), Makefile, Dockerfile, go.mod with pinned dependency versions. Conforms to project-layouts skill.

- [ ] **BE-02 — Database Layer**
  Implement DDL from SPEC-DM-01 through SPEC-DM-04. Migration system (golang-migrate). Repository pattern: TreeRepo, NodeRepo, ApprovalRepo, ProfileRepo interfaces. PostgreSQL connection pool config. sqlc or raw SQL per architecture decision.

- [ ] **BE-03 — Tree Service**
  Implements tree CRUD per SPEC-API-02. Tree creation with initial root node. Tree validation (max nodes, max depth). Tree snapshot computation with SHA256 hash. Delta computation between two snapshots.

- [ ] **BE-04 — Node Service**
  Implements node CRUD per SPEC-API-03. Node creation with parent validation. Reply vs fork edge type. Soft-delete with tombstone. Subtree queries. Path computation between nodes.

- [ ] **BE-05 — SSE Hub**
  Implements SSE event stream per SPEC-API-01. Connection manager: register/unregister clients per tree. Event broadcasting to subscribed clients. Reconnection: Last-Event-ID tracking, replay missed events. Heartbeat goroutine. Graceful shutdown: drain connections on SIGTERM.

- [ ] **BE-06 — Sync Engine**
  Delta sync: client sends last-known hash, server computes delta, streams only changes. Full tree sync on hash mismatch. Batch event delivery (coalesce rapid changes). Connection quality detection: reduce event detail on slow connections.

- [ ] **BE-07 — Auth & Approval Engine**
  JWT validation for existing Hermes auth tokens. Tree membership check middleware. Approval logic: pending queue, approve/deny, auto-approval rule evaluation, conflict resolution (most-specific wins). Audit trail: every approval action logged immutably.

- [ ] **BE-08 — Profile Routing**
  Profile registry: which Hermes profiles are available. Per-tree profile subscriptions. Profile-to-LLM routing: when a profile is @mentioned, route the context window to that profile's Hermes instance. Profile cost tracking.

- [ ] **BE-09 — Transport Adapter Layer**
  Implement transport adapter interface: Connect, Send, Receive, Disconnect. Adapters: SSE (HTTP/2), WebRTC (P2P via pion/webrtc), NATS (message queue). Relay protocol: tree sync opcodes over any adapter. Connection manager: track which members are on which transport. Graceful degradation: if SSE fails, try WebRTC, etc.

- [ ] **BE-10 — Encryption Layer (MLS-Only)**
  Implement MLS encryption via CGo binding to mls-rs (Rust). Per-tree MLS group: all members in a tree share one group. Per-topic subgroup: topics with restricted membership get their own MLS group. Key rotation on member join/leave. Forward secrecy enforced per MLS specification. Single dependency, single code path — no protocol negotiation. No Signal Protocol. 1:1 conversations are MLS groups of 2.

- [ ] **BE-11 — HTTP Router & Middleware**
  Chi or stdlib router. Middleware chain: auth, tree membership, rate limiting, request logging, CORS, recovery. Wire all handlers from BE-03 through BE-10. CLI: `canopyd serve --port 8080 --db postgres://...`

- [ ] **BE-12 — Backend Integration Tests**
  Test against real PostgreSQL (testcontainers or docker-compose). SSE tests: connect, send event, verify received, reconnect with Last-Event-ID. Approval flow tests: pending→approve→audit log. Concurrent tests: two clients creating branches from same node.

**Blocks:** Phase 3 (API specs define exact handler signatures and error behavior).

---

## Phase 5: Frontend (TypeScript/React)

**Goal:** Working tree UI with CRDT local-first, SSE sync, three-level navigation, approval panel.

**Dependencies:** Phase 4 backend running (needs live SSE endpoint for integration).

- [ ] **FE-01 — Project Scaffold**
  Vite + React + TypeScript. Directory layout (src/{components, hooks, stores, lib, types}). CRDT integration: Yjs + y-websocket provider (or custom SSE provider). PWA setup: service worker, manifest, offline fallback.

- [ ] **FE-02 — Tree Data Store**
  Yjs Y.Doc integration. Y.Map for nodes, Y.Map for edges. Local persistence: IndexedDB via y-indexeddb. Sync provider: SSE-backed Yjs provider that maps SSE events to Yjs updates. Offline queue: pending operations stored locally, synced on reconnect.

- [ ] **FE-03 — Tree Rendering Engine**
  Tree layout algorithm: spacing, depth-based indentation, branch visual connectors. Three rendering modes: macro (collapsed nodes, expandable), branch (full thread with ancestors), merge (side-by-side panels). Performance: virtualize nodes for large trees (1000+ nodes). Canvas or DOM per T1.3 decision.

- [ ] **FE-04 — Navigation System**
  Three-level navigation: keyboard shortcuts (j/k navigate siblings, h/l drill in/out, m merge selected), click to select node, double-click to drill in, breadcrumb trail showing path from root, back button pops up one level. Zoom: pinch-to-zoom on mobile, scroll wheel + modifier on desktop.

- [ ] **FE-05 — Message Composer**
  Rich text input with @mentions (users and profiles), code blocks, file attachments. Reply mode: composer anchored to selected node. Branch preview: shows context chain above composer. Send: creates new node via Yjs (local-first), SSE syncs to server.

- [ ] **FE-06 — Approval Panel**
  Side panel with: pending count badge, scrollable list of pending approvals, each shows sender + full message context + approve/deny/reply-first buttons. Approved/denied history with search. Auto-approval rule editor: "Always approve X in thread Y."

- [ ] **FE-07 — Multi-User Features**
  User presence: who's viewing which branch (colored dots). Invite flow: generate link, copy to clipboard, accept flow. Permissions UI: member list with role dropdowns, remove member confirmation. Profile integration: invite Hermes profile, set visibility.

- [ ] **FE-08 — Agent Context Visualization**
  Visual overlay showing which nodes are in the agent's current context window. Highlight on the tree: green = in context, yellow = will be dropped next turn, red = already dropped. Context window size indicator. Manual pinning: right-click → "pin to context."

- [ ] **FE-09 — Offline Mode**
  Offline indicator banner. Pending sync count. Conflict resolution UI: if server tree diverged, show diff with accept/reject per conflict. Bandwidth detection: neterror.js or Network Information API, reduce features on slow connections (no avatars, compressed images, fewer metadata fields).

- [ ] **FE-10 — Accessibility**
  Keyboard-first navigation (already in FE-04). Screen reader: aria-live regions for new messages, tree structure via aria-tree roles, approval alerts via assertive announcements. High-contrast theme. Reduced motion: disable branch animations. Focus management: don't lose focus on new message arrival.

- [ ] **FE-11 — Frontend Integration Tests**
  Cypress or Playwright tests: full user flows (create tree, write messages, branch, merge, approve, offline→online sync). Accessibility audit with axe-core. Performance: Lighthouse score > 90. Bundle size: < 500KB gzipped (initial load).

**Blocks:** Phase 4 (needs backend running for SSE sync integration tests).

---

## Phase 6: Integration & Wiring

**Goal:** End-to-end flows working. Frontend connects to backend. All features wired, no stubs.

**Dependencies:** Phase 4 + Phase 5.

- [ ] **INT-01 — End-to-End Tree Flow**
  Create account → create tree → write messages → branch → merge → approve → offline → reconnect → sync. Test with real browser against real backend.

- [ ] **INT-02 — Multi-User Integration**
  Owner creates tree. Invites friend. Friend sends message. Owner approves. Agent acts. Full flow with two browser instances + real SSE connections.

- [ ] **INT-03 — Multi-Profile Integration**
  Human writes message. @mentions coding-hermes. coding-hermes responds in branch. Human approves. Verify: profile context window isolation confirmed.

- [ ] **INT-04 — Offline Sync Integration**
  Client goes offline (devtools offline). Write 50 messages. Come back online. SSE reconnects. Verify: all 50 messages synced, tree structure preserved, no duplicates.

- [ ] **INT-05 — Performance Baseline**
  Load test: 100 concurrent SSE connections. 10,000 node tree. Delta sync latency < 100ms. Tree render: 500 visible nodes < 16ms frame. Memory: < 200MB for 10K node tree.

- [ ] **INT-06 — CLI Wiring**
  Backend: `canopyd serve` starts HTTP + SSE. Frontend: `npm run dev` serves Vite dev server. Docker Compose: backend + PostgreSQL + frontend one command. Health check endpoints.

**Blocks:** Phase 4 + Phase 5 complete.

---

## Phase 7: Testing & Hardening

**Goal:** Production-grade test coverage. Chaos engineering. Security audit.

**Dependencies:** Phase 6 complete (all features wired).

- [ ] **TEST-01 — Unit Test Coverage**
  Backend: 80%+ coverage (exclude generated code). Frontend: 80%+ coverage (Vitest). CRDT operations: property-based tests (fast-check) for Yjs operations.

- [ ] **TEST-02 — Integration Test Suite**
  All API endpoints tested against real PostgreSQL. SSE event stream: connect/disconnect/reconnect/event ordering. Approval flow: create→pending→approve→audit→deny→re-approve.

- [ ] **TEST-03 — Chaos & Resilience**
  Kill PostgreSQL mid-sync. Kill SSE connection mid-stream. Network partition: client and server can't reach each other for 5 minutes, then reconnect. Disk full: graceful degradation.

- [ ] **TEST-04 — Security Audit**
  SQL injection: all query paths. XSS: message content rendering. CSRF: state-changing endpoints. Auth bypass: direct API calls without tokens. Rate limiting: brute force protection. Tree isolation: user A can't access user B's trees.

- [ ] **TEST-05 — Accessibility Audit**
  Full WCAG 2.1 AA audit. Screen reader walkthrough (NVDA/VoiceOver). Keyboard-only operation: all features accessible without mouse. Color contrast compliance. Focus management audit.

**Blocks:** Phase 6 (need full integration to test real scenarios).

---

## Phase 8: Production Deployment

**Goal:** Deployable artifact. Observability. Documentation.

**Dependencies:** Phase 7 complete (hardened).

- [ ] **DEPLOY-01 — Docker + Compose + WebUI Native Binary**
  Production Dockerfile (multi-stage, distroless). Docker Compose: canopyd + PostgreSQL + frontend nginx. WebUI native packaging: Go binary bundles React frontend assets, uses system WebView. Build targets: Linux (GTK WebKit), macOS (WKWebView), Windows (Edge WebView2). Health checks. Resource limits. Log rotation.

- [ ] **DEPLOY-02 — Observability**
  Prometheus metrics: SSE connections, sync latency, approval queue depth, tree sizes. Structured logging (JSON). Distributed tracing (trace IDs through SSE→REST→DB). Grafana dashboard.

- [ ] **DEPLOY-03 — CI/CD**
  GitHub Actions: lint → test → build → docker → deploy. PR checks: conventional commits, test coverage gate, bundle size gate.

- [ ] **DEPLOY-04 — Documentation**
  API docs (OpenAPI 3.0). Architecture docs (C4 model). User guide (first tree, first branch, first approval, first profile). Developer guide (local setup, contributing).

- [ ] **DEPLOY-05 — Migration Plan**
  From Telegram bot to Canopy: how existing Hermes users transition. Tree import from chat history. Coexistence: Canopy + Telegram during transition. Gradual cutover per project.

**Blocks:** Phase 7 (don't deploy unhardened software).

---

## Phase 9: Distribution & Multi-Tenant

**Goal:** Hermes Canopy as a platform. Other users can deploy their own.

**Dependencies:** Phase 8 complete (proven in production).

- [ ] **DIST-01 — Multi-Tenant + Multi-Transport Isolation**
  Database-per-tenant or schema-per-tenant. SSE connection routing per tenant. P2P relay routing. Resource limits per tenant. Billing integration points per transport mode. STUN/TURN server provisioning for P2P users.

- [ ] **DIST-02 — Self-Host Guide**
  One-command deploy: docker compose up. Configuration reference. Custom theming. Plugin system for extensions. Health check dash.

- [ ] **DIST-03 — Open Source Readiness**
  LICENSE (AGPLv3 or MIT per Bane). CONTRIBUTING.md. Code of conduct. Issue templates. Release process. Changelog automation.

**Blocks:** Phase 8.

---

## Phase 10: Continuous Improvement

**Goal:** Never done. After Phase 9, foreman runs never-done 11-point audit and creates new tasks.

- [ ] **IMPROVE-01 — 11-Point Audit**
  Specs, docs, tests, deps, pitfalls, perf, endpoints, CI, DuckBrain, quality, middle-out wiring. Every finding → new task.

- [ ] **IMPROVE-02 — User Feedback Loop**
  Feedback widget in Canopy UI. Crash reporting. Usage analytics (opt-in). Prioritize based on real usage data.

**Blocks:** Phase 9 complete.

---

## Legend

| Marker | Meaning |
|--------|---------|
| `[ ]` | Not started |
| `[x]` | Complete (verified — test passes + wired) |
| **T1.x** | Phase 1 task |
| **SPEC-DM-xx** | Spec task (produces spec files) |
| **BE-xx** | Backend implementation |
| **FE-xx** | Frontend implementation |
| **INT-xx** | Integration |
| **TEST-xx** | Testing |
| **DEPLOY-xx** | Deployment |
| **DIST-xx** | Distribution |
| **IMPROVE-xx** | Continuous improvement |
