# Dogfood Integration Report — 2026-09-21 (human-path core loop, round 2)

**Run type:** usability dogfood round 2, fresh-user acceptance pass over the full
core loop. This run used the isolated scratch recipe, then one real gateway run on
live `:8091`. No live tree/card/file writes were made.

**Build under test:** HEAD `6dc47e6327bc1cd532825e202f84de3a4a4d49d3`.
The command `git rev-parse HEAD` returned that SHA. The command
`CANOPY_SCRATCH_PORT=8094 CANOPY_SCRATCH_TICK=095 CANOPY_SCRATCH_KEEP=1 scripts/scratch-instance.sh --keep`
printed `HEAD: 6dc47e63`, built `canopyd`, and proved scratch health
`schema_version=48 embedded_migrations=48`. The live `GET /health` probe also
returned HTTP 200 with schema 48 and embedded migrations 48.

**Verdict: 🟡 PROMISING-BUT-ROUGH** — the backend/API core loop is working and
resume/install are comfortably within the measured budgets, but a fresh human
cannot reach multi-reference synthesis from the PWA and the API documentation
omits the preflight request contract. Fresh dev-created nodes also return a blank
author display name. The verdict does not upgrade this round.

## Human-path step table

| # | Step | Result | LIVE evidence |
|---|---|---|---|
| 1 | Install/onboard fresh instance | PASS | `python3 /tmp/time095.py` ran `scripts/scratch-instance.sh --port 8094`; output: `health: status=ok schema_version=48 embedded_migrations=48`, `install_to_first_healthy_seconds=3.707`, `scratch_script_exit=0`. The full scratch run also reported port 8094 free, its own database `canopy_scratch_095_48df872f`, isolated HOME/file root, CLI create/list success, live non-contamination, and exit 0. |
| 2 | Create tree + first nodes via HTTP and CLI | PASS | Scratch HTTP calls `POST /api/v1/trees` → HTTP 201 with a root node; `POST /api/v1/trees/{id}/nodes` → HTTP 201; `POST /api/v1/trees/{id}/nodes/{root}/reply` → HTTP 201; `GET /api/v1/trees/{id}/nodes` → HTTP 200, `node_count=4` after the complete setup. The scratch command's CLI path was `canopyd tree create "$TITLE" --content 'scratch isolation check'`; output: `Tree created successfully.` and `CLI tree list shows the created tree`. |
| 3 | Branch from a mid-tree node | PASS | Scratch call `POST /api/v1/trees/{id}/nodes/{root}/fork` → HTTP 201 with `edgeType:"fork"`; the same root already had a child, so the documented branch rule was usable. Reply and fork both returned node+edge envelopes. |
| 4 | Synthesis / multi-reference reply over two sources | FRICTION | Scratch `POST /api/v1/trees/{id}/reference-selections` with two source IDs → HTTP 200; `POST /api/v1/trees/{id}/multi-reference-replies` → HTTP 201 with `source_count=2` and `is_synthetic_merge_point=true`; `GET /api/v1/nodes/{multi}/reference-context` → HTTP 200 with two sources; `GET /api/v1/context/{multi}` → HTTP 200 with a manifest. The API works, but `search_files` over `frontend/` for `reference-selections` returned `total_count: 0`; the route is not a PWA affordance. Reading `docs/API.md` showed the two route names but not the preflight request body, so the runnable shape had to be recovered from `internal/handler/multi_reference_integration_test.go`. Filed DF-HERMES-CANOPY-42 and DF-HERMES-CANOPY-43. |
| 5 | Real agent-run surface | PASS | `python3 /tmp/live095.py` ran `GET http://127.0.0.1:8091/api/v1/gateway/status` → HTTP 200, `connected=True`, `run_count=0`, `active=0`; `GET .../gateway/models` → HTTP 200, `model_entries=1`; one `POST .../gateway/runs` with `"Reply with the single word: pong"` and a node ID → HTTP 202, `status=started`; `GET .../runs/{run_id}` → HTTP 200, `status=completed`; the run record contained a context manifest and a manifest hash; `GET .../events` → HTTP 200, `event_count=11`, including `run.completed`. The JWT was generated in-process and never printed. |
| 6 | Resume next day against the same DB + HOME | PASS | The scratch walk stopped the first server, restarted against database `canopy_scratch_095_48df872f` and the same `/tmp/canopy-scratch-095-utRzmC/home`, then ran `GET /api/v1/trees` → HTTP 200 with `trees=2` in `milliseconds=510`. This is below the `<30s` resume promise. |
| 7 | Cards + file viewer quick pass | PASS | Scratch `POST /api/v1/cards` → HTTP 201; `GET /api/v1/cards/{id}` → HTTP 200 and returned the card data. Multipart `POST /api/v1/files/upload` → HTTP 201; `GET /api/v1/viewers` → HTTP 200 with `entries=7`; `POST /api/v1/viewers/dispatch` → HTTP 200, `viewerSlug=markdown`, `renderType=fullscreen`, `isBuiltIn=true`; file metadata GET → HTTP 200; file stream GET → HTTP 200, `bytes=25`. |
| 8 | Live read-only spot checks | PASS | `python3 /tmp/live095.py` ran `GET /health` → HTTP 200, `status=ok`, `schema=48`, `embedded=48`; `GET /api/v1/gateway/status` → HTTP 200; and `GET /api/v1/gateway/models` → HTTP 200. No live graph/card/file write endpoint was called. |

