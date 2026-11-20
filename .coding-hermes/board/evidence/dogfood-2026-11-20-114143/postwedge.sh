#!/usr/bin/env bash
# Post-wedge-proof checks on a FRESH boot: manifest omissions, profiles list vs
# docs, 412 body, import shape, metrics histogram names.
set -u
ROOT=/tmp/canopy-df19
B=http://127.0.0.1:18093
OLDPID=$(cat "$ROOT/server.pid" 2>/dev/null)
[ -n "$OLDPID" ] && kill -TERM "$OLDPID" 2>/dev/null; sleep 1
kill -0 "$OLDPID" 2>/dev/null && kill -KILL "$OLDPID" 2>/dev/null
"$ROOT/boot.sh" >/dev/null 2>&1 || { echo "BOOT FAILED"; exit 1; }
echo "fresh boot pid $(cat $ROOT/server.pid)"
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

# fresh corpus: tree, 2 nodes (root + reply), card on reply
T=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"title":"df20 main tree","description":"dogfood 2026-11-20 real use","rootMessage":{"content":"Root: I am planning a workshop for 12 people and need a venue shortlist.","contentFormat":"markdown","nodeType":"message"}}' "$B/api/v1/trees")
TREE=$(printf '%s' "$T" | jq -r '.id // .tree.id'); ROOTN=$(printf '%s' "$T" | jq -r '.root_node_id')
N=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "{\"parent_id\":\"$ROOTN\",\"content\":\"Reply: venue criteria are capacity >= 12, projector, < 30 min travel. Shortlist: Library room A, Community hall B.\",\"node_type\":\"message\",\"edge_type\":\"reply\"}" "$B/api/v1/trees/$TREE/nodes")
REPLY=$(printf '%s' "$N" | jq -r '.node.id')
F=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "{\"parent_id\":\"$ROOTN\",\"content\":\"Fork: online-only alternative\",\"node_type\":\"message\",\"edge_type\":\"fork\"}" "$B/api/v1/trees/$TREE/nodes")
FORK=$(printf '%s' "$F" | jq -r '.node.id')
C=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "{\"treeId\":\"$TREE\",\"nodeId\":\"$REPLY\",\"appId\":\"df20\",\"cardType\":\"compact\",\"data\":{\"title\":\"Venue notes\",\"body\":\"Library A holds 14, has projector\"}}" "$B/api/v1/cards")
CARD=$(printf '%s' "$C" | jq -r '.id')
M=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "{\"source_node_ids\":[\"$REPLY\",\"$FORK\"],\"content\":\"Synthesis: hybrid venue + stream option\",\"content_format\":\"markdown\",\"target_parent_id\":\"$ROOTN\"}" "$B/api/v1/trees/$TREE/merge")
SYNTH=$(printf '%s' "$M" | jq -r '.node.id')
cat >> "$ROOT/state.env" <<EOF
TREE1=$TREE
ROOTN=$ROOTN
REPLY=$REPLY
FORK=$FORK
SYNTH=$SYNTH
CARD=$CARD
EOF
echo "corpus: tree=$TREE reply=$REPLY card=$CARD synth=$SYNTH"

echo "== manifest omission transparency: default vs budget=32"
curl -s -H "Authorization: Bearer $TOKEN" -o "$ROOT/def.json" "$B/api/v1/context/$REPLY"
curl -s -H "Authorization: Bearer $TOKEN" -o "$ROOT/tiny.json" "$B/api/v1/context/$REPLY?budget=32"
echo "default: $(jq -c '{budget:.manifest.tokenBudget,used:.manifest.tokensUsed,omitted:.manifest.omittedCount,anc:(.manifest.ancestry|length)}' $ROOT/def.json)"
echo "tiny:    $(jq -c '{budget:.manifest.tokenBudget,used:.manifest.tokensUsed,omitted:.manifest.omittedCount,reason:.manifest.omittedReason,trunc:(.manifest.truncationMarkers|type),warn:(.manifest.warnings|type),anc:(.manifest.ancestry|length)}' $ROOT/tiny.json)"
echo "manifest keys: $(jq -r '.manifest|keys|join(",")' $ROOT/def.json)"
echo "default ancestry last-node content: $(jq -r '[.manifest.ancestry[]|.node.content][-1]' $ROOT/def.json | head -c 60)"
echo "tiny ancestry last-node content:    $(jq -r '[.manifest.ancestry[]|.node.content][-1]' $ROOT/tiny.json | head -c 60)"

echo "== profiles list: docs route vs live"
grep -n 'List Profiles' -A 6 /home/kara/hermes-canopy/docs/API.md | sed -n '1,8p'
curl -s --max-time 5 -o /dev/null -w '  GET /api/v1/profiles -> %{http_code}\n' -H "Authorization: Bearer $TOKEN" "$B/api/v1/profiles"
curl -s --max-time 5 -o pl.json -w '  GET /api/v1/gateway/profile-mappings -> %{http_code}\n' -H "Authorization: Bearer $TOKEN" "$B/api/v1/gateway/profile-mappings"

echo "== 412 body (revision present?)"
curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -H 'If-Match: 1' -d '{"data":{"title":"x"}}' -w '\ncode=%{http_code}\n' "$B/api/v1/cards/$CARD" | head -c 500

echo
echo "== import response shape"
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$(cat $ROOT/export.json)" -w '\ncode=%{http_code}\n' "$B/api/v1/trees/import" > "$ROOT/import.out"
head -c 400 "$ROOT/import.out"; echo

echo "== metrics: histogram series for compile/cards"
curl -s -H "Authorization: Bearer $TOKEN" "$B/metrics" | grep -E '^canopy.*(compile|card|histogram|seconds)' | grep -vE '^#' | head -8
curl -s -H "Authorization: Bearer $TOKEN" "$B/metrics" | grep -E 'HELP.*(compile|card_export|proxy)' | head -6
