# Dogfood Integration Report — 2026-09-23 (topics · #references · context compiler · export/import)

**Run type:** cron dogfood (9th for this repo), NEW ANGLE. Prior runs covered
trees/nodes/context-manifests/SSE (08-17), live gateway (08-27), fresh-DB +
compose install + file viewer (09-10/09-14), CLI + card export (09-16),
two-user collab/MLS (09-20), PWA human-path round (09-21), MCP + production
deploy (09-22). This run took the surfaces none of them touched: **Topics**
(core concept #5), the **#reference resolution surface** (autocomplete /
resolve / inject / SSE events — 6 live routes that are in NO documentation),
the **context compiler's reference injection** with budget/omission
transparency, **tree Export → Import** (the MVP promise, never exercised
before), plus the **fresh-machine install leg** on an ephemeral bunker.

**Build under test:** scratch instance from local HEAD `1fe262c5` (schema 48/48,
scratch DB `canopy_scratch_df0923_96888a81`, isolated HOME + file root, gateway
pointed at :9 — no live writes). Findings re-verified against `origin/master
bf0f7e02` before filing (only board commits since 1fe262c5 — nothing already
fixed).

**Verdict: 🟡 PROMISING-BUT-ROUGH** (6th consecutive) — the read/compile side
of topics+references is genuinely good and fast (search with highlighting,
preview, autocomplete, compile-time injection all work in <15 ms), but the
**write side of references is silently broken end-to-end** (P0: every
send-time #reference is lost to a users-vs-profiles FK mismatch, and reply/fork
never even attempt resolution), and **import fails for any real tree** (P1:
503 on every tree that has edges). Both are masked by green tests because the
compile path re-scans content and the import fixtures are edgeless.

Board rows: **DF-HERMES-CANOPY-50 … 55** (committed 78485bf0, pushed).

## Promise statement

README/AGENTS: *"Named, searchable subgraphs with #references. Context
compiler resolves references per budget rules. … Messages are nodes in a DAG …
resume work in <30 seconds."* Export/Import ships in the MVP scope. A real
user should be able to: create topics, reference them from messages with
#slug, get them autocompleted and injected into model context under a token
budget, and back a tree up by exporting and re-importing it.

## Human-path step table

| # | Step | Result | Evidence |
|---|---|---|---|
| 1 | Fresh scratch instance | PASS | `scripts/scratch-instance.sh --keep` full battery exit 0 (auth gate, refusal control, live non-contamination, card store isolation); long-lived server re-started by hand on :8097 → `/health` 200 schema 48/48 |
| 2 | Create tree + topics | PASS | `POST /trees` 201 (camelCase, `rootMessage.content` required); `POST /topics {treeId,rootNodeId,title}` 201 ×2 — server derives slug from title (`q3-outage`, `followups`); topic keys include `slug,status,node_count` |
| 3 | Build graph with #refs | PASS | reply 201, fork 201 (leaf rule respected), plain create 201; nodes with `#q3-outage`, `#followups`, edge cases (`#Q3-Outage` casing, `# card:`-adjacent, URL anchors, `#card:<id>`, unknown slug) all accepted |
| 4 | Reference discovery surface | PASS (undocumented) | `autocomplete?prefix=q3` → 200 `{slug,match_type:prefix}`; `topics/search?q=q3` → 200 with `<mark>q3</mark>` highlight + total + query_time_ms; `topics/recent` → 200 relevance-ordered; `topics/{id}/preview` → 200 snippets + participant_count. **None of these 6 routes exist in docs/API.md** → DF-52 |
| 5 | Resolve / inject | PASS (undocumented shapes) | `POST references/resolve {content,max_nodes,with_context}` → 200 per-ref resolved + `not_found[]` (archived topic reports `status:archived`); `POST references/inject {topic_ids:[…]}` → 200 merged MultiTopicContext + SSE `context_injected` broadcast. Shapes recovered from `reference_handler.go`, not docs → DF-52 |
| 6 | Send-time persistence of refs | **FAIL** | `node_resolved_refs` count = **0** after creating 6+ nodes carrying valid refs; `reference_resolution_log` shows the attempt ran (status `resolved` from plain create) and the server WARNs `node_resolved_refs_resolved_by_fkey (SQLSTATE 23503)`; reply/fork never attempt at all; zero `reference_resolved` SSE events on a live listener → **DF-50 (P0)** |
| 7 | Context compiler w/ refs | PASS | `GET /context/{node}?budget=4000` → envelope `{content,manifest}`; manifest has `references[{id,kind:topic,title,tokenCount,truncated}]`, `manifestHash`, honest omission (`omittedCount:151, omittedReason:"depth"`, truncationMarkers null at 201-node scale). Archived topics still inject (behavior to decide, noted in DF-50 detail) |
| 8 | Topic update / archive | PASS + doc bug | PATCH 200 (description updated, autocomplete/search reflect it); DELETE → **204 empty** while docs/API.md documents "200 Archived topic detail" → folded into DF-52 |
| 9 | Export → Import | **FAIL for real trees** | Export 200 `{tree,nodes,edges,version,exportedAt}` (5 nodes/4 edges, 3996 bytes; **no topics** → DF-54a). Import → **503 SERVICE_UNAVAILABLE**; log: `null value in column "sequence_num" … (SQLSTATE 23502)` — edgeless tree imports 201 fine → **DF-51 (P1)** |
| 10 | Fresh-machine install (bunker) | PASS via binary path, FAIL via documented make path | see below |

