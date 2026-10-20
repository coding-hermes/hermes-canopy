#!/usr/bin/env bash
#
# share-invite-flow.sh (DF-HERMES-CANOPY-89)
#
# Prove, against a throwaway canopyd on a FRESH database, that a tree can be
# shared with a user who has NO email on record — which is the state of every
# account a fresh instance has as soon as someone signs in (dev-mode
# provisioning inserts the users row without an email: PGUserRepo.EnsureByID).
#
# Cells (each one a hard gate; any failure exits non-zero):
#   1. owner creates a tree over HTTP                              → 201
#   2. a second user is provisioned by ONE authenticated API call   → 200
#   3. ground truth: that account's users.email IS NULL
#   4. POST share {user_id}                                          → 201 + tree_members row
#   5. POST share {user_id} again                                    → 409 ALREADY_MEMBER
#   6. a member (not viewer) may PATCH /api/v1/nodes/{id}            → 200
#   7. POST share {email} for the seeded dev user                    → 201 + row
#   8. POST share {email} for an address no account has              → 404 + user-UUID remedy
#   9. PATCH /api/v1/nodes/{id} as a viewer                          → 403 VIEWER_READ_ONLY
#
# Isolation is the contract from scripts/scratch-instance.sh: a uniquely named
# database, an explicit loopback port, its own HOME (card store + gateway run
# registry), its own CANOPY_FILE_ROOT, and a closed-port gateway URL. Nothing
# here reads or writes the live instance, and the database is dropped on exit
# (--keep retains it and prints the removal command).
#
# Usage:
#   scripts/share-invite-flow.sh [--keep] [--port N] [--out DIR]
# Overrides: CANOPY_SHARE_PORT, CANOPY_SHARE_PG_HOST, CANOPY_SHARE_PG_PORT,
#            CANOPY_SHARE_PG_USER, CANOPY_SHARE_PG_PASSWORD, CANOPY_SHARE_TICK
#
# Never prints a token or a password, and never restarts or reconfigures any
# existing service, container or database.

set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TICK="${CANOPY_SHARE_TICK:-889}"
PORT="${CANOPY_SHARE_PORT:-8097}"
PG_HOST="${CANOPY_SHARE_PG_HOST:-127.0.0.1}"
PG_PORT="${CANOPY_SHARE_PG_PORT:-5437}"
PG_USER="${CANOPY_SHARE_PG_USER:-canopy}"
PG_PASSWORD="${CANOPY_SHARE_PG_PASSWORD:-canopy}"
GATEWAY_URL="${CANOPY_SHARE_GATEWAY_URL:-http://127.0.0.1:9}"
KEEP=0
OUT_DIR=""

# The two identities under test. Both are dev-JWT subjects: the first matches
# the seeded dev user (internal/db/bootstrap.go — the ONLY writer of a non-NULL
# users.email), the second is provisioned on demand by signing in.
OWNER_SUB="00000000-0000-0000-0000-000000000001"
INVITEE_SUB="00000000-0000-0000-0000-000000000002"
SEEDED_EMAIL="dev@canopy.dev"

while [ $# -gt 0 ]; do
	case "$1" in
	--keep) KEEP=1 ;;
	--port)
		shift
		PORT="${1:-}"
		;;
	--out)
		shift
		OUT_DIR="${1:-}"
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

say() { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
die() {
	printf 'FATAL: %s\n' "$*" >&2
	exit 1
}

# --- guards ---------------------------------------------------------------
case "$PORT" in
8091 | 8092 | 8080) die "port $PORT is a documented live/default port — pick a scratch port" ;;
esac
case "$PORT" in
'' | *[!0-9]*) die "port must be a number, got '$PORT'" ;;
esac
[ "$((PORT))" -ge 1024 ] && [ "$((PORT))" -le 65535 ] || die "port $PORT is outside the usable range"

