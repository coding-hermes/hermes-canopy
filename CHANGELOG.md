# Changelog

## [Unreleased]

_Nothing yet._

## [v0.3.0] - 2026-11-22 (prep — no tag cut yet)

Range: `v0.2.0`..`d3113afe` — 249 non-merge commits (11 `feat`, 29 `fix`,
23 `docs`, 23 `chore`, 3 `test`, 1 `ci`, plus 6 non-conventional product
commits, with the remaining 153 board, releng, foreman, QA, dogfood and
gitreins bookkeeping commits summarized here rather than listed). No
`BREAKING CHANGE` footer and no `!` type token in the range; semver minor.

### Added

- **Observability (OBS series)** — minimal viable Prometheus set: scrape job,
  build info, gateway gauge and the `/metrics` exemption (`a642dbcc`); DB-backed
  tree/node gauges exercised against real PG (`a483ce3e`, test commit).
- Transport and relay metrics exposed to Prometheus (`164ebe74`).
- Duration histograms for upstream proxy, context compile and card export
  (`d9bf88f5`, OBS-4).
- Data-growth and relay cert metrics (`c9acd4d0`).
- Stall saturation metrics (`da544fe0`).
- Tree membership PATCH/DELETE routes (`8cdf3bce`, SPEC-API-06 §10–§11,
  CAN-MEM-02).
- Share dialog reads the real membership API; the mock is deleted
  (`d17b0485`, CAN-MEM-03).
- Wails v3 desktop packaging contract (`da98b0f0`, FTR-06).
- `canopyd config check` — SQLite config is verifiable without booting the
  server (`3e07215c`).
- `canopyd token` — mint and verify dev JWTs from the CLI (`d1a5c45c`).
- Profile ids exposed for MLS group creation (`3d6d7d05`,
  RVW2-CANOPY-104).

### Fixed

- Gateway: the documented single 202 envelope is returned for
  `POST /api/v1/gateway/runs` (`0850433f`).
- Gateway: a 30s client timeout no longer kills long gateway runs; dropped
  streams are reconciled (`7999ba51`).
- Health: `/health` reports database outage (`387b22d2`, CAN-OPS-01).
- Health: `/health` checks the dependencies map, with a DB-container
  watchdog (`a3fa6ee3`, CAN-OPS-01 r2).
- Cards: the includeCards compile path is unblocked and the cards-surface
  wedge is gone (`fc527c30`).
- Tree detail populates the members roster via `include_members`
  (`950937a2`, CAN-DB-01).
- Metrics: catch-all resolution counted as unmatched in the live-wire fix
  (`d1fe1b92`, CAN-MET-01).
- Frontend: author display name resolved from the payload with UUIDs treated
  as opaque (`2b7cd01e`).
- Frontend: fresh build and bundle budget made explicit (`01f8631b`,
  REVIEW-CANOPY-2026-10-27-FRONTEND).
- Frontend: deprecated service worker bundle option replaced (`355cb914`,
  REVIEW-CANOPY-2026-10-27-SW-BUILD-DEPRECATION).
- Composer: reply goes to the selected node (`b8b422ed`, DF-HERMES-CANOPY-93).
- API: tree-scoped node method errors are enveloped (`f16fa535`,
  DF-HERMES-CANOPY-95).
- API: flat `/nodes` MethodNotAllowed wrapped in the standard error envelope
  (`b21f37ac`, DF-HERMES-CANOPY-92).
- API: `INVALID_BODY` messages differentiate valid JSON with a wrong schema
  (`2b2bc573`, DF-94).
- Wails: explicit backend paths are validated (`4d81b30c`, INT-CI-007).
- Go modules: `prometheus/client_model` promoted to a direct require so
  `go mod tidy` stops breaking the build (`e8c4fa43`, INT-CI-010).
- SQLite: live exports are snapshot-consistent (`864db455`,
  DF-HERMES-CANOPY-97).
- docs-check: false negatives closed (`0d6b1da7`, DF-HERMES-CANOPY-96).
- docs-check: false-negative coverage completed (`47f0ee9b`,
  DF-HERMES-CANOPY-96).
- docs-check: command-path validation tightened (`f33e0ec4`,
  DF-HERMES-CANOPY-96).
- docs: README docs drift check added (`c6686a5c`, README-11).
- docs: blockquoted ghost-env docs drift closed (`26a93e45`).
- docs: ENV.md read-site citations updated to current HEAD (DOC-9), landed
  across four passes (`91054029`, `80bb6d74`, `3a3a7f9d`, `252d0af9`).
- Deploy: the checker's own board ledger is ignored by the deploy safety
  gate (`edb31d2f`, DEPLOY-STALE-1).
