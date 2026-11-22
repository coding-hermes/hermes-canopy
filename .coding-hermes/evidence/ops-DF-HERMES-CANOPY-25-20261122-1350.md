# Ops audit — DF-HERMES-CANOPY-25 (handler test timeout watch): quiet precondition measured UNMET

- Tick: hermes-canopy-ops-2026-11-22-13-46-56 (ops lane, hermes-canopy)
- Row: DF-HERMES-CANOPY-25 (P5, pending) — "watch: the handler package can exceed a 10m test
  timeout under concurrent PG load, blocked in testutil.TruncateAll"
- Resolved workdir: /home/kara/hermes-canopy (board symlink in the stand-in path
  /home/kara/.hermes/stand-in/pm-lane/hermes-canopy/.coding-hermes/board points here)
- Published tip at gate: 06887fa004df76b609ac2491a7b173e9765c66c7 (= HEAD = origin/master)
- What changed since the row's last audit (2026-11-03): nothing in the row itself; this is the
  FIRST tick to measure the row's own quiet-precondition arm against the live host instead of
  assuming it. The measurement result is material: the precondition cannot hold while the deployed
  service is running, so the baseline arm needs a maintenance window or a precondition revision.

## Probes (literal command + literal output)

### Precondition (the row's own literal command)

```
$ pgrep -fa 'go test|canopyd serve'    # redirected to scratch; rc=0, 4 lines
418864 /home/kara/bin/canopyd serve
2320007 /home/kara/.hermes/tools/python-3.14.9+202.../bin/python3 -I -c import os, re, sys ... (hermes worker harness, cmdline embeds 'go test ./...' from the boardctl project's live QA-BOARDCTL-005 worker)
2321589 <same python harness line>
2346922 /usr/bin/bash -c source /mnt/ssd/tmp/hermes-snap-...sh ... (the probe shell's own self-match)
```

Anchored cross-check (ps, wrapper lines excluded by reading, not by count):
`ps -eo pid,etime,cmd | grep -E 'go test|canopyd serve' | grep -v grep` printed the same 4 lines.
REAL matches: only pid 418864 (deployed `canopyd serve`, etime 1-01:08:23). The two python lines
are a foreign project's worker harness (cmdline contains the string "go test" inside its brief —
they are NOT canopy go test processes); line 4 is pgrep matching its own wrapper.

Verdict: the row's literal precondition ("prints nothing") is NOT met — the deployed service is
running, which the testutil itself declares shares the instance:

```
$ grep -n 'PRODUCTION' internal/testutil/integration.go
9:// that same server (:5437, container canopy-pg) is also the PRODUCTION
```

### Host load (second precondition component: quiet host)

```
$ uptime        # 16 cores (nproc=16)
 08:51:52 up 1 day, 13:00,  1 user,  load average: 42.91, 36.90, 36.83
 08:53:39 ...   load average: 18.98, 30.39, 34.51
```

1-minute load 19–43 on 16 cores: saturated, not quiet. An elapsed-time arm (<550s) measured under
this load proves nothing about TruncateAll lock contention.

### Shared PostgreSQL instance state (read-only, via docker exec)

```
$ docker ps --format '{{.Names}} {{.Ports}} {{.Status}}' | grep 5437
canopy-pg 0.0.0.0:5437->5432/tcp, [::]:5437->5432/tcp Up 37 hours (healthy)

$ docker exec canopy-pg psql -U canopy -d postgres -tAc "select application_name, usename, state, count(*) from pg_stat_activity group by 1,2,3 order by 4 desc"
|canopy|idle|5
|||4
|canopy||1
psql|canopy|active|1

$ docker exec canopy-pg psql -U canopy -d postgres -tAc "select mode, granted, count(*) from pg_locks group by 1,2 order by 3 desc"
AccessShareLock|t|1
ExclusiveLock|t|1

$ docker exec canopy-pg psql -U canopy -d postgres -tAc "select count(*) from pg_stat_activity where application_name='canopy' and state='idle' and now()-state_change > interval '1 hour'"
0
```

Reading: the deployed canopyd holds 6 connections on the shared instance (5 idle + 1 other), with
ZERO ungranted locks and no connection idle for more than an hour. An idle deployed service alone
does NOT reproduce lock contention today — the 2026-09-25 timeout's suspected cause (concurrent PG
load) only arises when a judge/sweep runs PG-backed handler tests WHILE the service (or another
PG consumer) is active. This sharpens the row's premise rather than refuting it.

### Deployed service liveness (ops scope)

```
$ curl -sS -m 5 http://127.0.0.1:8091/healthz   # rc=0
{"build_time":"2026-11-21T12:43:24Z","checks":{"db":"up"},...,"commit":"d3113afe",
 "schema_version":49,"service":"canopyd","status":"ok","version":"v0.2.0-274-gd3113afe-dirty"}
```

Matches the 2026-11-21 releng sweep's deploy evidence (same commit, build time, schema 49).

## Arm states today

- PRECONDITION: UNMET (canopyd serve live pid 418864; load 1m 19–43 on 16 cores).
- Arm 1 (quiet baseline, rc=0 under 550s): NOT RUN — deliberately, precondition unmet. Running it
  would produce a non-diagnostic elapsed time, and the row's own FALSIFIER ("a timeout that still
  reproduces on a quiet database is not contention and must not be closed as one") cuts the same
  way: a green under load 19–43 proves nothing either.
- Arm 2 (diagnose, do not assume): N/A — no run, no timeout.
- Arm 3 (repeatability): N/A.
- Arm 4 (no gate weakening): MET — `git status --porcelain internal/testutil` printed nothing
  (rc=0); last commit touching testutil is 22c8740 (2026-09-26).

## What this tick did NOT do, and why

- Did NOT run the handler baseline: precondition unmet (see above); a measurement under this load
  and with the production-shared PG in use would be attributed to nothing.
- Did NOT stop or restart canopyd (pid 418864): the deployed instance is live and healthy and a
  service restart to satisfy a test precondition is out of scope for an ops tick.
- Did NOT touch .coding-hermes/evidence/qa-20261121T124324Z/ (160K untracked QA battery logs from
  2026-11-21, another lane's residue — left in place, disclosed here).
- Did NOT edit the criterion: the conflict between the row's precondition and continuous
  deployment of canopyd on the same host is named here, not patched into the wording.

## Finding + fix order for the row

1. The row's precondition (pgrep prints nothing) is structurally unsatisfiable while canopyd is
   deployed on this host — it has been up 37h+ and shares the :5437 instance. The quiet baseline
   needs either (a) an owner-sanctioned maintenance window with canopyd stopped, or (b) a
   precondition revision to "no OTHER PG consumer" measured via pg_stat_activity (deployed service
   present but idle, zero ungranted locks) instead of pgrep over cmdlines.
2. Until then the row's honest state is: premise plausible but unmeasurable as written; arm 4
   holds; no code change warranted.
3. The idle-footprint data above (6 conns / 0 ungranted locks) gives any future diagnostic run its
   "quiet-enough" reference picture for comparison while a timeout is actually occurring.
