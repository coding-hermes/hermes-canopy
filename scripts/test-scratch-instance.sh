#!/usr/bin/env bash
#
# test-scratch-instance.sh (DF-HERMES-CANOPY-8): regression coverage for
# scripts/scratch-instance.sh and docs/SCRATCH_INSTANCE.md.
#
# Hermetic by default: the source-invariant checks and the argument/refusal
# guards never contact PostgreSQL, the live canopyd, or Docker. The refusal
# cases additionally point the script at a CLOSED PostgreSQL port, so a guard
# that is wrong-ordered (one that only fires after touching the database) fails
# this test instead of quietly reaching a live server.
#
# Covered:
#   1-16. source invariants: distinct API port, DB-name shape + tick-unique name,
#         CREATED_DB gate on DROP DATABASE, HOME + CANOPY_FILE_ROOT isolation,
#         `env -i` server environment with no CANOPY_DB_URL / gateway key, gateway
#         pointed at a closed loopback port, trap cleanup + managed PID, port
#         preflight ordered BEFORE the server start, schema guard, live-baseline
#         comparison, no secret ever printed, no compose/service mutation.
#  17-16. docs invariants: the scratch recipe documents explicit port/database/
#         HOME/CLI target, the four isolation requirements, cleanup, the current
#         PostgreSQL requirement vs the PENDING SQLite pivot, and never presents
#         :8091/:8092 as a scratch port.
#  21-26. runtime refusals: --port 8091 / 8092 / 8080, a non-numeric port, an
#         out-of-range port, the live database name, and an unknown argument —
#         each must exit non-zero with its own message and leak no temp root.
#
# CANOPY_SCRATCH_LIVE_TESTS=1 additionally runs the destructive-safety case
# against a real PostgreSQL server (a sentinel database this test creates and
# removes itself), and CANOPY_SCRATCH_E2E=1 runs the recipe end to end.
#
# Exit 0 only when every scenario passes.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC="$HERE/scratch-instance.sh"
DOC="$HERE/../docs/SCRATCH_INSTANCE.md"
PG_PORT="${CANOPY_SCRATCH_PG_PORT:-5437}"
PG_USER="${CANOPY_SCRATCH_PG_USER:-canopy}"
PG_PASSWORD="${CANOPY_SCRATCH_PG_PASSWORD:-canopy}"
LIVE_TESTS="${CANOPY_SCRATCH_LIVE_TESTS:-0}"

pass=0
fail=0

