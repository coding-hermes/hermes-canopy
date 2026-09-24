#!/usr/bin/env bash
# test-bunker-qa-ui-probe-frontend.sh — QA-HERMES-CANOPY-9 regression test.
#
# hermes-canopy has NO root package.json; its UI is a Vite app under frontend/.
# The ui-probe cell keyed UI detection on a root package.json, so the shipped
# user surface was graded N/A "no web UI detected" on 4 consecutive QA cycles.
#
# Proven three ways, on a real node fixture (no mocks):
#   RED  — an inline replica of the PRE-FIX decision grades the fixture N/A
#          (reproducible on a fresh machine; no /tmp relic needed).
#   GREEN — the POST-fix chain extracted from the GENERATED remote script
#          (bunker-qa.sh __gen-remote) grades the same fixture
#          "ui-probe OK ... (frontend/)" after actually starting the dev server
#          and probing :3111.
#   TEARDOWN — after the extracted chain runs, nothing is left listening on
#          :3111 and no fixture process survives (the process-group kill; the
#          old bare `pkill -f 'npm run dev'` measured LEAKING the listener).
#   NEGATIVE — an empty repo (no frontend/) still grades N/A; the new arm must
#          not manufacture a false positive.
#
# Extraction note: the extracted fragment is the chain from the root-npm `if`
# through the frontend `elif`, closed with an `else` N/A cell — the BIN arm is
# outside the fragment, which is exact for the fixtures here (no go.mod, so a
# real run would also find BIN empty). Run from the repo root:
#   bash scripts/test-bunker-qa-ui-probe-frontend.sh

set -u
SOURCE="${BUNKER_QA_SCRIPT:-$HOME/.hermes/scripts/bunker-qa.sh}"
command -v node >/dev/null 2>&1 || { echo "FAIL: node is required for the live fixture" >&2; exit 1; }
[ -f "$SOURCE" ] || { echo "FAIL: source script not found: $SOURCE" >&2; exit 1; }

ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
PROJECT="$ROOT/project"
GEN="$ROOT/generated-remote.sh"
EVID="$ROOT/evid.txt"

if [ -z "${BUNKER_QA_GENERATED:-}" ]; then
  "$SOURCE" __gen-remote "$PROJECT" > "$GEN" || { echo "FAIL: could not generate remote QA script" >&2; exit 1; }
else
  cp "$BUNKER_QA_GENERATED" "$GEN"
fi

# --- extract the post-fix decision chain (root-npm if .. frontend/ elif) ----
CHAIN=$(awk '/^if \[ -f package\.json \] && grep -qE/{f=1} f{print} f&&/^else cell ui-probe/{exit}' "$GEN")
[ -n "$CHAIN" ] || { echo "FAIL: could not extract ui-probe decision chain from $GEN" >&2; exit 1; }
grep -q 'frontend/package.json' <<<"$CHAIN" || { echo "FAIL: generated chain has no frontend/ arm (fix not present?)" >&2; exit 1; }
# the extraction runs from the root-npm `if` through the final N/A `else..fi`
# of the whole decision (BIN arm included), so the fragment is self-contained
RUN_CHAIN="$CHAIN"

passed=0
failed=0
ok()  { echo "  ok: $1"; passed=$((passed+1)); }
bad() { echo "  FAIL: $1"; failed=$((failed+1)); }

# --- PRE-FIX replica: the old decision, embedded so the red side is ---
# --- reproducible anywhere. Old logic: root package.json -> npm; BIN -> probe; else N/A.
pre_fix_grade() { # <repo-dir>
  local repo="$1"
  if [ -f "$repo/package.json" ]; then
    echo "npm"
  else
    echo "N/A"
  fi
}

