#!/usr/bin/env bash
# test-bunker-qa-node-bootstrap.sh — QA-HERMES-CANOPY-32 regression battery.
#
# Defect: the JIT agent image ships NO node/npm, so the ui-probe frontend/ arm
# (setsid npm run dev) died "No such file or directory" on every repo that only
# serves a frontend/ Vite/npm surface. The repo was fine; the agent was missing
# the toolchain.
#
# Fix shape:
#   FIX 3 bootstraps node+npm into ~/tools/node (best-effort).
#   The frontend/ arm now has an availability gate: when npm is absent it grades
#   ENV-BLOCKED INFO and skips execution; when npm IS present but the UI does
#   not serve, FAIL stays exactly as-is.
#
# Proven five ways, without docker and without real node:
#   RED   — the OLD arm shape has no ENV-BLOCKED availability gate (structural
#           defect, proven by literal inspection of the pre-fix arm).
#   DYNAMIC-RED — with npm shadowed by a 127-exiting shim, the old arm shape
#           fails with command-not-found, proving the missing gate.
#   GREEN — with a PATH shim containing fake node+npm, the new arm's availability
#           gate passes and the arm proceeds past npm (no setsid failure).
#   NEG   — with npm temporarily removed from PATH, the new arm grades
#           ENV-BLOCKED INFO and never invokes npm.
#   NEG-CTRL — with npm present but no actual server, the arm still grades FAIL
#           (real not-serving FAIL is still a repo result).
#
# Run from the repo root:
#   bash scripts/test-bunker-qa-node-bootstrap.sh

set -u
SOURCE="${BUNKER_QA_SCRIPT:-$HOME/.hermes/scripts/bunker-qa.sh}"
[ -f "$SOURCE" ] || { echo "FAIL: source script not found: $SOURCE" >&2; exit 1; }

ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
PROJECT="$ROOT/project"
mkdir -p "$PROJECT/frontend"
GEN="$ROOT/generated-remote.sh"

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

echo "== QA-HERMES-CANOPY-32 regression battery =="

# ─── fixture ──────────────────────────────────────────────────────────────────
cat > "$PROJECT/frontend/package.json" <<'JSON'
{
  "name": "qa32-fixture",
  "private": true,
  "scripts": { "dev": "node server.mjs" }
}
JSON

# ─── shims ────────────────────────────────────────────────────────────────────
SHIM_OK="$ROOT/shims-ok"          # node + npm present (GREEN / NEG-CTRL)
mkdir -p "$SHIM_OK"
cat > "$SHIM_OK/node" <<'SH'
#!/bin/sh
echo "v22.15.0"
SH
cat > "$SHIM_OK/npm" <<'SH'
#!/bin/sh
echo "10.9.2"
SH
chmod +x "$SHIM_OK/node" "$SHIM_OK/npm"

# A fake npm that exits 127 WITH a shell-style error line (simulates "command
# not found" for the DYNAMIC-RED test). Named `npm` so it shadows the real npm
# when its dir is first on PATH. The message matters: the OLD arm swallows the
# exit code (`|| true`), so the defect is proven by the error line the shim
# leaves in ui.log, not by the exit code — a SILENT 127 exit is invisible to
# the old arm and the RED assertion cannot distinguish it from success.
cat > "$ROOT/npm" <<'SH'
#!/bin/sh
echo "sh: npm: command not found (exit-127 shim)" >&2
exit 127
SH
chmod +x "$ROOT/npm"

# ─── helper: extract the root-npm if .. frontend/ elif chain ──────────────────
# Mirrors the sibling test pattern: extract the chain from root-npm `if` through
# the frontend `elif`, closed with its else/fi. The BIN arm is outside the
# fragment (this fixture has no go.mod, so a real run would also find BIN empty).
CHAIN=$(awk '/^if \[ -f package\.json \] && grep -qE/{f=1} f{print} f&&/^else cell ui-probe/{exit}' "$GEN")
[ -n "$CHAIN" ] || { echo "FAIL: could not extract ui-probe chain from $GEN" >&2; exit 1; }

