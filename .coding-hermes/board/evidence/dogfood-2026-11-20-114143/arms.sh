#!/usr/bin/env bash
# Control arms for the includeCards hang + manifest omissions + route checks.
set -u
. /tmp/canopy-df19/state.env
B="http://127.0.0.1:18093"
step() { echo "== $1"; }

step "arm A: REPLY?includeCards=true (node HAS card) — 8s cap"
curl -s --max-time 8 -o /dev/null -w 'code=%{http_code} time=%{time_total}\n' -H "Authorization: Bearer $TOKEN" "$B/api/v1/context/$REPLY?includeCards=true"

step "arm B: FORK?includeCards=true (node has NO card) — 8s cap"
curl -s --max-time 8 -o armB.json -w 'code=%{http_code} time=%{time_total}\n' -H "Authorization: Bearer $TOKEN" "$B/api/v1/context/$FORK?includeCards=true"
[ -s armB.json ] && jq -c '{budget:.manifest.tokenBudget,used:.manifest.tokensUsed,cards:(.manifest.cards|length)}' armB.json

step "arm C: REPLY default (no param)"
curl -s --max-time 8 -o /dev/null -w 'code=%{http_code} time=%{time_total}\n' -H "Authorization: Bearer $TOKEN" "$B/api/v1/context/$REPLY"

step "tiny-budget vs default ancestry diff"
curl -s -H "Authorization: Bearer $TOKEN" -o def.json "$B/api/v1/context/$REPLY"
curl -s -H "Authorization: Bearer $TOKEN" -o tiny.json "$B/api/v1/context/$REPLY?budget=64"
echo "default ancestry ids:  $(jq -c '[.manifest.ancestors[].nodeId] // [.manifest.ancestry[].nodeId]' def.json 2>/dev/null | head -c 300)"
echo "tiny ancestry ids:     $(jq -c '[.manifest.ancestors[].nodeId] // [.manifest.ancestry[].nodeId]' tiny.json 2>/dev/null | head -c 300)"
echo "default keys: $(jq -r '.manifest | keys | join(",")' def.json)"

step "profile list route — what docs say vs what answers"
grep -n 'GET /api/v1/profiles' /home/kara/hermes-canopy/docs/API.md | head -3
sed -n '3053,3083p' /home/kara/hermes-canopy/docs/API.md | head -25
echo "-- live --"
for p in /api/v1/profiles /api/v1/profiles/list /api/v1/gateway/profiles; do
  curl -s --max-time 5 -o /dev/null -w "  $p -> %{http_code}\n" -H "Authorization: Bearer $TOKEN" "$B$p"
done

step "412 body shape (revision in error body?)"
curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -H 'If-Match: 1' -d '{"data":{"title":"x"}}' -w '\ncode=%{http_code}\n' "$B/api/v1/cards/$CARD" | head -c 600

echo
step "import response shape"
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$(cat /tmp/canopy-df19/export.json)" -w '\ncode=%{http_code}\n' "$B/api/v1/trees/import" | head -c 400
