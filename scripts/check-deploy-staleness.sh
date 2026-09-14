#!/usr/bin/env bash
#
# check-deploy-staleness.sh (GAP-067): is the deployed canopyd artifact older
# than the newest committed change that can affect it?
#
# Compares the mtime of the installed canopyd binary against the committer
# date of the newest commit touching any watched path (internal/, cmd/,
# migrations/, go.mod, go.sum). Prints deployed/source timestamps, lag in
# seconds, and the relevant source commit.
#
# --check-schema mode (GAP-069): compares the migration schema version
# embedded in a canopyd binary against the live schema_migrations max of the
# target database, WITHOUT installing, restarting, or writing anything (the
# only DB statement is a read-only SELECT). This is the pre-deploy gate that
# would have prevented the 2026-09-12 outage: a schema-46 database met a
# schema-42 binary, whose own stale-build guard then crash-looped it for ~37h.
#   embedded >= db  -> exit 0 (binary can understand the DB; e.g. both 46)
#   embedded <  db  -> exit 1, "STALE BUILD: binary embeds schema N, DB is at M"
#   binary/DB/probe failures -> exit 3 (ERROR)
#
# Exit codes (stable, documented):
#   0 CURRENT         — deployed artifact is within the stale threshold
#                       (--check-schema: embedded schema >= DB schema)
#   1 STALE           — deployed artifact is over the threshold (or absent)
#                       (--check-schema: embedded schema < DB schema)
#   2 STALE_BLOCKED   — stale + --deploy, but the repo worktree is dirty
#                       (tracked changes); auto-deploy refused for safety.
#                       A half-written worker tree must never be deployed.
#   3 ERROR           — operational error (bad repo, unreadable paths, failed
#                       re-check after deploy, --check-schema probe failure)
#
# Environment overrides (tests never touch the real service):
#   CANOPYD_STALE_REPO_ROOT   repo to compare against
#                             (default: this script's repo root)
#   CANOPYD_STALE_PATH        installed artifact to check
#                             (default: /home/kara/bin/canopyd)
#   CANOPYD_STALE_THRESHOLD_S staleness threshold in seconds (default: 86400)
#   CANOPYD_DEPLOY_CMD        --deploy command (default: make deploy)
#   CANOPYD_DEPLOY_DIR        working directory for the deploy command
#                             (default: repo root)
#   CANOPYD_SCHEMA_DB_URL     --check-schema target database URL (read-only
#                             use). Default: postgres://canopy:canopy@localhost:5437/canopy?sslmode=disable
#                             (the shared canopy-pg, "canopy" DB — the same
#                             database the live service uses by default, so
#                             checking THAT is the point).
#   CANOPYD_SCHEMA_PSQL       psql binary (default: psql)
#   CANOPYD_SCHEMA_PROBE_BIN  canopyd binary to interrogate in --check-schema
#                             mode (default: the freshly built bin/canopyd —
#                             callers build BEFORE calling; deploy-canopyd.sh
#                             does exactly that)
#   CANOPYD_SYSTEMCTL         systemctl binary (default: systemctl)
#   CANOPYD_SERVICE_NAME      user service whose NRestarts counter is read
#                             (default: canopy-canopyd)
#   CANOPYD_CRASHLOOP_THRESHOLD
#                             crash-loop alert threshold: number of restarts
#                             the service counter may climb between two runs
#                             before a board alert is written (default: 50)
#
# Crash-loop alert (GAP-069 criterion 4): on EVERY staleness run the checker
# compares the unit's NRestarts counter against a baseline persisted in
# $REPO_ROOT/.coding-hermes/deploy-check-state.json (git-ignored runtime state
# kept alongside the board) and appends a deploy_crashloop_alert event to
# $REPO_ROOT/.coding-hermes/board/events.jsonl when the counter climbed by
# >= the threshold since the previous run. That covers a canopyd that
# crash-loops for ANY reason — not only a stale artifact (bad config, port
# taken, database down) — which is exactly the 2026-09-12 failure mode: the
# service restarted ~37k times over ~37h and produced zero alerts. The
# baseline file is rewritten on every run (alert or not) so it tracks reality;
# a missing, corrupt or unparseable file simply re-baselines with no alert.
# This path never changes the script's exit code.
#
# --deploy mode: only when stale, run the deploy command (the existing atomic
# `make deploy` path — build → install → restart → health poll → smoke), then
# re-run the staleness check; fail if the artifact is still stale afterwards.
# Never runs from a tracked-dirty worktree (STALE_BLOCKED).

