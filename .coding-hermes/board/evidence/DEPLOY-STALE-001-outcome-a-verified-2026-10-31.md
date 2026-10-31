# DEPLOY-STALE-001 — outcome (a) verification evidence

Tick: hermes-canopy-foreman-2026-10-31-18-46-47
Collected: 2026-10-31T19:23Z by the hermes-canopy foreman (verification-only closeout; no worker, no code change).
Row: .coding-hermes/board/tasks.jsonl id=DEPLOY-STALE-001 (filed 2026-10-29T05:20:10Z, Fleet supervisor).

Verdict: OUTCOME (a) VERIFIED and recorded. The deploy-stale premise that opened this
row (binary built 2026-10-26T02:59, service inactive, hourly STALE_BLOCKED alerts) is
no longer true: the deployed artifact was rebuilt 2026-10-29T21:00Z, the service has
been up since, /healthz answers 200, and the hourly checker reports CURRENT with no
alert events for over 48h.

## Acceptance criteria -> evidence

AC (row): "One of two outcomes is recorded with evidence: (a) canopyd is rebuilt from
HEAD, restarted, /healthz answers 200, and the next deploy-staleness check reports
FRESH (lag_seconds ~0 or no STALE_BLOCKED event for one full hour); or (b) the
deploy-staleness checker is retired/disabled ... Either way the events [are reconciled]."

### 1. canopyd rebuilt (code-equivalent to HEAD)

  $ /home/kara/bin/canopyd version
  v0.2.0-143-g4ed731a9 commit=4ed731a9 built=2026-10-29T21:00:00Z        (exit 0)
  $ ls -l --time-style=full-iso /home/kara/bin/canopyd
  44215355 2026-10-29 16:00:39.076906999 -0500                            (binary mtime)

  Deployed commit (full): 4ed731a94de01bdc7b3d2023bc0ba84008b7088a
  - 4ed731a9 was origin/master HEAD at build time (next commit da134b1c landed
    2026-10-30T02:49Z). The running artifact self-reports that commit and build time
    via /healthz and `version`.
  - Deployable-code drift check:
      $ git log 4d81b30c..HEAD -- internal/ cmd/ migrations/ go.mod go.sum
      (empty — exit 0, no output)
    Zero commits touching watched/deployable paths between the binary's tree and the
    current master HEAD 03230397452816f6b494871c57aabb8ccf4cfcfb (the 4 commits since
    4ed731a9 touch only .coding-hermes board/evidence files). A rebuild from current
    HEAD would produce a code-identical binary, so "rebuilt from HEAD" holds modulo
    board-only commits. This is the precise claim; the binary was NOT literally built
    from 03230397.

### 2. restarted and running

  $ systemctl --user is-active canopy-canopyd
  active                                                                (exit 0)
  $ systemctl --user status canopy-canopyd
  Active: active (running) since Thu 2026-10-29 16:00:40 -05 (2026-10-29T21:00:40Z)
  Main PID: 23099 (canopyd), Memory 11M, uptime 1d 21h at probe time
  Crash-loop check (checker journal, hourly): "canopy-canopyd restarts 0 -> 0
  (delta 0 < threshold 50) — no alert" on every observed run.

### 3. /healthz answers 200

  $ curl -s -m 5 -w 'HTTP %{http_code}' http://127.0.0.1:8091/healthz
  {"status":"ok","service":"canopyd","schema_version":49,"embedded_migrations":49,
   "relay":{"mode":"air_gapped","status":"disabled","sessions":0},
   "version":"v0.2.0-143-g4ed731a9","commit":"4ed731a9","build_time":"2026-10-29T21:00:00Z"}
  HTTP 200                                                             (exit 0)

  Correction to the row's original detail: the service does NOT listen on :8099.
  The unit sets Environment=HTTP_ADDR=:8091 (ss -tlnp confirms canopyd on *:8091).
  The :8099 refusal cited at row time was a wrong-port probe plus the service being
  down then. Schema parity: embedded_migrations 49 == db schema_migrations 49
  (checker --check-schema view in journal).

