#!/usr/bin/env bash
# Dogfood 2026-11-20 hermes-canopy: real-use journey against the isolated
# scratch instance (port 18093) with the LIVE Hermes gateway. Never prints secrets.
set -u
BASE="http://127.0.0.1:18093"
ROOT=/tmp/canopy-df19
STATE="$ROOT/state.env"
: > "$STATE"
T0=$(date +%s%3N)
step() { STEP_NAME="$1"; }
die() { echo "FAIL: $*"; exit 1; }
ok() { echo "OK  $STEP_NAME code=$1 ${2:-} (${3:-0} ms)"; }

# dev JWT (dev-secret provisioning)
TOKEN=$(python3 - <<'EOF'
import base64, hashlib, hmac, json, time
def b64(b): return base64.urlsafe_b64encode(b).rstrip(b"=")
h = b64(json.dumps({"alg":"HS256","typ":"JWT"},separators=(",",":")).encode())
now = int(time.time())
p = b64(json.dumps({"sub":"00000000-0000-0000-0000-000000000001","iat":now,"exp":now+7200},separators=(",",":")).encode())
s = b64(hmac.new(b"dev-secret-change-me", h+b"."+p, hashlib.sha256).digest())
print((h+b"."+p+b"."+s).decode())
EOF
) || die "jwt mint"
echo "TOKEN=$TOKEN" >> "$STATE"

