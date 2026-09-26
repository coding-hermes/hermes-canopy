# Dogfood Integration Report — 2026-09-26: Live Hermes Gateway Integration

**Round:** 9 (angle untouched by runs 1–8: collab/MLS, core loop, MCP+deploy, topics/#refs,
cards+search, synthesis/merge/sync/plugins — the LIVE GATEWAY surface had never been driven
end-to-end by a dogfood run).
**Stack used:** canopyd v0.1.0-133 (schema 48) on :8091, Vite dev server :5173, live Hermes
gateway `hermes gateway run` on 127.0.0.1:8642, PostgreSQL 16 on :5437.
**Verdict:** PROMISING-BUT-ROUGH.

## Promise under test

"Chat with a live Hermes gateway from inside Canopy — real runs, real events, no seeded
data. Every model call has a visible context manifest." (README flagship screenshot +
docs/API.md §13, SPEC-FTR-07, GAP-050/075/080/084/096.)

## What a real user does — and what actually happened

### 1. Dashboard chat (no tree context)

`POST /api/v1/gateway/runs {message}` → 202 + run_id in **21 ms** (cold). The dashboard
"Live event stream" panel is a real SSE feed: `reasoning.available`, `message.delta`,
`run.completed` all render live. A prompt that never needs a tool ("Reply with exactly:
ui-ok") completes end-to-end and the answer displays. ✅

### 2. Chat with a tool-using prompt — **broken (P0, DF-HERMES-CANOPY-66)**

Any prompt that makes the agent touch a tool (the agent `ls`s the home directory while
thinking about "what trees exist" — i.e. virtually every real prompt) dies canopy-side:

```
data: {"event":"tool.started","run_id":"run_1219…","tool":"terminal","preview":"ls -la ~ …"}
data: {"event":"run.observe_error","run_id":"run_1219…",
       "error":"gateway: decode SSE event: json: cannot unmarshal bool into Go struct
                field RunEvent.error of type string"}
```

Raw capture straight from the gateway (`GET /v1/runs/{id}/events` with the gateway key)
shows the poison pill: the gateway emits `"error": false` (bool) on `tool.completed`, and
canopyd's `RunEvent.Error string` (internal/gateway/sse.go) cannot decode it. The gateway
run keeps running and later completes; canopyd shows it as **disconnected** while the UI
rail simultaneously labels it **running** (DF-HERMES-CANOPY-69). Reproduced 2/2.

### 3. Tree-scoped context run (the flagship flow) — works until the output vanishes

Tree → click a node → "Run with context" lights up (GAP-084) → audit dialog (GAP-096)
previews the compiled manifest: `29 / 8,000 tokens`, manifest hash `d3711be061f8`,
included sources listed, Adjust budget / Cancel / Send. This dialog is genuinely good —
the auditable manifest is REAL. Send → `run.completed` (`manifest-ok`) verified against
`GET /api/v1/gateway/runs`.

But the agent's answer **never lands in the tree** (P1, DF-HERMES-CANOPY-67): the user's
message becomes a node (composer send path), the run completes, and the output exists only
in the transient run registry. `metadata:{}` on the source node, no reply node, no card.
The promised "conversations render as a live DAG" is one-sided: only the human side of the
conversation is a DAG.

### 4. Model catalog / budget derivation — inert by default

`GET /api/v1/gateway/models` → `context_window: 0, desired_budget: 8000,
source: "unknown_model"`: the live gateway's `/v1/models` carries no `context_length`
(verified again this round), so GAP-080 budget math is inert unless the operator sets
`CONTEXT_MODEL_WINDOWS` — a knob documented only in the env-var table (P1,
DF-HERMES-CANOPY-68, together with the docs' `message` vs gateway's `input` field
mismatch: a raw `POST /v1/runs {"message": …}` answers `400 Missing 'input' field`).

## Working example (verified commands)

```bash
# 1. dev JWT (README § Authentication), then:
TOKEN=...   # HS256, sub 00000000-…-0001, secret dev-secret-change-me
curl -H "Authorization: Bearer $TOKEN" http://localhost:8091/api/v1/gateway/status
# → {"connected":true,"run_count":N,"active_runs":0}

# 2. tree-scoped run with visible manifest:
curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  http://localhost:8091/api/v1/gateway/runs \
  -d '{"message":"Reply with exactly: manifest-ok","node_id":"<root-node-uuid>"}'
# 202; response carries the full compiled manifest + manifestHash.

# 3. stream events:
curl -N -H "Authorization: Bearer $TOKEN" \
  http://localhost:8091/api/v1/gateway/runs/<run_id>/events

# 4. raw gateway check (bypasses canopyd) — needs {"input": ...}, NOT {"message": ...}:
curl -X POST -H "Authorization: Bearer $GATEWAY_KEY" -H 'Content-Type: application/json' \
  http://127.0.0.1:8642/v1/runs -d '{"input":"Reply with exactly: raw-capture-ok"}'
```

## Perf (Step 2b — nothing worth a PERF row)

- `GET /api/v1/gateway/runs` warm: **7.3 ms ± 0.5 ms** (hyperfine, 20 runs).
- `GET /health`: 6.2 ms ± 0.3 ms.
- Gateway routes via curl: status 2.0–2.2 ms, models 0.7–0.8 ms.
- Compile POST (fresh manifest, node-scoped): 4.4 ms, 76 ms, 101 ms across 3 probes
  (outliers include the gateway POST handoff).
- SSE stream: first live event lands within ~2 s of the agent starting; heartbeats keep
  the stream open.

No operation was slow enough that a user would notice; no PERF rows filed.

## Install leg

SKIPPED-install-bunker (DF-HERMES-CANOPY-70): bunker-las-03 offline — tailscale reports
"offline, last seen 8h ago"; ssh connect timeout, ping 100% loss, `bunker list` deadline
exceeded. CLI itself was ready (bunker 0.1.4). No visibility/credential changes made.
Last proven install: 2026-09-24 (compose build 242 s, smoke ok).

## What to fix first (one hour of maintainer time)

1. `RunEvent.Error` → tolerate bool (P0). One struct field + one regression test with the
   captured `tool.completed` payload. Everything else about the gateway surface then works.
2. Persist run output into the tree (reply node or node metadata) — the conversation value
   claim depends on it.
3. Two doc fixes: `input` vs `message`, and `CONTEXT_MODEL_WINDOWS` in the quickstart.
