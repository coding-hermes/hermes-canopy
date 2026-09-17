#!/usr/bin/env bash
#
# scratch-instance.sh (DF-HERMES-CANOPY-8)
#
# Start a FULLY ISOLATED, throwaway canopyd instance from this checkout and prove
# that everything it touches is its own: a uniquely named PostgreSQL database, an
# explicit loopback API port, its own HOME (card store + gateway run registry) and
# its own file-viewer root. Nothing here reads or writes the primary instance.
#
# Why this exists: the published defaults collide. The README quick start serves
# the API on :8091 and PostgreSQL on :5437; docker-compose.yml publishes canopyd
# on :8092 with FIXED container names (canopy-server / canopy-pg), so
# `docker compose -p <name>` alone does NOT isolate a second stack. Running a
# "scratch" server with the documented values therefore lands next to the live
# service — and ~/.hermes/canopy/cards, ~/.hermes/canopy/gateway/runs.jsonl and
# ~/.canopy/files are shared by every instance that runs as the same user.
#
# What isolation requires (all four, verified below):
#   1. its own PostgreSQL DATABASE   (unique name; never the live `canopy`)
#   2. its own HTTP port             (loopback only; never 8091 / 8092 / 8080)
#   3. its own HOME                  (card SQLite DBs + gateway run registry)
#   4. CANOPY_FILE_ROOT override     (uploaded file bytes)
# …plus a CLEAN process environment: `env -i` drops any inherited CANOPY_DB_URL
# and the Hermes gateway key/endpoint, and the gateway base URL is pointed at a
# closed loopback port so a scratch run can never submit a live agent run.
#
# The CLI is an HTTP CLIENT: CANOPY_SERVER_URL is the only thing that selects its
# destination. This script proves that an explicit URL wins even while DB_* and
# HTTP_ADDR are set, and that the CLI refuses (before any request) without a URL.
#
# Usage:
#   scripts/scratch-instance.sh                 # full run, then clean up
#   scripts/scratch-instance.sh --keep          # stop the server, keep DB + root
#   scripts/scratch-instance.sh --port 8099
#   scripts/scratch-instance.sh --help
#
# Overrides (env): CANOPY_SCRATCH_PORT, CANOPY_SCRATCH_PG_HOST,
#   CANOPY_SCRATCH_PG_PORT, CANOPY_SCRATCH_PG_USER, CANOPY_SCRATCH_PG_PASSWORD,
#   CANOPY_SCRATCH_DB, CANOPY_SCRATCH_KEEP=1, CANOPY_SCRATCH_TICK
#
# Never prints a token or a password. Never restarts, stops or reconfigures any
# existing service, container or database.

set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

TICK="${CANOPY_SCRATCH_TICK:-473}"
PORT="${CANOPY_SCRATCH_PORT:-8093}"
PG_HOST="${CANOPY_SCRATCH_PG_HOST:-127.0.0.1}"
PG_PORT="${CANOPY_SCRATCH_PG_PORT:-5437}"
PG_USER="${CANOPY_SCRATCH_PG_USER:-canopy}"
PG_PASSWORD="${CANOPY_SCRATCH_PG_PASSWORD:-canopy}"
KEEP="${CANOPY_SCRATCH_KEEP:-0}"
# Closed loopback port: nothing listens on :9 (discard), so the scratch server
# cannot reach any Hermes gateway even if a key leaks into its environment.
GATEWAY_URL="${CANOPY_SCRATCH_GATEWAY_URL:-http://127.0.0.1:9}"

# Live instance the scratch run must never touch (read-only probes only).
LIVE_URL="http://127.0.0.1:8091"

while [ $# -gt 0 ]; do
	case "$1" in
	--keep) KEEP=1 ;;
	--port)
		shift
		PORT="${1:-}"
		;;
	-h | --help)
		sed -n '2,45p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "unknown argument: $1 (try --help)" >&2
		exit 2
		;;
	esac
	shift
done

