# Dogfood run 2026-10-13 — FTR-06 signed distribution surface

**Verdict: PROMISING-BUT-ROUGH** | Angle: release packaging + signed self-update (unexercised
since the 10-02 MLS run). HEAD 9a19c42e, local builds: canopyd@0.1.0-stamp dfhead, release-tool
(both from HEAD).

## What was driven (real-use, not tests)

1. `cmd/release-tool` end-to-end: built HEAD binary, signed a real artifact dir with a fresh
   Ed25519 key (PKCS#8 PEM accepted per dist.ParsePrivateKey), produced latest.json with
   sha256 + per-artifact + manifest signatures. rc=0.
2. A real consumer (not the project's own tests) driving `internal/updater`:
   CheckForUpdate 1.5ms / DownloadUpdate 61ms (44MB artifact, local HTTP) / ApplyUpdate 26µs
   atomic swap → swapped binary byte-identical to served artifact. All green.
3. Fail-closed ladder: tampered sha256 → "invalid Ed25519 signature"; wrong key → same;
   no-downgrade (same-version manifest re-signed, live-served) → available=false, no error.
   The crypto core is honest and complete.
4. Live server: `--update-interval` on the HEAD binary logs an update-check WRN per interval —
   the flag family is wired; the shipped default URL is dead (see rows).
5. Security-hardening spot-checks (da92d72a): CORS default now emits NO Access-Control-Allow-
   Origin on cross-origin /health and 401 preflight; production env logs both INSECURE warnings
   (dev JWT secret + dev profile-router key). Both behave as the commit message claims.

## Install leg

SKIPPED-install-bunker — not run this tick; the last two runs (09-28: 150s smoke ok, 10-02:
131s smoke ok) proved the same install premise (clone + go + make build on a fresh bunker
agent), and no install-path change landed since (FTR-06 commits touch release tooling, not the
build entry points). Cited skip, not silent: no new install evidence was added this run.

## Perf (Step 2b)

- release-tool full sign+manifest of a 44MB artifact: 58.5ms ± 2.3ms (hyperfine, 20 runs,
  warm). Cold single-run ~60ms. Not user-noticeable → no PERF row.
- canopyd cold boot-to-healthy (PG on 5437): 73–106ms over 3 runs. Comfortably fast → no row.
- Update flow: check 1.5ms, download 61ms (local), apply 26µs. No waiting anywhere.
- Verdict: nothing slow enough that a user would notice. No PERF rows filed — saying so
  explicitly per the perf skill.

## Findings → rows

- DF-HERMES-CANOPY-71 (P1): updater dead as shipped — parked manifest domain, no
  ManifestURL override, no embedded public key.
- DF-HERMES-CANOPY-72 (P2): latest.json not published at any stable URL; v0.1.0 has no
  manifest asset and predates --update-interval (flag not defined) — docs ahead of release.
- DF-HERMES-CANOPY-73 (P3): release-tool platform-filename contract undocumented; foreign
  files silently skipped; misleading "no release artifacts found".

Probe error corrected during the run: first boot test hit the HOST system postgres (5432,
stale workspaces schema, missing slug) — that database is not canopy's; canopy-pg is
docker on 5437 and is healthy at v48. Not a product defect.