ok() {
	pass=$((pass + 1))
	echo "PASS: $1"
}
bad() {
	fail=$((fail + 1))
	echo "FAIL: $1"
	[ $# -gt 1 ] && printf '%s\n' "$2" | sed 's/^/    /'
}

# has <label> <fixed-string> <file>
has() {
	if grep -qF -- "$2" "$3"; then ok "$1"; else bad "$1" "missing: $2"; fi
}
# hasnt <label> <regex> <file>  (regex, because the negative assertions are shape-based)
hasnt() {
	if grep -qE -- "$2" "$3"; then bad "$1" "unexpected match for: $2"; else ok "$1"; fi
}
# before <label> <earlier-string> <later-string> <file>
before() {
	local a b
	a="$(grep -nF -- "$2" "$4" | head -1 | cut -d: -f1)"
	b="$(grep -nF -- "$3" "$4" | head -1 | cut -d: -f1)"
	if [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ]; then
		ok "$1"
	else
		bad "$1" "expected '$2' (line ${a:-none}) before '$3' (line ${b:-none})"
	fi
}
# before_re <label> <earlier-ERE> <later-fixed-string> <file>: the earlier pattern
# is anchored, so a duplicate string deeper in the file (e.g. a second
# `if port_busy; then` in the post-cleanup verification) cannot satisfy the order
# check after the real call site was disabled.
before_re() {
	local a b
	a="$(grep -nE -- "$2" "$4" | head -1 | cut -d: -f1)"
	b="$(grep -nF -- "$3" "$4" | head -1 | cut -d: -f1)"
	if [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ]; then
		ok "$1"
	else
		bad "$1" "expected /$2/ (line ${a:-none}) before '$3' (line ${b:-none})"
	fi
}
tmp_roots() {
	find /tmp -maxdepth 1 -name 'canopy-scratch-473-*' 2>/dev/null | wc -l | tr -d ' '
}

echo "== source invariants: $SRC"
has "default scratch API port is its own value" 'PORT="${CANOPY_SCRATCH_PORT:-8093}"' "$SRC"
hasnt "no documented live/default port is used as the scratch default" 'CANOPY_SCRATCH_PORT:-80(80|91|92)' "$SRC"
has "refuses the documented live/default ports" '8091 | 8092 | 8080)' "$SRC"
has "database name is tick-unique + random" 'DB_NAME="canopy_scratch_${TICK}_${RAND}"' "$SRC"
has "refuses the live database name" 'canopy | postgres | template0 | template1)' "$SRC"
has "requires the canopy_scratch_ prefix" 'database name must start with canopy_scratch_' "$SRC"
has "server environment is built with env -i" 'env -i \' "$SRC"
hasnt "the scratch server is never handed CANOPY_DB_URL" 'CANOPY_DB_URL=' "$SRC"
hasnt "no gateway credential is passed to the scratch server" 'API_SERVER_KEY=|HERMES_WEBUI_GATEWAY_API_KEY=' "$SRC"
has "gateway endpoint is the closed loopback discard port" 'GATEWAY_URL="${CANOPY_SCRATCH_GATEWAY_URL:-http://127.0.0.1:9}"' "$SRC"
has "scratch HOME isolates the card store + gateway registry" 'HOME="$SCRATCH_HOME"' "$SRC"
has "file root is overridden per instance" 'CANOPY_FILE_ROOT="$SCRATCH_FILES"' "$SRC"
has "server is a managed child with its PID captured" 'SERVER_PID=$!' "$SRC"
has "cleanup is trapped on exit/interrupt/terminate" "trap 'cleanup' EXIT INT TERM" "$SRC"
has "cleanup terminates the scratch server" 'kill -TERM "$SERVER_PID"' "$SRC"
has "DROP DATABASE is gated on a database this run created" '[ "$CREATED_DB" != 1 ]' "$SRC"
hasnt "no blanket database drop" 'DROP DATABASE IF EXISTS (canopy|postgres|template)' "$SRC"
has "port preflight exists" 'port_busy() {' "$SRC"
before_re "port preflight runs before the server start" '^if port_busy; then' 'serve >"$SCRATCH_LOG" 2>&1 &' "$SRC"
before_re "readiness gate runs before the CLI checks" '^\[ "\$READY" = 1 \] [|][|] die' 'tree create "$TITLE"' "$SRC"
has "health is compared against embedded migrations" 'embedded_migrations' "$SRC"
has "live baseline is captured (pid + health + db list)" 'LIVE_PID_BEFORE="$(live_pid)"' "$SRC"
has "live instance is detected by process name, not command line" 'pgrep -x canopyd' "$SRC"
has "live service is verified untouched afterwards" 'live canopyd pid unchanged' "$SRC"
hasnt "no secret is ever printed" '(echo|printf|say)[^\n]*\$TOKEN' "$SRC"
hasnt "no database password is ever printed" '(echo|printf|say)[^\n]*\$PG_PASSWORD' "$SRC"
hasnt "never mutates compose or system services" 'docker (compose (up|down|stop|restart|rm)|(stop|restart|rm|kill) )|systemctl' "$SRC"
has "required tools are preflighted" 'for tool in go psql pg_isready curl python3 ss; do' "$SRC"

echo
echo "== docs invariants: $DOC"
if [ -f "$DOC" ]; then
	has "recipe documents its own API port space" '8093' "$DOC"
	has "recipe documents a uniquely named database" 'canopy_scratch_' "$DOC"
	has "recipe documents HOME isolation" 'HOME' "$DOC"
	has "recipe documents the CLI target" 'CANOPY_SERVER_URL' "$DOC"
	has "recipe documents cleanup" 'DROP DATABASE' "$DOC"
	has "recipe states the current PostgreSQL requirement" 'PostgreSQL' "$DOC"
	has "recipe marks the SQLite pivot as pending, not shipped" 'pending' "$DOC"
	has "recipe names the pending SQLite pivot task" 'GAP-076' "$DOC"
	has "recipe documents the compose API port" '8092' "$DOC"
	has "recipe says compose -p is not isolation" 'is not isolation' "$DOC"
	has "recipe explains why compose -p does not isolate (fixed names)" 'hard-coded' "$DOC"
	has "recipe explains the fixed compose volume" 'pgdata' "$DOC"
	has "recipe documents the stale-build rebuild path" 'STALE BUILD' "$DOC"
	has "recipe points at the executable recipe" 'scripts/scratch-instance.sh' "$DOC"
	# The ports table's scratch row must not advertise a live/compose port in its
	# API column (the last column may legitimately warn about 8091/8092/8080).
	scratch_row="$(awk -F'|' '/Scratch \(this page\)/ {print; exit}' "$DOC")"
	if [ -z "$scratch_row" ]; then
		bad "ports table has a scratch row"
	else
		ok "ports table has a scratch row"
		scratch_api="$(printf '%s' "$scratch_row" | awk -F'|' '{print $3}')"
		case "$scratch_api" in
		*8091* | *8092* | *8080*) bad "the scratch row's API column avoids live/compose ports" "api column:$scratch_api" ;;
		*) ok "the scratch row's API column avoids live/compose ports" ;;
		esac
		case "$scratch_api" in
		*:80[0-9][0-9]*) ok "the scratch row names an explicit API port" ;;
		*) bad "the scratch row names an explicit API port" "api column:$scratch_api" ;;
		esac
	fi
