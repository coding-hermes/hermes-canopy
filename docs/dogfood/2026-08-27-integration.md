# Hermes Canopy — Integration Report (2026-08-27)

*Deep real-use dogfood run: Canopy as the LIVE interface of Hermes. Everything below
was executed against the running stack (:5173 PWA, :8091 canopyd, :5437 PG, live
Hermes gateway :8642) — not test scripts.*

## 1. The promise (as of 2026-08-27)

Canopy is no longer just a graph-native knowledge surface. Since GAP-050/051
(BANKAI 08-27) it claims to be **the live interface of Hermes**: the Dashboard shows
real gateway runs, the chat composer starts REAL agent runs (`POST /v1/runs` on the
gateway), SSE streams events into the UI, and approvals can be resolved — with the
gateway API key held server-side (the browser never sees it). Plus the original
promise: DAG trees, context manifests, topics, cards, CLI.

## 2. Real-use walkthrough (what actually worked)

### 2.1 Gateway surface (the new promise) — WORKS

```bash
# mint a dev JWT (README one-liner)
TOKEN=$(node -e "const c=require('crypto');const h=Buffer.from(JSON.stringify({alg:'HS256',typ:'JWT'})).toString('base64url');const p=Buffer.from(JSON.stringify({sub:'00000000-0000-0000-0000-000000000001',iat:Math.floor(Date.now()/1000),exp:Math.floor(Date.now()/1000)+3600})).toString('base64url');const s=c.createHmac('sha256','dev-secret-change-me').update(h+'.'+p).digest('base64url');console.log(h+'.'+p+'.'+s)")

# status
curl -H "Authorization: Bearer $TOKEN" http://localhost:8091/api/v1/gateway/status
# → {"connected":true,"base_url":"http://127.0.0.1:8642","run_count":5,"active_runs":0}

# start a REAL Hermes run
curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  http://localhost:8091/api/v1/gateway/runs -d '{"message":"Reply with exactly: canopy-dogfood-ok"}'
# → {"run_id":"run_adeddf3e21d34226b3d5c0e5a0f0c888","status":"started"}

# SSE event stream (history replay + live fan-out)
curl -N -H "Authorization: Bearer $TOKEN" \
  http://localhost:8091/api/v1/gateway/runs/run_adeddf3e21d34226b3d5c0e5a0f0c888/events
# → data: {"event":"message.delta","delta":"can"} ... data: {"event":"run.completed","output":"canopy-dogfood-ok","usage":{...}}

# stop a RUNNING run
curl -X POST -H "Authorization: Bearer $TOKEN" \
  http://localhost:8091/api/v1/gateway/runs/run_f6abf92cf0874ba18097f65063a7f90a/stop
# → {"run_id":"...","status":"stopping"}  → record becomes status=cancelled, last_event=run.cancelled
```

**Browser path (vite proxy) — WORKS:** `http://localhost:5173/api/v1/gateway/...`
proxies identically. Playwright probe: Dashboard showed "Gateway live ·
http://127.0.0.1:8642", the 5 real probe runs with timestamps, live SSE stream
("streaming response…" → "run.completed — Hello! How can I help you today?").
Typing in the chat composer and sending started a real run that completed with
output "ui-probe-ok". **Zero console errors.**

### 2.2 Core DAG surface — WORKS (all re-tested)

| Flow | Command | Result |
|---|---|---|
| Create tree | `POST /api/v1/trees` `{"title","description","rootMessage":{"content","contentFormat":"markdown","nodeType":"message"}}` | 201, `root_node_id` returned |
| List trees | `GET /api/v1/trees` | `{"trees":[...],"pagination":{...}}` |
| Reply | `POST /api/v1/nodes/nodes/{id}/reply` **snake_case** body | 201 `{node, edge}` |
| Fork | `POST /api/v1/trees/{tid}/nodes/{nid}/fork` (source must have ≥1 child) | 201; leaf fork → 400 VALIDATION_ERROR (documented) |
| Context manifest | `GET /api/v1/context/{node_id}` | `{content, manifest:{tokenBudget:8000, tokensUsed:28, ancestry:[...]}}` |
| Graph stats | `GET /api/v1/graph/trees/{tid}/stats` | `{active_nodes, active_edges, max_depth, ...}` |
| UI create tree | Browser: Trees → New Tree → title + root message → Create | Tree appears in list (GAP-040 FIXED) |
| CLI | `CANOPY_SERVER_URL=... CANOPY_TOKEN=... ./bin/canopyd tree create/list/navigate` | All work; navigate shows hierarchy (GAP-042/045 FIXED) |

## 3. Errors hit and their fixes (the friction)

1. **`GET /api/v1/gateway/status` → 404 page not found** (first probe, 04:05 binary).
   Root cause: the deployed `/home/kara/bin/canopyd` was built 01:52, BEFORE the
   GAP-050 gateway commits (01:57–02:01). The stack was restarted at 11:05 with a
   fresh copy → surface live. **This is GAP-052** (no deploy step in the loop; E2E
   has zero gateway coverage so it passed while the surface 404'd).
2. **`POST /nodes/nodes/{id}/reply` with camelCase → 400 INVALID_BODY "request body
   must be valid JSON"** — the body WAS valid JSON. Root cause: node handlers use
   snake_case struct tags + `DisallowUnknownFields()`; camelCase fields are
   "unknown" → generic decode error. **GAP-053.** Use snake_case
   (`content_format`, `node_type`, `parent_id`) on ALL node endpoints; camelCase
   only on tree-create and topics.
3. **`contentFormat:"text"` → 400 "invalid content format"** — only `markdown` is
   accepted (and it's the default). **GAP-055** (API.md says "string (optional)").
4. **`POST /gateway/runs/{completed}/stop` → 404 run_not_found** — the run IS in the
   registry; the gateway just doesn't accept stop on terminal runs. Misleading.
   **GAP-054** (also: registry is in-memory — restart wipes history).
5. **Playwright probe filled the wrong textarea** (description vs root message) →
   Create button stayed disabled. That's correct UX (validation), not a bug.

## 4. What a new user needs that isn't documented

- The **deploy model**: `systemctl --user status canopy-canopyd` — the binary is
  `/home/kara/bin/canopyd`, NOT `./bin/canopyd` from the repo. After pulling new
  backend code you must rebuild + copy + restart, or the live stack silently runs
  an old binary (GAP-052).
- The **field-casing split** (GAP-053): tree-create/topics = camelCase; nodes =
  snake_case. API.md documents each schema correctly, but nothing says the split
  exists — and the error message actively misleads.
- The gateway surface is **in-memory**: run history is lost on canopyd restart
  (GAP-054). The gateway itself retains runs.

## 5. Verdict

🟡 **PROMISING-BUT-ROUGH** — the new promise (live Hermes interface) genuinely
works end-to-end and is impressive (real runs, real SSE, real approvals from a
browser, key held server-side). The roughness is operational: stale-binary deploys
with no smoke test, a misleading JSON error, in-memory run history, and an
undocumented enum. All four are small, well-scoped fixes (GAP-052..055).
