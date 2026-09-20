# Dogfood Integration Report — 2026-09-20 (workspace / collab / MLS surface)

**Run type:** cron dogfood (6th for this repo), NEW ANGLE. Prior runs covered
trees/nodes/context-manifests/SSE/topics/cards (08-17), the live gateway (08-27),
fresh-DB + bunker install + file viewer (09-10/09-14), CLI + SQLite export
(09-16). This run took the surface NONE of them touched: the multi-user
collaboration stack that AGENTS.md advertises as shipped — **workspace CRUD +
membership + invitations (SPEC-FTR-01), tree sharing + SPEC-API-06
invite/member endpoints, workspace channels (SPEC-023 §5), and MLS group
encryption (SPEC-FTR-03)** — driven as two real users (Alice + Bob, separate
JWTs) through a full create → lockout → invite → join → share → co-write →
MLS group → encrypt/decrypt lifecycle.

Build under test: HEAD `85eaf18`, locally compiled (CGO_ENABLED=0, 25s), run on
a fresh scratch DB (`canopy_dogfood_0920`) on :5438/:8098 — the live :5437 stack
was never touched. Plus an **ephemeral-bunker fresh install** on las-bunker-03
(agent `33c0cbb7`, documented clone URL, `docker compose up -d --build`).

**Verdict: 🟡 PROMISING-BUT-ROUGH** — see `.coding-hermes/dogfood-log.md`.
Board rows: **DF-HERMES-CANOPY-35 … 40**.

## Promise statement

AGENTS.md: *"Workspace CRUD, profiles, workspace channels, membership checks
and MLS group encryption (SPEC-FTR-01/03) are live behind auth."*

## What actually happened (two-user lifecycle, all evidence live)

| # | Step | Result |
|---|---|---|
| 1 | Bob authenticates (signed dev-secret JWT, no users row) | 200 on reads, **500 INTERNAL_ERROR on his first write** (`workspaces_owner_id_fkey`) — error only in the server log |
| 2 | Alice creates workspace | ❌ 500 first (same FK — bootstrap provisions only user `…0001`); ✅ 201 after hand-seeding users rows per the repo's own convention (`hermes_user_id` = JWT sub, learned from `internal/db/bootstrap.go`) |
| 3 | Bob GETs Alice's workspace | ✅ 403 `NOT_WORKSPACE_MEMBER` — membership guard is real |
| 4 | Alice PATCHes workspace description | ✅ 200 |
| 5 | Alice mints invitation → Bob joins with token | ✅ token + 7-day expiry; join 200; members list shows both, roles admin=2/member=1 |
| 6 | Alice shares her tree with Bob (`POST /trees/{id}/share`, email+permission) | ✅ 201, permission `editor` resolved |
| 7 | Bob **writes** a reply node in the shared tree | ✅ 201, seq 2/3, Alice sees his content in `/nodes` |
| 8 | Bob **reads** `GET /trees/{id}` after the share | ❌ still 403 `NOT_TREE_OWNER` — the share unlocks writes but not the tree read |
| 9 | SPEC-API-06 endpoints (`/trees/{t}/invite`, `/invites/{token}/accept`, `/trees/{t}/members`, …) | ❌ literal `404 page not found` — never mounted, though the spec marks every one **Required** |
| 10 | Workspace channels: list, cross-user POST, live SSE delivery | ✅ Bob's feed received Alice's message live (`channel_message`, full payload); ❌ no history replay for late joiners; channels are global (any authenticated user; `general`/`agents` exist for everyone), not per-workspace |
| 11 | MLS: create group (real Ed25519 keys), Bob joins via key package, epoch bump on join | ✅ 201/204 mechanics, SSE `mls:welcome_message` broadcast works |
| 12 | **MLS: Alice encrypts → BOB decrypts** | ❌ **`mls: gcm open: cipher: message authentication failed`** (500). Only the sender can decrypt their own ciphertext. `GET /state` shows `ratchet_tree: null` |
| 13 | Bob leaves group → re-encrypt | ✅ correctly rejected `NOT_GROUP_MEMBER`; unauth access 401 |

## The MLS reality (read the code, not the name)