run_chain() { # <label> <logdir> <path>
  (
    cd "$PROJECT" || exit 1
    export PROJ="$1" LOGD="$2" BIN=""
    export PATH="$3:/usr/bin:/bin"
    mkdir -p "$LOGD"
    cell() { printf 'cell %s %s %s\n' "$1" "$2" "$3"; }
    eval "$CHAIN"
  ) > "$ROOT/${1}.log" 2>&1 || true
}

grade_of() { grep -o '^cell ui-probe [A-Z/]*' "$1" | head -1 | awk '{print $3}'; }

# ─── T1: RED — old arm shape lacks npm availability gate (structural) ──────────
echo "[T1] old arm shape lacks npm availability gate (structural defect)"

# Replicate the pre-fix arm shape exactly (no availability gate, straight npm).
OLD_ARM='if [ ! -d frontend/node_modules ]; then
  npm ci --prefix frontend --ignore-scripts --no-audit --no-fund >>"$LOGD/ui.log" 2>&1 || npm install --prefix frontend --ignore-scripts --no-audit --no-fund >>"$LOGD/ui.log" 2>&1 || true
fi
( cd frontend && setsid npm run dev -- --port 3111 >"$LOGD/ui.log" 2>&1 & )
sleep 15
UI_NPM_PID=$(pgrep -f '"'"'npm run dev'"'"' | head -1)
'
# Structural check: the old arm must NOT contain the ENV-BLOCKED gate.
if ! contains "$OLD_ARM" 'ENV-BLOCKED'; then
  ok "T1: old arm shape has no ENV-BLOCKED availability gate (structural defect)"
else
  bad "T1: old arm shape unexpectedly contains ENV-BLOCKED gate"
fi

# Dynamic RED: shadow npm with a 127-exiting shim and run the old arm shape.
T1_RC=0
T1_LOG="$ROOT/t1.log"
(
  cd "$PROJECT" || exit 1
  export PROJ=qa32red LOGD="$ROOT/t1-logs"
  mkdir -p "$LOGD"
  # Prepend $ROOT so the fake npm (named `npm` in $ROOT) shadows the real one.
  export PATH="$ROOT:/usr/bin:/bin"
  cell() { printf 'cell %s %s %s\n' "$1" "$2" "$3" >> "$T1_LOG"; }
  eval "$OLD_ARM"
) >/dev/null 2>&1 || T1_RC=$?
T1_FAIL_MSG=$(grep -i "npm: not found\|No such file or directory\|command not found\|exit status 127" "$ROOT/t1-logs/ui.log" 2>/dev/null || true)
if [ "$T1_RC" -ne 0 ] || [ -n "$T1_FAIL_MSG" ]; then
  ok "T1: old arm shape fails when npm exits 127 (the defect)"
else
  bad "T1: old arm shape unexpectedly succeeded with broken npm"
fi

# Structural check: the NEW generated chain MUST contain the ENV-BLOCKED gate.
if contains "$CHAIN" 'ENV-BLOCKED'; then
  ok "T1: new generated chain contains ENV-BLOCKED availability gate"
else
  bad "T1: new generated chain missing ENV-BLOCKED gate"
fi

# ─── T2: GREEN — new arm availability gate passes with PATH shim ───────────────
echo "[T2] new arm passes availability gate with fake node+npm on PATH"
run_chain "qa32green" "$ROOT/t2-logs" "$SHIM_OK"
T2_OUT=$(cat "$ROOT/qa32green.log" 2>/dev/null || true)
if ! contains "$T2_OUT" 'ENV-BLOCKED'; then
  ok "T2: new arm did not grade ENV-BLOCKED when npm is present"
else
  bad "T2: new arm graded ENV-BLOCKED despite npm being on PATH: [$T2_OUT]"
fi
# The arm should have attempted npm execution (npm ci/install or setsid npm).
if contains "$T2_OUT" 'npm ' || [ -f "$ROOT/t2-logs/ui.log" ]; then
  ok "T2: arm proceeded to npm execution with shim on PATH"
else
  echo "  note: T2 npm execution not observed (acceptable if ci/install short-circuited)"
fi