else
	bad "docs/SCRATCH_INSTANCE.md exists"
fi

# The compose sections must describe the API on :8092 and must tell the reader to
# rebuild the image (acceptance C). Extracted per file so a native :8091 example
# elsewhere in the page cannot mask a wrong compose claim.
check_compose_section() {
	local label="$1" file="$2" from="$3" to="$4" bullet="$5"
	local section
	if [ ! -f "$file" ]; then
		bad "$label (file exists)"
		return
	fi
	section="$(awk -v a="$from" -v b="$to" 'index($0,a)==1{f=1} f{print} index($0,b)==1{f=0}' "$file")"
	if [ -z "$section" ]; then
		bad "$label (section found)"
		return
	fi
	ok "$label (section found)"
	case "$section" in
	*8092*) ok "$label: compose API is :8092" ;;
	*) bad "$label: compose API is :8092" ;;
	esac
	case "$section" in
	*"curl http://localhost:8091/health"*) bad "$label: no :8091 health probe in the compose section" ;;
	*) ok "$label: no :8091 health probe in the compose section" ;;
	esac
	case "$section" in
	*"docker compose build"*) ok "$label: compose section requires an image rebuild" ;;
	*) bad "$label: compose section requires an image rebuild" ;;
	esac
	case "$section" in
	*"STALE BUILD"*) ok "$label: compose section names STALE BUILD" ;;
	*) bad "$label: compose section names STALE BUILD" ;;
	esac
	# The "this starts" bullet is the exact line that used to name :8091.
	case "$section" in
	*"$bullet"*) ok "$label: the started-service bullet names the compose port ($bullet)" ;;
	*) bad "$label: the started-service bullet names the compose port" "expected: $bullet" ;;
	esac
}
check_compose_section "README.md compose section" "$HERE/../README.md" '### Docker (Recommended)' '### Production (Manual)' 'canopyd on :8092 (host)'
check_compose_section "SELF_HOST.md compose section" "$HERE/../docs/SELF_HOST.md" '### Option 2: Docker Compose' '### Option 3: Build from Source' 'host port `8092`'

