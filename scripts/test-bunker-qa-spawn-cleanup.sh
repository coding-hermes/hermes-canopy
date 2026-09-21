#!/usr/bin/env bash
# test-bunker-qa-spawn-cleanup.sh — QA-HERMES-CANOPY-16 (+22 harness companion) regression test.
#
# Source-extracts spawn_agent() from the HOST script
# ~/.hermes/scripts/bunker-qa.sh (not git-tracked) and drives it directly
# against a PATH-shimmed fake `bunker` CLI and fake `ssh`, asserting the
# orphan-cleanup contract:
#   1. hard-fail: bunker spawn fails every attempt → 3 spawn attempts,
#      rc 1, stdout empty, 0 destroys (nothing was ever created).
#   2. spawned-but-unusable: spawn yields an id but the agent is unreachable
#      via ssh on EVERY attempt → every created-but-not-handed-off id MUST be
#      destroyed (destroy count == created count), every destroyed id names an
#      actually-created id, stdout stays empty, rc 1, and each attempt emits
#      an audit line to stderr.
#      Field evidence: orphan agent cefd6920, bunker-las-02, 2026-09-17.
#   3. retry succeeds: attempt 1 unusable (destroyed), attempt 2 usable →
#      rc 0, stdout is ONLY the handed-off agent id, exactly 1 destroy of the
#      abandoned attempt-1 id, and the handed-off id is NEVER destroyed.
#   4. Ordering: every destroy immediately follows its own spawn and is
#      followed only by another spawn (or the ladder end) — destroy-before-
#      -retry with no orphan between attempts.
#
# RED-PROOF CONTRACT: this test must FAIL against the OLD spawn_agent (no
# post-spawn reachability check, no destroy of abandoned ids) and PASS against
# the NEW one. The original function is saved to /tmp/qa16-old-spawn_agent.txt
# at edit time; re-run via BUNKER_QA_SCRIPT pointing at a copy of the OLD
# script to prove the red.
#
# Uses only bash + coreutils. Run from the repo root:
#   bash scripts/test-bunker-qa-spawn-cleanup.sh

set -u
SOURCE="${BUNKER_QA_SCRIPT:-$HOME/.hermes/scripts/bunker-qa.sh}"
[ -f "$SOURCE" ] || { echo "FAIL: source script not found: $SOURCE" >&2; exit 1; }

FUNC=$(awk '/^spawn_agent\(\) \{/{f=1} f{print} f&&/^\}$/{exit}' "$SOURCE")
LOGDEF=$(awk '/^log\(\)/{print; exit}' "$SOURCE")
[ -n "$FUNC" ]  || { echo "FAIL: could not extract spawn_agent from $SOURCE" >&2; exit 1; }
[ -n "$LOGDEF" ] || { echo "FAIL: could not extract log() from $SOURCE" >&2; exit 1; }

passed=0; failed=0
ok()  { echo "  ok: $1"; passed=$((passed+1)); }
bad() { echo "  FAIL: $1"; failed=$((failed+1)); }
check() { # name, got, want
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (want: [$3] got: [$2])"; fi
}

RUN_STDOUT=""; LOG_TXT=""; RC=""
STDERR_LINES=()

# run_spawn drives the extracted spawn_agent under scenario env vars:
#   BQ_F_BUNKER_MODE=hardfail → `bunker spawn` always rc 1, prints no id
#   BQ_F_BUNKER_MODE=ids      → `bunker spawn` rc 0, prints a FRESH id each call
#   BQ_F_SSH_MODE=pass|fail|failfirst → the helper's ssh reachability probe
#                                pass / always-fail / fail-first-call-then-pass
# The fake bunker logs `spawn <args> keys/<id>` (id inline) and `destroy <id>`
# in invocation order; ids come from an increasing counter file so each spawn
# creates a NEW agent.
run_spawn() {
  local tmp; tmp=$(mktemp -d)
  mkdir -p "$tmp/bin"
  local logfile="$tmp/log"; : > "$logfile"
  printf '0\n' > "$tmp/ctr"

  cat > "$tmp/bin/bunker" <<'EOS'
#!/usr/bin/env bash
sub=$1; shift
case "$sub" in
  spawn)
    if [ "${BQ_F_BUNKER_MODE:-hardfail}" = ids ]; then
      ctr=$(( $(cat "$CTR") + 1 )); echo "$ctr" > "$CTR"
      id=$(printf '%016x' "$ctr")
      echo "spawn $* keys/$id" >> "$LOG"
      printf 'keys/%s ok\n' "$id"
    else
      echo "spawn $*" >> "$LOG"
      echo "capacity full: C/C agents" >&2
      exit 1
    fi ;;
  destroy) echo "destroy $1" >> "$LOG" ;;
esac
EOS
  cat > "$tmp/bin/ssh" <<'EOS'
#!/usr/bin/env bash
case "${BQ_F_SSH_MODE:-pass}" in
  fail) exit 255 ;;
  failfirst)
    n=$(($(cat "$SEEN") + 1)); echo "$n" > "$SEEN"
    [ "$n" -ge 2 ] && exit 0
    exit 255 ;;
  *) exit 0 ;;
