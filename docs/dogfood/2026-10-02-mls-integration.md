# Dogfood Integration Report — hermes-canopy — 2026-10-02 — MLS/workspace collaboration angle

Lane: hermes-canopy-dogfood (tick 2026-10-02-15-44-09). Angle chosen because runs 1-9
(2026-09-23 → 09-28) never touched the MLS surface (SPEC-FTR-03, newest shipped surface).

## Promise under test

"A user can set up an encrypted MLS group for a workspace via
`/api/v1/workspaces/{workspace_id}/mls` (docs/API.md §MLS, mounted per AGENTS.md
'Shipped beyond the MVP framing'), then encrypt/decrypt group messages."

## What I actually did (as a user, curl-first)

Local dev server :8091 (commit 032a87ef build, schema 48):
1. Minted the README-documented dev HS256 JWT (sub=…0001) — worked first try.
2. `POST /api/v1/collab` `{"name":"df-mls-ws"}` → 201 workspace, 23ms.
3. `GET .../mls/groups` on fresh workspace → 404 (correct, no group yet).
4. `POST .../mls/groups` with the docs' (empty) body → 400 INVALID_KEY_PAIR
   "admin_public_key must be a valid Ed25519 public key". Docs document NO body.
5. Sent `admin_public_key` (self-minted Ed25519) → 500 INTERNAL_ERROR. Retried → 500 again.
6. DB check: THREE mls_groups rows had been created by the failing calls — the create
   is non-atomic: group row inserts, then member insert fails (no profile), so the API
   returns 500 while the group persists. `GetByWorkspace` then returns whichever row
   sorts first; my real group had a member, the orphans did not, so encrypt 404'd
   "profile is not a group member" until I deleted the orphan rows.
7. With `creator_profile_id` = the canopy profiles.id (found via
   `docker exec canopy-pg psql -c 'select id from profiles'`): 201, epoch 0, tree_hash set.
8. `POST .../mls/key-packages` with the docs' schema verbatim → 201 + key_package_bytes,
   hash, 24h expiry. This route is honest to its docs.
9. `POST .../mls/encrypt` `{"profile_id","plaintext_base64"}` → 200 ciphertext
   (content_type application, wire_format mls_ciphertext_v1). The doc's implied field
   `plaintext` is actually `plaintext_base64` — had to read internal/handler/mls_handler.go.
10. `POST .../mls/decrypt` with the full MLSCiphertext object → 200, plaintext recovered
    byte-exact ("hello from dogfood"). Round-trip REAL.
11. Negative: tampered ciphertext → **500 INTERNAL_ERROR** (should be 4xx auth failure).
12. `GET .../mls/events` → 200 SSE stream (mls:welcome_message events flowing).

Fresh-box install leg (bunker-las-03, agent b35af4f1, destroyed):
- git bundle transfer (per-file cat skipped for scale; checksum verified 8911bf44…).
- Go 1.25.3 staged user-space (README's documented no-sudo path).
- `make build` = **INSTALL_SECONDS=131**; `canopyd --version` → v0.1.0-247-g032a87ef.
- `canopyd serve` hard-fails without postgres with a CLEAR message pointing at
  docs/INTEGRATION.md §2 — good error UX.
- compose `up -d postgres` OK on rootless docker; serve needed
  `CANOPY_DB_URL=postgres://canopy:canopy@localhost:5437/...` (compose password is
  `canopy`, not the dev docs' default; not in the quickstart).
- /health 200, workspace create 201 on the fresh box. Smoke PASS.

## Perf (Step 2b)

encrypt warm: mean 1.0ms, min 0.8ms, max 1.1ms (10 runs, urllib timing).
Workspace create 23ms. Nothing slow enough that a user would notice → no PERF rows.

## Verdict per surface

- Install/serve: SHIPPABLE (131s build, clear errors, one doc gap on port/password).
- Key packages + encrypt/decrypt crypto core: REAL, fast, round-trips.
- MLS quickstart/docs: DOES-NOT-DELIVER as documented — create-group body undocumented,
  500s instead of validation errors, non-atomic create poisons the workspace with
  orphan groups, profile-id space unstated.

Overall: PROMISING-BUT-ROUGH.

## Findings → board rows

- DF-75 (P1) create-group: undocumented body, 500s, non-atomic orphan groups.
- DF-76 (P2) tampered-ciphertext decrypt → 500 instead of 4xx.
- DF-77 (P2) fresh-box friction: :8080 vs docs' :8091, node required for JWT, DB password guess.
- DF-78 (P2) MLS profile-id space (canopy profiles vs gateway mappings) unstated in docs.
