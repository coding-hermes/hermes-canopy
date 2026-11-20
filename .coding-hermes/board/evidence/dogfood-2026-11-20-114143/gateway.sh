#!/usr/bin/env bash
# Gateway leg: status/models, plain run, tool-using run (DF-66 retest),
# node-linked run (context compile + DF-67 output-in-tree retest), 412 body,
# metrics scrape after activity.
set -u
. /tmp/canopy-df19/state.env
B=http://127.0.0.1:18093
step() { echo "== $1"; }
pollrun() { # pollrun RUN_ID MAX_S -> final status + elapsed
  local id=$1 maxs=$2 t0=$(date +%s) st="" i=0
  while [ $(( $(date +%s) - t0 )) -lt "$maxs" ]; do
    st=$(curl -s --max-time 5 -H "Authorization: Bearer $TOKEN" "$B/api/v1/gateway/runs/$id" | jq -r '.status // "?"' 2>/dev/null)
    case "$st" in completed|failed|disconnected|cancelled) break ;; esac
    sleep 2
  done
  echo "$st elapsed=$(( $(date +%s) - t0 ))s"
}

step "gateway status"
curl -s --max-time 8 -H "Authorization: Bearer $TOKEN" "$B/api/v1/gateway/status" -w '\n  code=%{http_code} time=%{time_total}\n' | head -c 400

step "gateway models (catalog)"
curl -s --max-time 8 -H "Authorization: Bearer $TOKEN" "$B/api/v1/gateway/models" -w '\n  code=%{http_code}\n' | head -c 300

step "RUN1 plain (no tool): Reply with exactly: ok"
R1=$(curl -s --max-time 10 -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"message":"Reply with exactly: ok"}' -w '\n%{http_code}' "$B/api/v1/gateway/runs")
R1CODE=$(printf '%s' "$R1" | tail -1)
R1BODY=$(printf '%s' "$R1" | sed '$d')
RUN1=$(printf '%s' "$R1BODY" | jq -r '.run_id // .id')
echo "  202-code=$R1CODE run=$RUN1 status-now=$(printf '%s' "$R1BODY" | jq -r '.status')"
echo "  poll: $(pollrun "$RUN1" 90)"
curl -s --max-time 8 -H "Authorization: Bearer $TOKEN" "$B/api/v1/gateway/runs/$RUN1" -o /tmp/canopy-df19/run1.json
echo "  final: $(jq -c '{status,error:(.error|type),events:(.events|length),output:((.output // .result // "")|tostring|.[0:80])}' /tmp/canopy-df19/run1.json)"
echo "RUN1=$RUN1" >> /tmp/canopy-df19/state.env

step "RUN2 tool-using (DF-66 retest)"
R2=$(curl -s --max-time 10 -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"message":"Use the shell tool to run the command: echo df20-df66-check , then reply with its exact output."}' -w '\n%{http_code}' "$B/api/v1/gateway/runs")
R2CODE=$(printf '%s' "$R2" | tail -1)
R2BODY=$(printf '%s' "$R2" | sed '$d')
RUN2=$(printf '%s' "$R2BODY" | jq -r '.run_id // .id')
echo "  202-code=$R2CODE run=$RUN2"
echo "  poll: $(pollrun "$RUN2" 120)"
curl -s --max-time 8 -H "Authorization: Bearer $TOKEN" "$B/api/v1/gateway/runs/$RUN2" -o /tmp/canopy-df19/run2.json
echo "  final: $(jq -c '{status,error:(.error|type),events:(.events|length)}' /tmp/canopy-df19/run2.json)"
echo "  event types: $(jq -r '[.events[]?.event_type // .events[]?.type] | unique | join(",")' /tmp/canopy-df19/run2.json 2>/dev/null | head -c 200)"
echo "  observe_error present: $(grep -c observe_error /tmp/canopy-df19/run2.json)"
echo "  output head: $(jq -r '(.output // .result // "")|tostring|.[0:120]' /tmp/canopy-df19/run2.json)"
echo "RUN2=$RUN2" >> /tmp/canopy-df19/state.env

step "RUN3 node-linked (context compiler in run path; DF-67 output-in-tree)"
R3=$(curl -s --max-time 15 -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "{\"message\":\"Reply with exactly: context ok\",\"node_id\":\"$REPLY\"}" -w '\n%{http_code}' "$B/api/v1/gateway/runs")
R3CODE=$(printf '%s' "$R3" | tail -1)
R3BODY=$(printf '%s' "$R3" | sed '$d')
RUN3=$(printf '%s' "$R3BODY" | jq -r '.run_id // .id')
echo "  202-code=$R3CODE run=$RUN3"
echo "  202 carries manifest: $(printf '%s' "$R3BODY" | jq -c '{manifestHash:(.manifest.manifestHash // "?")[0:12],budget:(.manifest.tokenBudget // .tokenBudget // "?")}')"
echo "  poll: $(pollrun "$RUN3" 120)"
curl -s --max-time 8 -H "Authorization: Bearer $TOKEN" "$B/api/v1/gateway/runs/$RUN3" -o /tmp/canopy-df19/run3.json
echo "  final: $(jq -c '{status,error:(.error|type)}' /tmp/canopy-df19/run3.json)"
echo "  manifest on record: $(jq -r '(.manifest.manifestHash // "none")[0:12]' /tmp/canopy-df19/run3.json)"
echo "  DF-67 output-in-tree: nodes on tree now:"
curl -s --max-time 8 -H "Authorization: Bearer $TOKEN" "$B/api/v1/trees/$TREE1/nodes" | jq -r '.nodes[]? | "\(.sequenceNum // .sequence_num) \(.nodeType // .node_type) \(.content[0:60])"' | tail -6
echo "RUN3=$RUN3" >> /tmp/canopy-df19/state.env

step "412 stale body shape (If-Match: 1 against rev 2)"
curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -H 'If-Match: 1' -d '{"data":{"title":"stale"}}' -w '\ncode=%{http_code}\n' "$B/api/v1/cards/$CARD" | head -c 700

echo
step "metrics after activity: compile + resume histograms"
curl -s -H "Authorization: Bearer $TOKEN" "$B/metrics" -o /tmp/canopy-df19/metrics.txt
grep -E 'canopy_context_compile_duration_seconds_(count|sum)' /tmp/canopy-df19/metrics.txt
grep -E 'canopy_resume_duration_seconds_(count|sum)' /tmp/canopy-df19/metrics.txt
grep -E 'canopy_(gateway_run|proxy|card_export)' /tmp/canopy-df19/metrics.txt | grep -v '^#' | head -6
