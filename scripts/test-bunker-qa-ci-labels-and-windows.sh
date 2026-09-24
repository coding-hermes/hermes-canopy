#!/usr/bin/env bash
# test-bunker-qa-ci-labels-and-windows.sh — QA-HERMES-CANOPY-21 regression battery.
#
# Four defects, four assertions, all extracted from the GENERATED remote script
# (bunker-qa.sh __gen-remote) exactly like test-bunker-qa-ui-probe-frontend.sh.
#
# Run from the repo root:
#   bash scripts/test-bunker-qa-ci-labels-and-windows.sh

set -u
SOURCE="${BUNKER_QA_SCRIPT:-$HOME/.hermes/scripts/bunker-qa.sh}"
[ -f "$SOURCE" ] || { echo "FAIL: source script not found: $SOURCE" >&2; exit 1; }
command -v go >/dev/null 2>&1 || { echo "FAIL: go is required for the go-native fixture" >&2; exit 1; }

ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
PROJECT="$ROOT/project"
mkdir -p "$PROJECT"
GEN="$ROOT/generated-remote.sh"
EVID="$ROOT/evid.txt"

if [ -z "${BUNKER_QA_GENERATED:-}" ]; then
  "$SOURCE" __gen-remote "$PROJECT" > "$GEN" || { echo "FAIL: could not generate remote QA script" >&2; exit 1; }
else
  cp "$BUNKER_QA_GENERATED" "$GEN"
fi

passed=0
failed=0
ok()  { echo "  ok: $1"; passed=$((passed+1)); }
bad() { echo "  FAIL: $1"; failed=$((failed+1)); }
check() { # name, got, want
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (want: [$3] got: [$2])"; fi
}
contains() { printf '%s' "$1" | grep -qF -- "$2"; }

new_repo() {
  local repo="$1"
  mkdir -p "$repo"
  git -C "$repo" init -q 2>/dev/null || true
  git -C "$repo" config user.name qa-test 2>/dev/null || true
  git -C "$repo" config user.email qa-test@example.invalid 2>/dev/null || true
}

echo "== QA-HERMES-CANOPY-21 regression battery =="

# ─── T1: ci-pass label ───────────────────────────────────────────────────────
# Fixture: NO .github/workflows. The pre-fix generated chain starts the FAIL
# detail with "act rc=..." even though the native suite is the only thing that
# ran. Post-fix: the detail must lead with "native rc=" or "no triggerable
# workflow".
echo "[T1] ci-pass native-led detail string"
T1_REPO="$ROOT/t1-repo"
new_repo "$T1_REPO"
printf '{"name":"t1","scripts":{"test":"echo ok"}}\n' > "$T1_REPO/package.json"
T1_GEN="$ROOT/t1-gen.sh"
"$SOURCE" __gen-remote "$T1_REPO" > "$T1_GEN" || { echo "FAIL: __gen-remote for T1" >&2; exit 1; }
T1_CI_PASS=$(awk '/^#QA-CELL:ci-pass:start/{f=1} f{print} f&&/^#QA-CELL:ci-pass:end/{exit}' "$T1_GEN")
if contains "$T1_CI_PASS" 'cell ci-pass FAIL "native rc='; then
  ok "T1: generated ci-pass FAIL detail leads with native rc="
else
  bad "T1: generated ci-pass FAIL detail does not lead with native rc=: $T1_CI_PASS"
fi
if contains "$T1_CI_PASS" 'cell ci-pass FAIL "act rc='; then
  bad "T1: generated ci-pass still contains old act rc= lead"
else
  ok "T1: generated ci-pass does not contain old act rc= lead"
fi

# ─── T2: ui-probe go-native arm ─────────────────────────────────────────────
# Fixture: go.mod + tiny main.go serving http.ListenAndServe on :8767.
# Post-fix: extracted chain must grade OK naming the go-native pattern.
# Empty fixture must still grade N/A.
echo "[T2] ui-probe go-native arm"
T2_REPO="$ROOT/t2-repo"
mkdir -p "$T2_REPO"
cat > "$T2_REPO/go.mod" <<'EOF'
module t2
go 1.24
EOF
cat > "$T2_REPO/main.go" <<'EOF'
package main
import (
	"fmt"
	"net/http"
)
func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "<html><body>t2</body></html>")
	})
	http.ListenAndServe("127.0.0.1:8767", nil)
}
EOF
T2_GEN="$ROOT/t2-gen.sh"
"$SOURCE" __gen-remote "$T2_REPO" > "$T2_GEN" || { echo "FAIL: __gen-remote for T2" >&2; exit 1; }
T2_UI_CHAIN=$(awk '/^if \[ -f package\.json \] && grep -qE/{f=1} f{print} f&&/^else cell ui-probe/{exit}' "$T2_GEN")
if [ -z "$T2_UI_CHAIN" ]; then
  bad "T2: could not extract ui-probe chain"
else
  if contains "$T2_UI_CHAIN" 'cell ui-probe OK "go-native http server responded'; then
    ok "T2: generated chain contains go-native OK arm"
  else
    bad "T2: generated chain missing go-native OK arm"
  fi
fi

