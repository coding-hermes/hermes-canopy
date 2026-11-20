# Dogfood integration report — hermes-canopy, 2026-11-20 (tick hermes-canopy-dogfood-2026-11-20-11-41-43)

## Promise

Canopy OS claims a technical power user can resume multi-session agent work in
<30 seconds on a graph-native collaboration surface: messages as DAG nodes, a
visible budgeted context manifest for every model call, and structured Cards —
by running `canopyd` (single Go binary + PostgreSQL) and opening the PWA, with
the live Hermes gateway as the run engine (GAP-050).

## What I did (real use, not tests)

Built `canopyd` from HEAD `c605da2d` (go1.26.5, warm build 3.19 s wall / 6.1 s
CPU / 44 MB binary; note: bare `go build` reports `dev commit=unknown` — the
stamp only comes from the Makefile LDFLAGS path). Booted an isolated scratch
instance (unique DB on :5437, port 18093, own HOME + file root, schema 49 ==
embedded_migrations) with the LIVE Hermes gateway (:8642) wired via
`HERMES_WEBUI_GATEWAY_*`, then drove a real user journey over the documented
API:

workspace → tree → reply/fork nodes → merge-tree synthesis (2 parents) →
negative guard (synthesis via node create = 400 VALIDATION_ERROR, correct) →
topic → context compile with manifest + stable `manifestHash` across identical
compiles → budget truncation (budget=32: omittedCount=1, omittedReason=budget,
truncation markers + warnings recorded — the transparency promise holds) →
Cards create/PATCH (If-Match)/action (known 200, unknown 422
CARD_ACTION_NOT_DECLARED, stale revision 412 CARD_REVISION_CONFLICT with the
current card snapshot in the body — contract holds) → card SSE (`card_snapshot`
first frame) → Profiles (DOC-16 surface: create 201, duplicate 409
DUPLICATE_PROFILE_NAME, unknown field 400 INVALID_BODY) → tree export/import
(201, camelCase summary `treeId/rootNodeId/nodeCount/edgeCount/topicCount`) →
gateway runs: plain run completed (18 s, output "ok"), tool-using run completed
(14 s, shell tool ran and returned `df20-df66-check` — the DF-66 P0 fix is
confirmed live), node-linked run failed 2/2 (see findings).

Journey scripts + raw outputs: `.coding-hermes/board/evidence/dogfood-2026-11-20-114143/`.

## Findings (all filed as DF-HERMES-CANOPY-98..101)

### 98 — P0: `GET /api/v1/context/{node_id}?includeCards=true` deadlocks and permanently wedges the cards store

One request with the documented `includeCards=true` param hangs forever (no
timeout middleware fires, no log line, health stays 200) AND globally wedges
the whole cards surface for the life of the process: after the first hang,
card GET / PATCH / LIST / CREATE all hang (000 at 8 s caps) on every tree,
while plain compiles and non-card routes stay healthy. Reproduced on three
fresh boots. Controls (all in `wedge_proof.sh` / `wedge_controls.sh`):

- hangs on a node whose ancestry has NO card (fork node),
- hangs on a tree with ZERO cards (trigger is unconditional on the param),
- plain compile (no param) on the same node: 200 in 3–8 ms,
- `/debug/pprof` is absent (404), so no goroutine stack can be captured from
  outside; the evidence is the behavioral A/B matrix.

The context compiler is the product's flagship and the UI audit dialog
(GAP-084/096) renders card manifests — any user touching this path wedges
their instance until restart. The compile-duration histogram records nothing
for the hung request (counted only at completion), so the wedge is invisible
in metrics too. Fix direction: find the lock/channel wait in the
cards-inclusion compile path; add a handler timeout that fires; consider
exposing pprof. Evidence: wedge_proof.sh, wedge_controls.sh outputs.

### 99 — P1: node-linked gateway runs fail >30 s canopy-side while the gateway completes