# --- output ---------------------------------------------------------------
say() { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
die() {
	printf 'FATAL: %s\n' "$*" >&2
	exit 1
}

# --- safety guards --------------------------------------------------------
case "$PORT" in
8091 | 8092 | 8080)
	die "port $PORT is a documented live/default port (native :8091, compose :8092, raw binary :8080) — pick a scratch port, e.g. --port 8099"
	;;
esac
case "$PORT" in
'' | *[!0-9]*) die "port must be a number, got '$PORT'" ;;
esac
if [ "$((PORT))" -lt 1024 ] || [ "$((PORT))" -gt 65535 ]; then
	die "port $PORT is outside the usable range (1024-65535)"
fi

DB_NAME="${CANOPY_SCRATCH_DB:-}"
if [ -z "$DB_NAME" ]; then
	# Tick-unique + random so two concurrent scratch runs never collide and a
	# stale name is never silently reused.
	RAND="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
	DB_NAME="canopy_scratch_${TICK}_${RAND}"
fi
case "$DB_NAME" in
canopy | postgres | template0 | template1) die "refusing to use the database name '$DB_NAME'" ;;
canopy_scratch_*) : ;;
*) die "database name must start with canopy_scratch_ (got '$DB_NAME')" ;;
esac
case "$DB_NAME" in
*[!a-z0-9_]*) die "database name must be [a-z0-9_] only (got '$DB_NAME')" ;;
esac

SCRATCH_ROOT="$(mktemp -d "/tmp/canopy-scratch-${TICK}-XXXXXX")" || die "mktemp failed"
SCRATCH_HOME="$SCRATCH_ROOT/home"
SCRATCH_FILES="$SCRATCH_ROOT/file-root"
SCRATCH_BIN="$SCRATCH_ROOT/bin/canopyd"
SCRATCH_LOG="$SCRATCH_ROOT/canopyd.log"
mkdir -p "$SCRATCH_HOME" "$SCRATCH_FILES" "$(dirname "$SCRATCH_BIN")"

BASE="http://127.0.0.1:$PORT"
TITLE="scratch-${TICK}-$(date +%H%M%S)"
SERVER_PID=""
STARTED_SERVER=0
# 1 only after THIS run successfully created the database. Every destructive step
# (DROP DATABASE) is gated on it, so a pre-existing database that happens to
# match the requested name is refused, never dropped.
CREATED_DB=0
# 1 only after the preflight recorded the live baseline (pid/health/db list), so
# post-cleanup verification never compares against unset values.
PREFLIGHT_DONE=0

# --- helpers --------------------------------------------------------------
# psql wrapper: the password travels in the environment, never on the command
# line, and is never echoed. -w keeps psql from prompting for one.
psql_do() {
	PGPASSWORD="$PG_PASSWORD" psql -w -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -v ON_ERROR_STOP=1 "$@"
}

port_busy() {
	ss -tln 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${PORT}\$"
}

# The live canopyd, never the scratch one this script started. -x matches the
# process NAME exactly, so a shell whose command line merely mentions
# "canopyd serve" can never be mistaken for the server.
live_pid() {
	pgrep -x canopyd 2>/dev/null | grep -v "^${SERVER_PID}$" | head -1
}

live_health() {
	curl -s --max-time 5 "$LIVE_URL/health" 2>/dev/null || true
}

db_names() {
	psql_do -d postgres -tAc "SELECT datname FROM pg_database ORDER BY 1" 2>/dev/null
}

# Listing of the shared, HOME-derived stores the scratch run must leave alone.
shared_store_snapshot() {
	{
		printf 'cards:\n'
		find "$HOME/.hermes/canopy/cards" -maxdepth 1 -type f -printf '%f %s %T@\n' 2>/dev/null | sort
		printf 'file-root:\n'
		find "$HOME/.canopy/files" -maxdepth 2 -type f -printf '%P %s %T@\n' 2>/dev/null | sort
	} 2>/dev/null
}