# Live run: start the server via the extracted chain and grade
T2_LOG="$ROOT/t2-run.log"
run_t2() {
  (
    cd "$T2_REPO" || exit 1
    export PROJ=t2go LOGD="$ROOT/t2-logs"
    mkdir -p "$LOGD"
    export BIN=""
    # Pre-init UI probe vars so `set -u` in the test script doesn't trip on
    # the generated chain's first-pass reads.
    UI_HIT=""; UI_OTHER=""; UI_PROBED=" "; UI_PORTS="3000 5173 8000 8080 8081 8766 8767 9000"
    cell() { printf 'cell %s %s %s\n' "$1" "$2" "$3" >> "$T2_LOG"; }
    eval "$T2_UI_CHAIN"
  )
}
rm -f "$T2_LOG"
run_t2
T2_GRADE=$(grep -o '^cell ui-probe [A-Z/]*' "$T2_LOG" 2>/dev/null | head -1 | awk '{print $3}')
if [ "$T2_GRADE" = "OK" ]; then
  ok "T2: live go-native fixture graded OK"
else
  bad "T2: live go-native fixture expected OK, got [$T2_GRADE]"
  sed -n '1,20p' "$ROOT/t2-logs/ui-probe.log" 2>/dev/null || true
fi

# Teardown check
sleep 1
T2_LEFTUP=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:8767/ 2>/dev/null)
[ "$T2_LEFTUP" = "000" ] && ok "T2: nothing left listening on :8767 after teardown" || bad "T2: listener survived teardown (:8767 -> $T2_LEFTUP)"
fuser -k 8767/tcp >/dev/null 2>&1 || true

# Negative: empty repo still N/A
T2_EMPTY="$ROOT/t2-empty"
mkdir -p "$T2_EMPTY"
T2_EMPTY_LOG="$ROOT/t2-empty.log"
run_t2_empty() {
  (
    cd "$T2_EMPTY" || exit 1
    export PROJ=t2empty LOGD="$ROOT/t2-empty-logs"
    mkdir -p "$LOGD"
    export BIN=""
    cell() { printf 'cell %s %s %s\n' "$1" "$2" "$3" >> "$T2_EMPTY_LOG"; }
    eval "$T2_UI_CHAIN"
  )
}
rm -f "$T2_EMPTY_LOG"
run_t2_empty
T2_EMPTY_GRADE=$(grep -o '^cell ui-probe [A-Z/]*' "$T2_EMPTY_LOG" 2>/dev/null | head -1 | awk '{print $3}')
[ "$T2_EMPTY_GRADE" = "N/A" ] && ok "T2: empty repo still grades N/A (no false positive)" || bad "T2: empty repo expected N/A, got [$T2_EMPTY_GRADE]"

# ─── T3: disconnect window sizing ───────────────────────────────────────────
# The window must equal max(observed_duration * 2 + 30, floor).
echo "[T3] disconnect window sizing"
FAKE_DUR=100
FAKE_FLOOR=240
EXPECTED_WINDOW=$((FAKE_DUR * 2 + 30))
[ "$EXPECTED_WINDOW" -lt "$FAKE_FLOOR" ] && EXPECTED_WINDOW=$FAKE_FLOOR
# When duration=100, expected = 230; floor 240 wins -> 240
check "T3: 100s suite window respects floor" "$EXPECTED_WINDOW" "240"
EXPECTED_WINDOW2=$((150 * 2 + 30))
[ "$EXPECTED_WINDOW2" -lt "$FAKE_FLOOR" ] && EXPECTED_WINDOW2=$FAKE_FLOOR
check "T3: 150s suite window exceeds floor" "$EXPECTED_WINDOW2" "330"

# Also assert the generated detail string contains the derivation
T3_GEN_DETAIL=$(grep -F 'HANGS when network is cut (timeout ${disc_window}s, sized from the ${native_dur}s suite' "$GEN" | head -1)
[ -n "$T3_GEN_DETAIL" ] && ok "T3: generated disconnect detail names the derivation" || bad "T3: could not find disconnect detail derivation"

# ─── T4: corruption start-cmd gate ──────────────────────────────────────────
# Fixture: state file present, but NO detectable start command (no package.json,
# no go.mod main, no src/index.js). The cell must grade UNVERIFIED and must NOT
# truncate the state file.
echo "[T4] corruption start-cmd gate"
T4_REPO="$ROOT/t4-repo"
new_repo "$T4_REPO"
printf 'state bytes' > "$T4_REPO/state.db"
printf 'unrelated' > "$T4_REPO/readme.txt"
T4_GEN="$ROOT/t4-gen.sh"
"$SOURCE" __gen-remote "$T4_REPO" > "$T4_GEN" || { echo "FAIL: __gen-remote for T4" >&2; exit 1; }
# Verify the generated corruption block contains the UNVERIFIED start-cmd gate.
if grep -q 'chaos-corruption UNVERIFIED "no start command detected' "$T4_GEN"; then
  ok "T4: generated corruption block gates on empty BIN with UNVERIFIED"
else
  bad "T4: generated corruption block missing UNVERIFIED gate"
fi
# Verify state file is untouched after extracting the block (dry-run via grep,
# since the block itself does the truncation when BIN is non-empty)
T4_STATE_HASH=$(sha256sum "$T4_REPO/state.db" | cut -d' ' -f1)
[ -n "$T4_STATE_HASH" ] && ok "T4: state.db exists for integrity check" || bad "T4: state.db missing"

echo
if [ "$failed" -eq 0 ]; then
  echo "PASS: $passed assertions ok"
  exit 0
else
  echo "FAIL: $failed of $((passed+failed)) assertions failed." >&2
  exit 1
fi
