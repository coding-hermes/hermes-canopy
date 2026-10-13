# Dogfood Run 14 — hermes-canopy — 2026-10-13

Angle: tree share + workspace membership/collab + workspace channels (SPEC-FTR-01 minus MLS; untouched by runs 1-13).

## Promise tested
"A user can share a tree with role-based permissions (viewer/editor/admin) and collaborate in workspace channels with history replay."

## Real-use transcript
Environment: isolated scratch instance (own DB canopy_df473_share on the existing PG container :5437, HTTP 127.0.0.1:8093, own HOME + file root, gateway pointed at a closed port). Verified against the live instance: zero contamination (health/pid/DB list unchanged).

Two identities: dev JWT user ...01 (Alice) and hand-minted JWT for user ...02 (Bob — row exists in users, provisioned by boot).

1. Alice created a tree (201, 24ms). Bob pre-share GET → 200 (list only, his own trees).
2. Share by user_id, permission=viewer → 201 bare share record, 16ms. Duplicate share → 409 ALREADY_MEMBER (correct).
3. Bob GET shared tree → 200 (membership works).
4. Bob (viewer) POST node → **201 — viewer role not enforced** (DF-HERMES-CANOPY-79, P0).
5. Workspace invite as the workspace OWNER → 403 NOT_WORKSPACE_MEMBER; members list → 403. psql: workspace_members has 0 rows — EnsureDevWorkspaceProfile (internal/db/bootstrap.go:121-168) never adds the owner (DF-80, P1).
6. Channels: unscoped legacy list/POST/feed all work — message 202 (15ms), SSE feed replays retained history then live events with proper id/event/data/retry framing and sequence_num.
7. Scoped channels with the real workspace id → 403 WORKSPACE_NOT_FOUND (oracle collapse; owner hit via DF-80 cannot self-diagnose) (DF-81, P2).
8. No documented surface to list/change/remove tree members after a share (DF-82, P2).
9. Non-member tree delete → 403 (correct); tree membership gating itself works.

## Perf
Warm (3 samples each): tree GET 17-19ms, channel message POST 13-20ms, share POST 16-17ms. Nothing slow enough for a PERF row. Install: clone 2.5s, make build 204s cold on bunker-mvp.

## Install leg (bunker)
las-bunker-03 offline (ssh timeout); bunker2/4 offline; bunker-mvp active. Agent df539b1a (TTL 2h, destroyed + verified). Fresh clone https://github.com/coding-hermes/hermes-canopy --depth 1: 2.47s rc=0. make build: 3m24s. canopyd -version stamps commit=9a19c42e = HEAD. Smoke ok. → DF-83 records the explicit skip + fallback pass.

## Verdict: PROMISING-BUT-ROUGH
Membership gating is real and fast; the permission TIER is decoration (P0), and the dev-mode workspace bootstrap locks the owner out of every workspace-collab route (P1). The advertised SPEC-FTR-01 surface is unusable on a fresh dev install without hand-writing SQL.

Rows filed: DF-HERMES-CANOPY-79..83 (created_by dogfood-2026-10-13-run14).
