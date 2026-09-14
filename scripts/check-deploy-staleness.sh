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
BOARD_EVENTS="$REPO_ROOT/.coding-hermes/board/events.jsonl"

# ── Board alert (GAP-069 / AC2): STALE must be VISIBLE ──────────────────────
# The 2026-09-12 crash-loop (~37h, ~27k restarts) was invisible because no
# surface anyone watches reported it. The foreman tick scans events.jsonl
# every run, so every STALE / STALE_BLOCKED detection appends ONE
# machine-readable event line there (never rewrites the file, never touches
# tasks.jsonl, never changes this script's exit code). Best-effort: a missing
# or unreadable board file degrades to a NOTE on stderr.
append_stale_event() {
	local reason="$1" # STALE | STALE_BLOCKED
	local lag="$2"   # seconds behind source ("" for missing artifact)
	[[ -f "$BOARD_EVENTS" ]] || {
		echo "NOTE: board events file missing ($BOARD_EVENTS) — stale alert NOT recorded" >&2
		return 0
	}
	local next_id ts nrestarts embedded_v="null" db_v="null" probe_bin detail_src
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
		echo "NOTE: could not derive next board event id — stale alert NOT recorded" >&2
		return 0
	}
	ts="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
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
		local w
		w="$("$SCHEMA_PSQL" "$SCHEMA_DB_URL" -t -A -c "SELECT COALESCE((SELECT max(version) FROM schema_migrations), 0)" 2>/dev/null)"
		[[ "$w" =~ ^[0-9]+$ ]] && db_v="$w"
	fi
	local lag_json
	if [[ "$lag" =~ ^[0-9]+$ ]]; then
		lag_json="$lag"
	else
		lag_json="null"
	fi
	detail_src="$(python3 - "$next_id" "$ts" "$reason" "$lag_json" "$SRC_COMMIT" "$DEPLOYED_PATH" "$embedded_v" "$db_v" "$nrestarts" <<'PY'
import json, sys
detail = {
    "reason": sys.argv[3],
    "lag_seconds": int(sys.argv[4]) if sys.argv[4] != "null" else None,
    "source_commit": sys.argv[5],
    "deployed_binary": sys.argv[6],
    "embedded_schema_version": int(sys.argv[7]) if sys.argv[7] != "null" else None,
    "db_schema_version": int(sys.argv[8]) if sys.argv[8] != "null" else None,
    "service_restarts": sys.argv[9],
}
print(json.dumps({
    "id": int(sys.argv[1]),
    "timestamp": sys.argv[2],
    "event_type": "deploy_stale_alert",
    "task_id": "GAP-069",
    "actor": "deploy-staleness-check",
    "detail": json.dumps(detail),
}))
PY
)" || {
		echo "NOTE: could not render board event JSON — stale alert NOT recorded" >&2
		return 0
	}
	printf '%s\n' "$detail_src" >>"$BOARD_EVENTS"
	echo "board alert appended: $BOARD_EVENTS (event_type=deploy_stale_alert reason=$reason)"
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
