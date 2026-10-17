# Dogfood Run 16 — hermes-canopy — 2026-10-16 (second angle, same tick as run 15)

Angle: **post-share surfaces** — workspace roster/join/role ladder, the in-memory
channel registry's scoped access, browser PWA drive of the trees UI, and
re-proofs of the run-14/15 fix set on a *fresh local scratch* instance. Run 15
(re-proof on a fresh bunker box, HEAD d5ea44ea) was a sibling tick in the same
hour; this run is the second angle per the sibling-run discipline — the
untouched surface here is the **post-share UX** run 15 never drove.

## Environment

Local isolated scratch via `scripts/scratch-instance.sh` on this control host:
PG database `canopy_scratch_dfcanopy16_71ad8d6a` on :5437, canopyd (HEAD build
`v0.1.0-346-g5230aed0` today) on loopback :18611, isolated `HOME`
(/tmp/canopy-scratch-dfcanopy16-BHEnY7/home) for cards/gateway registry, closed
gateway URL. Vite preview build with `VITE_API_BASE_URL` +
`VITE_API_TOKEN` (dev user) to serve the PWA cross-origin for the browser leg.

## What was driven (real use)

- owner: create workspace `df16-altar` (201, seeded as admin role 2), create
  tree (201), GET /collab on fresh boot (200 — DF-80 fix proven again here)
- share ladder as `reader` (a second identity): friend's tree; view 200,
  node create 403 VIEWER_READ_ONLY (DF-79 fix proved again), rename 403
  NOT_TREE_OWNER, delete 403
- reader create own tree → provision → re-share as viewer → 201 + both GET
  pair (per-participant views)
- workspace channels route (`/workspace/channels?workspace_id=…`):
  non-member 403 WORKSPACE_NOT_FOUND vs unknown 404 NOT_FOUND (DF-81
  message collapse is *documented* — matches run-15's PASS)
- workspace invite/join: token single-use (INVALID_TOKEN on re-use), leave
  (204) + re-join with a fresh invite (200), roster after membership works
- tree_members cross check: viewer grants work; DF-82's roster gap confirmed
  still real on public interfaces (still a pending row; NOT re-filed)
- browser drive (real browser, logged-in PWA): trees list, altar tree detail
  canvas, share dialog render, message POST interception artifact →

## Service-worker artifact (NOT a defect in Canopy)

In this harness's browser the page-level POST to :18611 returns a fabricated
`503 {"error":"offline","message":"Request queued for retry"}` — the PWA
service worker's offline queue. Not a Canopy defect because:
- curl to the same endpoint succeeds (server-side trusted path, 201/404 as
  expected),
- the sw was unregistered and POST still returned the same 503 fixture,
- and these SRC-only POST fixtures never leave the browser stack (no POST
  /share arrival in the canopyd log).

Interim conclusion for a real user: POSSIBLE pitfall on browsers that
intercept fetch (blocking proxy, iOS in some modes, ad-blocker). The UI
silently queues instead of alerting — worth a follow-up SCREEN for agent
 fleets that auto-drive the PWA, though not a defect this run could prove at
neutral-harness depth.

## Installability

SKIPPED-with-citation (not silent): same install premise already proven on a
fresh bunker box in run 15 THIS SAME HEAD (clone 3s + go toolchain 2s + build
110s + boot-to-healthy 251ms). No build-path change since 9a19c42e badge.
No new bunker leg protects a run-15-fresh-install run; per the dogfood-skill
rule an explicit citation replaces the silent pass.

## Findings (new rows)

- **DF-HERMES-CANOPY-87 (P2)**: share to a never-authenticated target gives
  404 USER_NOT_FOUND with no documented provisioning path. A fresh user has
  no way to know the share target must first make any authenticated call.
  Fix direction: either the docs should say it, or the backend should queue
  the invitation for unknown targets.
- **DF-HERMES-CANOPY-88 (P3)**: `member_count` is hardcoded 1 in every tree
  response (tree_service.go 516/1007/1156/1268, "owner only" comment) and
  TreesPage renders it as "N members" — a user cannot see their share land
  from any surface. Fix direction: real count from tree_members, or drop
  the field + chip until implemented.

## Re-proofs recorded (no new rows)

- DF-79 tree-scoped deny: 403 VIEWER_READ_ONLY at fresh HEAD ✓
- DF-80 dev-user /collab 200 fresh boot ✓
- DF-81 scoped-message collapse documented ✓ (does not contradict run 15)
- DF-82 rosters pending as known (unchanged premise) — no dup re-file
- Breeze-tree share response 201 shape ✓

## Performance

Nothing a user would feel: tree GET 3–6 ms, node create 10–12 ms, boot 1 s,
/health 380–900 µs, PWA preview render < 1 s cold. No PERF rows filed.

## Left behind

- `.coding-hermes/board/tasks.jsonl` (+2 rows P2/P3, line-wise append,
  0 duplicate ids), `events.jsonl` (+1 dogfood report event)
- `.coding-hermes/dogfood-log.md` (this run's entry)
- `docs/dogfood/2026-10-16-run16-postshare-integration.md` (this report)

## Commit

`git -C /home/kara/hermes-canopy` pathspec commit; no collaborators,
no code change, no repo-permission change, no scheduler mutation.
Dogfood lane is read-only w.r.t. code: no source modified.
