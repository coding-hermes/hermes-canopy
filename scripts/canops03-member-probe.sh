#!/usr/bin/env bash
# CAN-OPS-03 live member-write probe (foreman tick 2026-11-21T12:28Z)
# Proves the deployed :8091 binary's member write surface end to end:
#   share (viewer) -> GET reflects viewer -> owner PATCH role member -> GET
#   reflects member -> DELETE -> GET no longer lists it -> negatives.
# Net-zero: the invited member is created and removed inside this script; the
# production owner row and the tree itself are untouched.
set -uo pipefail

BASE="http://127.0.0.1:8091"
SECRET="${CANOPY_JWT_SECRET:-dev-secret-change-me}"
TREE="ee380e90-48a6-44fe-9f49-16bd38f223bc"
OWNER="00000000-0000-0000-0000-000000000001"
GUEST="00000000-0000-0000-0000-0000000000aa"
EVID=".coding-hermes/board/evidence/CAN-OPS-03-20261121-probe.jsonl"
mkdir -p .coding-hermes/board/evidence
: > "$EVID"

FAILS=0
note() { printf '%s\n' "$*"; }
rec() { # rec <step> <status> <body>
  jq -cn --arg s "$1" --arg st "$2" --arg b "${3:0:400}" \
     --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
     '{ts:$ts,step:$s,status:($st|tonumber? // $st),body:$b}' >> "$EVID"
}
ok() { note "PASS  $*"; }
bad() { note "FAIL  $*"; FAILS=$((FAILS+1)); }

b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }

mint_jwt() { # mint_jwt <sub> <ttl>
  local now header payload sig
  now="$(date +%s)"
  header="$(printf '{"alg":"HS256","typ":"JWT"}' | b64url)"
  payload="$(printf '{"sub":"%s","exp":%s}' "$1" $((now + $2)) | b64url)"
  sig="$(printf '%s.%s' "$header" "$payload" \
        | openssl dgst -sha256 -hmac "$SECRET" -binary \
        | openssl base64 -A | tr '+/' '-_' | tr -d '=')"
  printf '%s.%s.%s' "$header" "$payload" "$sig"
}

mint_none() { # unsigned alg=none token for the negative cell
  local header payload
  header="$(printf '{"alg":"none","typ":"JWT"}' | b64url)"
  payload="$(printf '{"sub":"%s","exp":%s}' "$OWNER" $(( $(date +%s) + 300 )) | b64url)"
  printf '%s.%s.' "$header" "$payload"
}

req() { # req <method> <path> <token> [body]  -> sets CODE BODY
  local m="$1" p="$2" t="$3" d="${4:-}"
  local out
  if [ -n "$d" ]; then
    out="$(curl -sS -o /tmp/canops03-body.$$ -w '%{http_code}' --max-time 15 \
      -X "$m" -H "Authorization: Bearer $t" -H 'Content-Type: application/json' \
      -d "$d" "$BASE$p")"
  else
    out="$(curl -sS -o /tmp/canops03-body.$$ -w '%{http_code}' --max-time 15 \
      -X "$m" -H "Authorization: Bearer $t" "$BASE$p")"
  fi
  CODE="$out"
  BODY="$(cat /tmp/canops03-body.$$ 2>/dev/null)"
  rm -f /tmp/canops03-body.$$
}

[ -z "$SECRET" ] && { bad "empty JWT secret"; exit 2; }
TOKEN_OWNER="$(mint_jwt "$OWNER" 600)"
TOKEN_GUEST="$(mint_jwt "$GUEST" 600)"
TOKEN_EXPIRED="$(mint_jwt "$OWNER" -600)"
TOKEN_NONE="$(mint_none)"

note "== CAN-OPS-03 probe: tree $TREE, binary $(curl -s --max-time 10 $BASE/health | jq -r .commit) =="

# 0. baseline members list
req GET "/api/v1/trees/$TREE/members" "$TOKEN_OWNER"
rec baseline_members "$CODE" "$BODY"
BASE_COUNT="$(printf '%s' "$BODY" | jq -r '(.members // .data // []) | length' 2>/dev/null)"
[ "$CODE" = "200" ] && ok "baseline GET /members -> 200 (count=${BASE_COUNT:-?})" || bad "baseline GET /members -> $CODE $BODY"
printf '%s' "$BODY" | jq -e '.members' >/dev/null 2>&1 || note "  note: envelope keys = $(printf '%s' "$BODY" | jq -rc 'keys' 2>/dev/null)"

# 1. share guest as viewer (non-owner member creation through the share flow)
req POST "/api/v1/trees/$TREE/share" "$TOKEN_OWNER" "{\"user_id\":\"$GUEST\",\"permission\":\"viewer\"}"
rec share_viewer "$CODE" "$BODY"
MEMBER_ID="$(printf '%s' "$BODY" | jq -r '.memberId // .member_id // empty' 2>/dev/null)"
if [ "$CODE" = "201" ] && [ -n "$MEMBER_ID" ]; then
  ok "POST /share viewer -> 201 memberId=$MEMBER_ID"
