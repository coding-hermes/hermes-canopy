# Scope honesty — shipped-but-deferred subsystems (GAP-081)

Status: decision record, no code changes. Every number and claim below was
measured on this working tree at HEAD `0397d6c8` (2026-09-25); the commands are
in the appendix so a reviewer can re-run them.

## The drift

AGENTS.md's "Deferred (Post-MVP)" section still lists "multi-agent federation"
as deferred. It is not deferred: `internal/federation` ships in the baseline
binary, is mounted at `/api/v1/federation/*`, and is migration-backed
(migrations 33-37). The same drift applies, in differing degrees, to
`internal/transport` and `internal/relay` (which carries the tenant surface —
there is no `internal/tenants` package; the tenant repo lives at
`internal/relay/tenant_repo.go`, per migration 42). MLS already had its wording
reconciled in AGENTS.md ("has SHIPPED … no longer deferred") and is the model
for how the others should be read.

## Summary table

| Subsystem | LOC incl. tests | LOC excl. tests | Migrations | Live importers (non-test) | Verdict | Owner action |
|---|---|---|---|---|---|---|
| internal/federation | 2305 | 1705 | 33 (federation_peers), 34 (federation_transport), 35 (profile_routes), 36 (federation_events), 37 (federation_conflicts) | cmd/canopyd/main.go, internal/server/server.go, internal/handler/federation_handler.go | ADOPTED-WITH-OWNER | Keep in-scope; maintain, audit, and say so in AGENTS.md |
| internal/transport | 5841 | 4537 | 11 (transport_connections), 12 (transport_configs), 13 (transport_events), 38 (transport_selector) | cmd/canopyd/main.go, internal/server/server.go, internal/handler/transport_handler.go, internal/relay/registry.go | ADOPTED-WITH-OWNER | Keep in-scope; maintain, audit, and say so in AGENTS.md |
| internal/relay (+ tenant surface) | 3231 | 1962 | 40 (relay_config), 41 (relay_registry), 42 (tenants) | cmd/canopyd/main.go, internal/server/server.go, internal/handler/relay_registry_handler.go | ADOPTED-WITH-OWNER | Keep in-scope; maintain, audit, and say so in AGENTS.md |
| internal/mls | 2317 | 707 | 14 (mls_groups), 15 (mls_group_members), 16 (mls_key_packages), 17 (mls_pending_proposals), 48 (mls_group_secret) | cmd/canopyd/main.go, internal/handler/mls_handler.go | ADOPTED-WITH-OWNER (already reconciled in AGENTS.md) | Keep in-scope; maintain, audit; wording already correct |

