# Dogfood Integration Report — Cards surface + Search (2026-09-24)

Run: hermes-canopy-dogfood tick 2026-09-24-03-04-54. Angle chosen because six
prior dogfood runs (08-17 → 09-23) covered CLI/CAG, collab/MLS, the human path,
MCP, and topics/#references — but never the **Cards** surface (one of the three
MVP pillars: "Every Card is a graph node with structured data") and the search
UX that backs the "searchable subgraphs" promise.

Environment: binary built at HEAD 3ab7e758, scratch PostgreSQL 16
(canopy-pg-dogfood), scratch card dir `CANOPY_CARD_DATA_DIR=/tmp/dogfood-canopy/cards`,
server on :18091, dev JWT minted per README "Authentication (dev mode)".

## The real task

An on-call incident workflow: create an incident tree, append log/rollback
nodes, attach a status card to the rollback node, drive it through
active → dismissed → archived, submit actions, watch the SSE event stream,
export the card store to JSONL.

## What worked (contract-correct, verified live)

- **Card CRUD.** `POST /api/v1/cards` (201, bare object), `GET /api/v1/cards`,
  list filters `tree_id`/`card_type`, `limit/offset`.
- **Optimistic concurrency, exactly as documented.** PATCH without `If-Match`
  → 428 `CARD_REVISION_REQUIRED`; stale revision → 412 with the current card
  snapshot in the body; non-integer → 400; empty patch → 400
  `CARD_PATCH_EMPTY`; PATCH+DELETE on archived → 409 `CARD_STATUS_ARCHIVED`;
  data mutation on dismissed → 409 `CARD_STATUS_DISMISSED`.
- **Status machine.** active→dismissed→(restore)→active and →archived all
  behave per the docs table; archived is terminal.
- **Actions.** Declared handler → 200 with the full event pair
  (`requested_seq`/`result_seq`/`result_event_id`); undeclared handler → 422
  `CARD_ACTION_NOT_DECLARED` with nothing written; missing handler → 400.
  A handler with no app adapter completes deterministically, as SPEC-PL-03 §4.3
  promises.
- **SSE.** `GET /api/v1/cards/{id}/events` writes `card_snapshot` first (no
  `id:` line, per docs), replays stored events after `after_sequence` with
  correct `id:`/`event: card_event` framing, and rejects a negative cursor
  with a JSON 400 before any SSE header.
- **Export CLI.** `canopyd card export --out` → 3 cards / 17 events from 3
  per-type SQLite stores, 6327 bytes, byte-identical across three runs
  including one across a server restart.
- **Durability.** Kill + restart: tree, cards, revisions, statuses, event
  sequences all intact; export unchanged.

## What broke (findings, filed as DF-HERMES-CANOPY-57..60)

1. **[P1] Action submission ignores card status.** `POST /api/v1/cards/{id}/actions`
   returns 200 and appends `action_completed` events to **dismissed** cards
   (docs promise 409 `CARD_STATUS_DISMISSED`) and even to **archived** cards
   (whose PATCH/DELETE are both 409). A "terminal" card keeps accepting
   actions and growing its event log.
2. **[P1] Topic search never searches node content.** Queries matching
   verbatim node text ("timeout", "rollback") return zero results while the
   topic preview lists those same nodes. Root cause: the content arm of the
   search UNION reads `topic_node_content_search`, which is only populated by
   `refresh_topic_node_content_index()` — and `RefreshNodeContentIndex` has no
   production caller (tests only). The table stayed at 0 rows on a live
   session. "Searchable subgraphs" holds only for titles/descriptions.
3. **[P2] Unknown `card_type` filter → HTTP 500** `CARD_LIST_ERROR "internal
   server error"` instead of an empty list or a 400.
4. **[P2] DX nits.** Card JSON uses `"type"` where docs say `"card_type"`;
   casing is mixed across adjacent create endpoints (trees/cards/topics
   camelCase, nodes snake_case — `parentIds` fails with `INVALID_BODY`);
   unknown routes answer raw `404 page not found` instead of the documented
   error envelope; docs don't mention that `actions` can only be declared via
   PATCH (create rejects it).

## Performance (Step 2b)

hyperfine, 20 runs, warm, release binary, localhost:

| operation | mean ± σ |
|---|---|
| GET card | 7.0 ms ± 0.8 |
| GET cards?tree_id | 7.3 ms ± 0.9 |
| POST card (create) | 6.7 ms ± 0.7 |
| topic search | 12.0 ms ± 3.2 |
| card export CLI (3 cards) | 6.9 ms ± 0.9 |
| cold start → /health | 0.02 s |

Nothing here is slow enough that a user would notice — no PERF rows filed,
by the perf skill's own rule.

## Fresh-install leg (ephemeral bunker las-bunker-03, agent 7d064fbb, destroyed)

Followed docs/SELF_HOST.md Option 2 (docker compose) verbatim on a bare
Debian agent with rootless docker:

- documented clone from GitHub: RC=0 in **4 s**
- `docker compose build canopyd`: RC=0 in **242 s**
- `docker compose up -d` + health gate: healthy in **4 s** (compose :8092)
- smoke on the fresh box: tree create 201, card create 201, card list 200

Install docs held up: prerequisites now state Go 1.25+, the compose plugin
requirement, and the rootless `DOCKER_HOST` note (DF-HERMES-CANOPY-55 fixes
landed). Smoke passed. Total documented-path time ≈ 250 s.

## Verdict

🟡 **PROMISING-BUT-ROUGH** — the Cards subsystem is real, fast, durable, and
contract-faithful; the search promise is hollow where it matters (content),
and one status-guard hole lets terminal cards keep taking actions.