### 4. deploy-staleness check reports FRESH/CURRENT

  canopy-deploy-check.timer (user systemd): active, last run 2026-10-31 13:00:00 -05,
  next 14:00 — the checker was NOT retired (outcome (b) is not claimed).

  $ journalctl --user -u canopy-deploy-check.service (runs 2026-10-31 11:00, 12:00, 13:00)
  deployed: /home/kara/bin/canopyd (2026-10-29T21:00:39Z)
  source:   4d81b30c47e74f1a33883353ddaf72cb9ca825ee (2026-10-26T23:24:17Z)
  watched:  internal cmd migrations go.mod go.sum
  lag:      -250582s (threshold 43200s)
  CURRENT: deployed canopyd is up to date

  Negative lag = deployed artifact is NEWER than the newest watched-path source
  commit (4d81b30c is still the newest commit touching watched paths — see the empty
  git log in section 1), i.e. FRESH by the checker's own definition.

  Alert-event silence (the AC's second disjunct):
  $ grep -c deploy_stale_alert .coding-hermes/board/events.jsonl
  230
  $ grep deploy_stale_alert ... | tail -1 -> timestamp 2026-10-29T16:23:21Z (reason "STALE")
  No deploy_stale_alert event has been appended for ~48h (> the AC's one full hour).
  All 230 events are committed (git status clean at tick start).

### 5. events reconciled

  Pre-tick working tree: clean (git status --short empty). The hourly alert backlog
  described at row time (10 uncommitted events) was committed by 2026-10-29 board
  work (commit a90930d7 "commit deploy-stale alert backlog" and later).

## Deploy attribution (labeled INFERENCE)

Not directly observed: journald unit entries before 2026-10-31 are rotated out
(`journalctl --user -u canopy-canopyd.service --since '2026-10-29'` returns no start
lines; retention currently begins 2026-10-31). INFERENCE: the 21:00:00Z build /
21:00:39Z binary mtime / 21:00:40Z service start one second apart match the hourly
canopy-deploy-check run at 2026-10-29T21:00Z taking its auto-deploy path
(unit ExecStart=/home/kara/hermes-canopy/scripts/check-deploy-staleness.sh --deploy;
CANOPYD_DEPLOY_CMD default `make deploy`). Attribution is not required by the AC —
the achieved state is — but is recorded so the row cannot be misread later.

## Gates run this tick (recorded verbatim)

| Command | Exit | Result |
|---|---|---|
| go build ./... | 0 | compile clean (all Go packages) |
| make build-ui | 0 | vite build + embedui build of bin/canopyd, version stamp v0.2.0-146-g03230397 |
| boardctl -C . validate | 0 | RESULT: OK (519 warning(s)) — inherited legacy duplicate/id-less event rows; baseline signature captured before write, must not worsen after |
| curl http://127.0.0.1:8091/healthz | 0 | HTTP 200 (payload above) |
| /home/kara/bin/canopyd version | 0 | v0.2.0-143-g4ed731a9 (payload above) |

Full suite (make test: all Go packages + frontend vitest 95 files/1490 tests) was NOT
re-run this tick: the last full run PASSED on the identical code state (tick
hermes-canopy-foreman-2026-10-31-12-40-34, board header last_tick 2026-10-31T13:01:47Z)
and zero source diffs exist between 03230397 and that run; this closeout changes only
board/evidence files. Exact-head CI on the closeout commit is the live gate for the
bookkeeping change and is reported in the tick report, not collapsed into the tested
parent's state.

## Identity fields (kept distinct per closeout discipline)

- tested_commit (published parent under test at probe time):
  03230397452816f6b494871c57aabb8ccf4cfcfb
  exact-head CI for tested_commit: Build run 2026-10-31T13:03:33Z, conclusion success
- deployed_commit (embedded in the running artifact):
  4ed731a94de01bdc7b3d2023bc0ba84008b7088a
- bookkeeping_commit (the closeout commit containing this file): reported separately
  in the closeout board event and the tick report; per closeout discipline this file
  is not amended to name the commit that contains it.

## Witness / verdict

witness=systemd:canopy-canopyd-active-since-2026-10-29T21:00:40Z + http:healthz-200-8091 +
systemd:canopy-deploy-check-CURRENT-2026-10-31 + events:deploy_stale_alert-silent-since-2026-10-29T16:23:21Z
STATUS: complete  TIER: T3 (invoked and observed: live probes against the running
service, checker journal and board store)  EVIDENCE: this file.
Next owner action: none required for outcome (a). The checker stays enabled (its
baseline source ref 4d81b30c is correctly the newest watched-path commit; nothing to
re-point). Outcome (b) is not claimed: the checker was never retired.
