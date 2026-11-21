# QA-HERMES-CANOPY-058 evidence — deployed harness synced to tracked mirror (tick hermes-canopy-foreman-2026-11-21-08-36-09)

Fix: deployed ~/.hermes/scripts/bunker-qa.sh replaced byte-identically with the tracked mirror
/home/kara/coding-hermes-tools/bunker-qa.sh @ origin/master 1d3f247 (last mirror touch d69a46b 2026-11-20).

## Arms (run 2026-11-21, coding-hermes-tools @ 1d3f247, both trees clean)

1. Pre-sync state (matches PM cycle-3 measurement):
   deployed wc -l 4770, sha256 7f82eb5d496e588d... ; mirror wc -l 5963, sha256 db98fcfbc802eeb5...
2. cmp -s mirror deployed -> exit 0 (byte-identical) [PASS]
3. sha256 after sync: BOTH = db98fcfbc802eeb5d689389209f26bb1f04715e844a01610532af4b5bb6642c5 [PASS]
4. make qa-runner-drift: PASS (11 arms) incl. rr-mirror-identity (db98fcfbc802) [PASS]
5. make qa-runner-drift-selftest: PASS (control green 11 arms; strip-jq and strip-jqctx regressions both caught by name) [PASS]
6. Deployed wc -l after sync: 5963 (lag 1193 -> 0)

Rollback: pre-sync deployed copy preserved at /tmp/bunker-qa.sh.pre-QA58-backup (sha 7f82eb5d496e588d...).

## Residual note for QA-HERMES-CANOPY-44-R2

The now-deployed copy carries the disconnect_log_valid disc ladder guard (UNVERIFIED when
disc.log lacks NETWORK_CUT_APPLIED + suite counts) and the DF-CRIER-499 go-floor preflight
(toolchain-bootstrap FAILs loudly when installed go < go.mod floor) — both verified present in
the synced file by the qa-runner-drift rendered-cell arms. The live-battery re-proof of the
floor shape grading not-OK is tracked by QA-HERMES-CANOPY-55's battery rerun; QA-44-R2 closes
on that battery, not on this static proof.