`internal/mls/service.go` is **not RFC 9420** despite the cipher-suite string,
`wire_format: "mls_ciphertext_v1"`, and the "opaque RFC 9420 application-message
representation" type comment. Encrypt derives ONE static AES-256 key from
`sha256(group_id ‖ member.EncryptionPublicKey)` — public, non-secret material
(the code's own comment: *"In a full MLS implementation, this would use the
group's epoch secret"*). Decrypt re-derives the key from the RECIPIENT's public
key, so any ciphertext is readable only by its sender. Forward secrecy,
group-key agreement, and cross-member decryption — the entire point of MLS — do
not exist. What ships is per-sender AES-GCM with keys stored in the DB, plus
faithful-looking envelope shapes.

## Fresh-machine install leg (bunker las-bunker-03, agent 33c0cbb7 — destroyed)

- Clone with the project's own documented URL
  (`github.com/coding-hermes/hermes-canopy`) worked anonymously — no visibility
  or credential changes made.
- `docker compose up -d --build` (the documented compose path): **RC=0 in
  153s**, `canopy-pg` healthy, `canopy-server` up.
- Smoke (documented `curl /health`): 200, `schema_version 47` =
  `embedded_migrations 47`. Headline probe: authed `GET /api/v1/collab` → 200
  `{"workspaces":[]}` on the fresh DB.
- One friction: the install docs assume a system (root-owned) Docker daemon.
  On a rootless/bunker layout the user-level socket needs
  `DOCKER_HOST=unix:///run/bunker/<agent>/docker.sock` — the docs never
  mention non-root daemon layouts. Compose v5.5.0 and Node 22 were present;
  the README's `go 1.25+` prerequisite is NOT needed on the compose path.

## Time-to-first-success & friction

- This angle: ~90s of API calls once identities existed; **~18 min wall clock**
  to get there, almost all spent diagnosing the silent second-user FK 500 and
  reverse-engineering provisioning from source.
- Friction count: **10** (each one is a bullet in `.coding-hermes/tasks.md`
  or a board row). Had-to-read-source-to-proceed moments: collab handler
  routes (API.md has no /collab section), bootstrap.go seeding convention,
  MLS request shapes (docs/API.md documents paths only, not bodies),
  `internal/mls/service.go` (to learn what "MLS" actually is).

## Working recipe for the next person (scratch DB, dev secret)

```bash
# 0. scratch PG + HEAD binary (never the live :5437)
docker run -d --name canopy-df-pg -e POSTGRES_USER=canopy -e POSTGRES_PASSWORD=canopy \
  -e POSTGRES_DB=canopy -p 5438:5432 postgres:16
DB_HOST=localhost DB_PORT=5438 DB_USER=canopy DB_PASSWORD=canopy DB_NAME=canopy \
  HTTP_ADDR=:8098 ./bin/canopyd serve

# 1. mint a JWT per user (HS256, dev-secret-change-me, distinct sub UUIDs)
# 2. SECOND+ USERS: seed users rows (only …0001 is auto-provisioned):
#   INSERT INTO users (id, hermes_user_id, email, display_name, is_active)
#   VALUES ('<sub>','<sub>','<email>','<name>',true);   -- hermes_user_id = sub convention
# 3. collab lifecycle:
#   POST /api/v1/collab                              {"name":"…"}                    → 201
#   GET  /api/v1/collab/{ws}  (non-member)                                            → 403
#   PATCH /api/v1/collab/{ws} {"description":"…"}                                     → 200
#   POST /api/v1/collab/{ws}/invite                                                   → {token}
#   POST /api/v1/collab/{ws}/join?token=…                                             → 200
#   GET  /api/v1/collab/{ws}/members                                                  → roles 2/1
# 4. tree share + co-write:
#   POST /api/v1/trees/{t}/share {"email":"…","permission":"editor"}                  → 201
#   (grantee node writes work; GET /trees/{t} still 403s — DF-36)
# 5. channels (global, in-memory):
#   GET  /api/v1/workspace/channels            POST /{ch}/message {"content":"…"}
#   GET  /{ch}/feed  (SSE, live-only — connect BEFORE sending; no replay)
# 6. MLS mechanics (create/join/epoch/SSE all work; cross-decrypt does NOT — DF-35):
#   POST /api/v1/workspaces/{ws}/mls/groups  (real base64 Ed25519 pair, 32B pub / 64B priv)
#   POST …/mls/groups/join  {"workspace_id","profile_id","key_package":{...},"welcome_bytes"}
#   POST …/mls/encrypt      {"workspace_id","profile_id","plaintext_base64"}
#   POST …/mls/decrypt      → sender-only; other members: gcm auth failed (500)
```

## Bottom line

The **collaboration plumbing is real and mostly right**: workspace CRUD,
membership guards, token invitations, tree sharing (write side), and live
channel delivery all behave correctly behind auth with proper 401/403/404
error envelopes. The **collaboration promise is not**: MLS is a façade that
cannot deliver a message between two members, the SPEC-API-06 invite/member
surface is a phantom, the share grant doesn't unlock reads, and onboarding a
second human is an undocumented DB-surgery exercise. Files:
`DF-HERMES-CANOPY-35..40`, `docs/dogfood/diagnostics.md` §8,
`skills/hermes-canopy-usage/SKILL.md` v2.3.0.
