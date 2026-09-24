# Dogfood diagnostics — how the merge/sync/plugin surfaces are built, and what bit me (2026-09-24b)

This is the "why is it like this and what did it cost to find out" trail for run 8
(synthesis/merge, sync/snapshot, plugins, approvals, context compiler). Reads together with
docs/dogfood/diagnostics.md (runs 1-7) and docs/dogfood/2026-09-24b-integration.md.

## How synthesis works, and why it is safe

The ONLY path to a `synthesis` node is `POST /trees/{id}/merge`
(sentinel `ErrSynthesisViaMergeOnly` guards node-create; the DB `chk_node_type` CHECK still
allows message/synthesis/system, so the guard is service-level, not schema-level). One
transaction inserts the node plus N+1 edges: one `reply` edge to the placement target and one
`synthesis` edge per source. `merged_source_ids` echoes what was consumed. Merges chain — a
synthesis node is a legal source of a later merge — and the read side (subtree/ancestors/stats)
treats them as ordinary nodes, so the DAG invariants are the reply/fork invariants plus the
multi-parent edge. Validation order in the handler: existence → tree membership → deleted →
target-overlap → min-2-sources, each with a NAMED error code (`TREE_MISMATCH`,
`SOURCE_NODE_DELETED`, `SOURCE_TARGET_OVERLAP`, `MIN_SOURCE_NODES`). This is the best-documented
surface in the repo; the docs and the code agreed on everything I hit.

## Snapshot hashing: the canonical form, and the 500 behind it

`computeSnapshotHash` (internal/db/snapshot_repo.go) builds a canonical byte buffer: nodes
sorted by (seqNum, id) as `id:seq:createdMs:parentOrNil:contentHash:format:type\n`, edges
sorted by (source,target,type,id) as `id:src:tgt:type\n`, then sha256. I recomputed that hash
independently from psql and matched the stored snapshot byte-for-byte — the hash is honest.
The problem is the write, not the digest: `idx_snapshots_tree_hash` (migration 000012) makes
(tree_id, hash) unique, and `CreateSnapshot` does a plain INSERT. Any snapshot of an unchanged
tree — including the ones the sync engine takes automatically after mutations
(engine.go:130,161) — collides and the raw 23505 reaches the client as 500 INTERNAL_ERROR.
The right shape is ON CONFLICT DO NOTHING + return the existing row.

## Plugin identity: three different UUIDs, one of them undocumented

The plugin path requires the JWT subject to be a PROFILES id, because
`plugin_registry.author_profile_id REFERENCES profiles(id)`. The README quickstart provisions
the dev JWT with sub = USERS id (00000000-…-0001) — which is what every other surface keys on
— so plugins 500 on FK violation while trees/nodes/merge/context all work with the same token.
The dev profile id lives in the `profiles` table (01a0d018-… on the dev instance) and is
provisioned by canopyd on boot, but no documented route returns it
(GET /workspaces/{id}/profiles rows have no id field — DF-64c). The lifecycle itself
(register → versions → activate → rollback → disable → archive, with name-vs-slug path
semantics) works exactly as the docs' table says; my only false start there was calling
disable with the slug where the docs' table says the manifest name — read the table first.

## Export/import asymmetry

Export intentionally omits soft-deleted nodes ("deleted_at IS NULL" filters) but does not
filter edges whose endpoints were deleted, so the payload can self-reference. Import validates
edge endpoints and refuses. Whichever side you fix, keep them symmetric — today a pruned tree
cannot leave the system and come back.

## Getting a fresh-box compose install to run (bunker leg, 2026-09-24)

- Git clone over SSH fails on a fresh agent (no key, and we never mint credentials — hard
  rule), so the tree goes over tar-stream: 137s for the repo minus .git/node_modules.
- Rootless docker on the bunker: dockerd starts (spawn pre-arms it) but the systemd unit can
  bounce into a restart loop (`docker.pid` from the pre-armed daemon still live). The daemon
  IS running on `/run/bunker/<agent>/docker.sock` — just set
  `DOCKER_HOST=unix:///run/bunker/<agent>/docker.sock` instead of fighting the unit.
- Rootless port publishing: `0.0.0.0:8092->8080` exists but connections from the agent user to
  the published port get RST (rootless netns loopback quirk). Smoke from INSIDE the server
  container instead (busybox `wget` is present, curl is not; `--header "k: v"` quoting matters
  — wget treats the space as an address separator if you use `--header=k: v`).
- End-to-end: compose build+up 232s, health 200, tree → 2 replies → merge → synthesis node
  with reply+2×synthesis edges, on a box that ten minutes earlier had nothing. The compose
  path in SELF_HOST.md is real.

## Things that cost time and should not have

- Create-tree without `rootMessage` → 400 naming the field (good), but the API index section
  of docs/API.md buries it; the error text is what saved me. Keep that pattern.
- The busybox-wget quoting trap and the DOCKER_HOST override together cost ~15 minutes of the
  install leg. Both are now written here; next agent skips them.
