# Canopy real-use integration — 2026-09-14 (HEAD 9bbe3dc)

## TL;DR

The product's core promise works when it runs: tree → messages → fork →
context manifest → SSE → topics → export, all verified with real API calls on
a scratch DB, plus a 174s fresh docker-compose install on a clean machine that
comes up healthy. What this run found instead is that **the deployed service
had been dead for ~37 hours and nobody was notified** — migrations from the
new file-viewer subsystem were applied to the shared live DB by the E2E/foreman
run, the deployed binary embedded schema 42 vs DB 46, and the STALE BUILD guard
refused every start, forever (GAP-069), while the staleness checker that saw it
coming stayed silent for a day (GAP-070). The brand-new file-viewer subsystem
also shipped with zero docs and unusable-on-fresh-DB provisioning (GAP-071).

## Promise vs reality

**Promise (README/AGENTS.md):** "A user can run the local server + PWA, create
a tree, branch from any message, and see a visible, budgeted context manifest —
resuming work in <30 seconds."

**Reality this run:** on a binary built from HEAD (embeds schema 46), yes —
time-to-first-success ≈ 40s (build) + 15s (JWT + tree + node + context call).
On the DEPLOYED service (`/home/kara/bin/canopyd` + live :5437 DB): no — the
service was down (crash-looping ~27k restarts) at run start.

## The working end-to-end walk (scratch DB, :5438, HEAD binary)

```bash
# 1. Scratch postgres (never touch the live :5437)
docker run -d --name canopy-pg-dogfood -e POSTGRES_USER=canopy \
  -e POSTGRES_PASSWORD=canopy -e POSTGRES_DB=canopy -p 127.0.0.1:5438:5432 postgres:16

# 2. Build + run (HEAD embeds schema 46; migrations apply on first start)
make build
DB_HOST=127.0.0.1 DB_PORT=5438 DB_USER=canopy DB_PASSWORD=canopy DB_NAME=canopy \
  HTTP_ADDR=127.0.0.1:8092 ./bin/canopyd serve

# 3. Dev JWT (dev secret; sub 00000000-…-0001)
node -e "/* README §Authentication one-liner */" > jwt

# 4. Real workflow — every step verified 2026-09-14:
curl -s http://127.0.0.1:8092/api/v1/trees                      # 401 (auth enforced)
curl -s -X POST :8092/api/v1/trees -d '{"title":"T","rootMessage":{"content":"root"}}'
  # → 201 {id, root_node_id}                                    # NOTE: bare object
curl -s -X POST :8092/api/v1/trees/$T/nodes -d '{"content":"child","parent_id":"'$P'"}'
  # → 201 {"node":{...}}                                        # NOTE: {node:...} envelope
curl -s -X POST :8092/api/v1/trees/$T/nodes/$N/fork -d '{"content":"branch"}'
  # → 201; fork a LEAF → 400 "fork requires parent with at least one child"
curl -s ':8092/api/v1/context/$N?budget=4000'
  # → {content, manifest:{tokenBudget, tokensUsed:61, ancestry:[...]}}  # the core promise
curl -s :8092/api/v1/graph/trees/$T/stats                       # node/edge counts
curl -s -X POST :8092/api/v1/topics -d '{"treeId":"…","rootNodeId":"…","title":"t"}'
  # → 201 with VALID root_node_id (GAP-066 verified FIXED)
# SSE: subscribe, then create a node → "event: node_added" arrives mid-stream
curl -s :8092/api/v1/trees/$T/export                            # tree+nodes+edges+version
```

CLI (same session): `canopyd tree create <name> --content <text>` → created;
`tree list`; `tree navigate <id>` → hierarchy. Missing `--content` gives a
helpful error + usage. GAP-042/045 stay fixed.

## File-viewer subsystem (SPEC-PL-02) — what works, what bricks

After hand-seeding workspace+profile+profile_route (see GAP-071), the pipeline
is real and good:

```bash
curl -X POST :8092/api/v1/files/upload -F file=@note.txt
  # → 201 {file:{id, sha256, byteSize, mimeType, viewerHint:"code", …}, was_deduped}
# upload the same bytes again → was_deduped: true (content-addressed dedup)
curl -X POST :8092/api/v1/viewers/dispatch -d '{"file_id":"…"}'
  # → {viewerSlug:"code", bundlePath:"/static/viewers/code/monaco.bundle.js", isBuiltIn}
curl -s :8092/api/v1/files/$FID/stream            # 200, bytes
curl -s -D- -o /dev/null :8092/api/v1/files/$FID/stream -H 'Range: bytes=0-9'
  # → 206 Partial Content, Accept-Ranges: bytes, Content-Range: bytes 0-9/58
curl -X POST :8092/api/v1/files/$FID/access -d '{"action":"open","viewer_slug":"code"}'
  # → 201; GET /files/recents now returns the file
```

**The brick:** without the hand-seeded rows, upload/list/dispatch → 404
PROFILE_NOT_FOUND, and `POST /workspaces/{ws}/profiles` → 500
`fk_profile_route_workspace` (no workspaces row exists; the endpoint doesn't
create one). Reproduced on the bunker fresh install too. Docs: NONE of the 12
routes appear in docs/API.md, README, or INTEGRATION.md.

## Fresh-install leg (las-bunker-03, agent 73a4ebd4, destroyed after)

- Clone from public GitHub @ 9bbe3dc: OK.
- `docker compose up -d --build`: **PASS in 174s**; canopy-pg healthy,
  canopy-server up; `/health` 200 (`schema_version:46, embedded_migrations:46`),
  `/version` 200, unauthenticated `/api/v1/trees` → 401.
- **GAP-068 verified FIXED**: compose comes up WITHOUT a hand-made `.env`.
- Bunker platform note: the agent docker socket is
  `/run/bunker/<agent>/docker.sock` (the per-user `~/bin/dockerd-rootless`
  unit lock-conflicts with the platform daemon — don't start it).
- Same fresh-DB caveat: first `/files` call on the compose DB 404s (GAP-071).

## Friction log (chronological, from the run)

1. `/health` empty + systemd `activating` → discovered the outage (GAP-069).
2. Tree create with `name` → 400 INVALID_BODY "must be valid JSON" (docs say
   `title`; error doesn't name the field). GAP-072-adjacent.
3. `{"parent_id":""}` → 201 ROOT-level node (silent re-parent). GAP-072.
4. Node envelope `{node:…}` vs tree create bare → one real parse failure. GAP-072.
5. Fork empty body → generic INVALID_BODY. GAP-072.
6. `/files` on fresh DB → 404 PROFILE_NOT_FOUND; provisioning endpoint → 500
   FK violation; zero docs. GAP-071.
7. `recents` empty after upload → only /access POSTs count. GAP-072.

## Fix-first list (if you have 1 hour)

1. `make deploy` on a clean worktree (ends the GAP-069 outage).
2. Point E2E/foreman at a dedicated test DB, never :5437 (GAP-069 AC).
3. Alert on crash-loop + STALE_BLOCKED (GAP-070).
4. Seed/documented path for workspace+profile (GAP-071) + API.md section.