# --- fixture: a repo whose UI is ONLY frontend/ -----------------------------
make_fixture() { # <dir>
  local d="$1"
  mkdir -p "$d/frontend"
  cat > "$d/frontend/package.json" <<'JSON'
{
  "name": "qa9-fixture",
  "private": true,
  "scripts": { "dev": "node server.mjs" }
}
JSON
  cat > "$d/frontend/server.mjs" <<'JS'
import http from 'node:http';
let port = 3111;
const i = process.argv.indexOf('--port');
if (i !== -1 && process.argv[i + 1]) port = parseInt(process.argv[i + 1], 10) || 3111;
http.createServer((req, res) => {
  res.writeHead(200, { 'content-type': 'text/html' });
  res.end('<html><body>qa9 fixture</body></html>');
}).listen(port, '127.0.0.1');
JS
}

run_chain() { # <repo-dir> — executes the extracted chain, cell() grades to stdout
  local repo="$1"
  (
    cd "$repo" || exit 1
    export PROJ=qa9fix LOGD="$repo/.qa9logs"
    # the harness always defines BIN above this section; empty here = the
    # no-go-binary case the fixtures exercise
    export BIN=""
    mkdir -p "$LOGD"
    cell() { echo "cell $1 $2 $3"; }
    eval "$RUN_CHAIN"
  )
}

grade_of() { grep -o '^cell ui-probe [A-Z/]*' <<<"$1" | head -1 | awk '{print $3}'; }

make_fixture "$PROJECT"

# 1 · RED: the pre-fix logic false-NAs this fixture
G_OLD=$(pre_fix_grade "$PROJECT")
[ "$G_OLD" = "N/A" ] && ok "pre-fix replica grades the frontend-only fixture N/A (the defect)" \
                     || bad "pre-fix replica expected N/A, got [$G_OLD]"

# 2 · GREEN: the fixed chain starts the dev server and grades OK
OUT=$(run_chain "$PROJECT")
GRADE=$(grade_of "$OUT")
if [ "$GRADE" = "OK" ]; then
  ok "post-fix chain grades ui-probe OK for the frontend-only fixture"
  # The OK detail may enrich the arm name (e.g. "(frontend/, landed: URL)" since
  # 00a91e4) — the invariant is that the detail NAMES the frontend/ arm.
  grep -q "(frontend/" <<<"$OUT" && ok "OK detail names the frontend/ arm" || bad "OK detail missing '(frontend/': [$OUT]"
else
  bad "post-fix chain expected OK, got [$GRADE]: [$OUT]"
  sed -n '1,20p' "$PROJECT/.qa9logs/ui.log" 2>/dev/null
fi

# 3 · the probe was real: the log shows the dev server was started via npm
grep -q "node server.mjs\|vite" "$PROJECT/.qa9logs/ui.log" 2>/dev/null \
  && ok "ui.log shows the real dev-server run" || bad "ui.log has no dev-server evidence"

# 4 · TEARDOWN: nothing left on :3111, no fixture process left alive
sleep 1
LEFTUP=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:3111/ 2>/dev/null)
[ "$LEFTUP" = "000" ] && ok "nothing left listening on :3111 after teardown" \
                      || bad "listener survived teardown (:3111 -> $LEFTUP)"
LEFTPROC=$(ps -eo args | grep -F 'server.mjs' | grep -v grep | grep -F "$ROOT" || true)
[ -z "$LEFTPROC" ] && ok "no fixture process survived teardown" || bad "leaked process: [$LEFTPROC]"
fuser -k 3111/tcp >/dev/null 2>&1 || true

# 5 · NEGATIVE control: an empty repo must still grade N/A (no false positive)
EMPTY="$ROOT/empty"
mkdir -p "$EMPTY"
OUT2=$(run_chain "$EMPTY")
GRADE2=$(grade_of "$OUT2")
[ "$GRADE2" = "N/A" ] && ok "empty repo still grades N/A (no false positive)" \
                      || bad "empty repo expected N/A, got [$GRADE2]: [$OUT2]"

echo
if [ "$failed" -eq 0 ]; then
  echo "PASS: $passed assertions ok"
  exit 0
else
  echo "FAIL: $failed assertion(s) failed ($passed passed)"
  exit 1
fi
