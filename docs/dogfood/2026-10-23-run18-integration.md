# Run 18 — operator-surface dogfood: the doc-drift check and the PostgreSQL→SQLite export

**Date:** 2026-10-23 · **Tick:** `hermes-canopy-dogfood-2026-10-23-14-46-35` · **Target HEAD:** `bcf6edd2`

**Angle:** the operator/maintainer surface that shipped this week. Runs 1–17 covered the
human collaboration loop, MCP, cards, topics/compiler, synthesis/sync/plugins, the live
gateway, the CLI/API ladder, MLS/workspace collab, signed distribution, tree sharing and
the embedded single-binary SPA. Two surfaces added in the last three days had never been
driven: the README-vs-code **drift check** (`README-11`, `c6686a5c`, 2026-10-23) and the
documented **`canopyd db export-sqlite`** operator verb (`README-10`, 2026-10-22).

**Promise under test:** *"A maintainer can trust `make docs-check` to fail when the docs
drift from the binary, and an operator can take a faithful portable copy of their graph
with one documented command."*

**Verdict: 🟡 PROMISING-BUT-ROUGH.** The export is faithful, contract-correct and fast —
its fidelity held on a 50,337-node corpus down to the last byte. The drift check is a real
guard but fails open on four drift classes. Neither surface is broken in a way that loses
data; both cost the operator work at exactly the moment they need them.

Environment: binary built from a detached worktree of HEAD (`/tmp/hcdf23/src`), scratch
PostgreSQL 16 container on `127.0.0.1:5478`, scratch `canopyd serve` on `127.0.0.1:8099`
with an isolated `HOME` and `CANOPY_FILE_ROOT`. No repository file was modified to run the
probes; every README mutation was applied to a throwaway copy under `/tmp/hcdf23/dc/`.

---

## 1. `make docs-check` — a guard that reports PASS on drift it claims to catch

`scripts/docs-check.py` (301 lines, added by README-11) validates: fenced `canopyd`
commands against the built binary's `--help`, the README Flags table, the Makefile Targets
table, the Environment Variables table, and the docs index. CI runs it as its own step
right after `make build`; `make docs-check-test` covers the docs-index and
README→index-link arms.

Control on an unmodified tree:

```
$ make docs-check
docs-check: PASS (14 docs pages, 12 canopyd commands, 11 Makefile targets, 37 environment variables)
```

Arm matrix (one realistic drift per arm, applied to a pristine copy of `README.md` + `docs/`):