esac
EOS
  printf '#!/bin/bash\n' > "$tmp/bin/sleep"; chmod +x "$tmp/bin/sleep"
  chmod +x "$tmp/bin/bunker" "$tmp/bin/ssh"

  (
    PATH="$tmp/bin:$PATH"
    export LOG="$logfile" BQ_F_BUNKER_MODE BQ_F_SSH_MODE CTR="$tmp/ctr" SEEN="$tmp/seen"
    export SERVER=bunkertest TTL=4h BUNKER_QA_RETRY_SLEEP=0
    eval "$LOGDEF"
    eval "$FUNC"
    spawn_agent
  ) > "$tmp/out" 2> "$tmp/err"
  RC=$?
  RUN_STDOUT=$(tr -d '\n' < "$tmp/out")
  mapfile -t STDERR_LINES < <(cat "$tmp/err")
  LOG_TXT=$(grep -E '^(spawn|destroy) ' "$logfile" || true)
  rm -rf "$tmp"
}
spawns()   { grep -c '^spawn ' <<< "$LOG_TXT" || true; }
destroys() { grep -c '^destroy ' <<< "$LOG_TXT" || true; }
# ids created by the fake bunker (the part it would write to ~/.bunker/keys/)
created_ids() { sed -n 's/^spawn .*keys\/\([0-9a-f]\+\)$/\1/p' <<< "$LOG_TXT"; }

echo "== spawn_agent orphan-cleanup regression (QA-HERMES-CANOPY-16) =="

# ── Scenario 1: bunker spawn hard-fails every attempt, no ids ever created ──
echo "[1] total spawn failure (capacity-full ladder)"
BQ_F_BUNKER_MODE=hardfail BQ_F_SSH_MODE=pass run_spawn
check "hardfail: rc=1 on total failure" "$RC" "1"
check "hardfail: stdout empty (no id handed off)" "$RUN_STDOUT" ""
check "hardfail: 3 spawn attempts" "$(spawns)" "3"
check "hardfail: 0 destroys (nothing was created, nothing to abandon)" "$(destroys)" "0"

# ── Scenario 2: spawn yields an id, ssh probe fails every attempt ──
echo "[2] spawned-but-unusable ladder (orphan cefd6920 shape)"
BQ_F_BUNKER_MODE=ids BQ_F_SSH_MODE=fail run_spawn
check "unusable: rc=1 after ladder exhausted" "$RC" "1"
check "unusable: stdout empty (no id handed off)" "$RUN_STDOUT" ""
check "unusable: 3 spawn attempts" "$(spawns)" "3"
check "unusable: destroy count == IDs created but not handed off (3)" "$(destroys)" "$(spawns)"
MISSING=0
for id in $(created_ids); do
  grep -q "^destroy $id$" <<< "$LOG_TXT" || MISSING=$((MISSING+1))
done
check "unusable: every created id was destroyed before return" "$MISSING" "0"
AUDIT_OK=1
AUDIT_COUNT=0
for line in "${STDERR_LINES[@]}"; do
  echo "$line" | grep -q "produced agent .* was unusable" && AUDIT_COUNT=$((AUDIT_COUNT+1))
done
check "unusable: one audit line on stderr per unusable attempt" "$AUDIT_COUNT" "$(created_ids | wc -l)"
AUDIT_ID=$(printf '%s\n' "${STDERR_LINES[@]}" | sed -n 's/.*produced agent \([0-9a-f]*\).*/\1/p' | head -1)
grep -q "^destroy $AUDIT_ID$" <<< "$LOG_TXT" \
  && ok "unusable: audit line names a destroyed id" \
  || bad "unusable: audit id '$AUDIT_ID' never destroyed"

# ── Scenario 3: attempt 1 unusable (destroyed), attempt 2 usable ──
echo "[3] retry succeeds on attempt 2, handed-off id never destroyed"
BQ_F_BUNKER_MODE=ids BQ_F_SSH_MODE=failfirst run_spawn
check "retry-success: rc=0" "$RC" "0"
check "retry-success: stdout is ONLY the agent id (16 hex chars)" \
      "$([[ "$RUN_STDOUT" =~ ^[0-9a-f]{16}$ ]] && echo yes || echo no)" "yes"
check "retry-success: 2 spawn attempts" "$(spawns)" "2"
check "retry-success: exactly 1 destroy (abandoned attempt-1 id)" "$(destroys)" "1"
FIRST_CREATED=$(created_ids | head -1)
grep -q "^destroy $RUN_STDOUT$" <<< "$LOG_TXT" \
  && bad "retry-success: HANDED-OFF id was destroyed (contract break)" \
  || ok  "retry-success: handed-off id never destroyed"
grep -q "^destroy $FIRST_CREATED$" <<< "$LOG_TXT" \
  && ok  "retry-success: destroyed id IS the abandoned attempt-1 id" \
  || bad "retry-success: expected destroy of attempt-1 id '$FIRST_CREATED'"

# ── Scenario 4: destroy-before-retry ordering across the scenario-3 ladder ──
echo "[4] destroy-before-retry ordering (scenario 3 ladder)"
BEFORE_OK=1; AFTER_OK=1
mapfile -t LINES < <(grep -E '^(spawn|destroy) ' <<< "$LOG_TXT")
for i in "${!LINES[@]}"; do
  if [[ "${LINES[$i]}" == destroy* ]]; then
    prev=${LINES[$((i-1))]:-}
    [[ "$prev" == spawn* ]] || BEFORE_OK=0
    if [ $((i+1)) -lt ${#LINES[@]} ]; then
      [[ "${LINES[$((i+1))]}" == spawn* ]] || AFTER_OK=0
    fi
  fi
done
check "ordering: every destroy immediately follows its own spawn" "$BEFORE_OK" "1"
check "ordering: mid-ladder destroys followed only by a spawn or ladder end" "$AFTER_OK" "1"

echo
if [ "$failed" -eq 0 ]; then
  echo "PASS: $passed assertions"
  exit 0
fi
echo "FAIL: $failed of $((passed+failed)) assertions failed. Abandoned bunker" >&2
echo "agents hold slots until TTL and appear as orphans with no evidence pointing" >&2
echo "at them (QA-16; field evidence agent cefd6920, 2026-09-17)." >&2
exit 1
