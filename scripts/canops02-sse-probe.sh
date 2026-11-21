#!/usr/bin/env bash
# CAN-OPS-02 AC4 — SSE member_updated within 2s of the member PATCH, on the
# deployed :8091 binary. Creates a viewer member, opens the tree event stream,
# PATCHes the role, waits, greps the stream, then removes the member (net-zero).
set -uo pipefail
BASE="http://127.0.0.1:8091"
SECRET="${CANOPY_JWT_SECRET:-dev-secret-change-me}"
TREE="ee380e90-48a6-44fe-9f49-16bd38f223bc"
OWNER="00000000-0000-0000-0000-000000000001"
GUEST="00000000-0000-0000-0000-0000000000aa"
EVID=".coding-hermes/board/evidence/CAN-OPS-02-20261121-sse.jsonl"
mkdir -p .coding-hermes/board/evidence
: > "$EVID"
SSELOG="/tmp/canops02-sse-$$.log"
FAILS=0
ok() { printf 'PASS  %s\n' "$*"; }
bad() { printf 'FAIL  %s\n' "$*"; FAILS=$((FAILS+1)); }
b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
mint_jwt() {
  local now header payload sig
  now="$(date +%s)"
  header="$(printf '{"alg":"HS256","typ":"JWT"}' | b64url)"
  payload="$(printf '{"sub":"%s","exp":%s}' "$1" $((now + $2)) | b64url)"
  sig="$(printf '%s.%s' "$header" "$payload" | openssl dgst -sha256 -hmac "$SECRET" -binary | openssl base64 -A | tr '+/' '-_' | tr -d '=')"
  printf '%s.%s.%s' "$header" "$payload" "$sig"
}
TOKEN="$(mint_jwt "$OWNER" 600)"

# create the member to be patched
SHARE_BODY="$(curl -sS --max-time 15 -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"user_id\":\"$GUEST\",\"permission\":\"viewer\"}" "$BASE/api/v1/trees/$TREE/share")"
MID="$(printf '%s' "$SHARE_BODY" | jq -r '.memberId // .member_id // empty')"
jq -cn --arg s share --arg b "$SHARE_BODY" '{step:$s,body:$b}' >> "$EVID"
if [ -z "$MID" ]; then bad "share failed, cannot run SSE cell: $SHARE_BODY"; exit 1; fi
ok "member created for SSE cell: $MID"

# open the stream FIRST, give it a moment to subscribe
curl -sN --max-time 12 -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/trees/$TREE/events" > "$SSELOG" 2>&1 &
SSE_PID=$!
sleep 2

# PATCH the role and note the wall clock
T0="$(date +%s)"
CODE="$(curl -sS -o /tmp/canops02-patch-$$.json -w '%{http_code}' --max-time 15 -X PATCH \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"role":"member"}' "$BASE/api/v1/trees/$TREE/members/$MID")"
PATCH_BODY="$(cat /tmp/canops02-patch-$$.json)"; rm -f /tmp/canops02-patch-$$.json
jq -cn --arg s patch_role --arg st "$CODE" --arg b "$PATCH_BODY" '{step:$s,status:$st,body:$b}' >> "$EVID"
[ "$CODE" = "200" ] && ok "PATCH role -> 200 (t0=$T0)" || bad "PATCH role -> $CODE $PATCH_BODY"

# poll the stream for up to 5s, record the first member_updated line + latency
FOUND=""
for i in 1 2 3 4 5 6 7 8 9 10; do
  if grep -q 'member_updated' "$SSELOG" 2>/dev/null; then
    T1="$(date +%s)"; FOUND="$T1"; break
  fi
  sleep 0.5
done
if [ -n "$FOUND" ]; then
  LAT=$((FOUND - T0))
  ok "SSE emitted member_updated within ~${LAT}s (limit 2s)"
  [ "$LAT" -le 2 ] && ok "latency ${LAT}s <= 2s" || bad "latency ${LAT}s > 2s"
  grep -m1 'member_updated' "$SSELOG" | head -c 400 | jq -Rc '{step:"sse_line",body:.}' >> "$EVID"
else
  bad "no member_updated line on the tree event stream within 5s"
  head -c 400 "$SSELOG" | jq -Rc '{step:"sse_raw",body:.}' >> "$EVID"
fi
kill "$SSE_PID" 2>/dev/null
wait "$SSE_PID" 2>/dev/null

# cleanup: remove the member (net-zero)
DCODE="$(curl -sS -o /tmp/canops02-del-$$.json -w '%{http_code}' --max-time 15 -X DELETE \
  -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/trees/$TREE/members/$MID")"
DBODY="$(cat /tmp/canops02-del-$$.json)"; rm -f /tmp/canops02-del-$$.json
jq -cn --arg s cleanup_delete --arg st "$DCODE" --arg b "$DBODY" '{step:$s,status:$st,body:$b}' >> "$EVID"
[ "$DCODE" = "200" ] && ok "cleanup DELETE -> 200 (tree net-zero)" || bad "cleanup DELETE -> $DCODE $DBODY"
COUNT="$(curl -sS --max-time 10 -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/trees/$TREE/members" | jq -r '(.members // .data // []) | length')"
[ "$COUNT" = "1" ] && ok "member count back to 1 (owner only)" || bad "member count = $COUNT (want 1)"
rm -f "$SSELOG"
printf '== RESULT: %s failure(s); evidence %s ==\n' "$FAILS" "$EVID"
exit "$FAILS"
