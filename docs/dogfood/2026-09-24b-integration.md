# Dogfood Integration Report — 2026-09-24 (run 8): synthesis/merge, sync/snapshot, plugins, approvals, context compiler

Angle: surfaces untouched by runs 1-7 (CLI/CAG, human core loop, MCP, topics/#refs, cards,
collab/MLS). Target server: live dev instance `canopyd v0.1.0-91-g47effa62` on :8091
(PostgreSQL 16.14 via docker-compose). Auth per README "Authentication (dev mode)":
HS256 JWT signed with `dev-secret-change-me`, sub `00000000-…-0001`.

## The real workflow used

"Two competing approaches → branch → synthesize → keep-in-sync":

1. `POST /api/v1/trees` with `rootMessage` (first attempt without it → 400
   `tree service: root message content is required` — message is clear, docs carry the field,
   my miss).
2. Two replies off the root (`POST /trees/{id}/nodes/{id}/reply`).
3. **Synthesis**: `POST /trees/{id}/merge` with two sources + content + target parent.
   → 201, node_type `synthesis`, edges `[reply, synthesis, synthesis]` — exactly as documented.
4. **Chained merge**: second merge using the first synthesis node as a source → 201, works.
5. Read-back: `GET /graph/trees/{id}/subtree/{S1}` shows the synthesis edge S1→S2;
   `GET /graph/trees/{id}/stats` aggregates correctly (edge_type_reply/synthesis counts).
6. Validation surface, all correct per docs: `SOURCE_TARGET_OVERLAP` (target also a source),
   `MIN_SOURCE_NODES` (1 source), `TREE_MISMATCH` (cross-tree sources),
   `SOURCE_NODE_DELETED` (soft-deleted source), `VALIDATION_ERROR
   "synthesis nodes via merge endpoint only"` (node-create with node_type synthesis).
7. Context compiler over the synthesis node: `GET /context/{S2}?budget=4000` → manifest with
   ancestry, tokensUsed=70, stable `manifestHash` across identical requests (verified twice).
   `budget=0` → 400 INVALID_BUDGET; deleted node → 404 NODE_NOT_FOUND.
8. Sync: `GET /trees/{id}/sync?sinceHash=<64-hex>` → 200 delta with addedNodes/addedEdges,
   then 204 on the current hash. Delta shape matches the snapshot's compact form.
9. Plugins (whole documented lifecycle): register → versions → activate → rollback → disable
   → archive → source 404 on non-active. See finding DF-63 for the registration landmine.
10. Approvals: list routes return `{"approvals":[],"limit":50,"offset":0,"total":0}` — empty
    but consistent. No approval-creating workflow was exercised (creation is tied to runs/agent
    activity not covered by this angle; recorded as a known untested arm, not a defect).

## Findings (board rows DF-61..65, evidence below)

1. **DF-61 (P1) — export→import is dead for any tree with a deleted node.** Export omits
   soft-deleted nodes but keeps their edges (verified: 1-node tree, child deleted, export has
   `nodes:1` + 1 edge whose targetId is absent from nodes). Import then rejects the payload:
   400 `VALIDATION_ERROR "export service: edge references node not in import payload"`.
   The synthesis dogfood tree (source deleted mid-run) exported 5 nodes / 9 edges and refused
   to import. A clean tree round-trips fine (import 200: treeId, nodeCount=2, edgeCount=1).
   The merger of "export drops deleted nodes" with "import requires edge endpoints" makes
   export→import lossy→fatal for every real tree someone has pruned.
2. **DF-62 (P1) — `POST /trees/{id}/sync/snapshot` returns 500 whenever the current tree-state
   hash already exists** (unique index `idx_snapshots_tree_hash`, migration 000012): the repo
   does a plain INSERT and `23505` bubbles up as INTERNAL_ERROR (journal: `snapshot: insert:
   ERROR: duplicate key … idx_snapshots_tree_hash`). Mutation-triggered auto-snapshots
   (internal/sync/engine.go:130,161) mean the manual snapshot almost always collides right
   after a change settles. Reproduce: POST snapshot twice, or once after any auto-snapshot.
   Expected: idempotent return of the existing row (200) or 409 — not 500.
3. **DF-63 (P1) — plugin registration 500s with the documented dev token.** The README
   provisions the dev identity with JWT sub = user UUID
   `00000000-0000-0000-0000-000000000001`; `plugin_registry.author_profile_id` has
   `REFERENCES profiles(id)`. Registering a manifest-valid plugin with that token → 500
   INTERNAL_ERROR (FK violation masked by the handler's default case). The identical request
   with sub = the dev profile UUID (`01a0d018-…-328400`, from `profiles` table) → 201. The
   documented zero-auth path of the README quickstart is broken for plugins; every other
   surface (trees/nodes/merge/context/sync) worked with the user-sub token.
4. **DF-64 (P2) — three response-envelope doc drifts found while reading real payloads**:
   (a) `GET /plugins/{name}/versions` — docs promise `{"plugins":[…]}`; live answer is a bare
   array (versions route under internal/service Versions());
   (b) import route returns a flat summary object (`treeId`,`nodeCount`,…) — no `tree` wrapper;
   (c) plugin profile mapping route returns profile rows without a `profileId` field
   (keys: workspaceId/profileName/displayName/isActive/mappedAt/lastUsedAt) — clients cannot
   reference the profile by id from this response.
5. **DF-65 (P2) — tree detail `node_count` excludes synthesis-adjacent? No — excludes nothing
   but is stale-adjacent**: tree detail said `node_count:1` while the node list returned 5 and
   graph stats 5 active — `node_count` appears to count only direct root children (root +
   replies under root are excluded, i.e. it is the ROOT CHILD count, undocumented). Re-verified
   after every mutation; delta was stable. Minor, but any UI trusting tree detail's
   node_count under-reports by the whole branch depth.

(Also observed, not filed: get-plugin-by-id answers 404 PLUGIN_NOT_FOUND for any
non-active row while versions/activate resolve the same rows fine — the handler maps
`db.ErrPluginNotFound` from `GetByID`, which is `WHERE id=$1` with no status filter;
the 404 for an existing row surfaced when the row was disabled. Low impact: docs even
document the source-404-on-non-active behavior, so only the metadata 404 is off.)

## Perf (Step 2b, hyperfine 20 runs, warmup 3, live :8091 instance)

Nothing slow enough for a user to notice — no PERF rows filed (a win nobody can feel is
not a finding):

- merge (synthesis create, 2 sources): 7.0 ms ± 0.8
- context compile (budget 4000, 2-node ancestry): 7.5 ms ± 0.9
- sync delta (full delta, 6 nodes/10 edges): 6.9 ms ± 0.6
- graph stats: 7.0 ms ± 0.9

Cold start of the tree was effectively "open a browser and the data is there" — no wait
signal anywhere in the session. Install leg: SKIPPED (see board row; bunker host check
blocked the leg this tick).

## The one-paragraph verdict

The flagship DAG mechanics — merge, chained synthesis, graph reads, context compilation with
stable manifest hashes, sync deltas — are real, fast and correctly validated, with error
codes that match the docs. The damage is in the edges of the story: export→import breaks on
real (pruned) trees, manual snapshot 500s on unchanged state, and the plugin registry — the
one surface the docs say "just register with the dev token" — 500s unless you know to mint a
profile-sub token the docs never mention. All three are exactly the kind of thing green
tests miss and one honest run catches.