stop_server() {
	[ "$STARTED_SERVER" = 1 ] || return 0
	if kill -0 "$SERVER_PID" 2>/dev/null; then
		kill -TERM "$SERVER_PID" 2>/dev/null || true
		for _ in $(seq 1 40); do
			kill -0 "$SERVER_PID" 2>/dev/null || break
			sleep 0.5
		done
		if kill -0 "$SERVER_PID" 2>/dev/null; then
			kill -KILL "$SERVER_PID" 2>/dev/null || true
			sleep 1
		fi
	fi
	kill -0 "$SERVER_PID" 2>/dev/null && return 1
	return 0
}

drop_db() {
	# Destructive step: only ever runs for a database THIS script created (the
	# guards above reject the live name and anything that is not
	# canopy_scratch_*, and CREATED_DB gates the case where the name collided
	# with a pre-existing database — that one is refused, never dropped).
	if [ "$CREATED_DB" != 1 ]; then
		say "skip: this run created no database (nothing to drop)"
		return 0
	fi
	psql_do -d postgres -c "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE)" >/dev/null 2>&1
}

# Post-cleanup verification: every claim in this script's summary is re-checked
# against the live system AFTER the scratch resources were released.
verify_after() {
	step "post-cleanup verification"
	local rc=0 health_now pid_now
	if [ "$PREFLIGHT_DONE" != 1 ]; then
		say "SKIP: the run stopped in preflight, before any live baseline existed — nothing to verify"
		return 0
	fi
	if [ "$CREATED_DB" = 1 ]; then
		if psql_do -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='${DB_NAME}'" | grep -q 1; then
			say "FAIL: database ${DB_NAME} still present"
			rc=1
		else
			say "OK: database ${DB_NAME} dropped"
		fi
	else
		say "OK: this run created no database — nothing was dropped"
	fi
	if [ -e "$SCRATCH_ROOT" ]; then
		say "FAIL: scratch root $SCRATCH_ROOT still present"
		rc=1
	else
		say "OK: scratch root removed"
	fi
	if port_busy; then
		say "FAIL: port $PORT still listening"
		rc=1
	else
		say "OK: port $PORT released"
	fi
	health_now="$(live_health)"
	pid_now="$(live_pid)"
	if [ "$health_now" = "$LIVE_HEALTH_BEFORE" ]; then
		say "OK: live /health unchanged ($health_now)"
	else
		say "FAIL: live /health changed: '$LIVE_HEALTH_BEFORE' → '$health_now'"
		rc=1
	fi
	if [ "$pid_now" = "$LIVE_PID_BEFORE" ]; then
		say "OK: live canopyd pid unchanged (${LIVE_PID_BEFORE:-none}) — never restarted"
	else
		say "FAIL: live canopyd pid changed: '$LIVE_PID_BEFORE' → '$pid_now'"
		rc=1
	fi
	if [ "$(db_names)" = "$LIVE_DBS_BEFORE" ]; then
		say "OK: PostgreSQL database list unchanged (no live DB created/dropped/modified)"
	else
		say "FAIL: PostgreSQL database list changed"
		rc=1
	fi
	if [ "$(shared_store_snapshot)" = "$SHARED_BEFORE" ]; then
		say "OK: shared \$HOME stores unchanged (cards/, files/)"
	else
		say "WARN: shared stores under \$HOME changed during the run — check whether the live instance wrote them"
	fi
	return "$rc"
}

cleanup() {
	local rc=$?
	trap - EXIT INT TERM
	if ! stop_server; then
		say "WARN: scratch server pid $SERVER_PID did not exit; NOT dropping ${DB_NAME} (inspect it first)"
		rc=1
	elif [ "$KEEP" = 1 ]; then
		say "KEEP: scratch server stopped; database ${DB_NAME} and root ${SCRATCH_ROOT} retained"
		say "      remove with: psql -h $PG_HOST -p $PG_PORT -U $PG_USER -d postgres -c 'DROP DATABASE ${DB_NAME};'"
		say "                   rm -rf ${SCRATCH_ROOT}"
	else
		step "cleanup"
		drop_db
		rm -rf "$SCRATCH_ROOT"
		say "dropped database: ${DB_NAME}"
		say "removed root:     ${SCRATCH_ROOT}"
		verify_after || {
			say "post-cleanup verification FAILED"
			[ "$rc" = 0 ] && rc=1
		}
	fi
	exit "$rc"
}
trap 'cleanup' EXIT INT TERM