RAND="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
DB_NAME="canopy_scratch_${TICK}_${RAND}"
SCRATCH_ROOT="$(mktemp -d "/tmp/canopy-share-${TICK}-XXXXXX")" || die "mktemp failed"
SCRATCH_HOME="$SCRATCH_ROOT/home"
SCRATCH_FILES="$SCRATCH_ROOT/file-root"
SCRATCH_BIN="$SCRATCH_ROOT/bin/canopyd"
SCRATCH_LOG="$SCRATCH_ROOT/canopyd.log"
mkdir -p "$SCRATCH_HOME" "$SCRATCH_FILES" "$(dirname "$SCRATCH_BIN")"
[ -n "$OUT_DIR" ] || OUT_DIR="$SCRATCH_ROOT/evidence"
mkdir -p "$OUT_DIR"

BASE="http://127.0.0.1:$PORT"
SERVER_PID=""
STARTED_SERVER=0
CREATED_DB=0

psql_do() {
	PGPASSWORD="$PG_PASSWORD" psql -w -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -v ON_ERROR_STOP=1 "$@"
}
q() { psql_do -d "$DB_NAME" -tAc "$1"; }

stop_server() {
	[ "$STARTED_SERVER" = 1 ] || return 0
	if kill -0 "$SERVER_PID" 2>/dev/null; then
		kill -TERM "$SERVER_PID" 2>/dev/null || true
		for _ in $(seq 1 40); do
			kill -0 "$SERVER_PID" 2>/dev/null || break
			sleep 0.5
		done
		kill -KILL "$SERVER_PID" 2>/dev/null || true
	fi
	return 0
}

cleanup() {
	local rc=$?
	trap - EXIT INT TERM
	stop_server
	if [ "$KEEP" = 1 ]; then
		say ""
		say "KEEP: database ${DB_NAME} and root ${SCRATCH_ROOT} retained"
		say "      drop with: PGPASSWORD=*** psql -h $PG_HOST -p $PG_PORT -U $PG_USER -d postgres -c 'DROP DATABASE ${DB_NAME};'"
	else
		if [ "$CREATED_DB" = 1 ]; then
			psql_do -d postgres -c "DROP DATABASE IF EXISTS ${DB_NAME} WITH (FORCE)" >/dev/null 2>&1
			say ""
			say "dropped database ${DB_NAME} (this run created it)"
			say "evidence kept in ${OUT_DIR}"
		fi
	fi
	exit "$rc"
}
trap 'cleanup' EXIT INT TERM

# --- helpers --------------------------------------------------------------
FAILED=0
CELLS="$OUT_DIR/cells.tsv"
: >"$CELLS"

# check <cell> <expected> <actual>
check() {
	local cell="$1" want="$2" got="$3"
	if [ "$want" = "$got" ]; then
		printf 'PASS  %-58s %s\n' "$cell" "$got"
		printf '%s\tPASS\t%s\t%s\n' "$cell" "$want" "$got" >>"$CELLS"
	else
		printf 'FAIL  %-58s want %s, got %s\n' "$cell" "$want" "$got"
		printf '%s\tFAIL\t%s\t%s\n' "$cell" "$want" "$got" >>"$CELLS"
		FAILED=1
	fi
}

# check_sub <cell> <needle> <haystack-file>
check_sub() {
	local cell="$1" needle="$2" file="$3"
	if grep -qF "$needle" "$file"; then
		printf 'PASS  %-58s contains %q\n' "$cell" "$needle"
		printf '%s\tPASS\tcontains %s\t\n' "$cell" "$needle" >>"$CELLS"
	else
		printf 'FAIL  %-58s missing %q\n' "$cell" "$needle"
		printf '%s\tFAIL\tcontains %s\t\n' "$cell" "$needle" >>"$CELLS"
		FAILED=1
	fi
}

