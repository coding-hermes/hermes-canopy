# Hermes Canopy — Specification Index (AGENTS.md)

Index of the Hermes Canopy specification suite. Every file listed below is a real file in this directory; the one-line description for each entry is taken verbatim from that file's own first `# ` heading, which remains the authoritative title.

## Navigation

Spec files are grouped by filename prefix:

- `SPEC-API-*` — REST/SSE endpoint specifications
- `SPEC-DM-*` — data model DDL specifications
- `SPEC-DPL-*` — data migration plans
- `SPEC-FTR-*` — future architecture specifications
- `SPEC-IMPL-GAP-*` — implementation specs for gated gaps
- `SPEC-PL-*` — plugin / card system specifications
- `SPEC-TM-*` — topic system specifications
- `T1.*` — research documents
- `ARCHITECTURE.md` — system architecture overview

For project overview and terminology, read the root [../AGENTS.md](../AGENTS.md). The task board lives at `.coding-hermes/board/tasks.jsonl` (JSONL canonical).

## Spec Index

- [ARCHITECTURE.md](ARCHITECTURE.md) — Hermes Canopy — Architecture Document
- [SPEC-API-01-sse-event-stream.md](SPEC-API-01-sse-event-stream.md) — SPEC-API-01 — SSE Event Stream Spec
- [SPEC-API-02-tree-crud-endpoints.md](SPEC-API-02-tree-crud-endpoints.md) — SPEC-API-02 — Tree CRUD Endpoints
- [SPEC-API-03-node-crud-endpoints.md](SPEC-API-03-node-crud-endpoints.md) — SPEC-API-03 — Node CRUD Endpoints
- [SPEC-API-04-merge-navigation-endpoints.md](SPEC-API-04-merge-navigation-endpoints.md) — SPEC-API-04 — Merge & Navigation Endpoints
- [SPEC-API-05-approval-endpoints.md](SPEC-API-05-approval-endpoints.md) — SPEC-API-05 — Approval Endpoints
- [SPEC-API-06-multi-user-profile-endpoints.md](SPEC-API-06-multi-user-profile-endpoints.md) — SPEC-API-06 — Multi-User & Profile Endpoints
- [SPEC-API-07-error-catalog.md](SPEC-API-07-error-catalog.md) — SPEC-API-07 — Error Catalog
- [SPEC-DM-01-tree-node-edge-ddl.md](SPEC-DM-01-tree-node-edge-ddl.md) — SPEC-DM-01 — Tree Node & Edge DDL
- [SPEC-DM-02-tree-snapshot-delta-model.md](SPEC-DM-02-tree-snapshot-delta-model.md) — SPEC-DM-02 — Tree Snapshot & Delta Model
- [SPEC-DM-03-approval-audit-trail-ddl.md](SPEC-DM-03-approval-audit-trail-ddl.md) — SPEC-DM-03 — Approval & Audit Trail DDL
- [SPEC-DM-04-user-profile-model.md](SPEC-DM-04-user-profile-model.md) — SPEC-DM-04 — User & Profile Model
- [SPEC-DPL-05-migration-plan.md](SPEC-DPL-05-migration-plan.md) — SPEC-DPL-05 — Hermes Data Migration Plan
- [SPEC-FTR-01-multi-user-collaboration-approval-model.md](SPEC-FTR-01-multi-user-collaboration-approval-model.md) — SPEC-FTR-01 — Multi-User Collaboration & Approval Model
- [SPEC-FTR-02-federated-multi-agent-architecture.md](SPEC-FTR-02-federated-multi-agent-architecture.md) — SPEC-FTR-02 — Federated Multi-Agent Architecture
- [SPEC-FTR-03-mls-encryption-model.md](SPEC-FTR-03-mls-encryption-model.md) — SPEC-FTR-03 — MLS Encryption Model
- [SPEC-FTR-04-multi-transport-architecture.md](SPEC-FTR-04-multi-transport-architecture.md) — SPEC-FTR-04 — Multi-Transport Architecture
- [SPEC-FTR-05-self-hosted-saas-relay.md](SPEC-FTR-05-self-hosted-saas-relay.md) — SPEC-FTR-05 — Self-Hosted & SaaS Relay Architecture
- [SPEC-FTR-06-webui-native-packaging-distribution.md](SPEC-FTR-06-webui-native-packaging-distribution.md) — SPEC-FTR-06 — WebUI Native Packaging & Distribution
- [SPEC-FTR-07-hermes-agent-gateway-integration.md](SPEC-FTR-07-hermes-agent-gateway-integration.md) — SPEC-FTR-07 — Hermes Agent Gateway Integration
- [SPEC-IMPL-GAP-001-context-compiler.md](SPEC-IMPL-GAP-001-context-compiler.md) — SPEC-IMPL-GAP-001 — Context Compiler Implementation Spec
- [SPEC-IMPL-GAP-002-plugin-sandbox.md](SPEC-IMPL-GAP-002-plugin-sandbox.md) — SPEC-IMPL-GAP-002 — Plugin Sandbox Implementation Spec
- [SPEC-PL-01-js-plugin-system.md](SPEC-PL-01-js-plugin-system.md) — SPEC-PL-01 — JS Plugin System
- [SPEC-PL-02-built-in-file-viewers.md](SPEC-PL-02-built-in-file-viewers.md) — SPEC-PL-02 — Built-in File Viewers
- [SPEC-PL-03-app-card-system.md](SPEC-PL-03-app-card-system.md) — SPEC-PL-03 — App Card System + Database-per-Card Architecture
- [SPEC-PL-04-dynamic-thinking-interface.md](SPEC-PL-04-dynamic-thinking-interface.md) — SPEC-PL-04 — Dynamic Thinking Interface (Iteration Cards)
- [SPEC-PL-05-calendar-integration.md](SPEC-PL-05-calendar-integration.md) — SPEC-PL-05 — Calendar Integration
- [SPEC-PL-06-multi-message-reference-model.md](SPEC-PL-06-multi-message-reference-model.md) — SPEC-PL-06 — Multi-Message Reference Model
- [SPEC-TM-01-topic-data-model.md](SPEC-TM-01-topic-data-model.md) — SPEC-TM-01 — Topic Data Model
- [SPEC-TM-02-auto-topic-detection.md](SPEC-TM-02-auto-topic-detection.md) — SPEC-TM-02 — Auto-Topic Detection
- [SPEC-TM-03-topic-search-one-button-context.md](SPEC-TM-03-topic-search-one-button-context.md) — SPEC-TM-03 — Topic Search & One-Button Context
- [SPEC-TM-04-topic-reference-resolution.md](SPEC-TM-04-topic-reference-resolution.md) — SPEC-TM-04 — #Reference Resolution
- [SPEC-TM-05-topic-lifecycle-sidebar.md](SPEC-TM-05-topic-lifecycle-sidebar.md) — SPEC-TM-05 — Topic Lifecycle & Sidebar
- [T1.1-transport-research.md](T1.1-transport-research.md) — T1.1 — Transport Research: SSE vs WebSocket vs NATS
- [T1.2-crdt-evaluation.md](T1.2-crdt-evaluation.md) — T1.2 — CRDT Library Evaluation: Yjs vs Automerge
- [T1.3-tree-visualization-research.md](T1.3-tree-visualization-research.md) — T1.3 — Tree Visualization Research
- [T1.4-offline-stack-research.md](T1.4-offline-stack-research.md) — T1.4 — Offline-Stack Research
- [T1.5-approval-ux-research.md](T1.5-approval-ux-research.md) — T1.5 — Approval UX Research
- [T1.6-webui-evaluation.md](T1.6-webui-evaluation.md) — T1.6 — WebUI Native App Evaluation
- [T1.7-mls-encryption.md](T1.7-mls-encryption.md) — T1.7 — Security Protocol: MLS-Only Architecture
- [T1.8-multi-transport-architecture.md](T1.8-multi-transport-architecture.md) — T1.8 — Multi-Transport Architecture Design