say "scratch instance (DF-HERMES-CANOPY-8)"
say "  repo:         $REPO"
say "  HEAD:         $(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo unknown)"
say "  database:     $DB_NAME (PostgreSQL $PG_HOST:$PG_PORT)"
say "  api:          $BASE (loopback only)"
say "  scratch root: $SCRATCH_ROOT"
say "  HOME:         $SCRATCH_HOME  (card store + gateway registry)"
say "  file root:    $SCRATCH_FILES"
say "  gateway:      $GATEWAY_URL (closed port — scratch never submits a live run)"

# --- 1. preflight ---------------------------------------------------------
step "preflight"
for tool in go psql pg_isready curl python3 ss; do
	command -v "$tool" >/dev/null 2>&1 || die "required tool not found: $tool"
done

if port_busy; then
	die "port $PORT is already in use — pick another with --port (nothing was started)"
fi
say "port $PORT free"

pg_isready -h "$PG_HOST" -p "$PG_PORT" >/dev/null 2>&1 || die "PostgreSQL $PG_HOST:$PG_PORT is not accepting connections"
say "PostgreSQL ready"

if psql_do -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='${DB_NAME}'" | grep -q 1; then
	die "database ${DB_NAME} already exists — nothing was started (re-run for a fresh name)"
fi
say "database name ${DB_NAME} unused"

LIVE_PID_BEFORE="$(live_pid)"
LIVE_HEALTH_BEFORE="$(live_health)"
LIVE_DBS_BEFORE="$(db_names)"
SHARED_BEFORE="$(shared_store_snapshot)"
PREFLIGHT_DONE=1
say "live instance: pid=${LIVE_PID_BEFORE:-none} health=${LIVE_HEALTH_BEFORE:-unreachable}"

# --- 2. build from HEAD ---------------------------------------------------
step "build from HEAD"
if ! (cd "$REPO" && go build -o "$SCRATCH_BIN" ./cmd/canopyd); then
	die "go build failed"
fi
say "built version $("$SCRATCH_BIN" -version 2>/dev/null || echo unknown)"

# --- 3. create the throwaway database ------------------------------------
step "create database ${DB_NAME}"
psql_do -d postgres -c "CREATE DATABASE ${DB_NAME}" >/dev/null || die "CREATE DATABASE ${DB_NAME} failed"
CREATED_DB=1
say "created (the scratch server runs the embedded migrations on first boot)"

# --- 4. start the scratch server with a clean environment ----------------
step "start scratch server"
# env -i: NO inherited CANOPY_DB_URL, no DB_PASSWORD from the caller's shell, no
# API_SERVER_KEY / HERMES_WEBUI_GATEWAY_API_KEY, no HTTP_ADDR from anywhere else.
# JWT_SECRET is deliberately left unset: the default dev secret makes the server
# provision the dev user/workspace/profile on a fresh database, which is what
# lets an authenticated create/list work with no manual SQL.
env -i \
	PATH="$PATH" \
	HOME="$SCRATCH_HOME" \
	HTTP_ADDR="127.0.0.1:${PORT}" \
	DB_HOST="$PG_HOST" DB_PORT="$PG_PORT" DB_USER="$PG_USER" \
	DB_PASSWORD="$PG_PASSWORD" DB_NAME="$DB_NAME" DB_SSLMODE=disable \
	CANOPY_FILE_ROOT="$SCRATCH_FILES" \
	HERMES_WEBUI_GATEWAY_BASE_URL="$GATEWAY_URL" \
	METRICS_ENABLED=false \
	LOG_LEVEL=info LOG_FORMAT=json \
	"$SCRATCH_BIN" serve >"$SCRATCH_LOG" 2>&1 &
SERVER_PID=$!
STARTED_SERVER=1
say "pid $SERVER_PID → log $SCRATCH_LOG"