set -uo pipefail

EXIT_CURRENT=0
EXIT_STALE=1
EXIT_STALE_BLOCKED=2
EXIT_ERROR=3

WATCH_PATHS=("internal" "cmd" "migrations" "go.mod" "go.sum")
DEFAULT_THRESHOLD=86400
DEFAULT_DEPLOYED_PATH="/home/kara/bin/canopyd"
DEFAULT_DEPLOY_CMD="make deploy"
DEFAULT_SCHEMA_DB_URL="postgres://canopy:canopy@localhost:5437/canopy?sslmode=disable"

MODE="check"
if [[ "${1:-}" == "--deploy" ]]; then
	MODE="deploy"
elif [[ "${1:-}" == "--check-schema" ]]; then
	MODE="check-schema"
elif [[ -n "${1:-}" ]]; then
	echo "usage: $0 [--deploy] [--check-schema]" >&2
	exit "$EXIT_ERROR"
fi

fail_error() {
	echo "STALE_CHECK ERROR: $*" >&2
	exit "$EXIT_ERROR"
}

REPO_ROOT="${CANOPYD_STALE_REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
DEPLOYED_PATH="${CANOPYD_STALE_PATH:-$DEFAULT_DEPLOYED_PATH}"
THRESHOLD_S="${CANOPYD_STALE_THRESHOLD_S:-$DEFAULT_THRESHOLD}"
DEPLOY_CMD="${CANOPYD_DEPLOY_CMD:-$DEFAULT_DEPLOY_CMD}"
DEPLOY_DIR="${CANOPYD_DEPLOY_DIR:-$REPO_ROOT}"
SCHEMA_DB_URL="${CANOPYD_SCHEMA_DB_URL:-$DEFAULT_SCHEMA_DB_URL}"
SCHEMA_PSQL="${CANOPYD_SCHEMA_PSQL:-psql}"
SCHEMA_PROBE_BIN="${CANOPYD_SCHEMA_PROBE_BIN:-$REPO_ROOT/bin/canopyd}"
SYSTEMCTL_BIN="${CANOPYD_SYSTEMCTL:-systemctl}"
CANOPYD_SERVICE="${CANOPYD_SERVICE_NAME:-canopy-canopyd}"
DEFAULT_CRASHLOOP_THRESHOLD=50
CRASHLOOP_THRESHOLD="${CANOPYD_CRASHLOOP_THRESHOLD:-$DEFAULT_CRASHLOOP_THRESHOLD}"
[[ "$CRASHLOOP_THRESHOLD" =~ ^[0-9]+$ ]] \
	|| fail_error "CANOPYD_CRASHLOOP_THRESHOLD not a non-negative integer: '$CRASHLOOP_THRESHOLD'"
BOARD_EVENTS="$REPO_ROOT/.coding-hermes/board/events.jsonl"
# Baseline for the crash-loop counter. Local runtime state (git-ignored — see
# .gitignore) rather than a tracked file: the checker must never make the
# worktree dirty, or its own --deploy gate would refuse to deploy.
DEPLOY_STATE_FILE="$REPO_ROOT/.coding-hermes/deploy-check-state.json"

