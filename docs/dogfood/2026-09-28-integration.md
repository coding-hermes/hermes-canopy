# Dogfood Integration Report — 2026-09-28

**Project:** hermes-canopy
**Angle:** CLI + API surface (tree/node/file/topic-config/session) + bunker install leg
**Verdict:** PROMISING-BUT-ROUGH

## Promise (from README/AGENTS.md)

"Graph-native collaboration surface for human-agent work. Messages are nodes in a DAG. Every model call has a visible context manifest. Every Card is a graph node with structured data."

First customer: "Technical power users working with AI agents across multi-session projects who need to resume work in <30 seconds without manually reconstructing context."

## What Worked

- **Workspace creation** (POST /api/v1/collab): 201 with full workspace object, members array, role=admin. JWT auth (dev-secret-change-me, HS256, 24h) worked first try.
- **Tree create via CLI**: `canopyd tree create "Dogfood Test Tree" --content "..."` returned tree ID + root node ID. `tree list` showed it immediately.
- **Node reply via API**: POST /api/v1/trees/{id}/nodes/{id}/reply returned node+edge wrapper with correct parent, sequenceNum=2, depth=1. Edge type="reply".
- **File upload**: POST /api/v1/files/upload with multipart form returned full file metadata (sha256, mime, storageKind, stream_url, expires_at). Dedup flag (was_new_upload/was_deduped) present.
- **File list**: GET /api/v1/files returned paginated list with correct metadata.
- **Topic detection config**: GET /api/v1/trees/{id}/topic-detection returned defaults (auto_create:false, always_ask:true, detection_level:full, min_messages:3, cooldown:10). PUT with partial update worked — only changed fields updated, others preserved.
- **Bunker install leg**: Clone from GitHub worked. Go install (user-local to ~/go, 33s) + make build (111s) produced 31M binary. `canopyd --version` stamped correctly (v0.1.0-217-gd2d70160).
- **Performance**: --version 9.3ms mean (10 runs), /health 51ms mean (15 runs, 78ms stddev, one 325ms cold-cache outlier). Nothing slow enough that a user would notice.

## What Didn't

- **Session browse CLI**: `canopyd session browse` failed with "snapshot directory unavailable: /home/kara/.hermes/state-backups does not exist". The error message says "use --db to read a specific file" but doesn't explain what populates state-backups or how to create it. A fresh user following the README would hit this with no guidance.
- **Card create API**: POST /api/v1/cards with {tree_id, node_id, type, title, content} returned 400 "json: unknown field tree_id". The docs show the response shape (bare object) but NOT the request body schema. A user has to read source or guess.
- **Bunker Go install**: README says "Go 1.25+" is a prerequisite but doesn't say how to install it on a fresh machine. Bunker agents are non-root with password-required sudo, so `sudo tar -C /usr/local -xzf go.tar.gz` fails. User-local install to ~/go works but isn't documented. A fresh user following the README verbatim would get stuck.

## Friction Count

3 real friction points (session-browse dead-end, card-schema gap, Go-install gap). 0 guesswork moments for the surfaces that worked (workspace/tree/node/file/topic-config all matched docs).

## Time-to-First-Success

~4 minutes (JWT mint + workspace create + tree create + first reply node). Fast once auth was figured out (docs/INTEGRATION.md §Authentication has the exact node -e command).

## Install Leg (bunker-las-03, agent 0a750faf, destroyed)

- Clone: ~5s
- Go install (user-local ~/go): 33s
- make build: 111s (downloaded deps + compiled)
- Binary: 31M, stamped v0.1.0-217-gd2d70160 commit=d2d70160 built=2026-09-28T19:54:47Z
- Total clone-to-binary: ~150s
- Smoke: `canopyd --version` worked, `canopyd tree create` worked against local Postgres (not tested on bunker — would need Postgres install, which is a separate docs gap not probed this run)

## Perf

Nothing slow enough for PERF rows. --version 9.3ms, /health 51ms. The 325ms /health outlier was first-run cold cache; subsequent runs 7-80ms. A user would not notice.

## Findings Filed

- DF-71 (P2): session browse requires ~/.hermes/state-backups but dir doesn't exist by default; error message doesn't explain how to populate it
- DF-72 (P2): Card create API request body schema undocumented — sent tree_id, got "unknown field"; docs show response shape but not request fields
- DF-73 (P2): README says "Go 1.25+" prerequisite but no install instructions for fresh machines; bunker agents can't sudo to /usr/local; user-local ~/go works but isn't documented

## What I Left Behind

- docs/dogfood/2026-09-28-integration.md (this file)
- Board rows DF-71, DF-72, DF-73
- Dogfood log entry appended
- Bunker agent 0a750faf destroyed

## What I Did NOT Touch

- Frontend (no browser verification this run — angle already covered by runs 1-8)
- MCP surface (covered 09-22)
- Gateway integration (covered 09-26)
- Cards UI (covered 09-24)
- Synthesis/merge (covered 09-24b)
- Topics/references (covered 09-23)

This run focused on the CLI + API surface that a fresh user would hit before opening the browser, plus the bunker install leg that proves a fresh machine can build from source.

</