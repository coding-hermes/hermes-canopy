#!/usr/bin/env bash
# Wedge proof: fresh boot -> includeCards A/B -> card-store wedge check.
set -u
ROOT=/tmp/canopy-df19
B=http://127.0.0.1:18093
OLDPID=$(cat "$ROOT/server.pid" 2>/dev/null)
[ -n "$OLDPID" ] && kill -TERM "$OLDPID" 2>/dev/null; sleep 1
kill -0 "$OLDPID" 2>/dev/null && kill -KILL "$OLDPID" 2>/dev/null
echo "old server pid $OLDPID stopped"
"$ROOT/boot.sh" | tail -4
. "$ROOT/state.env" 2>/dev/null || true
# state.env has OLD token/ids; refresh token + fetch fresh ids from the new DB
TOKEN=$(python3 - <<'EOF'
import base64, hashlib, hmac, json, time
def b64(b): return base64.urlsafe_b64encode(b).rstrip(b"=")
h = b64(json.dumps({"alg":"HS256","typ":"JWT"},separators=(",",":")).encode())
now = int(time.time())
p = b64(json.dumps({"sub":"00000000-0000-0000-0000-000000000001","iat":now,"exp":now+7200},separators=(",",":")).encode())
s = b64(hmac.new(b"dev-secret-change-me", h+b"."+p, hashlib.sha256).digest())
print((h+b"."+p+b"."+s).decode())
EOF
)
echo "TOKEN=$TOKEN" > "$ROOT/state.env"
echo "BASE=$B" >> "$ROOT/state.env"
# fresh tree + node + card in the new DB
T=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"title":"wedge-proof tree","rootMessage":{"content":"root msg","contentFormat":"markdown","nodeType":"message"}}' "$B/api/v1/trees")
TREE=$(printf '%s' "$T" | jq -r '.id // .tree.id')
ROOTN=$(printf '%s' "$T" | jq -r '.root_node_id')
N=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "{\"parent_id\":\"$ROOTN\",\"content\":\"reply with card target\",\"node_type\":\"message\",\"edge_type\":\"reply\"}" "$B/api/v1/trees/$TREE/nodes")
REPLY=$(printf '%s' "$N" | jq -r '.node.id')
C=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "{\"treeId\":\"$TREE\",\"nodeId\":\"$REPLY\",\"appId\":\"wedge\",\"cardType\":\"compact\",\"data\":{\"title\":\"t\",\"body\":\"b\"}}" "$B/api/v1/cards")
CARD=$(printf '%s' "$C" | jq -r '.id')
echo "fresh: tree=$TREE reply=$REPLY card=$CARD"
# second node WITHOUT card (fork)
F=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "{\"parent_id\":\"$ROOTN\",\"content\":\"fork no card\",\"node_type\":\"message\",\"edge_type\":\"fork\"}" "$B/api/v1/trees/$TREE/nodes")
FORK=$(printf '%s' "$F" | jq -r '.node.id')
cat >> "$ROOT/state.env" <<EOF
TREE1=$TREE
ROOTN=$ROOTN
REPLY=$REPLY
FORK=$FORK
CARD=$CARD
EOF

echo "== arm B: compile FORK?includeCards=true (NO card in ancestry), cap 8s"
curl -s --max-time 8 -o /dev/null -w '  code=%{http_code} time=%{time_total}\n' -H "Authorization: Bearer $TOKEN" "$B/api/v1/context/$FORK?includeCards=true"

echo "== arm A: compile REPLY?includeCards=true (card ON node), cap 8s — expect hang"
curl -s --max-time 8 -o /dev/null -w '  code=%{http_code} time=%{time_total}\n' -H "Authorization: Bearer $TOKEN" "$B/api/v1/context/$REPLY?includeCards=true"

echo "== wedge check: card GET after the hang, cap 5s"
curl -s --max-time 5 -o /dev/null -w '  code=%{http_code} time=%{time_total}\n' -H "Authorization: Bearer $TOKEN" "$B/api/v1/cards/$CARD"

echo "== control: plain compile REPLY (no param), cap 5s"
curl -s --max-time 5 -o /dev/null -w '  code=%{http_code} time=%{time_total}\n' -H "Authorization: Bearer $TOKEN" "$B/api/v1/context/$REPLY"

echo "== control: trees list, cap 5s"
curl -s --max-time 5 -o /dev/null -w '  code=%{http_code} time=%{time_total}\n' -H "Authorization: Bearer $TOKEN" "$B/api/v1/trees"

echo "== second includeCards compile (is IT blocked now too?), cap 5s"
curl -s --max-time 5 -o /dev/null -w '  code=%{http_code} time=%{time_total}\n' -H "Authorization: Bearer $TOKEN" "$B/api/v1/context/$REPLY?includeCards=true"

echo "server pid: $(cat $ROOT/server.pid)"
