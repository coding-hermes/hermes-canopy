# Dogfood run — 2026-10-26 — FTR-06 native-packaging angle

Lane: hermes-canopy-dogfood-2026-10-26-16-41-48
Target: hermes-canopy
HEAD under test: 85f5e250e3a2deae627908ae8f1ddf6ca74a960a

Verdict: PROMISING-BUT-ROUGH for the broader Canopy product; the exercised embedded-PWA workflow works. FTR-06 Phase 5 itself remains unimplemented: the repository has no Wails entry point, wails.json, or build-wails target, so native desktop packaging was not claimable.

Promise

A user can build Canopy's single-binary embedded SPA, run canopyd, open the API port, authenticate with the documented local development token, create a real tree, and use the graph UI without Vite or a reverse proxy. The FTR-06 Phase 5 extension promises an additional Wails native desktop surface sharing the same frontend and update manifest.

Real use performed

1. Built the UI from a detached HEAD worktree with `npm --prefix frontend install --ignore-scripts` followed by `time make build-ui`. Result: rc=0, 10.202s wall. Vite produced the SPA and service worker; Go compiled the `-tags embedui` binary stamped 85f5e250.
2. Started the resulting binary against an isolated PostgreSQL database on 127.0.0.1:18095 with isolated HOME, card directory, and file root. `/health` returned 200 with schema_version=49 and embedded_migrations=49. `/` returned the 688-byte embedded index shell.
3. Used a real browser against the embedded UI. The page title was “🐴 Hermes Canopy”; the unauthenticated UI exposed the documented Bearer-token requirement. After setting the documented dev JWT in localStorage, the dashboard loaded without auth errors and reported the deliberately closed gateway as offline rather than fabricating runs.
4. Followed the actual onboarding flow: Trees → Create your first tree → filled title, description, and root message → Create. The UI showed “1 conversation tree”, the created title, “1 node”, and “1 member”.
5. Browser navigation timing for the embedded `/trees` route: TTFB 7.5ms, DOMContentLoaded/load 48.4ms, transfer interval 7.2ms on localhost.
6. The scratch harness independently exercised CLI tree create/list, authenticated API list, unauthenticated rejection (401), card creation (201), and live-instance non-contamination. The harness exited 0 and removed its database, scratch root, port, and shared-store residue.

Observed friction and limits

- The gateway was intentionally pointed at 127.0.0.1:9, so a real Hermes run was not attempted. Gateway value is therefore unverified in this run.
- The browser flow reached tree creation but was not extended to branching/share because the target angle was packaging and the available binary had no native desktop surface.
- The source checkout was dirty before the run because `.gitreins/tasks.yaml` was being written by another worker. All product measurements came from the clean detached worktree; no source or board files were changed.
- The repository's own Phase 5 spec still says “Install Wails v3 CLI”, create `cmd/canopy-desktop`, configure `wails.json`, add `build-wails`, wire the shared `latest.json`, and add platform packaging. No such files are tracked at this HEAD. This is the existing FTR-06 implementation gap, not a newly duplicated board finding.

Performance

No PERF row. The meaningful user-facing operation was comfortably fast: build-ui was 10.202s one-shot on this host, embedded browser load completed in 48.4ms, and health was sub-second during the scratch run. There was no measured user-noticeable runtime wait that justified profiling. The timing command and captured results are retained in this report and the canonical log.

Fresh-machine install leg

The required `bunker-qa.sh` leg ran on fallback `bunker-mvp` because the named las-bunker-03 host was unreachable and bunker2/bunker4 were not used for this run. Agent 82225e36 was destroyed and absent from the final `bunker list` census. The battery reached 18 cells, but it was not a clean product PASS: fresh-install failed with `os/user: mkdir ...: no space left on device`; Node/npm was unavailable, so the UI cell was environment-blocked; CI was not triggerable; chaos-disconnect was UNVERIFIED because the native suite failed first; lifecycle cells were safety-skipped on the live Hermes host. This is install evidence, not a Canopy product failure. Installability for this HEAD is therefore PARTIAL/UNVERIFIED, not PASS.

Evidence

- Detached product worktree: /tmp/canopy-df-20261026-164148-src
- Install battery: /tmp/bunker-qa-evidence-hermes-canopy-20261026-164148.jsonl
- Install battery summary: agent 82225e36, bunker-mvp, fresh-install rc=1 with no-space-left-on-device; teardown verified by `bunker destroy` and final list census.
- Browser session: canopy-dogfood-20261026
- Scratch harness: `scripts/scratch-instance.sh --port 18093`, rc=0, with post-cleanup database/port/root/shared-store checks.

What remains unverified

Wails v3 native-window startup, Wails IPC bindings, Wails updater behavior against stable latest.json, macOS DMG creation/signing, Windows MSI creation/signing, and native CI matrix execution. No signed platform artifact is claimed.

ch:trace row=FTR-06 spec=specs/SPEC-FTR-06-webui-native-packaging-distribution.md#phase-5 wave=none:serial-tick-hermes-canopy-dogfood-2026-10-26-16-41-48 test=time make build-ui+scripts/scratch-instance.sh doc=docs/dogfood/2026-10-26-ftr06-integration.md+skills/hermes-canopy-usage/SKILL.md evidence=/tmp/bunker-qa-evidence-hermes-canopy-20261026-164148.jsonl witness=browser:canopy-dogfood-20261026+http://127.0.0.1:18095/health verdict=PROMISING-BUT-ROUGH commit=pending
