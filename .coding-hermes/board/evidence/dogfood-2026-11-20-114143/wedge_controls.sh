#!/usr/bin/env bash
# Final controls: includeCards on a card-less tree; wedge scope across trees.
set -u
. /tmp/canopy-df19/state.env
B=http://127.0.0.1:18093

echo "== fresh card-LESS tree + node"
T=$(curl -s --max-time 8 -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"title":"no-cards tree","rootMessage":{"content":"root","contentFormat":"markdown","nodeType":"message"}}' "$B/api/v1/trees")
T3=$(printf '%s' "$T" | jq -r '.id // .tree.id'); R3=$(printf '%s' "$T" | jq -r '.root_node_id')
echo "  tree3=$T3 root=$R3"

echo "== compile includeCards=true on card-less tree, cap 8s"
curl -s --max-time 8 -o /dev/null -w '  code=%{http_code} time=%{time_total}\n' -H "Authorization: Bearer $TOKEN" "$B/api/v1/context/$R3?includeCards=true"

echo "== is cards surface now wedged for the card-less tree too? create card on tree3, cap 8s"
curl -s --max-time 8 -o /dev/null -w '  code=%{http_code} time=%{time_total}\n' -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "{\"treeId\":\"$T3\",\"nodeId\":\"$R3\",\"appId\":\"w\",\"cardType\":\"compact\",\"data\":{\"title\":\"x\",\"body\":\"y\"}}" "$B/api/v1/cards"

echo "== cross-tree: card GET on TREE1 card (wedged), cap 5s"
curl -s --max-time 5 -o /dev/null -w '  code=%{http_code} time=%{time_total}\n' -H "Authorization: Bearer $TOKEN" "$B/api/v1/cards/$CARD"

echo "== compile tree3 node WITHOUT includeCards (control), cap 5s"
curl -s --max-time 5 -o /dev/null -w '  code=%{http_code} time=%{time_total}\n' -H "Authorization: Bearer $TOKEN" "$B/api/v1/context/$R3"