# ── Board alerts (GAP-069 / AC2): staleness AND crash-loops must be VISIBLE ─
# The 2026-09-12 crash-loop (~37h, ~37k restarts) was invisible because no
# surface anyone watches reported it. The foreman tick scans events.jsonl
# every run, so this script appends ONE machine-readable event line there for
# each condition it detects — staleness (STALE / STALE_BLOCKED) and a
# crash-looping service (restart counter climbing between runs). It never
# rewrites the file, never touches tasks.jsonl, never changes an exit code.
# Best-effort: a missing or unreadable board file degrades to a NOTE on stderr.
#
# append_board_event <event_type> <detail_json> [context]
# The single writer for every board event this script emits: derives the next
# id from the file itself, renders the canonical line, appends it, and prints
# one stdout confirmation. Both alert paths below route through it — never add
# a second, divergent JSON writer.
append_board_event() {
	local event_type="$1" detail_json="$2" context="${3:-}"
	[[ -f "$BOARD_EVENTS" ]] || {
		echo "NOTE: board events file missing ($BOARD_EVENTS) — alert NOT recorded" >&2
		return 0
	}
	local next_id ts line
	next_id="$(python3 - "$BOARD_EVENTS" <<'PY'
import json, sys
mx = 0
with open(sys.argv[1], encoding="utf-8") as f:
    for line in f:
        line = line.strip()
        if not line:
            continue
        try:
            i = json.loads(line).get("id", 0)
        except Exception:
            continue
        if isinstance(i, int) and i > mx:
            mx = i
print(mx + 1)
PY
)" || next_id=""
	[[ "$next_id" =~ ^[0-9]+$ ]] || {
		echo "NOTE: could not derive next board event id — alert NOT recorded" >&2
		return 0
	}
	ts="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
	line="$(python3 - "$next_id" "$ts" "$event_type" "$detail_json" <<'PY'
import json, sys
print(json.dumps({
    "id": int(sys.argv[1]),
    "timestamp": sys.argv[2],
    "event_type": sys.argv[3],
    "task_id": "GAP-069",
    "actor": "deploy-staleness-check",
    "detail": sys.argv[4],
}))
PY
)" || {
		echo "NOTE: could not render board event JSON — alert NOT recorded" >&2
		return 0
	}
	printf '%s\n' "$line" >>"$BOARD_EVENTS"
	echo "board alert appended: $BOARD_EVENTS (event_type=$event_type${context:+ $context})"
}

# append_stale_event <reason:STALE|STALE_BLOCKED> <lag_seconds or "">
# Staleness-specific detail, rendered and handed to the shared writer above.
append_stale_event() {
	local reason="$1" # STALE | STALE_BLOCKED
	local lag="$2"    # seconds behind source ("" for missing artifact)
	local nrestarts embedded_v="null" db_v="null" probe_bin v w lag_json detail
	nrestarts="$("$SYSTEMCTL_BIN" --user show "$CANOPYD_SERVICE" -p NRestarts --value 2>/dev/null)"
	[[ "$nrestarts" =~ ^[0-9]+$ ]] || nrestarts="unknown"
	# Opportunistic schema context: probe the deployed binary (what the live
	# service runs) for its embedded version, and the DB for its schema max.
	# Both best-effort null — the staleness verdict itself never depends on it.
	for probe_bin in "$DEPLOYED_PATH" "$SCHEMA_PROBE_BIN"; do
		if [[ -x "$probe_bin" ]]; then
			v="$("$probe_bin" -print-schema-version 2>/dev/null)"
			if [[ "$v" =~ ^[0-9]+$ ]]; then
				embedded_v="$v"
				break
			fi
		fi
	done
	if command -v "$SCHEMA_PSQL" >/dev/null 2>&1; then
		w="$("$SCHEMA_PSQL" "$SCHEMA_DB_URL" -t -A -c "SELECT COALESCE((SELECT max(version) FROM schema_migrations), 0)" 2>/dev/null)"
		[[ "$w" =~ ^[0-9]+$ ]] && db_v="$w"
	fi
	if [[ "$lag" =~ ^[0-9]+$ ]]; then
		lag_json="$lag"
	else
		lag_json="null"
	fi
	detail="$(python3 - "$reason" "$lag_json" "$SRC_COMMIT" "$DEPLOYED_PATH" "$embedded_v" "$db_v" "$nrestarts" <<'PY'
import json, sys
detail = {
    "reason": sys.argv[1],
    "lag_seconds": int(sys.argv[2]) if sys.argv[2] != "null" else None,
    "source_commit": sys.argv[3],
    "deployed_binary": sys.argv[4],
    "embedded_schema_version": int(sys.argv[5]) if sys.argv[5] != "null" else None,
    "db_schema_version": int(sys.argv[6]) if sys.argv[6] != "null" else None,
    "service_restarts": sys.argv[7],
}
print(json.dumps(detail))
PY
)" || {
		echo "NOTE: could not render board event detail — alert NOT recorded" >&2
		return 0
	}
	append_board_event "deploy_stale_alert" "$detail" "reason=$reason"
}