`POST /api/v1/gateway/runs` with `node_id` failed 2/2 at ~30 s with
`gateway: read SSE stream: context deadline exceeded`, 1 event captured. The
gateway's own record for the same run says `status: completed` with a full
answer — `internal/gateway/client.go:65` sets a 30 s whole-client Timeout on
the SSE read (error surfaced at `internal/gateway/sse.go:158`), so any run
whose model work exceeds 30 s dies client-side and the user never sees output
that exists. Plain (18 s) and tool runs (14 s) fit under the cap; the compiled
context makes the model slower, so the flagship "Run with context" flow fails
~always for realistic prompts. DF-66/67 fixes verified NOT the cause (tool run
completes end-to-end). Fix direction: no client timeout on the SSE read (or a
run-length-scale timeout; keep stop/approval paths responsive), and surface
gateway-side completion when the stream drops. Evidence: run3.json, run3b.json,
gateway-side record quote in the row.

### 100 — P3: POST /api/v1/gateway/runs returns two different envelope shapes

Without `node_id` the 202 body is a bare run object (`run_id`, `status` at
top level); with `node_id` it is `{"run": {...}}`. docs/API.md §Gateway
documents neither shape (it only says "202 + run_id"). A client written
against one call breaks on the other. Fix direction: one envelope, documented.

### 101 — P1: deploy auto-remediation is deadlocked; live :8091 is 52 h stale

`canopy-deploy-check.service` (hourly, GAP-067/070) reports the deployed
`/home/kara/bin/canopyd` (built 2026-11-18T03:18Z) is 189281 s behind source
`d9bf88f5` and refuses auto-deploy with `STALE_BLOCKED: worktree has
tracked/untracked changes` — streak 4+, 275 `deploy_stale_alert` events total.
The dirty files are the checker's own alert appends to the tracked
`events.jsonl` plus routine fleet board writes in the same checkout: on a fleet
project whose board lives in-repo, the worktree is effectively never clean, so
the STALE_BLOCKED remediation can never fire. Prior instances of the staleness
class: GAP-067, GAP-070, DF-HERMES-CANOPY-32, CARTER-CANOPY-003; this is the
new mechanism detail (self-inflicted dirty state + board-in-repo). Fix
direction: exclude `.coding-hermes/**` from the dirty check (deploy ships
`internal cmd migrations go.mod go.sum` only), or deploy from a detached
worktree at origin/master. Evidence: systemctl status quote, event count.

## Performance (Step 2b) — measured, nothing a user would feel

- Boot-to-ready on a fresh DB (49 migrations): ~2–3 s (log-derived, three boots).
- API warm latencies (curl time_total, one representative run): health 4 ms,
  tree create 36 ms, node create 28–39 ms, merge 13 ms, topic 23 ms, context
  compile 5–10 ms (histogram after leg: count=3, sum=25.6 ms → avg 8.5 ms),
  card create 33 ms, card PATCH 3 ms, profile create 11 ms, export 8 ms,
  import 22 ms. Everything a user touches in the DAG editor is sub-40 ms —
  no PERF row; a win nobody can feel is not a finding.
- Gateway round-trips are LLM work, not canopy cost: 14–18 s for tiny prompts.
- Server-observable resume (`canopy_resume_duration_seconds`, 5-min idle gap
  design): observed after a real >5 min gap in this run — see the tick report
  numbers; the metric was count=0 before the deliberate gap (trigger requires
  the gap; behavior correct, not a finding).

## Notes for maintainers

- `-version` on a bare `go build` prints `dev commit=unknown`; only the
  Makefile path stamps. Fine, but the health payload reports the same
  unknowns — worth knowing when correlating health with a deployed binary.
- Import response is a camelCase bare summary (`treeId/rootNodeId/
  nodeCount/edgeCount/topicCount`) — easy to miss if you expect the created
  tree object.
- The 412 stale-card body carries the current snapshot under `.card` —
  matches the documented contract.

## Verdict

PROMISING-BUT-ROUGH. The DAG/synthesis/topic/context-manifest core is fast,
correct, and genuinely useful; the PWA's flagship context-cards + run-with-
context paths are one P0 deadlock and one P1 timeout away from breaking real
sessions, and the deployed instance most users touch is 52 h stale because its
own repair loop deadlocks.
