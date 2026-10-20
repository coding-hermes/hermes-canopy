# Dogfood run 17 — 2026-10-20 — embedded single-binary SPA, end to end

**Lane:** `hermes-canopy-dogfood-2026-10-20-13-33-32` · **Target:** hermes-canopy @ `/home/kara/hermes-canopy`
**HEAD under test:** `7eecdfac` (version string `v0.1.0-383-g7eecdfac-dirty`) · **Build:** `make build-ui` (Go 1.26.5, Node 22.22.3, 14.47 s wall)
**Verdict:** 🟡 **PROMISING-BUT-ROUGH** — the promise holds and is fast; two of the shipped user-facing surfaces (Share, composer branch) are unusable-from-the-UI dead ends.

## 1. The Promise (null hypothesis, Step 1)

> A user can `make build-ui`, run **one** binary (`canopyd serve`), open a browser on the API port, and get the whole
> graph-native workspace — create a tree, post and branch messages into a DAG, deep-link into a tree, share it — with no
> reverse proxy, no Vite dev server, and no second process.

This run is the first to exercise the embedded-SPA angle: `UI-SPA-1` (commit `b2669538`, 2026-10-19) landed `-tags embedui`
+ `make build-ui`, and no prior dogfood run (1–16) touched it. Prior runs either drove the SPA behind the Vite dev proxy or
behind `deploy/reference-proxy.py`.

## 2. What was actually run (Step 2)

Scratch instance per `docs/SCRATCH_INSTANCE.md` §3 (hand recipe), not the live deployment:

```bash
# own DB on the existing server, own port, own HOME/cards/files, zero inherited env
DB=canopy_scratch_df20_1792503428 ; PORT=8093 ; ROOT=/tmp/canopy-df20
env -i PATH=... HOME=$ROOT/home HTTP_ADDR=127.0.0.1:8093 \
  DB_HOST=127.0.0.1 DB_PORT=5437 DB_USER=canopy DB_PASSWORD=canopy DB_NAME=$DB DB_SSLMODE=disable \
  CANOPY_CARD_DATA_DIR=$ROOT/cards CANOPY_FILE_ROOT=$ROOT/files \
  CANOPY_GATEWAY_STATE_FILE=$ROOT/home/gateway-runs.jsonl \
  HERMES_WEBUI_GATEWAY_BASE_URL=http://127.0.0.1:9 \
  /home/kara/hermes-canopy/bin/canopyd serve
```

Dev JWT minted with the README's own openssl one-liner (`sub=…0001`). Real browser (CDP) used for every UI step; `curl`/`psql`
for ground truth. Gateway pointed at a closed port so no real agent run could fire.

Boot log (the promise, stated by the binary itself):

```
INF canopyd starting db_host=127.0.0.1 http_addr=127.0.0.1:8093 version=v0.1.0-383-g7eecdfac-dirty
INF embedded web UI enabled: serving the built SPA on the API port http_addr=127.0.0.1:8093
INF dev JWT user provisioned user_id=00000000-0000-0000-0000-000000000001
INF dev workspace + profile provisioned profile_name=dev-hermes workspace_id=00000000-0000-0000-0000-000000000010
INF HTTP server listening addr=127.0.0.1:8093
```

### 2a. Surface probe (one binary, one port)

| probe | result | note |
|---|---|---|
| `GET /` | 200 `text/html` 688 B | byte-identical to `frontend/dist/index.html` |
| `GET /trees` | 200 `text/html` 688 B | history fallback ✓ |
| `GET /tree/<uuid>` | 200 `text/html` 688 B | deep link ✓ |
| `GET /assets/index-B2H6JlE2.js` | 200 `text/javascript` 1,029,552 B | single bundle (Vite warns >500 kB) |
| `GET /assets/index-BSyMbYTo.css` | 200 94,757 B | |
| `GET /sw.js`, `/manifest.webmanifest` | 200 | service worker + manifest served |
| `GET /health`, `/healthz`, `/version` | 200 JSON | not shadowed ✓ |
| `GET /metrics` (METRICS_ENABLED unset) | 404 JSON `ROUTE_NOT_FOUND` | documented default ✓ |
| `GET /ws`, `GET /mcp` | 404 JSON | reserved prefixes not shadowed ✓ |
| `GET /api/v1/zzz-not-a-route` **with** token | 404 JSON `ROUTE_NOT_FOUND` | envelope, no HTML leak ✓ |
| `GET /api/v1/trees` **without** token | 401 JSON `TOKEN_MISSING` | ✓ |
| `GET /apixyz`, `/healthcare`, `/versions` | 200 index shell | segment-boundary matching ✓ |
| `--path-as-is /../../etc/passwd`, `/assets/…/../../etc/hosts`, `/assets/` | 200 index shell, 688 B | no traversal, no directory listing ✓ |