## Fresh-machine install leg (ephemeral bunker las-bunker-03)

Agent `b35d2bfc` (ttl 2h, destroyed after — verified `bunker list` no longer
shows it), bare Debian user, uid 1002, preinstalled: git 2.47.3, Node
22.23.2, **no Go toolchain**, no docker-compose plugin.

| Leg | Command per docs | Result |
|---|---|---|
| Clone | `git clone --depth 1 https://github.com/coding-hermes/hermes-canopy.git` | 22 s, HEAD `bf0f7e02` |
| `make build` (README Quick Start) | documented | **FAIL** `make: go: No such file or directory` (Error 127) — Go is not in README prerequisites → DF-55 |
| Release binary (SELF_HOST Option 1) | `curl -L -o canopyd …/canopyd_linux_amd64; chmod +x` | 5 s download, `./canopyd -version` → v0.1.0; the documented `sudo mv /usr/local/bin/` fails (no sudo) → DF-55 |
| PostgreSQL (Quick Start block) | `docker run … postgres:16-alpine` + pg_isready loop | PASS (rootless docker via per-user socket) |
| Serve + health | `DB_* HTTP_ADDR=:8091 canopyd serve` | healthy in **<1 s**; 48 embedded migrations ran; note: `schema_version:0` reported on first boot (embedded_migrations:48) — cosmetic quirk worth a look someday |
| Smoke (CLI) | `canopyd tree create` + `tree list` against :8091 | PASS — created `2317a146…` and listed it |

## Measurements (Step 2b — nothing slow enough for a PERF row)

Perf law: a win nobody can feel is not a finding. Every operation here is
comfortably fast; numbers recorded so the "fast enough" claim is measured, not
vibes. No PERF-<n> rows filed.

| Operation | Numbers |
|---|---|
| Node create (chain, 200 sequential) | 4.0 s total = **20 ms/node**, 0 failures |
| Compile context, 201-node chain leaf, budget=4000 | warm 10–19 ms (mean 13–15 ms, 13 runs); tokensUsed 1738, omitted 151, omittedReason "depth", 50-ancestor cap |
| Compile context, tiny budget=120 | warm 12–14 ms, tokensUsed 95, omitted 198 |
| Cold start | server restart → health **0.20 s**; first compile after restart 12 ms; resume tree list 3 ms (7 trees) — the <30 s resume promise remains nowhere near binding |
| Export 201-node tree | 160 KB JSON, ~2 ms |
| topics/search / preview / autocomplete / resolve / inject | 1–12 ms each |
| Install leg | clone 22 s; binary download 5 s; serve→health <1 s (compare 09-22 source build: 152 s) |

## Friction count and time-to-first-success

Time-to-first-success (API consumer): ~20 minutes including scratch boot and
two self-inflicted request-shape misses (`rootMessage` required on tree
create — docs correct, my miss; `authorId` rejected — docs correct). The REAL
friction was undiscoverable surface: 6 live routes with shapes recoverable
only from handler source (DF-52), and a reference feature that accepts your
`#ref` with 201 and then silently drops it (DF-50) — the worst kind of
friction because nothing tells you it broke. Friction events: 4 (2 self-, 2
product).

## What I left behind

- `docs/dogfood/2026-09-23-integration.md` (this file)
- `skills/hermes-canopy-usage/SKILL.md` v2.5.0 — new Topics & #references
  section (real request shapes for the 6 undocumented routes), install-truth
  update, silent-loss warning
- `docs/dogfood/diagnostics.md` — three new lessons (FK domains differ per
  table; swallow-and-continue hides feature death; edgeless fixtures hide
  import breakage)
- `.coding-hermes/board/tasks.jsonl` rows DF-50…55 + events (commit 78485bf0)
- `.coding-hermes/dogfood-log.md` entry

Scratch cleanup: DB `canopy_scratch_df0923_96888a81` and
`/tmp/canopy-scratch-df0923-lXAQ1Z` retained for foreman reproduction (same
KEEP contract as prior runs); bunker agent destroyed.