# ── Crash-loop alert (GAP-069 criterion 4) ──────────────────────────────────
# A canopyd that dies and restarts in a loop is an outage even when the
# deployed binary is NOT stale — and every other crash cause (bad config, port
# taken, database down) looks identical from the outside: systemd keeps
# restarting it and nothing watches NRestarts. So EVERY staleness run compares
# the unit's restart counter against the baseline persisted in
# $DEPLOY_STATE_FILE and appends ONE board event when it climbed by at least
# the threshold since the previous run.
#   * No readable counter (no systemd session, unknown unit) -> skip, note.
#   * No/corrupt/unparseable baseline -> record it, never alert.
#   * Counter equal, lower (systemd reset it on restart) or below threshold
#     -> just re-baseline.
# Never changes an exit code; never writes anywhere but the state file and the
# board event log.
check_crashloop() {
	local nrestarts prev delta detail
	nrestarts="$("$SYSTEMCTL_BIN" --user show "$CANOPYD_SERVICE" -p NRestarts --value 2>/dev/null)"
	if [[ ! "$nrestarts" =~ ^[0-9]+$ ]]; then
		echo "NOTE: no restart counter from $SYSTEMCTL_BIN for $CANOPYD_SERVICE — crash-loop baseline NOT updated" >&2
		return 0
	fi
	prev="$(python3 - "$DEPLOY_STATE_FILE" <<'PY'
import json, sys
try:
    with open(sys.argv[1], encoding="utf-8") as f:
        v = json.load(f).get("nrestarts")
    if isinstance(v, int) and v >= 0:
        print(v)
except Exception:
    pass
PY
)"
	# Persist the counter observed NOW, before any early return: the baseline
	# must track reality even when the climb is below threshold. Atomic
	# replace so a concurrent reader never sees a half-written file.
	python3 - "$DEPLOY_STATE_FILE" "$nrestarts" "$CANOPYD_SERVICE" <<'PY' || echo "NOTE: could not write crash-loop state file $DEPLOY_STATE_FILE" >&2
import json, os, sys, time
path, n, svc = sys.argv[1], int(sys.argv[2]), sys.argv[3]
parent = os.path.dirname(path)
if parent:
    os.makedirs(parent, exist_ok=True)
