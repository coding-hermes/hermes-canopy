#!/usr/bin/env bash
# test-bunker-qa-chaos-cells.sh — QA-HERMES-CANOPY-23 + 29 regression test.
#
# Extracts corr_grade() and ep_grade() from the GENERATED remote script emitted
# by the host QA harness, then drives both pure classifiers against fixtures.
# The old classifiers are preserved in /tmp/qa23-old-corr.txt and
# /tmp/qa23-old-ep.txt; the second run splices them into a generated-script
# copy and must fail the same assertions.
#
# Run from the repo root:
#   bash scripts/test-bunker-qa-chaos-cells.sh

set -u
SOURCE="${BUNKER_QA_SCRIPT:-$HOME/.hermes/scripts/bunker-qa.sh}"
OLD_CORR=/tmp/qa23-old-corr.txt
OLD_EP=/tmp/qa23-old-ep.txt
[ -f "$SOURCE" ] || { echo "FAIL: source script not found: $SOURCE" >&2; exit 1; }
[ -s "$OLD_CORR" ] || { echo "FAIL: saved old corr_grade is missing: $OLD_CORR" >&2; exit 1; }
[ -s "$OLD_EP" ] || { echo "FAIL: saved old ep_grade is missing: $OLD_EP" >&2; exit 1; }

ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
PROJECT="$ROOT/project"
mkdir -p "$PROJECT"
GEN="${BUNKER_QA_GENERATED:-$ROOT/generated-remote.sh}"
if [ -z "${BUNKER_QA_GENERATED:-}" ]; then
  "$SOURCE" __gen-remote "$PROJECT" > "$GEN" || {
    echo "FAIL: could not generate remote QA script" >&2
    exit 1
  }
fi

CORR_FUNC=$(awk '/^corr_grade\(\) \{/{f=1} f{print} f&&/^\}$/{exit}' "$GEN")
EP_FUNC=$(awk '/^ep_grade\(\) \{/{f=1} f{print} f&&/^\}$/{exit}' "$GEN")
[ -n "$CORR_FUNC" ] || { echo "FAIL: could not extract corr_grade from $GEN" >&2; exit 1; }
[ -n "$EP_FUNC" ] || { echo "FAIL: could not extract ep_grade from $GEN" >&2; exit 1; }

passed=0
failed=0
ok()  { echo "  ok: $1"; passed=$((passed+1)); }
bad() { echo "  FAIL: $1"; failed=$((failed+1)); }
check() { # name, got, want
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (want: [$3] got: [$2])"; fi
}
contains() { printf '%s' "$1" | grep -qF -- "$2"; }

run_corr() { # repo, rc, log, state path, ref note, tracked yes/no
  local repo="$1" rc="$2" logfile="$3" state="$4" ref="$5" tracked="$6"
  (
    cd "$repo" || exit 1
    eval "$CORR_FUNC"
    corr_grade "$rc" "$logfile" "$state" "$ref" "$tracked"
  )
}
run_ep() { # rc, log, probe started yes/no
  local rc="$1" logfile="$2" started="$3"
  (
    eval "$EP_FUNC"
    ep_grade "$rc" "$logfile" "$started"
  )
}
result_status() { printf '%s' "$1" | cut -f1; }
result_detail() { printf '%s' "$1" | cut -f2-; }

new_repo() {
  local repo="$1"
  mkdir -p "$repo"
  git -C "$repo" init -q
  git -C "$repo" config user.name qa-test
  git -C "$repo" config user.email qa-test@example.invalid
}