# status <curl args...> — prints the HTTP status, body lands in $OUT_DIR/<slug>.json
post_json() {
	local slug="$1" token="$2" url="$3" body="$4"
	curl -s -o "$OUT_DIR/$slug.json" -w '%{http_code}' \
		-X POST -H "Authorization: Bearer ${token}" \
		-H 'Content-Type: application/json' -d "$body" "$url"
}

mint_dev_token() {
	python3 -c '
import base64, hashlib, hmac, json, sys, time
def b64(b): return base64.urlsafe_b64encode(b).rstrip(b"=")
sub = sys.argv[1]
h = b64(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
now = int(time.time())
p = b64(json.dumps({"sub": sub, "iat": now, "exp": now + 3600},
                   separators=(",", ":")).encode())
sig = b64(hmac.new(b"dev-secret-change-me", h + b"." + p, hashlib.sha256).digest())
print((h + b"." + p + b"." + sig).decode())
' "$1"
}

say "share invite flow — DF-HERMES-CANOPY-89"
say "  repo:         $REPO"
say "  HEAD:         $(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo unknown)"
say "  database:     $DB_NAME (PostgreSQL $PG_HOST:$PG_PORT)"
say "  api:          $BASE (loopback only)"
say "  scratch root: $SCRATCH_ROOT"
say "  evidence:     $OUT_DIR"

# --- preflight ------------------------------------------------------------
step "preflight"
for tool in go psql pg_isready curl python3 jq ss; do
	command -v "$tool" >/dev/null 2>&1 || die "required tool not found: $tool"
done
if ss -tln 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${PORT}\$"; then
	die "port $PORT is already in use — pick another with --port"
fi
pg_isready -h "$PG_HOST" -p "$PG_PORT" >/dev/null 2>&1 || die "PostgreSQL $PG_HOST:$PG_PORT is not accepting connections"
psql_do -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='${DB_NAME}'" | grep -q 1 &&
	die "database ${DB_NAME} already exists — re-run for a fresh name"
say "preflight ok (port free, PostgreSQL ready, database name unused)"

# --- build from HEAD ------------------------------------------------------
step "build from HEAD"
(cd "$REPO" && go build -o "$SCRATCH_BIN" ./cmd/canopyd) || die "go build failed"
say "built $("$SCRATCH_BIN" -version 2>/dev/null || echo unknown)"

# --- fresh database + server ---------------------------------------------
step "create database and start the scratch server"
psql_do -d postgres -c "CREATE DATABASE ${DB_NAME}" >/dev/null || die "CREATE DATABASE failed"
CREATED_DB=1
# env -i drops any inherited CANOPY_DB_URL / gateway key, so this server can
# only ever talk to its own database and a closed loopback gateway port.
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

READY=0
for _ in $(seq 1 60); do
	kill -0 "$SERVER_PID" 2>/dev/null || {
		tail -n 20 "$SCRATCH_LOG" | sed 's/^/    /'
		die "scratch server exited during startup"
	}
	if curl -s --max-time 2 "$BASE/health" >/dev/null 2>&1; then
		READY=1
		break
	fi
	sleep 1
done
[ "$READY" = 1 ] || die "scratch server did not become ready within 60s"
curl -s --max-time 5 "$BASE/health" >"$OUT_DIR/health.json"
jq -e '.status == "ok" and (.schema_version == .embedded_migrations)' "$OUT_DIR/health.json" >/dev/null ||
	die "health is not ok / schema does not match embedded migrations (STALE BUILD?)"
say "ready: $(jq -c '{status, schema_version, embedded_migrations}' "$OUT_DIR/health.json")"

OWNER_TOKEN="$(mint_dev_token "$OWNER_SUB")" || die "failed to mint the owner token"
INVITEE_TOKEN="$(mint_dev_token "$INVITEE_SUB")" || die "failed to mint the invitee token"

# --- 1. owner creates a tree ---------------------------------------------
step "cell 1 — owner creates a tree"
code=$(post_json tree-create "$OWNER_TOKEN" "$BASE/api/v1/trees" \
	'{"title":"DF-89 share cell A","rootMessage":{"content":"root node for the share cells"}}')
check "1. POST /api/v1/trees (owner)" "201" "$code"
TREE_A="$(jq -r '.id' "$OUT_DIR/tree-create.json")"
NODE_A="$(jq -r '.root_node_id' "$OUT_DIR/tree-create.json")"
[ "$TREE_A" != "null" ] && [ -n "$TREE_A" ] || die "no tree id in the create response"
[ "$NODE_A" != "null" ] && [ -n "$NODE_A" ] || die "no root node id in the create response"
say "    tree A $TREE_A root node $NODE_A"

# --- 2-3. provision the second user with ONE authenticated call ----------
step "cells 2-3 — one authenticated call provisions the second user"
code=$(curl -s -o "$OUT_DIR/invitee-provision.json" -w '%{http_code}' \
	-H "Authorization: Bearer ${INVITEE_TOKEN}" "$BASE/api/v1/trees")
check "2. invitee's first authenticated call" "200" "$code"
INVITEE_ID="$(q "SELECT id FROM users WHERE hermes_user_id = '${INVITEE_SUB}'")"
[ -n "$INVITEE_ID" ] || die "no users row was provisioned for ${INVITEE_SUB}"
EMAIL_IS_NULL="$(q "SELECT (email IS NULL)::text FROM users WHERE id = '${INVITEE_ID}'")"
check "3. provisioned users.email IS NULL" "true" "$EMAIL_IS_NULL"
say "    invitee id: $INVITEE_ID (users.email NULL — the whole reason email invites fail here)"

# --- 4-5. share by user_id ----------------------------------------------
step "cells 4-5 — share by user_id (what the Share dialog now sends for a UUID)"
code=$(post_json share-userid "$OWNER_TOKEN" "$BASE/api/v1/trees/$TREE_A/share" \
	"{\"user_id\":\"$INVITEE_ID\",\"permission\":\"editor\",\"message\":\"DF-89 cell\"}")
check "4. POST /trees/{id}/share {user_id, permission}" "201" "$code"
check "4a. response userId == invitee" "$INVITEE_ID" "$(jq -r '.userId' "$OUT_DIR/share-userid.json")"
check "4b. response permission" "editor" "$(jq -r '.permission' "$OUT_DIR/share-userid.json")"
check "4c. tree_members row (role=member)" "member" \
	"$(q "SELECT role FROM tree_members WHERE tree_id = '${TREE_A}' AND user_id = '${INVITEE_ID}'")"
code=$(post_json share-userid-again "$OWNER_TOKEN" "$BASE/api/v1/trees/$TREE_A/share" \
	"{\"user_id\":\"$INVITEE_ID\",\"permission\":\"editor\"}")
check "5. duplicate share is refused (membership really exists)" "409" "$code"

# --- 6. a non-viewer member may write ------------------------------------
step "cell 6 — member write control (editor may PATCH a node)"
code=$(curl -s -o "$OUT_DIR/node-patch-member.json" -w '%{http_code}' \
	-X PATCH -H "Authorization: Bearer ${INVITEE_TOKEN}" \
	-H 'Content-Type: application/json' -d '{"content":"edited by the invited member"}' \
	"$BASE/api/v1/nodes/$NODE_A")
check "6. PATCH /api/v1/nodes/{id} as invited member" "200" "$code"

# --- 7. email path for the seeded dev user -------------------------------
step "cell 7 — email invite still works for a user that HAS an email"
# The seeded dev account already owns tree A, so a tree owned by the invitee is
# where its email invite can land: the email path is what is under test.
code=$(post_json tree-create-b "$INVITEE_TOKEN" "$BASE/api/v1/trees" \
	'{"title":"DF-89 share cell B","rootMessage":{"content":"tree owned by the invitee"}}')
check "7a. invitee creates tree B" "201" "$code"
TREE_B="$(jq -r '.id' "$OUT_DIR/tree-create-b.json")"
NODE_B="$(jq -r '.root_node_id' "$OUT_DIR/tree-create-b.json")"
OWNER_ID="$(q "SELECT id FROM users WHERE email = '${SEEDED_EMAIL}'")"
[ -n "$OWNER_ID" ] || die "the seeded dev user ${SEEDED_EMAIL} has no users row"
code=$(post_json share-email "$INVITEE_TOKEN" "$BASE/api/v1/trees/$TREE_B/share" \
	"{\"email\":\"$SEEDED_EMAIL\",\"permission\":\"viewer\"}")
check "7b. POST /trees/{id}/share {email} (seeded user)" "201" "$code"
check "7c. response email echoes the invitee" "$SEEDED_EMAIL" "$(jq -r '.email' "$OUT_DIR/share-email.json")"
check "7d. tree_members row (role=viewer)" "viewer" \
	"$(q "SELECT role FROM tree_members WHERE tree_id = '${TREE_B}' AND user_id = '${OWNER_ID}'")"

# --- 8. the recorded dead end, now with a usable remedy ------------------
step "cell 8 — email invite for an address with no account names the user-UUID remedy"
code=$(post_json share-email-unknown "$INVITEE_TOKEN" "$BASE/api/v1/trees/$TREE_B/share" \
	'{"email":"lexi.dogfood@example.com","permission":"viewer"}')
check "8. POST /trees/{id}/share {email} (no account)" "404" "$code"
check "8a. error code" "INVITEE_NOT_REGISTERED" "$(jq -r '.error.code' "$OUT_DIR/share-email-unknown.json")"
check_sub "8b. remedy named in the message" "user UUID" "$OUT_DIR/share-email-unknown.json"

# --- 9. viewer write gate ------------------------------------------------
step "cell 9 — the email-granted viewer is read-only"
code=$(curl -s -o "$OUT_DIR/node-patch-viewer.json" -w '%{http_code}' \
	-X PATCH -H "Authorization: Bearer ${OWNER_TOKEN}" \
	-H 'Content-Type: application/json' -d '{"content":"viewer attempt"}' \
	"$BASE/api/v1/nodes/$NODE_B")
check "9. PATCH /api/v1/nodes/{id} as viewer" "403" "$code"
check "9a. error code" "VIEWER_READ_ONLY" "$(jq -r '.error.code' "$OUT_DIR/node-patch-viewer.json")"
code=$(curl -s -o "$OUT_DIR/node-patch-treeB-owner.json" -w '%{http_code}' \
	-X PATCH -H "Authorization: Bearer ${INVITEE_TOKEN}" \
	-H 'Content-Type: application/json' -d '{"content":"owner write on tree B"}' \
	"$BASE/api/v1/nodes/$NODE_B")
check "9b. same route by tree B's owner (control)" "200" "$code"

# --- summary -------------------------------------------------------------
step "summary"
jq -n \
	--arg db "$DB_NAME" \
	--arg head "$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo unknown)" \
	--arg treeA "$TREE_A" --arg nodeA "$NODE_A" \
	--arg treeB "$TREE_B" --arg nodeB "$NODE_B" \
	--arg invitee "$INVITEE_ID" --arg seeded "$OWNER_ID" \
	'{database: $db, head: $head, tree_a: $treeA, node_a: $nodeA, tree_b: $treeB,
	  node_b: $nodeB, invitee_id: $invitee, seeded_id: $seeded}' \
	>"$OUT_DIR/ids.json"
say "cells:"
sed 's/^/    /' "$CELLS"
if [ "$FAILED" = 0 ]; then
	say ""
	say "PASS — every cell above held on a fresh database."
	exit 0
fi
say ""
say "FAIL — at least one cell did not hold."
exit 1