| arm | drift applied | expected | observed |
|---|---|---|---|
| A0 control | none | PASS | PASS ✓ |
| A1 bad flag | `session import --limit` → `--limitt` | FAIL | `FAIL: README:1309: documented flag '--limitt' is absent from canopyd session import --help` ✓ |
| A2 ghost env | env row `GHOST_VAR_XYZ` (no read site) | FAIL | `FAIL: … lists 'GHOST_VAR_XYZ' with no production read site` ✓ |
| A3 env prefix | env row `CANOPY_DB` (never read; only `CANOPY_DB_DRIVER` is) | FAIL | **PASS** (38 env rows — the row *was* parsed) ✗ |
| A4 dangling link | README links `docs/DOES-NOT-EXIST.md` | FAIL | **PASS** ✗ |
| A5 bad verb | `./bin/canopyd tree lst` (runtime rc=1 `unknown tree subcommand: lst`) | FAIL | **PASS** ✗ |
| A6 blockquoted flag | `db export-sqlite --sqlite-out` inside the `> ```bash` example | FAIL | **PASS** ✗ |

Raw transcript: `docs/dogfood/evidence/2026-10-23-run18/docs-check-arms.txt`.

Mechanisms, from the checker's own source:

- `shell_commands()` (scripts/docs-check.py:68-100) opens a shell fence only when the
  fence itself starts the line (`fence_re = ^\s*````). The README documents the newest
  operator exceptions — `card export` and `db export-sqlite` — inside **blockquoted**
  fences (`> ```bash`), so those examples are never scanned. A6 shows the consequence: a
  flag the binary rejects (`rc=2 flag provided but not defined: -sqlite-out`) passes.
- `command_help()` (:140-167) tries progressively longer positional prefixes and keeps the
  **longest that produced a matching usage line** — so `tree lst` resolves to the `tree`
  prefix and the unknown trailing verb is never named. Only flags are compared afterwards,
  which is why A5 passes with zero flags to check.
- `has_real_env_read()` (:307-320) tests `variable not in line`, a **substring** match on a
  line that must also carry a `Getenv`-ish call. `CANOPY_DB` matches
  `c.DBDriver = parseDBDriver(os.Getenv("CANOPY_DB_DRIVER"))`
  (internal/config/config.go:346), so a variable nobody reads is documented as real.
- `check_docs_index()` (:193-261) counts the single `docs/README.md` link and validates the
  index rows, but never opens any other relative link — and DOC-11's whole premise was
  discoverability of a 14-page docs tree.

One latent hole, not proven with a fixture: the Makefile `docs-check` target has no build
prerequisite, so a local run validates against whatever `bin/canopyd` happens to be on
disk (CI is safe because it builds first).

**Filed: `DF-HERMES-CANOPY-96` (P2)** with one acceptance criterion per arm plus a
"still green on the unmodified tree" criterion, so the fix cannot close by loosening the
working arms.

## 2. `canopyd db export-sqlite` — faithful, contract-correct, and unusable on a live instance

Real-user story: *"I run Canopy; give me a portable copy of my graph."* Followed exactly
what `docs/SQLITE-RUNTIME.md` and the README's CLI exception say, against a scratch
instance seeded **through its own API** (2 trees, 29 nodes including a 60,005-byte node,
unicode/emoji and a markdown/quote/backslash sample, 29 edges from reply + merge).

### 2a. Fidelity — PASS

`db export-sqlite` on a quiesced instance, then an independent comparison written for this
run (psql `json_agg` on one side, stdlib `sqlite3` on the other, **every column of every
row**, not just counts):

```
users: pg=1 sqlite=1          trees: pg=2 sqlite=2
nodes: pg=50337 sqlite=50337  edges: pg=337 sqlite=337
content_hash recomputation: 0 mismatch(es) over 50337 nodes
integrity_check: ok           foreign_key_check: clean
orphan nodes=0 orphan edges=0 max content length in sqlite: 60005
unicode round-trip ok: True
RESULT: PASS
```

Transcript: `docs/dogfood/evidence/2026-10-23-run18/export-fidelity.txt` (the 50,337-node
run is the large sample; the 29-node corpus gave the same verdict earlier in the tick).

### 2b. Documented contract — PASS, all of it

| probe | expected | observed |
|---|---|---|
| destination already has data | rc=1, name the path | `refusing non-empty target "…"; choose a new path` ✓ |
| zero-byte destination | accepted | rc=0 ✓ |
| missing `--sqlite-path` | rc=2 | `Error: --sqlite-path is required` ✓ |
| unknown `db` subcommand | rc=2 | `unknown db subcommand: frobnicate` ✓ |
| `--dsn` alias | rc=0 | ✓ |
| `--sqlite-path` and `--dsn` disagree | rc=2 | ✓ |
| unreachable PostgreSQL | rc=1 | `ping PostgreSQL: … dial tcp …` ✓ |
| destination in a non-existent directory | created | rc=0 ✓ |
| `--help` | rc=0 | ✓ |
| temp files after the run | none | 0 ✓ |

Transcript: `docs/dogfood/evidence/2026-10-23-run18/export-contract.txt`.

### 2c. The live instance — 24/24 exports fail

An operator migrating a **running** service is the normal case, not the edge case. Two
independent samples this tick, each 12 attempted exports against the same instance while a
writer POSTed nodes at ~50/s:

```
sample 1: rc=1 on 12/12 (308 nodes written during the loop)
  8x  sqlite export: insert edges: constraint failed: FOREIGN KEY constraint failed (787)
  4x  sqlite export: row count mismatch for nodes: PostgreSQL=N SQLite=N-1
sample 2: rc=1 on 12/12, all FOREIGN KEY mode
rc=0 files with referential damage: 0      <- never a silent torn backup
```

Transcript: `docs/dogfood/evidence/2026-10-23-run18/export-concurrency.txt`.

Cause: `internal/db/sqlite/export.go:74-93` issues **four independent `pg.Query` calls**
(users → trees → nodes → edges) with no enclosing source-side transaction, so a row
committed between the nodes read and the edges read is absent from the target and trips
the target's FK, or moves the source count the post-write check re-reads.

What a user experiences: the command fails every time they run it against the service they
are trying to migrate, with a message about `FOREIGN KEY constraint failed` or a row-count
mismatch — both of which read as *data corruption* rather than *concurrent writes*. Neither
`docs/SQLITE-RUNTIME.md` ("One-time PostgreSQL export") nor the README exception says the
source must be quiesced, and nothing suggests retrying.

To the exporter's credit, the failure mode is the right one: it never published a torn
file (`rc=0` with referential damage: 0/24). The defect is the missing snapshot and the
missing words, not silent corruption.

**Filed: `DF-HERMES-CANOPY-97` (P2).**

## 3. Cost of use (Step 2b) — nothing slow enough to file

| corpus | destination | wall (first run / warm) | user CPU |
|---|---|---|---|
| 29 nodes | NVMe | 0.17 s / 0.15 s | 0.02 s |
| 5,337 nodes | NVMe / tmpfs | 0.32 s / 0.25 s | 0.22 s |
| 50,337 nodes | NVMe | 4.5–4.9 s first (cold page cache) / 2.52–2.58 s warm | 2.0–2.1 s |
| 50,337 nodes | tmpfs | 2.25–2.40 s | 2.0–2.1 s |

Transcript: `docs/dogfood/evidence/2026-10-23-run18/export-perf.txt`.

The live instance runs ~51k nodes (`DF-HERMES-CANOPY-34`), so the 50k row is the realistic
scale: **~2.5 s for a one-shot migration export**. The tmpfs A/B shows the write path is
not paying per-statement fsync — user CPU is identical (~2.0 s) and the load happens in a
single `BeginTx`/`Commit` pair — so the suspected "loader without a transaction" class does
**not** apply here. `make docs-check` is 0.31 s. No PERF row was filed: a win nobody can
feel is not a finding.

## 4. Install leg (fresh machine)

`bunker-qa.sh launch` was run in the normal two-phase form against `bunker-mvp`
(agent `29d9a617`, TTL 4h, HEAD `bcf6edd2`). See §5 for the graded cells; the leg was
**not** skipped and no credential was minted to make it pass.

## 5. Install battery cells

Battery launched at `14:59Z`, `CELLS-DONE` at `15:19Z` (20 min wall on a host running four
concurrent batteries), collected and the agent destroyed — `bunker info 29d9a617` answers
`not_found` and a `bunker list` grep counts 0.

19 evidence rows (`/tmp/bunker-qa-evidence-20261023T145912Z-3015391.jsonl`):

| cell | status | detail |
|---|---|---|
| repo-head | OK | tested HEAD `bcf6edd2` (pull skipped: clone dirty) |
| launch | OK | agent 29d9a617 @ bunker-mvp, script 135,546 B, TTL 4h |
| toolchain-bootstrap ×7 | OK | go 1.27.3, gcc/g++ wrappers, make 4.3, compose plugin, buildx v0.38.1, node v22.15.0, harness compose/node probes |
| fresh-install | OK | clone → build on the bare agent (cell detail is the tail of the `go build` download log) |
| **ci-pass** | **OK** | `act: 1 jobs green — rc=0` — the repo's own workflow, **including the `README docs drift check` step added by README-11**, runs green end-to-end in a container on a clean box (`Tidy` 14.9 s, `golangci-lint` 43.3 s, gitleaks, docker build) |
| upgrade | OK | previous release `v0.1.0` (via `git archive`) → HEAD reinstall clean |
| ui-probe | OK | UI serves 200 on `:3111` from `frontend/` |
| chaos-resource | PASS | suite survives a 3 GB memory cap (3,145,728 KB observed in the child and the subshell) |
| chaos-errorpath | OK | clean error (rc=1) on missing config |
| chaos-disconnect | **UNVERIFIED** | `disc.log` empty / no-test-path marker — the network cut was never exercised; the harness says so itself rather than passing |
| docker-deploy, chaos-shutdown | SKIPPED | safety gate: the controller is the live Hermes host, Compose lifecycle cells forced off |
| chaos-corruption | N/A | no db/state files in the repository |
| collect | OK | 19 rows pulled, merge-deduped by timestamp, temp agent destroyed |

Installability verdict: **PASS** on the parts a fresh user needs (clone → build → CI →
upgrade → UI serves), with one cell UNVERIFIED and two skipped by documented safety gates —
not a silent pass. Nothing was minted or borrowed to make it run.

One observation about the harness, not the product: the `fresh-install` row's detail is the
last line of the build log (`go: downloading …`), and at `15:00Z` the agent had **no**
`bin/canopyd` yet, so that cell's "OK" is not by itself proof the binary was produced. The
`upgrade` (reinstall) and `ci-pass`/`ui-probe` cells are what actually demonstrate a working
build on the clean box; grading the install leg on the fresh-install cell alone would be a
proxy.


## 6. Reproduce this run

```bash
# drift-checker arms (no repo mutation)
python3 scripts/docs-check.py --readme <arm>/README.md --docs-dir <arm>/docs --binary ./bin/canopyd
# export: faithful + contract
canopyd db export-sqlite --sqlite-path /tmp/new.sqlite
# export against a live instance
#   (start a writer at ~50/s against /api/v1/trees/{id}/nodes, then repeat the export 12x)
```

The fixture generator, the two verifiers and the concurrency probe written for this run are
quoted in the transcripts' headers; they are throwaway scripts, not repo code.