### 2b. Real use — the whole loop in a browser

1. `/trees` empty state → **Create your first tree** → filled title / description / root message → **Create** → card appears
   (`1 node · 1 member`, singular labels correct — `UI-POLISH-1` holds).
2. Opened the tree: `Tree View: Dogfood 2026-10-20 tree`, `1 node · 0 edges`, Share button, composer, presence `1 online`.
3. Sent a message from the composer → `2 nodes · 0 edges`.
4. Clicked a node → its context panel opened (`Root: …`, `Context 40 / 8,000 tokens`, model `Server default`).
5. Sent with a node selected → `3 nodes · 0 edges` (**finding DF-HERMES-CANOPY-93**).
6. Clicked the canvas ghost **Add reply** for the root, sent → `4 nodes · 1 edge` (correct singular), API confirms
   `parentId=01a15988…`, `depth=1`, `childCount=1` on the root. **Branching works via the ghost slot.**
7. Deep-link reload `GET /tree/<uuid>` → the DAG re-renders (4 nodes, 1 edge) from a cold navigation.
8. Share dialog → invite by email → `404 INVITEE_NOT_REGISTERED` (**finding DF-HERMES-CANOPY-89**).
9. Trees header count vs. list (**finding DF-HERMES-CANOPY-91**, reproduced twice).