- Deploy: other tracked hourly bookkeeping ignored by the deploy gate
  (`401253a4`).
- Deploy: untracked non-deployable debris (wave manifests, lane dumps) no
  longer trips the gate (`cf9d62b5`).
- Canary: `mcp_sdk_canary.py` adapted to the current MCP SDK arity with a
  version gate (`68489afd`).

### Notes

- CI on the candidate tip `d3113afe` is green (27/27 steps on the recorded
  tag-candidate run).
- Release tooling is workflow-only (`.github/workflows/release.yml` on `v*`
  tags); there is no `make release` target. The release path, including the
  `RELEASE_SIGNING_KEY` refusal-to-publish-unsigned rule, is documented in
  `docs/releases/README.md`.
- The deployed `/home/kara/bin/canopyd` is NOT part of this cut; redeploying
  it is a separate step.

## [v0.2.0] - 2026-10-21

Range: `v0.1.0`..`v0.2.0` — 368 non-merge commits (22 `feat`, 56 `fix`,
33 `docs`, 15 `test`, 6 `chore`, plus 195 board and dogfood/qa/gitreins
bookkeeping). No `BREAKING CHANGE` footer and no `!` type token in the range;
semver minor.

### Added

- **Iteration cards (SPEC-PL-04)** — engine core (`d7f68c90`), HTTP/SSE surface
  (`d9329ad6`), live side panel exposing progress and agent activity
  (`e28e6e58`), §8.3 progress model with header aggregation (`2500594d`,
  `3a84f02e`) and the §8.3 frontend ProgressHeader + IterationPluginRenderer
  (`eec5217f`), plus interrupt-safe crash recovery (`0a4f451b`).
- **Cards (SPEC-PL-03)** — renderer dispatch and the card-action invoke path
  (`515777eb`), lifecycle state machine with `If-Match` optimistic concurrency
  (`1f5f3d41`), §6.4 graph attachment via `#card` references (`ee6bb0a2`), and
  node picker / lifecycle controls on the Cards page (`b6e52aff`).
- **Calendar phase one** — event store wired in-process (`2e049dc0`); no HTTP
  surface yet by design.
- **FTR-06 release and update channel** — tag-triggered release workflow
  cross-building five targets with checksums (`9040c059`), signed updater
  (`ce30fb8d`), release packaging with a signed manifest and the
  `cmd/release-tool` packager (`c40d7b14`), and stable manifest publication at a
  well-known URL (`1db1cdec`, validated by `1f86a0ce`).
- **Single-binary SPA** — the built UI is embedded and served by `canopyd`
  (`b2669538`).
- **Build identity** — `canopyd` stamps and serves its own version, commit and
  build time through `-version`, `GET /version` and `GET /health` (`3c5bb29c`).
- **Context compiler (GAP-080)** — multi-reference audit with invalidation
  (`b3b601c5`), budget-dropped node summaries (`d42b4d40`), and the retrieved
  tier rendered in the run indicator (`939216ed`); multi-reference composer
  selection flow (`a2127b73`).
- **Gateway** — run output persisted as a reply node on the source node
  (`e41dcd21`).

### Fixed

- **Security defaults** — viewer read-only permission enforced on shared trees
  with the RED sabotage removed (`3ec76422`, `a831e83d`), hardcoded CORS wildcard
  dropped from the SSE handlers (`bf84f40c`), workspace scope required and the
  client-supplied `sender_id` no longer trusted (`f18c0182`), and three insecure
  defaults hardened (`da92d72a`).
- `NodeAccessMiddleware` mounted on the flat `/nodes` surface (`6c9a2188`) and
  the flat node error contract normalised (`fba87e31`).
- Tree `member_count` returns the real value instead of a hardcoded 1
  (`334c5a6e`); the Share dialog sends `user_id` as a UUID so an invite completes
  on a fresh instance (`a4f5c69e`); `pagination.total` stays in sync with the
  tree list on create/delete (`67946293`).
- MLS: atomic group creation with creator-profile validation (`4c8d61f0`) and
  tampered ciphertext mapped to `400 DECRYPT_FAILED` (`18569ab0`).
- Deploy: compose readiness gate + port wiring (`a05333c6`), version/commit/build
  time stamped in the Docker build (`93539949`), and the staleness threshold
  tightened from 24h to 12h so the live binary cannot lag HEAD for a full day
  (`160d2cbb`).
- Config fails fast on malformed numeric `CANOPY_*` values (`0e9214be`); session
  browse guides users to populate `~/.hermes/state-backups` when it is missing
  (`820c4455`).
- Gateway stream termination treated as terminal truth in both the API and the UI
  (`c69c1e9a`, `32f8fb79`, `cda13edc`); per-instance gateway run registry default
  (`7118ae6c`).