echo
echo "== runtime refusals (no PostgreSQL, no live service contact)"
# A closed PG port proves guard ORDER: a refusal that needs the database first
# would report the connection failure instead of its own message.
closed_pg="1"
run_refusal() {
	local label="$1" expect="$2"
	shift 2
	local out rc
	out="$(env CANOPY_SCRATCH_PG_PORT="$closed_pg" bash "$SRC" "$@" 2>&1)"
	rc=$?
	if [ "$rc" -eq 0 ]; then
		bad "$label" "expected a refusal, got exit 0"
		return
	fi
	if printf '%s' "$out" | grep -qF -- "$expect"; then
		ok "$label"
	else
		bad "$label" "expected message '$expect'; got:
$out"
	fi
}

roots_before="$(tmp_roots)"
run_refusal "refuses --port 8091" "port 8091 is a documented live/default port" --port 8091
run_refusal "refuses --port 8092" "port 8092 is a documented live/default port" --port 8092
run_refusal "refuses --port 8080" "port 8080 is a documented live/default port" --port 8080
run_refusal "refuses a non-numeric port" "port must be a number" --port abc
run_refusal "refuses an out-of-range port" "outside the usable range" --port 80
if [ "$(tmp_roots)" = "$roots_before" ]; then
	ok "refusals leak no scratch temp root"
else
	bad "refusals leak no scratch temp root"
fi

out="$(env CANOPY_SCRATCH_PG_PORT="$closed_pg" CANOPY_SCRATCH_DB=canopy bash "$SRC" --port 8099 2>&1)"
rc=$?
if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -qF -- "refusing to use the database name 'canopy'"; then
	ok "refuses the live database name before any database contact"
else
	bad "refuses the live database name before any database contact" "rc=$rc out=$out"
fi

out="$(bash "$SRC" --nonsense 2>&1)"
rc=$?
if [ "$rc" -eq 2 ]; then ok "unknown argument exits 2"; else bad "unknown argument exits 2" "rc=$rc out=$out"; fi

if [ "$LIVE_TESTS" = 1 ]; then
	echo
	echo "== live tests (CANOPY_SCRATCH_LIVE_TESTS=1): PostgreSQL $PG_PORT"
	psql_live() {
		PGPASSWORD="$PG_PASSWORD" psql -w -h 127.0.0.1 -p "$PG_PORT" -U "$PG_USER" -v ON_ERROR_STOP=1 "$@"
	}
	sentinel="canopy_scratch_test_sentinel_$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')"
	if psql_live -d postgres -c "CREATE DATABASE ${sentinel}" >/dev/null 2>&1; then
		out="$(env CANOPY_SCRATCH_DB="$sentinel" bash "$SRC" --port 8099 2>&1)"
		rc=$?
		if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -qF "already exists"; then
			ok "a pre-existing database name is refused"
		else
			bad "a pre-existing database name is refused" "rc=$rc out=$out"
		fi
		if psql_live -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='${sentinel}'" | grep -q 1; then
			ok "the pre-existing database was NOT dropped"
		else
			bad "the pre-existing database was NOT dropped"
		fi
		psql_live -d postgres -c "DROP DATABASE IF EXISTS ${sentinel} WITH (FORCE)" >/dev/null 2>&1
		if psql_live -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='${sentinel}'" | grep -q 1; then
			bad "sentinel cleanup"
		else
			ok "sentinel cleanup"
		fi
	else
		bad "sentinel database could be created (PostgreSQL $PG_PORT reachable?)"
	fi
fi

if [ "${CANOPY_SCRATCH_E2E:-0}" = 1 ]; then
	echo
	echo "== end-to-end recipe run (CANOPY_SCRATCH_E2E=1)"
	out="$(bash "$SRC" --port "${CANOPY_SCRATCH_E2E_PORT:-8093}" 2>&1)"
	rc=$?
	if [ "$rc" -eq 0 ]; then
		ok "recipe exits 0"
	else
		bad "recipe exits 0" "rc=$rc
$out"
	fi
	for expect in "OK: database canopy_scratch_" "OK: scratch root removed" "OK: port " "OK: live canopyd pid unchanged"; do
		if printf '%s' "$out" | grep -qF -- "$expect"; then
			ok "recipe proves: $expect"
		else
			bad "recipe proves: $expect" "$out"
		fi
	done
fi

echo
echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
