# Dogfood Run 15 — hermes-canopy — 2026-10-16

Angle: re-proof of the run-14 fix set (DF-79/80/81 + DF-71/73) on a fresh bunker
install — every P0/P1 fix from run 14 landed after that run and none had been
re-dogfooded through real use. Delta since run 14: 20 commits (9a19c42e..d5ea44ea).

## Environment

Ephemeral bunker agent dfdogfood15 on bunker-mvp (las-bunker-03/02/04 offline;
bunker-mvp reachable, bunkerd active). Fresh clone of the public GitHub repo,
Go 1.25.11 via the README no-sudo recipe, `make build` 110s, `docker compose up
-d postgres` (per-user socket via the SELF_HOST.md DOCKER_HOST note), canopyd
boot healthy in 2s. Version stamp: commit=d5ea44ea = HEAD at run start.

## Install leg (fresh machine, timed)

- clone: 3s (rc=0)
- Go toolchain from README: 2s
- make build: 110s (rc=0)
- boot + health: 2s; cold boot-to-healthy measured at 251ms
- -version stamps d5ea44ea; --help lists the new -update-manifest-url /
  -update-public-key-file flags (DF-71 fix present in the built binary)

Result: PASS on the documented path. The one silent gap: `docker run` (the
README standalone path) fails on a per-user-socket host until you find the
DOCKER_HOST note in docs/SELF_HOST.md — the compose path used here works.

## Re-proof results (discriminating probes, fresh box)

| Row | Fix | Re-proof verdict |
|---|---|---|
| DF-80 (P1) | owner collab 403 on fresh install | PASS — GET /collab/{dev-ws}/members 200 with dev user role 2, POST /collab creates workspace 201 with owner membership seeded |
| DF-79 (P0) | viewer write enforcement | PARTIAL — tree-scoped POST /trees/{id}/nodes as viewer: 403 VIEWER_READ_ONLY (fix works as mounted). **BUT the flat /api/v1/nodes mount bypasses it entirely: viewer PATCH 200, DELETE 200, reply 201, fork 201** → new row DF-84 (P0) |
| DF-81 (P2) | scoped-channel 403 message | PASS on the scoped path — non-member gets 403 WORKSPACE_NOT_FOUND with the membership-naming text via ?workspace_id=. **BUT scoping is optional: without the query param the same non-member write returns 202, and sender_id is client-spoofable** → new row DF-85 (P1) |
| DF-71 (P1) | updater dead as shipped | PASS — embedded public key + manifest URL override live; bad key file = fatal at boot (fail-closed), valid override boots and serves |
| DF-73 (P3) | release-tool filename contract | PASS — release-tool built and run on the fresh box: canopyd_windows_arm64.exe now maps (glob-based platformForFilename), foreign files skipped, rc=0, manifest lists both artifacts |

## New findings → rows

- DF-HERMES-CANOPY-84 (P0): flat /nodes mount has no NodeAccessMiddleware in
  production wiring (server.go:412 mounts it bare; the middleware with the
  viewer gate is only wired in *_test.go) — the DF-79 hole reopens on the flat
  surface.
- DF-HERMES-CANOPY-85 (P1): workspace-channel authorization keyed on an
  undocumented query param and skipped when absent; client-supplied sender_id
  recorded verbatim.
- DF-HERMES-CANOPY-86 (P3): flat mount returns raw chi 404 envelope on
  malformed node id; POST /trees response unwrapped vs list endpoints.

## Perf (Step 2b)

Nothing a user would notice: tree GET 3.3–6.0ms (10 warm samples), tree
create 9.9–12.1ms, cold boot-to-healthy 251ms, install 115s total. No PERF
rows — saying so explicitly.

## Verdict: PROMISING-BUT-ROUGH

The run-14 fixes are real and hold on the surfaces they were written for —
but the flat node mount (the exact path the DF-79 middleware comment says
"must not reopen the write path") was left unwired in production, so the P0
viewer bypass survives on half the API surface. Shared-tree permissions are
enforced on tree-scoped routes only.

Rows filed: DF-HERMES-CANOPY-84..86 (created_by dogfood-2026-10-16-run15).