tmp = "%s.tmp.%d" % (path, os.getpid())
with open(tmp, "w", encoding="utf-8") as f:
    json.dump({
        "nrestarts": n,
        "service": svc,
        "updated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }, f)
    f.write("\n")
os.replace(tmp, path)
PY
	if [[ ! "$prev" =~ ^[0-9]+$ ]]; then
		echo "crash-loop baseline: $CANOPYD_SERVICE at $nrestarts restarts (no usable prior state) — no alert"
		return 0
	fi
	delta=$((nrestarts - prev))
	if ((delta < CRASHLOOP_THRESHOLD)); then
		echo "crash-loop: $CANOPYD_SERVICE restarts $prev -> $nrestarts (delta $delta < threshold $CRASHLOOP_THRESHOLD) — no alert"
		return 0
	fi
	detail="$(python3 - "$prev" "$nrestarts" "$delta" "$CANOPYD_SERVICE" "$SRC_COMMIT" "$DEPLOYED_PATH" "$CRASHLOOP_THRESHOLD" <<'PY'
import json, sys
print(json.dumps({
    "restart_before": int(sys.argv[1]),
    "restart_after": int(sys.argv[2]),
    "delta": int(sys.argv[3]),
    "service": sys.argv[4],
    "source_commit": sys.argv[5],
    "deployed_binary": sys.argv[6],
    "threshold": int(sys.argv[7]),
}))
PY
)" || {
		echo "NOTE: could not render crash-loop alert detail — alert NOT recorded" >&2
		return 0
	}
	append_board_event "deploy_crashloop_alert" "$detail" "delta=$delta"
	echo "CRASHLOOP: $CANOPYD_SERVICE restarts climbed $prev -> $nrestarts (delta $delta >= threshold $CRASHLOOP_THRESHOLD)"
}

# ── --check-schema: embedded schema vs live DB schema (read-only) ───────────
if [[ "$MODE" == "check-schema" ]]; then
	[[ -x "$SCHEMA_PROBE_BIN" ]] || fail_error "check-schema: canopyd binary not found/executable: $SCHEMA_PROBE_BIN (build it first: make build)"
	command -v "$SCHEMA_PSQL" >/dev/null 2>&1 || fail_error "check-schema: psql not available"

	EMBEDDED_V="$("$SCHEMA_PROBE_BIN" -print-schema-version)" \
		|| fail_error "check-schema: could not read embedded schema version from $SCHEMA_PROBE_BIN"
	[[ "$EMBEDDED_V" =~ ^[0-9]+$ ]] || fail_error "check-schema: embedded schema version not numeric: '$EMBEDDED_V'"

	# Read-only: a bare SELECT max(version) against the target DB. The URL is
	# psql's argv connection string — postgres:// URLs carry their own
	# credentials, matching the rest of this repo's tooling. stderr is merged
	# into the capture so a failed query's text lands in the error message.
	DB_V="$("$SCHEMA_PSQL" "$SCHEMA_DB_URL" -t -A -c "SELECT COALESCE((SELECT max(version) FROM schema_migrations), 0)" 2>&1)" \
		|| fail_error "check-schema: schema_migrations query failed against target DB: $DB_V"
	[[ "$DB_V" =~ ^[0-9]+$ ]] || fail_error "check-schema: DB schema version not numeric: '$DB_V'"

	echo "check-schema: binary embeds schema $EMBEDDED_V, DB is at schema $DB_V"
	if ((EMBEDDED_V < DB_V)); then
		echo "STALE BUILD: binary embeds schema $EMBEDDED_V, DB is at $DB_V — rebuild and redeploy"
		exit "$EXIT_STALE"
	fi
	echo "SCHEMA_OK: embedded schema >= DB schema — binary understands this database"
	exit "$EXIT_CURRENT"
fi

# ── Repo sanity ──────────────────────────────────────────────────────────────
[[ -d "$REPO_ROOT/.git" || -f "$REPO_ROOT/.git" ]] || fail_error "not a git repo: $REPO_ROOT"

if ! git -C "$REPO_ROOT" rev-parse HEAD >/dev/null 2>&1; then
	fail_error "cannot resolve HEAD in $REPO_ROOT"
fi

# ── Newest committed change on the watched paths ────────────────────────────
SRC_COMMIT="$(git -C "$REPO_ROOT" log -1 --format=%H -- "${WATCH_PATHS[@]}")"
[[ -n "$SRC_COMMIT" ]] || fail_error "no commits touch watched paths: ${WATCH_PATHS[*]}"