else
  bad "POST /share viewer -> $CODE $BODY"
fi
[ "$CODE" = "409" ] && { bad "share returned ALREADY_MEMBER: guest already a member -> cannot prove net-zero from a clean state"; }

# 2. GET /members reflects role viewer
if [ -n "${MEMBER_ID:-}" ]; then
  req GET "/api/v1/trees/$TREE/members" "$TOKEN_OWNER"
  rec members_after_share "$CODE" "$BODY"
  ROLE_AFTER_SHARE="$(printf '%s' "$BODY" | jq -r --arg m "$MEMBER_ID" '(.members // .data // [])[] | select((.memberId // .member_id // .id) == $m) | .role' 2>/dev/null)"
  [ "$ROLE_AFTER_SHARE" = "viewer" ] && ok "GET /members lists new member with role=viewer" || bad "role after share = '${ROLE_AFTER_SHARE:-missing}' (want viewer)"
else
  MEMBERS_PATH_EMPTY=1
fi

# 3. owner PATCH viewer -> member
if [ -n "${MEMBER_ID:-}" ]; then
  req PATCH "/api/v1/trees/$TREE/members/$MEMBER_ID" "$TOKEN_OWNER" '{"role":"member"}'
  rec patch_role_member "$CODE" "$BODY"
  ROLE_IN_PATCH="$(printf '%s' "$BODY" | jq -r '.role // .member.role // empty' 2>/dev/null)"
  [ "$CODE" = "200" ] && ok "PATCH role viewer->member -> 200 (role=${ROLE_IN_PATCH:-?})" || bad "PATCH role -> $CODE $BODY"

  # 4. GET /members reflects member
  req GET "/api/v1/trees/$TREE/members" "$TOKEN_OWNER"
  rec members_after_patch "$CODE" "$BODY"
  ROLE_AFTER_PATCH="$(printf '%s' "$BODY" | jq -r --arg m "$MEMBER_ID" '(.members // .data // [])[] | select((.memberId // .member_id // .id) == $m) | .role' 2>/dev/null)"
  [ "$ROLE_AFTER_PATCH" = "member" ] && ok "GET /members reflects role=member" || bad "role after patch = '${ROLE_AFTER_PATCH:-missing}' (want member)"

  # 5. DELETE the non-owner member
  req DELETE "/api/v1/trees/$TREE/members/$MEMBER_ID" "$TOKEN_OWNER"
  rec delete_member "$CODE" "$BODY"
  HAS_KEYS="$(printf '%s' "$BODY" | jq -r '[(has("member_id") or has("memberId")), (has("tree_id") or has("treeId")), (has("removed_at") or has("removedAt"))] | all' 2>/dev/null)"
  [ "$CODE" = "200" ] && [ "$HAS_KEYS" = "true" ] && ok "DELETE member -> 200 with member_id/tree_id/removed_at" || bad "DELETE member -> $CODE $BODY"

  # 6. GET /members no longer lists it
  req GET "/api/v1/trees/$TREE/members" "$TOKEN_OWNER"
  rec members_after_delete "$CODE" "$BODY"
  STILL="$(printf '%s' "$BODY" | jq -r --arg m "$MEMBER_ID" '[ (.members // .data // [])[] | select((.memberId // .member_id // .id) == $m) ] | length' 2>/dev/null)"
  [ "$STILL" = "0" ] && ok "GET /members no longer lists the removed member (net-zero)" || bad "removed member still listed (count=${STILL:-?})"
  FINAL_COUNT="$(printf '%s' "$BODY" | jq -r '(.members // .data // []) | length' 2>/dev/null)"
  [ "${FINAL_COUNT:-x}" = "${BASE_COUNT:-y}" ] && ok "member count back to baseline ($FINAL_COUNT)" || bad "member count drifted: baseline=${BASE_COUNT:-?} final=${FINAL_COUNT:-?}"
fi

# 7. negatives
req GET "/api/v1/trees/$TREE/members" "$TOKEN_GUEST"
rec neg_nonmember_get "$CODE" "$BODY"
[ "$CODE" = "403" ] && ok "non-member GET /members -> 403" || bad "non-member GET /members -> $CODE (want 403)"

req GET "/api/v1/trees/$TREE/members" ""
rec neg_no_token "$CODE" "$BODY"
[ "$CODE" = "401" ] && ok "no token -> 401" || bad "no token -> $CODE (want 401)"

req GET "/api/v1/trees/$TREE/members" "$TOKEN_EXPIRED"
rec neg_expired_token "$CODE" "$BODY"
[ "$CODE" = "401" ] && ok "expired token -> 401" || bad "expired token -> $CODE (want 401)"

req GET "/api/v1/trees/$TREE/members" "$TOKEN_NONE"
rec neg_alg_none "$CODE" "$BODY"
case "$CODE" in 401|403) ok "unsigned alg=none token rejected -> $CODE" ;; *) bad "alg=none token -> $CODE (want 401/403)" ;; esac

note "== RESULT: $FAILS failure(s); evidence $EVID =="
exit "$FAILS"
