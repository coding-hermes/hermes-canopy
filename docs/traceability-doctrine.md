# Traceability Doctrine

Every board row in this repository follows a single traceability contract so that a foreman tick can judge whether work is truly done.

## Marker Fields

Board rows carry these fields when they are traceable:

- **`id`** — unique identifier (e.g. `DF-HERMES-CANOPY-42`)
- **`ch:trace`** — compact marker string that ties the row to its specification, test, documentation, and evidence paths. Format:
  ```
  ch:trace row=<id> spec=<spec-path> evidence=<evidence-path> witness=<witness-type>:<witness-target> test=<test-path>
  ```
- **`STATUS:`** — current state from the worker's perspective. Values: `IN_PROGRESS`, `COMPLETE`, `REJECTED`, `STALE_FINDING`.
- **`TIER:`** — minimum traceability tier achieved (see below).
- **`EVIDENCE:`** — semicolon-separated list of evidence artifacts that the worker produced.
- **`closeout_sha`** — the commit that closed the row.

## Evidence and Witness

Evidence is proof that something was exercised. Acceptable evidence types:

1. **`path@<repo>:<path>`** — the file exists at a specific path in the remote repo
2. **`test:<test-name>`** — a test was written and runs green
3. **`live:<resource>`** — a live service or endpoint was exercised (e.g. `live:canopyd-8091`)
4. **`gitreins:<verdict-hash>`** — a GitReins Tier 1 or Tier 2 verdict
5. **`commit:<sha>`** — a specific commit hashes

Evidence is only valid when it can be independently re-checked. A claim of "tested" without a named test or output is not evidence.

A **witness** is an independent source that confirms the evidence. Examples:
- `github:<run-id>` — GitHub Actions run result
- `gitlab:<pipeline-id>` — GitLab CI pipeline result
- `path@origin:<file>` — file contents at the remote HEAD

## Close-out Requirements

A board row is **CLOSED** only when ALL of the following are true:

1. **The work is committed** — a commit exists in the repo that implements the acceptance criteria
2. **Guard battery passed** — `gitreins guard` ran on the committed tree and reported PASS (or was skipped for a doc-only change with documented reason)
3. **Traceability marker is complete** — `ch:trace` contains a non-empty row id, spec, evidence path, and witness
4. **STATUS and TIER are set** — the row has `STATUS: COMPLETE TIER: <value>` in its title or commit message
5. **CI is green** — either the commit's CI run succeeded, or the lane is known to be CI-exempt (documented)

### Traceability Tiers

| Tier | Requirement | Meaning |
|------|------------|---------|
| **T3** | Invoked and observed | The worker ran the test/guard/command and observed the result. This is the minimum for closure. |
| **T2** | Judged by LLM | A Tier 2 judge evaluated the diff against the acceptance criteria. |
| **T1** | Guard passed | Static checks (lint, build, test) passed but no acceptance-criteria judgment was made. |
| **T0** | Claimed only | The worker claims work is done but no external check was performed. Never close at T0. |

**Rule:** a row must reach at least **T3** (invoked and observed) before it can be marked complete. T0–T2 alone never close a row.

## Evidence Archive

Evidence files are stored in `docs/dogfood/evidence/` organized by date. Each run creates a subdirectory:
```
docs/dogfood/evidence/<YYYY-MM-DD>/
  <finding-id>.txt    — raw output or logs
  <finding-id>.md     — summary and verdict
```

## Updating This Doctrine

Changes to this file itself must be reviewed by the foreman tick before landing. This ensures the traceability contract stays consistent across all board rows.