Ground truth for the DAG (not the UI's own claim):

```
$ curl -s -H "$AUTH" $B/api/v1/trees/$TID/nodes | jq -c '.nodes[]|{id:.id[0:8],parentId:(.parentId//"NULL")[0:8],depth,childCount}'
{"id":"01a15988","parentId":"NULL","depth":0,"childCount":1}
{"id":"01a15989","parentId":"NULL","depth":0,"childCount":0}
{"id":"01a15989","parentId":"NULL","depth":0,"childCount":0}
{"id":"01a1598a","parentId":"01a15988","depth":1,"childCount":0}
$ tree GET → node_count 4, member_count 1, stats{branch_count 1, max_depth 2}
```

## 3. Re-proofs of previously landed fixes (this run, HEAD 7eecdfac)

| row | what was re-proven | result |
|---|---|---|
| **DF-84** (P0, fix `6c9a2188`) | flat `/nodes` mount enforces the viewer gate | **PASS** — a real second user shared as `viewer` gets `403 VIEWER_READ_ONLY` on flat `PATCH`, `DELETE`, `/reply`, `/fork`; tree-scoped `PATCH` also 403; **owner control arm 200** and the write landed (PG shows the owner's text). This is the fix for run 15's P0, verified with a *real* viewer membership rather than a synthetic one. |
| **DF-88** (fix `334c5a6e`) | `member_count` is real, not hardcoded `1` | **PASS** — `member_count` 1 → **2** after a successful share, in both the tree GET and `stats`. |
| **DF-79** (fix from run 14) | tree-scoped viewer write denial | **PASS** — `403 VIEWER_READ_ONLY`, message names the tree scope. |
| **DF-87** (closed `b2669538`) | the invitee error message | **PASS on wording, FAIL as a flow** — the message is now explicit and actionable-sounding, but the action it names cannot produce the state it requires (see DF-HERMES-CANOPY-89). |
| **UI-SPA-1** (`b2669538`) | embedded SPA serves and works | **PASS** — §2a/§2b; additionally: unknown `/api/*` is JSON 404, reserved prefixes unshadowed, no traversal, no listing, deep link intact. |
| **UI-POLISH-1** (`b2669538`) | count pluralisation | **PASS** — `1 node`, `1 member`, `1 edge`, `1 conversation tree` all singular-correct. (`DF-HERMES-CANOPY-91` is the *freshness* half of the same counter, not the inflection half.) |

A second user **can** be created on a fresh instance without any register/login endpoint: mint a JWT with a new `sub`,
make one authenticated call, and dev-mode provisioning (`internal/handler/auth.go:39-41` → `db.UserProvisioner.EnsureByID`)
creates the row. It has `email = NULL` — which is exactly why §4/F1 exists.

## 4. Findings (board rows filed, `DF-HERMES-CANOPY` family)

| row | P | one line |
|---|---|---|
| **DF-HERMES-CANOPY-89** | **P1** | The Share dialog can never complete an invite: it only sends `email`, provisioned users are permanently email-less, and the 404's remedy ("the invitee must sign in once") produces exactly such a user. The working path (share by `user_id`) has no UI and no CLI. |
| **DF-HERMES-CANOPY-90** | P2 | README says twice that `canopyd` is API-only / must be behind a reverse proxy; `make build-ui` (the embedded SPA) appears in **no** markdown file. The shipped headline capability is undiscoverable and the docs contradict it. |
| **DF-HERMES-CANOPY-91** | P3 | Trees header count is stale after an in-session create (`0 conversation trees` while the list shows 1; correct only after reload; reproduced twice). |
| **DF-HERMES-CANOPY-92** | P3 | Flat `/nodes` answers `405` with an **empty body and no Content-Type**, while `docs/API.md:1210` says all errors use the envelope — residual of the DF-86 normalisation. |
| **DF-HERMES-CANOPY-93** | P3 | With a node selected, composer **Send** creates a new root (3 sends → 3 orphan roots, `parentId=null`); branching only happens via the canvas ghost "Add reply", which no doc mentions. |
| **DF-HERMES-CANOPY-94** | P5 | `POST …/profiles` answers `INVALID_BODY "request body must be valid JSON"` for a body that *is* valid JSON (unknown/missing fields) — same message as malformed JSON. |

### F1 evidence chain (the P1, quoted)

```
Share dialog (browser, email lexi.dogfood@example.com)
  → 404 {"error":{"code":"INVITEE_NOT_REGISTERED",
          "message":"no account is registered for \"lexi.dogfood@example.com\" on this Canopy instance;
                     the invitee must sign in to the instance once before they can be shared with"}}
# follow the instruction: new subject's first authenticated call provisions the user
$ curl -H "Authorization: Bearer <sub ...0002>" $B/api/v1/trees          → 200
$ POST /api/v1/trees/$TID/share {"email":"..."}                          → 404 (unchanged)
$ POST /api/v1/trees/$TID/share {"user_id":"00000000-...-0002","permission":"viewer"}
  → 201 {"treeId":"…","userId":"…0002","permission":"viewer","memberId":"01a1598c-…"}
$ psql: select id, coalesce(email,'<null>') from users
  00000000-0000-0000-0000-000000000001 | dev@canopy.dev | Dev User
  00000000-0000-0000-0000-000000000002 | <null>        | Hermes user 00000000-0000-0000-0000-000000000002
$ tree GET after the user_id share → member_count 2      (DF-88 re-proof)
```

Code anchors: `frontend/src/components/ShareDialog.tsx:91-144` (payload is `{email, permission, message}` only),
`internal/db/user_repo.go:101-113` (provisioning INSERT omits `email`), `internal/db/bootstrap.go:80` (the only writer of
`users.email`), `internal/handler/tree_handler.go:360-375` (the message), `canopyd --help` (no `user`/`account` subcommand).

## 5. Error-ladder probe (flat `/nodes`, exact commands)

```bash
B=http://127.0.0.1:8093 ; A="Authorization: Bearer <owner>"
curl -s -o /dev/null -w '%{http_code} %{size_download} %{content_type}\n' -X GET    -H "$A" $B/api/v1/nodes/<uuid>          # 405 0 (empty, no CT)
curl -s -o /dev/null -w '%{http_code} %{size_download} %{content_type}\n' -X GET    -H "$A" $B/api/v1/nodes/<uuid>/reply   # 405 0 (empty, no CT)
curl -s -H "$A" -X PATCH  $B/api/v1/nodes/not-a-uuid   # 400 {"error":{"code":"INVALID_NODE_ID","message":"node_id must be a valid UUID"}}
curl -s -H "$A" -X DELETE $B/api/v1/nodes/not-a-uuid   # 400 INVALID_NODE_ID
curl -s -H "$A" -X POST   $B/api/v1/nodes/<uuid>/nope  # 404 {"error":{"code":"ROUTE_NOT_FOUND",…}}
```

## 6. Profiles probe (DF-HERMES-CANOPY-94)

```bash
POST /api/v1/workspaces/<ws>/profiles  -d '{"nonsense_field":1}'   # 400 INVALID_BODY "request body must be valid JSON"
POST /api/v1/workspaces/<ws>/profiles  -d '{oops'                  # 400 INVALID_BODY "request body must be valid JSON"  ← identical
POST /api/v1/workspaces/<ws>/profiles  -d '{"profile_name":"x","profile_token":"y"}'   # 200, mapping created
```

## 7. Step 2b — what it costs in time (measured, not eyeballed)

Build profile: release (`-tags embedui` binary, `bin/canopyd`). Host load average during the run: **15–19** (other fleet
lanes building on the same box — stated because it inflates every cold number).

| operation | method | number |
|---|---|---|
| `make build-ui` (npm build + `go build -tags embedui`) | `time -p`, one-shot | **14.47 s** wall (frontend 1.38 s) |
| boot → first `/health` 200, **existing** DB | port guaranteed free, 3 runs | **0.396 / 0.154 / 0.091 s** (steady ≈0.1 s) |
| boot → first `/health` 200, **brand-new** DB (48 migrations) | 2 runs, fresh DB each | **5.995 / 5.926 s** (an earlier run on a quieter minute: 3.47 s) |
| `GET /trees` (embedded index shell) | hyperfine -N -r 20 warm | **6.4 ms ± 0.6 ms** |
| `GET /assets/index-*.js` (1.03 MB) | hyperfine -N -r 20 warm | **7.0 ms ± 0.6 ms** |
| `GET /api/v1/trees` (authenticated) | hyperfine -N -r 20 warm | **9.6 ms ± 4.4 ms** |
| `GET /api/v1/trees/{id}` | hyperfine -N -r 20 warm | **7.3 ms ± 0.7 ms** |
| browser cold load of `/trees` (cache disabled, CDP) | `performance.getEntriesByType('navigation')` | TTFB **3 ms**, DOMContentLoaded **66 ms**, load **67 ms**; JS asset fetched in 3 ms |
| tree create via UI → card visible | stopwatch in the drive transcript | ≲1 s (perceived instant) |

**No PERF rows filed.** Every user-facing HTTP operation is 6–10 ms and dominated by `curl`'s own ~6 ms process cost
(User 2.2 ms + System 3.8 ms), the 1 MB bundle is served in 3 ms locally and renders in 67 ms cold, and the only
"wait" a user can feel is a **one-time** 3.5–6.0 s first boot while 48 migrations apply on a box at load 19 — not a
hot path to optimise, and measuring it on an intentionally-loaded host would produce a fake hotspot. The 1.03 MB
single-chunk bundle (Vite warns >500 kB) is noted as an observation: on localhost it is invisible, and no honest
network-leg measurement was possible from this box, so there is nothing to file.

## 8. Install leg (fresh machine)

`bunker-qa.sh launch /home/kara/hermes-canopy --keep` → agent `64ab58fc` @ `bunker-mvp` (TTL 4h), testing
`HEAD=7eecdfac4ae710543f9d5f221b6d6a52976474cb`; cells `repo-head OK`, `launch OK`; the detached battery progressed
through `HARNESS-COMPOSE=ok`, `HARNESS_NODE=ok` and into the `act` CI cell (observed live on the agent: `qa-run.sh` at
599 s, `act -q --pull -W ~/qa-act-wf`). Evidence file:
`/tmp/bunker-qa-evidence-20261020T133541Z-1266937.jsonl`. Verdict of the leg is recorded in the run-log line for this
date (see `.coding-hermes/dogfood-log.md`); a leg that never finished is reported as **UNVERIFIED**, never as a pass.

## 9. What is left behind

* Board rows `DF-HERMES-CANOPY-89 … 94` (`boardctl validate` → `RESULT: OK`; each id present exactly once).
* This report (`docs/dogfood/2026-10-20-run17-integration.md`).
* The run line in `.coding-hermes/dogfood-log.md`.
* `skills/hermes-canopy-usage/SKILL.md` — new embedded-SPA section (build command, what it serves, the three traps this run
  hit: Send-vs-reply, share-by-email, README saying API-only).
* Scratch state deliberately left in place (not "cleaned"): `/tmp/canopy-df20/` (logs, probe bodies, tokens),
  DB `canopy_scratch_df20_1792503428` + two `…_boot1/2` DBs, instance on `127.0.0.1:8093`.

## 10. Not done / honest gaps

* No PERF row (§7 explains why).
* The install battery had not finished at the time this report was written — its verdict is the run-log line, and any
  UNVERIFIED/SKIPPED cell it reports must be read as UNVERIFIED, not as a pass.
* The composer/Share findings are UI-behaviour findings from a real browser; no screenshot artefacts were captured this run
  (the transcript is textual: DOM text + API ground truth + PG rows).
* Multi-user **editor/admin** behaviour beyond the viewer gate was not driven (the viewer arm was what DF-84 needed).
