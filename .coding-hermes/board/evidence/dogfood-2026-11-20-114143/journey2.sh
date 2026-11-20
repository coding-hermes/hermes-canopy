#!/usr/bin/env bash
# Dogfood 2026-11-20 continuation: cards PATCH/action/SSE, profiles, export/import,
# plus a truncation-manifest inspection. Loads /tmp/canopy-df19/state.env.
set -u
. /tmp/canopy-df19/state.env
T0=$(date +%s%3N)
step() { STEP_NAME="$1"; }
die() { echo "FAIL: $*"; exit 1; }
ok() { echo "OK  $STEP_NAME code=$1 ${2:-} (${3:-0} ms)"; }

reqh() { # reqh METHOD PATH IFMATCH BODY
  local m=$1 p=$2 im=${3:-} b=${4:-} out
  local args=(-s -w '\n%{http_code} %{time_total}' --max-time 30 -X "$m" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json')
  [ -n "$im" ] && args+=(-H "If-Match: $im")
  [ -n "$b" ] && args+=(-d "$b")
  out=$(curl "${args[@]}" "$BASE$p")
  R_BODY=$(printf '%s' "$out" | sed '$d')
  R_META=$(printf '%s' "$out" | tail -1)
  R_CODE=${R_META%% *}
  R_MS=$(python3 -c "import sys;print(int(float('${R_META##* }')*1000))")
}
req() { reqh "$1" "$2" "" "${3:-}"; }

# ---- card patch (with If-Match) ----
step card-patch-add-action
reqh PATCH /api/v1/cards/$CARD "1" '{"data":{"title":"Groceries","body":"eggs, coffee, bread"},"actions":[{"handler":"noop","label":"noop"}]}'
[ "$R_CODE" = 200 ] || die "card patch $R_CODE: $R_BODY"
REV2=$(printf '%s' "$R_BODY" | jq -r '.revision')
ok "$R_CODE" "rev 1->$REV2" "$R_MS"

step card-action-known
req POST /api/v1/cards/$CARD/actions '{"handler":"noop","payload":{}}'
ok "$R_CODE" "(expect 200)" "$R_MS"

step card-action-unknown
req POST /api/v1/cards/$CARD/actions '{"handler":"no-such","payload":{}}'
ok "$R_CODE" "(expect 422) $(printf '%s' "$R_BODY" | jq -r '.error.code // "no-code"')" "$R_MS"

step card-stale-if-match
reqh PATCH /api/v1/cards/$CARD "1" '{"data":{"title":"stale-write"}}'
ok "$R_CODE" "(expect 412 stale) $(printf '%s' "$R_BODY" | jq -r '.error.code // "no-code"' ) rev-now=$(printf '%s' "$R_BODY" | jq -r '.revision // "?"')" "$R_MS"

step card-sse-first-frame
SSE=$(timeout 6 curl -s -N -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/cards/$CARD/events" | head -c 300)
echo "    sse-head: $(printf '%s' "$SSE" | tr '\n' ' ' | head -c 150)"

# ---- context with cards ----
step context-compile-includeCards
req GET "/api/v1/context/$REPLY?includeCards=true"
CC=$(printf '%s' "$R_BODY" | jq -r '.manifest.cards | length' 2>/dev/null)
ok "$R_CODE" "cards-in-manifest=$CC" "$R_MS"

# ---- truncation manifest inspection (budget=64) ----
step tiny-budget-manifest
req GET "/api/v1/context/$REPLY?budget=64"
printf '%s' "$R_BODY" | jq '{tokenBudget:.manifest.tokenBudget,tokensUsed:.manifest.tokensUsed,omittedCount:.manifest.omittedCount,omittedReason:.manifest.omittedReason,truncationMarkers:.manifest.truncationMarkers,warnings:.manifest.warnings}' > /tmp/canopy-df19/tiny-manifest.json
ok "$R_CODE" "wrote tiny-manifest.json" "$R_MS"
cat /tmp/canopy-df19/tiny-manifest.json

# ---- profiles ----
step profile-create
req POST /api/v1/profiles '{"name":"df20-dogfood-agent","token":"placeholder-not-persisted","display_name":"DF Dogfood","description":"dogfood 2026-11-20","can_auto_respond":false,"context_window_size":65536,"is_public":false,"config_json":{"provider":"test"}}'
[ "$R_CODE" = 201 ] || die "profile $R_CODE: $R_BODY"
PROF=$(printf '%s' "$R_BODY" | jq -r '.id')
ok "$R_CODE" "profile=$PROF" "$R_MS"

step profile-duplicate
req POST /api/v1/profiles '{"name":"df20-dogfood-agent","token":"x"}'
ok "$R_CODE" "(expect 4xx) $(printf '%s' "$R_BODY" | jq -r '.error.code // "no-code"')" "$R_MS"

step profile-list
req GET /api/v1/profiles
N=$(printf '%s' "$R_BODY" | jq '.profiles // . | length')
ok "$R_CODE" "count=$N" "$R_MS"

step profile-unknown-field
req POST /api/v1/profiles '{"name":"df20-bad","token":"x","bogus_field":1}'
ok "$R_CODE" "(expect 400 INVALID_BODY) $(printf '%s' "$R_BODY" | jq -r '.error.code // "no-code"')" "$R_MS"

# ---- export/import ----
step tree-export
req GET "/api/v1/trees/$TREE1/export"
[ "$R_CODE" = 200 ] || die "export $R_CODE: $R_BODY"
printf '%s' "$R_BODY" > /tmp/canopy-df19/export.json
EN=$(jq '[.. | objects | select(has("content"))] | length' /tmp/canopy-df19/export.json 2>/dev/null)
ok "$R_CODE" "export bytes=$(wc -c < /tmp/canopy-df19/export.json) content-nodes=$EN" "$R_MS"

step tree-import
req POST /api/v1/trees/import "$(cat /tmp/canopy-df19/export.json)"
[ "$R_CODE" = 201 ] || { echo "    import body: $(printf '%s' "$R_BODY" | head -c 200)"; die "import $R_CODE"; }
TREE2=$(printf '%s' "$R_BODY" | jq -r '.id // .tree.id')
IN=$(printf '%s' "$R_BODY" | jq -r '.node_count // .nodes_created // "?"')
ok "$R_CODE" "imported=$TREE2 nodes=$IN" "$R_MS"
echo "TREE2=$TREE2" >> /tmp/canopy-df19/state.env

echo "CONT_MS=$(( $(date +%s%3N) - T0 ))"