# --- 5. readiness + schema guard -----------------------------------------
step "readiness"
READY=0
for _ in $(seq 1 60); do
	if ! kill -0 "$SERVER_PID" 2>/dev/null; then
		tail -n 20 "$SCRATCH_LOG" | sed 's/^/    /'
		die "scratch server exited during startup (see $SCRATCH_LOG)"
	fi
	if curl -s --max-time 2 "$BASE/health" >/dev/null 2>&1; then
		READY=1
		break
	fi
	sleep 1
done
[ "$READY" = 1 ] || die "scratch server did not become ready within 60s (see $SCRATCH_LOG)"

HEALTH="$(curl -s --max-time 5 "$BASE/health")"
printf '%s\n' "$HEALTH" | python3 -c '
import json, sys
h = json.load(sys.stdin)
sv, em = h.get("schema_version"), h.get("embedded_migrations")
print("health: status=%s schema_version=%s embedded_migrations=%s" % (h.get("status"), sv, em))
if h.get("status") != "ok":
    sys.exit("health status is not ok")
if sv is None or em is None or sv != em:
    sys.exit("schema_version %r != embedded_migrations %r (STALE BUILD guard)" % (sv, em))
' || die "scratch health/schema check failed"
say "schema matches embedded migrations (no STALE BUILD)"

# LOG_FORMAT=json makes the effective paths machine-checkable (the console writer
# wraps `file_root=` in ANSI colour codes, so a plain grep would miss the value).
grep -qF "\"file_root\":\"$SCRATCH_FILES\"" "$SCRATCH_LOG" || die "scratch server did not resolve CANOPY_FILE_ROOT to $SCRATCH_FILES"
say "scratch server log confirms its own file root: $SCRATCH_FILES"

# --- 6. dev JWT (never printed) ------------------------------------------
TOKEN="$(python3 -c '
import base64, hashlib, hmac, json, time
def b64(b): return base64.urlsafe_b64encode(b).rstrip(b"=")
h = b64(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
now = int(time.time())
p = b64(json.dumps({"sub": "00000000-0000-0000-0000-000000000001",
                    "iat": now, "exp": now + 3600}, separators=(",", ":")).encode())
sig = b64(hmac.new(b"dev-secret-change-me", h + b"." + p, hashlib.sha256).digest())
print((h + b"." + p + b"." + sig).decode())
')"
[ -n "$TOKEN" ] || die "failed to mint the dev JWT"
say "minted dev JWT (not printed)"

# --- 7. CLI: explicit URL wins over DB_*/HTTP_ADDR -----------------------
step "CLI create/list against the scratch API"
# DB_*/HTTP_ADDR are deliberately present and WRONG for the scratch instance:
# an explicit CANOPY_SERVER_URL must still win (README §CLI rule 1).
CREATE_OUT="$(cd "$REPO" && env \
	CANOPY_SERVER_URL="$BASE" CANOPY_TOKEN="$TOKEN" \
	HTTP_ADDR=":59991" DB_HOST="127.0.0.1" DB_PORT="5437" \
	DB_USER="canopy" DB_PASSWORD="canopy" DB_NAME="canopy" \
	"$SCRATCH_BIN" tree create "$TITLE" --content 'scratch isolation check' 2>&1)"
printf '%s\n' "$CREATE_OUT" | sed -n '1,4p' | sed 's/^/    /'
printf '%s' "$CREATE_OUT" | grep -q "Tree created successfully." || die "CLI tree create failed against the scratch API"
TREE_ID="$(printf '%s' "$CREATE_OUT" | awk '/^  ID:/{print $2}')"
ROOT_NODE_ID="$(printf '%s' "$CREATE_OUT" | awk '/^  Root Node:/{print $3}')"
[ -n "$TREE_ID" ] || die "could not parse the created tree id"
say "created tree $TREE_ID (root node $ROOT_NODE_ID)"

LIST_OUT="$(cd "$REPO" && env CANOPY_SERVER_URL="$BASE" CANOPY_TOKEN="$TOKEN" "$SCRATCH_BIN" tree list 2>&1)"
printf '%s' "$LIST_OUT" | grep -q "$TREE_ID" || die "CLI tree list did not show the created tree"
say "CLI tree list shows the created tree"

# negative control: no CANOPY_SERVER_URL + DB_* set → refusal before any request
REFUSAL_OUT="$(cd "$REPO" && env -u CANOPY_SERVER_URL CANOPY_TOKEN="$TOKEN" \
	DB_PORT="5437" DB_NAME="canopy" "$SCRATCH_BIN" tree list 2>&1)"
REFUSAL_RC=$?
[ "$REFUSAL_RC" -ne 0 ] || die "CLI ran without CANOPY_SERVER_URL while DB_* were set (expected a refusal)"
printf '%s' "$REFUSAL_OUT" | grep -q "ambiguous API target" || die "CLI refusal did not name the ambiguous target"
say "negative control: CLI refused without CANOPY_SERVER_URL (exit $REFUSAL_RC, no request sent)"

# --- 8. authenticated API read + live non-contamination ------------------
step "authenticated API + isolation checks"
SCRATCH_TREES="$(curl -s --max-time 5 -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/trees")"
printf '%s' "$SCRATCH_TREES" | grep -q "$TREE_ID" || die "scratch API list did not include the created tree"
SCRATCH_COUNT="$(printf '%s' "$SCRATCH_TREES" | python3 -c 'import json,sys; print(len(json.load(sys.stdin).get("trees") or []))')"
say "scratch API: $SCRATCH_COUNT tree(s), includes $TREE_ID"