### Notes

- Gates at the tag point: `go build ./...` and `go vet ./...` exit 0;
  `go test -short -count=1 ./...` all packages `ok` (37 packages with tests, 0
  failures).
- The deployed `/home/kara/bin/canopyd` is NOT part of this cut; redeploying it is
  a separate step.

## [v0.1.0] - 2026-09-21

### App Card stream — client half (SPEC-PL-03 §5, §5.3, §6.1, §9)
- The card SSE backend (`GET /api/v1/cards/{card_id}/events`, commit 628dce6) now has a consumer. New `frontend/src/types/card.ts` carries zod schemas for the three REAL wire shapes — `service.CardSummary` (the `card_snapshot` body, whose card type is tagged `type`, not `card_type`), the stored snake_case event row (`event_id`/`actor_kind`/`payload`/`created_at`) and the SSE envelope (`card_type` is `omitempty`, `sequence` is absent-or-0 on a heartbeat) — plus the §5.1 canonical camelCase types and pure wire→canonical adapters. Schemas constrain only what the server actually guarantees (no UUIDv7 regex, no `.strict()`, no `revision`/`dismissedAt`/`archivedAt` demands); every parse returns `{ok:true, value}` or `{ok:false, issues, sequence, eventId}` and never throws at the UI.
- New `frontend/src/lib/cardStore.ts` is the §5.3 reducer, adapted to the shipped wire on two points: the snapshot ordering key is the summary's `last_event_seq` (the wire has no `revision`), and the canonical state is camelCase over a snake_case wire. Rules pinned as pure-function tests: a snapshot applies only when strictly newer and never advances the cursor; an event appends only at `sequence === lastSequence + 1`; a duplicate is a no-op; a gap produces a replay request for `after_sequence = lastSequence` with no synthetic or buffered event; a rejected payload adds a bounded (50-entry) non-destructive error entry and mutates nothing.
- New `frontend/src/hooks/useCardStream.ts` drives the store from `subscribeSse` (the app's only SSE transport — it carries the bearer token, which native `EventSource` cannot) with `eventTypes: ['card_snapshot','card_event','heartbeat']`, re-subscribes ONCE per distinct gap cursor with `?after_sequence=`, records attempted cursors so a gap cannot loop, and exposes card / events / lastSequence / errors / replay plus a connection state that distinguishes heartbeat liveness (`live`) from `open`, `closed` and `error`.
- New `frontend/src/components/CardActivityPanel.tsx` renders the §6.1 default frame derivable from the wire (app id, card type, status badge, creation time, context-hash affordance, activity indicator, declared actions — rendered `disabled`, since `invokeAction` is not wired in this task), the live event list, a connection indicator and the rejection banner. Values are React text children only; no raw-HTML injection API is used. `frontend/src/pages/CardsPage.tsx` opens the panel when a card row is clicked — one subscription at a time (the panel owns it, not the row) — with tree select, type filter, search, create and archive/delete behaviour unchanged.
- Docs: `docs/API.md` § Cards gained a "Client stream consumption" note (route, the three event names, the snapshot-has-no-`id:` rule, the `last_event_seq` ordering key, the gap→`after_sequence` replay, the discarded `id:` line).
- Tests: 61 new vitest cases across 4 files (wire schemas + adapters, the store rules, the hook against a module-mocked `subscribeSse`, and the panel rendering real frames including the non-destructive rejection path). Falsified by mutation: removing the exact-sequence rule turns 9 cases red, removing the strictly-newer snapshot guard turns 1 red, and widening it to `<` turns 1 red.

### Context compiler retrieved tier (GAP-080 phase 4a)
- The context compiler gained an optional retrieved tier: when `CONTEXT_RETRIEVAL_MAX` > 0 (default 0 = disabled), topic full-text search results (SPEC-TM-03) are folded into the compiled payload between the references block and the cards block, with their own token allocation (`floor(budget * 12 / 100)`, clamped by remaining budget). Ordering is deterministic (relevance DESC, slug ASC, id ASC), topics already included as references are never duplicated, and the tier degrades safely: reader errors and budget omissions are warnings, never compile failures, and no search is issued when the tier has no budget or the current node's content is blank. The manifest records the tier as `retrieved` (items with `kind: retrieved_topic`, `relevance`, `tokenCount`) plus `retrievalBudget` when the search ran; with the tier unwired, `Content`, manifest JSON and `manifestHash` are byte-identical to the previous build. New adapter package `internal/retrieval` maps `search.TopicSearchService` onto the compiler's `RetrievalReader` seam with no side effects (no `LogSearch`, no logging). The tier is scoped to the CURRENT NODE's tree (`currentNode.TreeID`); `CompileRequest.TreeID` is not consulted (no production caller sets it, so a request-scoped search silently disabled the tier in production), and a node with no tree skips the tier silently.

### Hermes session source (GAP-077)
- Session reads now come from the 6-hourly Hermes state SNAPSHOT (`~/.hermes/state-backups/state_<YYYYMMDD>-<HHMMSS>.db[.zst|.gz]`), read read-only through `ATTACH DATABASE 'file:<copy>?mode=ro'` — the live `~/.hermes/state.db` is never opened, so browsing/importing cannot contend with (or write to) the gateway's database. Snapshot selection is deterministic (newest by embedded stamp, regular files only, no symlinks, no traversal), bounded by `--max-snapshot-age` (default 24h), and fails loudly rather than falling back to the live DB. New `canopyd session browse [--session <id>]` prints the session list/messages from that source; `session import` / `associations-backfill` use it by default, with `--db <path>` (read-only), `--snapshot-dir`, and `--max-snapshot-age 0` as explicit overrides.

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

### Phase 12 — Real-Data Hardening, Topics Engine & UI Polish (2026-08-09 → 2026-08-23)
- Topics engine (TM-02/03/04): NLP auto-topic detection with configurable sensitivity, PostgreSQL tsvector full-text search with one-button context injection, `#reference` resolution building context DAGs (context compiler)
- Real-data import hardening: BUG-034 `set_content_hash` bytea/backslash crash → migration 000025 `convert_to(content,'UTF8')` + regression test; BUG-035 per-session watermark idempotency (legacy 48-tree partial state deduped); BUG-037 NUL-byte/invalid-UTF-8 sanitization — full session import runs to completion (3,665 trees)
- Association layer (WIRE-006) + Related panel (UI-REL-001): session lineage (parent/child/delegation goals) as tree edges + task/commit/project metadata; per-tree drill-down UI
- Cursor pagination (PAG-001/002): keyset `(created_at, id)` + real COUNT total (3,611), Trees page load-more; page-2 correctness proven live
- Hermes-webui pattern adoption: schema-tolerant state.db reader (IMP-008 — PRAGMA introspection, optional-column SQL, graceful degradation) + source-classified session list (UI-LIVE-001 — CLI/Cron/Telegram/API/Tool/Webhook/Kanban groups, continuation merging, search)
- Mobile sidebar drawer (UI-MOB-001): slide-in drawer, hamburger (md:hidden), X/backdrop/ESC/route-change close; desktop pixel-identical
- Real-world wiring fixes: BUG-038 `/tree/demo` alias resolution, BUG-039 plain-HTTP `crypto.randomUUID` fallback + demo alias, BUG-040 same-user two-tab realtime sync (regression pin), BUG-041 real `node_count` (GROUP BY counts), BUG-042 SW SSE cache-poison + thin-node race (SW_VERSION 1.0.1, SSE never cached), BUG-043 deleted-tree node routes → 410 TREE_DELETED, BUG-044 E2E battery DB pollution (TreeCleanup teardown ×6 files + `scripts/sweep-e2e-test-trees.py`)
- Canvas 2D fallback renderer (STACK-03): custom manual renderer for >2000-node trees + TreeCanvasSwitch; STACK-04 verified (offline PWA already delivered by FE-09 — manual SW, not Workbox)
- Deploy/infra: INFRA-002 canopyd image rebuilt from HEAD (embeds migrations ≤031, restart-loop fixed); INFRA-003 GitHub credentials restored, 12-commit backlog pushed
- Docs/devx sweep (GAP-021..046): INTEGRATION API walkthrough port, `make test-short`/`make test` timeouts (300s/600s) + `-short` PG skips, LOG_FORMAT/CORS_ORIGIN/DB_SCHEMA env vars wired, dev-mode auth story documented, PWA-embed claims corrected, README API tables regenerated from server routes (49/49 live), CLI `--help` + `tree create --content` + `navigate` hierarchy output, dead-org rename (totalwindupflightsystems → coding-hermes), .env.example sync, board source-of-truth reconciliation, E2E evidence trail (docs/E2E-EVIDENCE.md), tree-scoped fork route + UI Branch action (GAP-041/043), topic dialog root-node auto-resolve (GAP-044)
- E2E windows (392–397): integration battery 61/61 incl. two-tab sync pin; visual-regression stability (VREG-001 demo-tree selection by name, VREG-003 resolved via backend route-shadowing fix E2E-ROUTE-001)
- Current test state (2026-08-23): vitest unit 710/710 (37 files), integration 61/61 (13 files, Playwright + visual goldens), Go build/vet/`make test` PASS (PG-backed), judge-verified per task

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