## Findings

### DF-HERMES-CANOPY-42 — multi-reference synthesis is API-only from a human's PWA perspective

The two-step multi-reference path is functional over HTTP, but the frontend has
no `reference-selections` call or corresponding affordance. A user following the
normal tree UI can create, reply, branch, and use the existing merge interaction,
but cannot discover this distinct multi-reference reply path without manually
constructing API requests. The acceptance result is therefore API PASS / human
path FRICTION.

### DF-HERMES-CANOPY-43 — preflight request shape is absent from the API reference

`docs/API.md` names the preflight and create routes, but the preflight body needed
for a successful call (`source_node_ids` plus the context budget field) was not
present in the route documentation. The working shape was found by reading
`internal/handler/multi_reference_integration_test.go`. This is a documentation
friction even though the endpoint returned HTTP 200 when called with the recovered
shape.

### DF-HERMES-CANOPY-44 — fresh dev-created nodes have a blank author display name

The fresh scratch node-create responses returned `authorId` equal to the dev user
but `authorDisplayName:""`. The graph operation succeeds, but a human viewing a
new conversation has no readable author label unless the UI substitutes one. This
was not a transport failure; it is a visible identity-quality defect, filed as a
P3.

## Premises checked

- The prior roughness premise that the full graph loop is broadly broken was not
  reproduced: fresh install, HTTP tree creation, CLI creation, reply, fork, cards,
  file viewers, and next-day resume all passed.
- The gateway context-manifest carrier is live at the HTTP surface: the one real
  run completed and its run record contained a manifest hash. The remaining
  human/UI discoverability gap is separate from transport correctness.
- The `<30s` resume promise was not disproved: measured time to the first useful
  authenticated tree page was 510 ms.

## Gate evidence

- `cd /home/kara/hermes-canopy && go build -o /dev/null ./cmd/canopyd` — `STEP_EXIT=0`.
- `gitleaks detect --no-git -v -c .gitleaks.toml` — `STEP_EXIT=0`; output said
  `scanned ~368186775 bytes (368.19 MB)` and `no leaks found`.
- Frontend Vitest/Oxlint — not run; no `frontend/` files were touched.
- `jq -c . .coding-hermes/board/tasks.jsonl >/dev/null && echo BOARD_OK` —
  `BOARD_OK`; the equivalent events command — `EVENTS_OK`; combined
  `STEP_EXIT=0`.

Scratch resources were stopped after the resume pass. The scratch database and
root were retained during evidence collection, then removed after the report and
board writes were verified.