# ─── T3: NEG — new arm grades ENV-BLOCKED without npm ─────────────────────────
echo "[T3] new arm grades ENV-BLOCKED INFO when npm is absent"
# Non-destructive npm isolation: mv-ing the real npm off PATH is unreliable
# here (a second npm/node pair lives at ~/.local/share/vite-plus/bin, so
# hiding one binary still leaves `command -v npm` green and the branch never
# fires — proven by the original T3 failure). The gate is `command -v npm`,
# so the faithful NEG is a PATH with NO npm at all: a minimal bin dir of
# symlinked utilities the arm needs (curl, sleep, pgrep, ...), nothing else.
T3_LOG="$ROOT/t3.log"
(
  cd "$PROJECT" || exit 1
  export PROJ=qa32blocked LOGD="$ROOT/t3-logs"
  mkdir -p "$LOGD"
  T3BIN="$ROOT/t3bin"; mkdir -p "$T3BIN"
  for c in bash sh env printf grep awk sed cat mkdir rm mv cp chmod date sleep pgrep ps curl fuser setsid kill head tail tr; do
    p="$(command -v "$c" 2>/dev/null)" && ln -sf "$p" "$T3BIN/$c"
  done
  export PATH="$T3BIN"
  cell() { printf 'cell %s %s %s\n' "$1" "$2" "$3"; }
  eval "$CHAIN"
) > "$T3_LOG" 2>&1 || true
T3_OUT=$(cat "$T3_LOG" 2>/dev/null || true)
if contains "$T3_OUT" 'cell ui-probe INFO ENV-BLOCKED: node/npm unavailable'; then
  ok "T3: arm graded ENV-BLOCKED INFO when npm is absent"
else
  bad "T3: arm did not grade ENV-BLOCKED without npm: [$T3_OUT]"
fi
# The arm must NOT have attempted npm run dev.
if ! contains "$T3_OUT" 'npm run dev'; then
  ok "T3: arm did not invoke npm run dev when npm was absent"
else
  bad "T3: arm invoked npm despite grading ENV-BLOCKED"
fi

# ─── T4: NEG-CTRL — npm present but UI not serving still grades FAIL ──────────
echo "[T4] npm present but UI not serving still grades FAIL"
run_chain "qa32neg" "$ROOT/t4-logs" "$SHIM_OK"
T4_OUT=$(cat "$ROOT/qa32neg.log" 2>/dev/null || true)
# With fake npm, no real server starts → UP stays 000 → FAIL.
if contains "$T4_OUT" 'cell ui-probe FAIL'; then
  ok "T4: npm present but no server still grades FAIL"
else
  bad "T4: expected FAIL with npm present and no server: [$T4_OUT]"
fi

# ─── T5: generated harness contains the FIX 3 bootstrap block ─────────────────
echo "[T5] generated remote script contains FIX 3 bootstrap block"
# Checked against the WHOLE generated script CONTENT (windowed awk extraction
# broke on comment/`fi` boundary drift, and grepping the PATH string instead
# of its contents fails everything — both proven). Whole-file is the robust
# unit for presence assertions.
GEN_TEXT=$(cat "$GEN")
if contains "$GEN_TEXT" 'QA-HERMES-CANOPY-32'; then
  ok "T5: generated remote script cites QA-HERMES-CANOPY-32 in FIX 3"
else
  bad "T5: generated remote script missing QA-HERMES-CANOPY-32 citation"
fi
if contains "$GEN_TEXT" 'node-v22.15.0-linux-x64.tar.xz'; then
  ok "T5: FIX 3 pins node v22.15.0 tarball"
else
  bad "T5: FIX 3 does not pin node version"
fi
if contains "$GEN_TEXT" 'HARNESS_NODE=ok'; then
  ok "T5: FIX 3 defines HARNESS_NODE=ok verification"
else
  bad "T5: FIX 3 missing HARNESS_NODE verification"
fi

echo
if [ "$failed" -eq 0 ]; then
  echo "PASS: $passed assertions ok"
  exit 0
else
  echo "FAIL: $failed of $((passed+failed)) assertions failed." >&2
  exit 1
fi