run_suite() {
  passed=0
  failed=0
  local repo stray_log state_log result status detail tracked ref

  echo "== chaos-cell classifier regression (QA-HERMES-CANOPY-23+29) =="

  echo "[1] untracked, unreferenced corruption candidate is not a state store"
  repo="$ROOT/stray-repo"
  new_repo "$repo"
  printf 'unrelated artifact\n' > "$repo/stray.db"
  printf 'clean start\n' > "$ROOT/stray.log"
  tracked=no
  ref='not referenced by any source file'
  result=$(run_corr "$repo" 0 "$ROOT/stray.log" './stray.db' "$ref" "$tracked")
  status=$(result_status "$result"); detail=$(result_detail "$result")
  check "stray candidate is INFO, not OK" "$status" INFO
  contains "$detail" 'candidate ./stray.db is untracked and referenced by no source file' \
    && ok "stray detail names both failed state-store tests" \
    || bad "stray detail omits untracked/unreferenced reason: $detail"
  contains "$detail" 'state-restart behavior UNVERIFIED' \
    && ok "stray detail says restart behavior is unverified" \
    || bad "stray detail does not say restart behavior is unverified: $detail"

  echo "[2] genuine tracked + referenced store keeps corruption semantics"
  repo="$ROOT/genuine-repo"
  new_repo "$repo"
  printf 'state.db\n' > "$repo/main.go"
  printf 'state bytes\n' > "$repo/state.db"
  git -C "$repo" add main.go state.db
  git -C "$repo" commit -q -m fixture
  printf 'clean start\n' > "$ROOT/genuine-clean.log"
  tracked=yes
  ref='referenced by ./main.go'
  result=$(run_corr "$repo" 0 "$ROOT/genuine-clean.log" './state.db' "$ref" "$tracked")
  check "tracked store rc=0 remains OK" "$(result_status "$result")" OK
  contains "$(result_detail "$result")" 'next start unaffected by a truncated ./state.db' \
    && ok "tracked store OK detail preserves restart wording" \
    || bad "tracked store OK detail changed unexpectedly: $(result_detail "$result")"

  printf 'panic: corrupted state\n' > "$ROOT/genuine-panic.log"
  result=$(run_corr "$repo" 1 "$ROOT/genuine-panic.log" './state.db' "$ref" "$tracked")
  check "tracked store crash is FAIL" "$(result_status "$result")" FAIL
  printf 'corrupt database\n' > "$ROOT/genuine-corrupt.log"
  result=$(run_corr "$repo" 1 "$ROOT/genuine-corrupt.log" './state.db' "$ref" "$tracked")
  check "tracked store corruption error is OK" "$(result_status "$result")" OK

  echo "[3] errorpath grades only a started app's clean non-zero exit as OK"
  printf 'missing config: config.json not found\n' > "$ROOT/app-error.log"
  result=$(run_ep 2 "$ROOT/app-error.log" yes)
  check "started app clean error is OK" "$(result_status "$result")" OK
  contains "$(result_detail "$result")" 'clean error on missing config' \
    && ok "started app detail names missing-config error" \
    || bad "started app detail lost missing-config wording: $(result_detail "$result")"

  printf "go: go.mod file not found in current directory or any parent directory; see 'go help modules'\n" > "$ROOT/go-refusal.log"
  result=$(run_ep 1 "$ROOT/go-refusal.log" no)
  check "module refusal without app start is INFO" "$(result_status "$result")" INFO
  contains "$(result_detail "$result")" 'go.mod file not found' \
    && ok "module refusal detail names the observed refusal" \
    || bad "module refusal detail omitted the refusal: $(result_detail "$result")"
  printf 'no Go files\n' > "$ROOT/no-go-files.log"
  result=$(run_ep 1 "$ROOT/no-go-files.log" no)
  check "no-Go-files refusal is INFO" "$(result_status "$result")" INFO
  printf 'panic: bad config\n' > "$ROOT/app-panic.log"
  result=$(run_ep 2 "$ROOT/app-panic.log" yes)
  check "started app panic is FAIL" "$(result_status "$result")" FAIL
  printf 'still waiting\n' > "$ROOT/app-timeout.log"
  result=$(run_ep 124 "$ROOT/app-timeout.log" yes)
  check "timeout remains INFO/unverified" "$(result_status "$result")" INFO
  : > "$ROOT/no-command.log"
  result=$(run_ep 1 "$ROOT/no-command.log" na)
  check "no start command remains N/A" "$(result_status "$result")" N/A

  echo
  if [ "$failed" -eq 0 ]; then
    echo "PASS: $passed assertions"
    return 0
  fi
  echo "FAIL: $failed of $((passed+failed)) assertions failed." >&2
  return 1
}

if [ -n "${QA23_RED_PROOF_RUN:-}" ]; then
  run_suite
  rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "RED-PROOF RESULT: UNEXPECTED PASS (saved pre-fix classifiers did not fail the regression)" >&2
    exit 1
  fi
  echo "RED-PROOF RESULT: expected FAIL against saved pre-fix classifiers"
  exit 0
fi

run_suite || exit 1

echo "[4] pre-fix classifier red proof"
OLD_GEN="$ROOT/generated-old-remote.sh"
awk -v old_corr="$OLD_CORR" -v old_ep="$OLD_EP" '
  BEGIN {
    n = 0; while ((getline line < old_corr) > 0) corr[++n] = line; close(old_corr)
    m = 0; while ((getline line < old_ep) > 0) ep[++m] = line; close(old_ep)
  }
  /^corr_grade\(\) \{/ {
    for (i = 1; i <= n; i++) print corr[i]
    replacing = "corr"; next
  }
  /^ep_grade\(\) \{/ {
    for (i = 1; i <= m; i++) print ep[i]
    replacing = "ep"; next
  }
  replacing && /^\}$/ { replacing = ""; next }
  !replacing { print }
' "$GEN" > "$OLD_GEN"
[ -s "$OLD_GEN" ] || { echo "FAIL: could not build pre-fix generated-script copy" >&2; exit 1; }

if BUNKER_QA_GENERATED="$OLD_GEN" QA23_RED_PROOF_RUN=1 bash "$0"; then
  echo "RED-PROOF RESULT: expected FAIL against saved pre-fix classifiers"
else
  echo "RED-PROOF RESULT: UNEXPECTED PASS (pre-fix classifier run exited non-zero)" >&2
  exit 1
fi

echo "PASS: fixed classifiers green; pre-fix classifiers red"
echo "PASS: $passed assertions"
exit 0