# %ct = committer date, epoch seconds.
SRC_TS="$(git -C "$REPO_ROOT" log -1 --format=%ct -- "${WATCH_PATHS[@]}")"
[[ "$SRC_TS" =~ ^[0-9]+$ ]] || fail_error "cannot parse commit timestamp for $SRC_COMMIT"
SRC_HUMAN="$(date -u -d "@$SRC_TS" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -r "$SRC_TS" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || echo "epoch=$SRC_TS")"

# ── Crash-loop check (GAP-069 criterion 4) ──────────────────────────────────
# Runs on EVERY staleness run, before and independently of the staleness
# verdict, so it covers all three exit paths below — deliberately including
# the CURRENT one: a crash-looping service is an outage even when the deployed
# binary is up to date. It reports on stdout and may append a board event; it
# never changes this script's exit code. --check-schema returns earlier and is
# left untouched (it is a pre-deploy gate, not a health watch).
check_crashloop

# ── Deployed artifact ────────────────────────────────────────────────────────
LAG="" # seconds behind source; "" when the artifact is missing (set -u safe)
if [[ ! -f "$DEPLOYED_PATH" ]]; then
	echo "STALE: deployed binary missing: $DEPLOYED_PATH"
	echo "source: commit $SRC_COMMIT ($SRC_HUMAN)"
	if [[ "$MODE" == "deploy" ]]; then
		# A missing binary is also a reason to deploy; fall through to the
		# deploy gate below with staleness flag set.
		NEEDS_DEPLOY=1
	else
		append_stale_event "STALE" ""
		exit "$EXIT_STALE"
	fi
else
	DEP_TS="$(stat -c %Y "$DEPLOYED_PATH" 2>/dev/null)" \
		|| fail_error "cannot stat deployed binary: $DEPLOYED_PATH"
	LAG=$((SRC_TS - DEP_TS))
	DEP_HUMAN="$(date -u -d "@$DEP_TS" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -r "$DEP_TS" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || echo "epoch=$DEP_TS")"

	echo "deployed: $DEPLOYED_PATH ($DEP_HUMAN)"
	echo "source:   $SRC_COMMIT ($SRC_HUMAN)"
	echo "watched:  ${WATCH_PATHS[*]}"
	echo "lag:      ${LAG}s (threshold ${THRESHOLD_S}s)"

	if ((LAG <= THRESHOLD_S)); then
		echo "CURRENT: deployed canopyd is up to date"
		exit "$EXIT_CURRENT"
	fi
	echo "STALE: deployed canopyd is ${LAG}s behind source"
	NEEDS_DEPLOY=1
fi

# ── Not deploying → plain STALE result ──────────────────────────────────────
if [[ "$MODE" != "deploy" ]]; then
	append_stale_event "STALE" "$LAG"
	exit "$EXIT_STALE"
fi

# ── Safety gate: never auto-deploy a tracked-dirty worktree ─────────────────
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain 2>/dev/null)" ]]; then
	echo "STALE_BLOCKED: worktree has tracked/untracked changes; refusing to auto-deploy" >&2
	echo "STALE_BLOCKED: commit or stash, then re-run $0 --deploy" >&2
	echo "STALE_BLOCKED: deploy command NOT invoked" >&2
	append_stale_event "STALE_BLOCKED" "$LAG"
	exit "$EXIT_STALE_BLOCKED"
fi

# ── Deploy via the existing atomic path ─────────────────────────────────────
echo "stale → deploying via: $DEPLOY_CMD (cwd: $DEPLOY_DIR)"
if ! (cd "$DEPLOY_DIR" && $DEPLOY_CMD); then
	fail_error "deploy command failed: $DEPLOY_CMD"
fi
echo "deploy command finished; re-checking staleness"

# Re-run this script WITHOUT --deploy: a stale result (exit 1) means the
# deploy did not actually refresh the artifact, so the automation must fail.
"$0"
rc=$?
if ((rc != 0)); then
	fail_error "deployed artifact still stale after deploy (re-check rc=$rc)"
fi
exit "$EXIT_CURRENT"