Board-row figure corrections (GAP-081 row said "federation 2308, transport
5842, relay 3230, MLS 2039"): measured today, federation is 2305
(-3 vs row), transport is 5841 (-1), relay is 3231 (+1), MLS is 2317 (+278 vs
the row's 2039 — the row figure was stale). The corrected figures above govern.

## Per-subsystem decisions

### internal/federation — ADOPTED-WITH-OWNER

Evidence:
- Mounted unconditionally in `cmd/canopyd/main.go`: `federationRepo :=
  federation.NewPGRepository(database.Pool)` (main.go:582) and wired into
  `internal/server/server.go`.
- Live routes in `internal/server/server.go` (guarded only by
  `federationSvc != nil`, which main.go always satisfies):
  `/federation/link`, `/federation/routes`, `/federation/conflicts`,
  `/federation/health`, plus the P2P-authenticated handshake/events routes at
  `/api/v1/federation/*` with `FederationAuthMiddleware`.
- Migration-backed: migrations 33-37 (federation_peers,
  federation_transport, profile_routes, federation_events,
  federation_conflicts).
- 15 test functions inside the package; handler tests exist
  (`federation_handler_test.go`, `federation_relay_test.go`).
- Partially security-audited via the board-wide security work (TEST-04 security
  audit row; BUG-016/BUG-025 ownership/middleware fixes cover the auth surfaces
  federation routes also sit behind).

Rationale: live-mounted, migration-backed, tested, no evidence of dead or
unsafe code, and SPEC-FTR-02 work is actively referenced. Per project doctrine
(additive framing, prefer adopted-with-owner absent concrete evidence of
dead/unsafe code) the honest verdict is adopted-with-owner, not a gate and not
an extraction.

### internal/transport — ADOPTED-WITH-OWNER

Evidence:
- Always-on core wiring in `cmd/canopyd/main.go`: the SSE adapter, the relay
  adapter, and the connection manager are constructed unconditionally
  (main.go:483-491); the transport layer is the app's primary client channel
  (SSE + HTTP POST per the AGENTS.md Architecture section).
- Environment-gated extras only: the Pion WebRTC adapter registers via
  `RegisterPionAdapterFromEnv` and the NATS production bus only when
  `cfg.NATSURL` is set (main.go:493-518) — the gating that exists is opt-in by
  configuration, which is fine; it is not a build tag and should not be
  described as deferred.
- Live routes in `internal/server/server.go`: `/api/v1/transports/*`
  (SPEC-FTR-04 §6), `/api/v1/transport/relay` + `/poll`, and unauthenticated
  `/health/transports/<type>` probes.
- Migration-backed: migrations 11-13 (transport_connections, transport_configs,
  transport_events) and 38 (transport_selector).
- Imported by `internal/relay/registry.go` and the server; 34 test functions in
  the package plus handler/server integration tests (chaos, health,
  transport_integration, timeout, route parity).

Rationale: it is the primary data path for the product; there is no coherent
way to call it deferred. Adopted-with-owner. No new gate proposed — the
existing env-gated adapters (WebRTC, NATS) are already opt-in at the right
level.

### internal/relay (+ tenant surface) — ADOPTED-WITH-OWNER

Evidence:
- Constructed unconditionally in `cmd/canopyd/main.go`:
  `NewDeploymentConfigManager`, `NewRelayRegistry(database.Pool, mode, …)`
  (main.go:382), `NewRelayService` (main.go:383); the relay goroutine runs via
  server.go's `provider.Relay().Run(ctx)` when the federation service provides
  one; graceful shutdown cancels it (`relayCancel`, server.go:131-146/639).
- Mode is operator-configurable (`-relay-mode`, default `air_gapped`, main.go:157):
  `air_gapped` disables relay participation but does not remove the code, and
  `/health/relay` plus the relay block in the health JSON are always served.
- `/relays` discovery routes mount when
  `relayRegistry.DiscoveryAPIEnabled()` (server.go:453-455) — i.e. behind a
  deployment mode, not a build tag.
- Migration-backed: migrations 40 (relay_config), 41 (relay_registry),
  42 (tenants). The tenant surface is inside internal/relay
  (`tenant_repo.go` + `tenant_routing_test.go`); there is no
  `internal/tenants` package in this tree.
- 26 test functions in the package (auth, TLS, rate limiting, key rotation,
  tenant routing, registry).

Rationale: the baseline ships multi-mode (air-gapped/self-hosted/saas) relay
and tenant assignment with tests and migrations. A default-off *mode* is not a
deferred feature; extraction would rip out deployment modes the product
documented in docs/SELF_HOST.md territory. Adopted-with-owner, with the honest
note that `air_gapped` remains the default mode.

### internal/mls — ADOPTED-WITH-OWNER (already reconciled in AGENTS.md)

Evidence:
- Mounted at `/api/v1/workspaces/{workspace_id}/mls` (server.go:600-604,
  SPEC-FTR-03), service + event bridge constructed in main.go:420-427.
- Migration-backed: migrations 14-17 (mls_groups, mls_group_members,
  mls_key_packages, mls_pending_proposals) and 48 (mls_group_secret).
- 40 test functions in the package; handler-side MLS tests
  (`mls_integration_test.go`, `mls_key_package_test.go`) and the security-audit
  test file (`security_audit_test.go`) exercise it. TEST-04 (board row) is the
  Tick-74 security audit that produced SECURITY_AUDIT.md covering MLS key
  rotation, JWT expiry, and auth bypass.
- AGENTS.md already states it "has SHIPPED" and is "no longer deferred" — this
  is the wording pattern the other three should follow.

Rationale: the model case. Adopted-with-owner; no doc change needed beyond
keeping the existing AGENTS.md MLS wording intact.

No subsystem was judged feature-gated or extraction-proposed: none of the four
is behind a build tag (checked: zero `//go:build` lines in all four packages),
and none shows evidence of dead or unauditable code that would justify removal
from the baseline.

## Proposed reconciled Deferred-section text

Replacement for the single "Deferred (Post-MVP)" paragraph in AGENTS.md
(current text: "The full multi-user collaboration UX and multi-user CRDTs (the
workspace/profile/channel/MLS surfaces listed above have shipped), approval
gates, arbitrary JS plugins, multi-agent federation, all deployment modes
beyond local server."):

> ## Deferred (Post-MVP)
>
> The full multi-user collaboration UX and multi-user CRDTs, approval gates,
> arbitrary JS plugins, and hosted/managed deployment modes.
>
> Shipped beyond what the MVP framing promised, and therefore adopted and
> maintained rather than deferred: workspace/profile/channel collaboration and
> MLS group encryption (SPEC-FTR-01/03), multi-agent federation
> (SPEC-FTR-02, `/api/v1/federation/*`, migrations 33-37), the multi-transport
> layer (SPEC-FTR-04, `/api/v1/transports/*`, migrations 11-13/38; WebRTC and
> NATS adapters activate only via configuration), and the relay/tenant
> deployment surface (`/relays`, migrations 40-42; default mode
> `air_gapped`, other modes operator-selected). The application remains a
> single local binary; "deferred" above covers product UX and hosted
> operations, not these already-shipped backend surfaces.

This text (i) removes multi-agent federation from the deferred list and marks
it adopted, (ii) keeps transport, relay/tenants, and MLS consistent with their
adopted-with-owner verdicts, and (iii) changes nothing else in AGENTS.md. Per
repo rule, applying it is Bane-only; this doc proposes and does not apply.

## Appendix — commands used (re-run to verify every claim)

Measured on the working tree at commit `0397d6c8`, 2026-09-25.

    # LOC incl. and excl. tests (second number = excl. tests)
    cd /home/kara/hermes-canopy
    for p in federation transport relay mls; do
      find internal/$p -name '*.go' | xargs wc -l | tail -1
      find internal/$p -name '*.go' ! -name '*_test.go' | xargs wc -l | tail -1
    done

    # Tenants package location (none exists; tenant code lives in internal/relay)
    ls internal/relay/tenant_repo.go internal/relay/tenant_routing_test.go
    find . -type d -name 'tenants*' -not -path './.git*'

    # Live importers (non-test import evidence; *_test.go lines were read and excluded by hand)
    grep -rl 'internal/federation"' --include='*.go' . | grep -v internal/federation/
    grep -rl 'internal/transport"' --include='*.go' . | grep -v internal/transport/
    grep -rl 'internal/relay"'    --include='*.go' . | grep -v internal/relay/
    grep -rl 'internal/mls"'      --include='*.go' . | grep -v internal/mls/

    # Migration backing
    ls migrations/ | grep -Ei 'federation|transport|relay|tenant|mls'

    # Route mounts / construction (see section quotes above)
    grep -n 'federation\|relay\|transport\|mls' internal/server/server.go
    grep -n 'transport.New\|federation.New\|relaypkg.New\|mls.New\|NewRelayRegistry' cmd/canopyd/main.go

    # No build tags in any of the four packages
    grep -rn '//go:build' internal/federation internal/transport internal/relay internal/mls

    # Test counts
    for p in federation transport relay mls; do
      grep -rh '^func Test' internal/$p --include='*_test.go' | wc -l
    done

    # Security-audit evidence on the board
    grep -o 'BUG-016[^"]*' .coding-hermes/board/tasks.jsonl | head -1
    grep -o 'BUG-025[^"]*' .coding-hermes/board/tasks.jsonl | head -1
    grep -o 'TEST-04[^"]*'  .coding-hermes/board/tasks.jsonl | head -1

    # Build gate
    go build ./... && echo BUILD_OK

    # Scope check (must list only docs/scope-honesty.md plus .gitreins noise)
    git status --short