LIVE_TREES="$(curl -s --max-time 5 -H "Authorization: Bearer $TOKEN" "$LIVE_URL/api/v1/trees" 2>/dev/null || echo '')"
if printf '%s' "$LIVE_TREES" | grep -q "$TREE_ID"; then
	die "the scratch tree is visible on the LIVE instance — isolation failed"
fi
say "live instance does NOT see the scratch tree (writes stayed in ${DB_NAME})"

UNAUTH_CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$BASE/api/v1/trees")"
[ "$UNAUTH_CODE" = "401" ] || die "scratch API answered $UNAUTH_CODE for an unauthenticated list (expected 401)"
say "scratch API rejects unauthenticated reads (401)"

# --- 9. non-graph stores are fresh and scratch-local ---------------------
step "fresh non-graph stores"
CARD_BODY="$(python3 -c '
import json, sys
print(json.dumps({"treeId": sys.argv[1], "nodeId": sys.argv[2], "appId": "df-hermes-canopy-8",
                  "cardType": "compact", "data": {"title": "scratch card"}}))
' "$TREE_ID" "$ROOT_NODE_ID")"
CARD_CODE="$(curl -s -o "$SCRATCH_ROOT/card.json" -w '%{http_code}' --max-time 5 \
	-X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
	-d "$CARD_BODY" "$BASE/api/v1/cards")"
[ "$CARD_CODE" = "201" ] || die "scratch card create answered $CARD_CODE (expected 201)"
CARD_DIR="$SCRATCH_HOME/.hermes/canopy/cards"
[ -d "$CARD_DIR" ] || die "card store not created under the scratch HOME ($CARD_DIR)"
CARD_DB_COUNT="$(find "$CARD_DIR" -maxdepth 1 -type f -name '*.db' | wc -l | tr -d ' ')"
[ "$CARD_DB_COUNT" -ge 1 ] || die "no card SQLite DB under $CARD_DIR"
say "card create 201; card store is scratch-local: $CARD_DIR ($CARD_DB_COUNT db file(s))"
say "gateway run registry path for this instance: $SCRATCH_HOME/.hermes/canopy/gateway/runs.jsonl"

GATEWAY_CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 \
	-H "Authorization: Bearer $TOKEN" "$BASE/api/v1/gateway/status" || echo 000)"
say "scratch /api/v1/gateway/status → HTTP $GATEWAY_CODE (endpoint is $GATEWAY_URL — no live run submitted)"

step "checks"
say "PASS: build, readiness, schema match, auth, create/list, refusal control, live non-contamination"
say "next: stopping the scratch server and dropping only ${DB_NAME}"

exit 0