req() { # req METHOD PATH [BODY] -> sets R_CODE R_MS R_BODY
  local m=$1 p=$2 b=${3:-}
  local out
  if [ -n "$b" ]; then
    out=$(curl -s -w '\n%{http_code} %{time_total}' --max-time 30 -X "$m" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$b" "$BASE$p")
  else
    out=$(curl -s -w '\n%{http_code} %{time_total}' --max-time 30 -H "Authorization: Bearer $TOKEN" "$BASE$p")
  fi
  R_BODY=$(printf '%s' "$out" | sed '$d')
  R_META=$(printf '%s' "$out" | tail -1)
  R_CODE=${R_META%% *}
  R_MS=$(python3 -c "import sys;print(int(float('${R_META##* }')*1000))")
}

# ---- 1. health ----
step health
req GET /health
[ "$R_CODE" = 200 ] || die "health $R_CODE"
SCHEMA=$(printf '%s' "$R_BODY" | jq -r '.schema_version')
EMB=$(printf '%s' "$R_BODY" | jq -r '.embedded_migrations')
[ "$SCHEMA" = "$EMB" ] || die "schema mismatch $SCHEMA!=$EMB"
ok "$R_CODE" "schema=$SCHEMA relay=$(printf '%s' "$R_BODY" | jq -c .relay)" "$R_MS"
echo "T_FIRST_HEALTH=$(( $(date +%s%3N) - T0 ))ms" >> "$STATE"

# ---- 2. workspace ----
step workspace
req POST /api/v1/collab '{"name":"df20-dogfood-ws"}'
[ "$R_CODE" = 201 ] || die "workspace $R_CODE: $R_BODY"
WS=$(printf '%s' "$R_BODY" | jq -r '.workspace_id')
ok "$R_CODE" "ws=$WS" "$R_MS"

# ---- 3. tree ----
step tree-create
req POST /api/v1/trees '{"title":"df20 dogfood tree","description":"real-use run 2026-11-20","rootMessage":{"content":"Root: planning the dogfood meal","contentFormat":"markdown","nodeType":"message"}}'
[ "$R_CODE" = 201 ] || die "tree $R_CODE: $R_BODY"
TREE1=$(printf '%s' "$R_BODY" | jq -r 'if .id then .id else .tree.id end')
ROOTN=$(printf '%s' "$R_BODY" | jq -r '.root_node_id // .rootNode.id // .root_node.id')
ok "$R_CODE" "tree=$TREE1 root=$ROOTN" "$R_MS"

# ---- 4. reply ----
step reply-node
req POST "/api/v1/trees/$TREE1/nodes" "{\"parent_id\":\"$ROOTN\",\"content\":\"Reply: grocery list for the meal\",\"node_type\":\"message\",\"edge_type\":\"reply\"}"
[ "$R_CODE" = 201 ] || die "reply $R_CODE: $R_BODY"
REPLY=$(printf '%s' "$R_BODY" | jq -r '.node.id')
ok "$R_CODE" "reply=$REPLY" "$R_MS"

# ---- 5. fork ----
step fork-node
req POST "/api/v1/trees/$TREE1/nodes" "{\"parent_id\":\"$ROOTN\",\"content\":\"Fork: vegetarian alternative plan\",\"node_type\":\"message\",\"edge_type\":\"fork\"}"
[ "$R_CODE" = 201 ] || die "fork $R_CODE: $R_BODY"
FORK=$(printf '%s' "$R_BODY" | jq -r '.node.id')
ok "$R_CODE" "fork=$FORK" "$R_MS"

# ---- 6. synthesis via merge ----
step synthesis-merge
req POST "/api/v1/trees/$TREE1/merge" "{\"source_node_ids\":[\"$REPLY\",\"$FORK\"],\"content\":\"Synthesis: combined meal plan (meat + veg), budget ok\",\"content_format\":\"markdown\",\"target_parent_id\":\"$ROOTN\",\"metadata\":{\"merge_summary\":\"df20 dogfood\",\"decision\":\"combined\"}}"
[ "$R_CODE" = 201 ] || die "merge $R_CODE: $R_BODY"
SYNTH=$(printf '%s' "$R_BODY" | jq -r '.node.id')
NTYPE=$(printf '%s' "$R_BODY" | jq -r '.node.nodeType // .node.node_type')
ok "$R_CODE" "synth=$SYNTH type=$NTYPE" "$R_MS"
echo "T_FIRST_WRITE=$(( $(date +%s%3N) - T0 ))ms" >> "$STATE"

# ---- 7. synthesis-only-route guard (negative) ----
step synthesis-via-node-create
req POST "/api/v1/trees/$TREE1/nodes" "{\"parent_id\":\"$ROOTN\",\"content\":\"should be refused\",\"node_type\":\"synthesis\",\"edge_type\":\"reply\"}"
ok "$R_CODE" "(expect 400) $(printf '%s' "$R_BODY" | jq -r '.error.code // "no-code"')" "$R_MS"

# ---- 8. topic ----
step topic-create
req POST /api/v1/topics "{\"treeId\":\"$TREE1\",\"rootNodeId\":\"$REPLY\",\"title\":\"meal-planning\",\"description\":\"df20 topic\"}"
[ "$R_CODE" = 201 ] || die "topic $R_CODE: $R_BODY"
TOPIC=$(printf '%s' "$R_BODY" | jq -r '.id')
ok "$R_CODE" "topic=$TOPIC" "$R_MS"

# ---- 9. context compile (flagship) ----
step context-compile-default
req GET "/api/v1/context/$REPLY"
[ "$R_CODE" = 200 ] || die "context $R_CODE: $R_BODY"
BUDGET=$(printf '%s' "$R_BODY" | jq -r '.manifest.tokenBudget // .tokenBudget // "?"')
USED=$(printf '%s' "$R_BODY" | jq -r '.manifest.tokensUsed // .tokensUsed // "?"')
HASH1=$(printf '%s' "$R_BODY" | jq -r '.manifest.manifestHash // .manifestHash // "?"')
ok "$R_CODE" "budget=$BUDGET used=$USED hash=${HASH1:0:12}.." "$R_MS"
echo "T_FIRST_RESULT=$(( $(date +%s%3N) - T0 ))ms" >> "$STATE"

step context-compile-repeat
req GET "/api/v1/context/$REPLY"
HASH2=$(printf '%s' "$R_BODY" | jq -r '.manifest.manifestHash // .manifestHash // "?"')
[ "$HASH1" = "$HASH2" ] && ok "$R_CODE" "manifestHash stable" "$R_MS" || ok "$R_CODE" "manifestHash DIFFERS ($HASH1 vs $HASH2)" "$R_MS"

step context-compile-tiny-budget
req GET "/api/v1/context/$REPLY?budget=64"
TOK=$(printf '%s' "$R_BODY" | jq -r '.manifest.tokenBudget // .tokenBudget // "?"')
TRUNC=$(printf '%s' "$R_BODY" | jq -r '.manifest.truncationMarkers | length' 2>/dev/null)
ok "$R_CODE" "budget=$TOK truncMarkers=$TRUNC" "$R_MS"

step context-compile-model-window
req GET "/api/v1/context/$REPLY?model=gpt-6-luna"
MB=$(printf '%s' "$R_BODY" | jq -r '.manifest.tokenBudget // .tokenBudget // "?"')
ok "$R_CODE" "model-budget=$MB (catalog window known?)" "$R_MS"

# ---- 10. cards ----
step card-create
req POST /api/v1/cards "{\"treeId\":\"$TREE1\",\"nodeId\":\"$REPLY\",\"appId\":\"df20\",\"cardType\":\"compact\",\"data\":{\"title\":\"Groceries\",\"body\":\"eggs, coffee\"}}"
[ "$R_CODE" = 201 ] || die "card $R_CODE: $R_BODY"
CARD=$(printf '%s' "$R_BODY" | jq -r '.id')
REV=$(printf '%s' "$R_BODY" | jq -r '.revision')
ok "$R_CODE" "card=$CARD rev=$REV" "$R_MS"

step card-patch-add-action
req PATCH /api/v1/cards/$CARD "{\"data\":{\"title\":\"Groceries\",\"body\":\"eggs, coffee, bread\"},\"actions\":[{\"handler\":\"noop\",\"label\":\"noop\"}]}"
[ "$R_CODE" = 200 ] || die "card patch $R_CODE: $R_BODY (rev was $REV)"
REV2=$(printf '%s' "$R_BODY" | jq -r '.revision')
ok "$R_CODE" "rev=$REV2" "$R_MS"

step card-action
req POST /api/v1/cards/$CARD/actions '{"handler":"noop","payload":{}}'
ok "$R_CODE" "(expect 200)" "$R_MS"

step card-action-unknown-handler
req POST /api/v1/cards/$CARD/actions '{"handler":"no-such","payload":{}}'
ok "$R_CODE" "(expect 422)" "$R_MS"

step card-stale-if-match
req PATCH /api/v1/cards/$CARD "{\"data\":{\"title\":\"stale\"}}"
ok "$R_CODE" "(expect 428 no if-match)" "$R_MS"

step card-sse-first-frame
SSE=$(timeout 6 curl -s -N -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/cards/$CARD/events" | head -c 400)
echo "    sse-first-400b: $(printf '%s' "$SSE" | head -c 160 | tr '\n' ' ')"

# ---- 11. context compile with cards ----
step context-compile-includeCards
req GET "/api/v1/context/$REPLY?includeCards=true"
CC=$(printf '%s' "$R_BODY" | jq -r '.manifest.cards | length' 2>/dev/null)
ok "$R_CODE" "cards-in-manifest=$CC" "$R_MS"

# ---- 12. profiles (DOC-16 new surface) ----
step profile-create
req POST /api/v1/profiles '{"name":"df20-dogfood-agent","token":"placeholder-not-persisted","display_name":"DF Dogfood","description":"dogfood 2026-11-20","can_auto_respond":false,"context_window_size":65536,"is_public":false,"config_json":{"provider":"test"}}'
[ "$R_CODE" = 201 ] || die "profile $R_CODE: $R_BODY"
PROF=$(printf '%s' "$R_BODY" | jq -r '.id')
ok "$R_CODE" "profile=$PROF" "$R_MS"

step profile-duplicate-name
req POST /api/v1/profiles '{"name":"df20-dogfood-agent","token":"x"}'
ok "$R_CODE" "(expect 4xx dup) $(printf '%s' "$R_BODY" | jq -r '.error.code // "no-code"')" "$R_MS"

step profile-list
req GET /api/v1/profiles
N=$(printf '%s' "$R_BODY" | jq '.profiles | length' 2>/dev/null || printf '%s' "$R_BODY" | jq 'length')
ok "$R_CODE" "list-count=$N" "$R_MS"

# ---- 13. export/import ----
step tree-export
req GET "/api/v1/trees/$TREE1/export"
[ "$R_CODE" = 200 ] || die "export $R_CODE: $R_BODY"
printf '%s' "$R_BODY" > "$ROOT/export.json"
EN=$(printf '%s' "$R_BODY" | jq '.. | objects | select(has("content")) | .content' 2>/dev/null | grep -c .)
ok "$R_CODE" "export=$ROOT/export.json content-fields=$EN" "$R_MS"

step tree-import
req POST /api/v1/trees/import "$(cat "$ROOT/export.json")"
[ "$R_CODE" = 201 ] || die "import $R_CODE: $R_BODY"
TREE2=$(printf '%s' "$R_BODY" | jq -r '.id // .tree.id')
ok "$R_CODE" "imported=$TREE2" "$R_MS"

# ---- state for later legs ----
cat >> "$STATE" <<EOF
BASE=$BASE
TREE1=$TREE1
ROOTN=$ROOTN
REPLY=$REPLY
FORK=$FORK
SYNTH=$SYNTH
TOPIC=$TOPIC
CARD=$CARD
PROF=$PROF
TREE2=$TREE2
EOF
echo "STATE written"
echo "TOTAL_JOURNEY_MS=$(( $(date +%s%3N) - T0 ))"
