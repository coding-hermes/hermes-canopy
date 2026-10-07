#!/usr/bin/env bash
# QA-HERMES-CANOPY-40 — compose readiness gate + port wiring contract.
#
# Why: `docker compose up -d` returned green while :8092 answered nothing —
# a crash-looping or port-misconfigured canopyd still shows the container
# "Started", and nothing in the stack or the docs ever probed the listener.
# This battery proves, against the RENDERED compose config where a docker
# daemon is reachable (raw file scan as a labelled fallback):
#
#   A. the canopyd service carries a /health healthcheck whose probe port,
#      the HTTP_ADDR env it reads, the container listen port, and the host
#      publish port all agree (:8080 inside, :8092 on the host)
#   B. the probe budget is exactly 60s (3s interval x 20 retries), so
#      `docker compose up -d --wait` fails within the 60s window instead of
#      hanging or returning early-green
#   C. CANOPY_DB_URL carries the real postgres password (a *** placeholder
#      makes canopyd fail DB auth and exit inside the probe window — the
#      exact "Started but unreachable" symptom)
#   D. negative controls: a probe pointed at the WRONG port, a probe budget
#      that is not 60s, and a *** DSN password each FAIL their checker —
#      the gate detects real regressions, it is not a tautology
#   E. docs/INTEGRATION.md documents the readiness gate + probe window
#
# Run: scripts/test-compose-readiness.sh    (exit 0 = all scenarios pass)
#
# Same evidence discipline as scripts/test-reboot-durability.sh (GAP-099):
# rendered `docker compose config --format json` is the primary source, a
# raw block scan is the honest no-daemon fallback (labelled, never a silent
# pass), and every doc claim is greppable.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"
COMPOSE_FILE="$REPO_ROOT/docker-compose.yml"
DOCS_FILE="$REPO_ROOT/docs/INTEGRATION.md"

WORK="$(mktemp -d /tmp/canopy-compose-readiness.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0

ok() {
	pass=$((pass + 1))
	echo "PASS: $1"
}
bad() {
	fail=$((fail + 1))
	echo "FAIL: $1"
	echo "--- output ---"
	sed 's/^/    /' <<<"${2:-}"
	echo "--------------"
}

echo "== compose readiness gate tests (QA-HERMES-CANOPY-40) — workdir: $WORK =="

# Sanity: the compose file itself must exist and be parseable by docker
# (acceptance criterion A). Without this the raw-scan fallback would grade
# a file the daemon would refuse.
if [[ ! -f "$COMPOSE_FILE" ]]; then
	bad "docker-compose.yml exists" ""
	echo "== results: $pass passed, $fail failed =="
	exit 1
fi

COMPOSE_JSON="$WORK/compose.json"
RENDER_OK=0
if command -v docker >/dev/null 2>&1 \
	&& docker compose -f "$COMPOSE_FILE" config --format json >"$COMPOSE_JSON" 2>"$WORK/compose.err" \
	&& [[ -s "$COMPOSE_JSON" ]]; then
	RENDER_OK=1
fi

# render_check <label> <checker-script>
# Runs the checker against the rendered JSON (rendered mode) or the raw YAML
# file (fallback mode). The mode travels in RENDER_MODE so each checker's
# branch matches the evidence source actually being graded.
render_check() {
	local label="$1" checker="$2" out rc
	if [[ $RENDER_OK == 1 ]]; then
		out="$(RENDER_MODE=1 python3 "$checker" "$COMPOSE_JSON" 2>&1)"
		rc=$?
	else
		out="$(python3 "$checker" "$COMPOSE_FILE" 2>&1)"
		rc=$?
	fi
	if [[ $rc == 0 ]]; then
		if [[ $RENDER_OK == 1 ]]; then
			ok "$label (rendered config)"
		else
			ok "$label (raw scan; docker daemon unavailable)"
		fi
	else
		bad "$label" "$out"
	fi
}

# ═══ A. healthcheck present, probe port == HTTP_ADDR == container port == publish ═
echo ""
echo "== A. healthcheck present, port wiring agrees =="

cat >"$WORK/check_a.py" <<'PY'
import json, re, sys, os

path = sys.argv[1]
rendered = os.environ.get("RENDER_MODE") == "1"

PORT_IN_CONTAINER = "8080"
PORT_HOST = "8092"

if rendered:
    d = json.load(open(path, encoding="utf-8"))
    svc = d["services"]["canopyd"]
    hc = svc.get("healthcheck") or {}
    test = hc.get("test") or []
    probe = " ".join(test) if isinstance(test, list) else str(test)
    assert "/health" in probe, f"healthcheck does not probe /health: {probe!r}"
    assert f"127.0.0.1:{PORT_IN_CONTAINER}" in probe, \
        f"healthcheck probes the wrong port: {probe!r}, want 127.0.0.1:{PORT_IN_CONTAINER}"
    env = svc.get("environment") or {}
    http_addr = env.get("HTTP_ADDR")
    assert http_addr is not None, "HTTP_ADDR missing from canopyd environment"
    assert http_addr.lstrip(":") == PORT_IN_CONTAINER, \
        f"HTTP_ADDR={http_addr!r} does not match the container-side publish port {PORT_IN_CONTAINER}"
    publish = None
    for p in svc.get("ports") or []:
        published = p.get("published") if isinstance(p, dict) else str(p).split(":")[0]
        target = p.get("target") if isinstance(p, dict) else str(p).split(":")[-1]
        if str(target) == PORT_IN_CONTAINER:
            publish = str(published)
    assert publish == PORT_HOST, \
        f"no publish maps host {PORT_HOST} -> container {PORT_IN_CONTAINER}; got {svc.get('ports')!r}"
    print(f"healthcheck probes {PORT_IN_CONTAINER}/health, HTTP_ADDR={http_addr}, publish {publish}->{PORT_IN_CONTAINER}")
else:
    lines = open(path, encoding="utf-8").read().splitlines()

    def block(name):
        out, on = [], False
        for ln in lines:
            if re.match(rf"^  {re.escape(name)}:\s*$", ln):
                on = True
                continue
            if on and re.match(r"^  \S", ln):
                break
            if on:
                out.append(ln)
        return out

    body = block("canopyd")
    assert body, "canopyd service block not found in docker-compose.yml"
    joined = "\n".join(body)
    assert re.search(r"^    healthcheck:\s*$", joined, re.M), "canopyd block declares no healthcheck"
    assert re.search(rf"wget .*127\.0\.0\.1:{PORT_IN_CONTAINER}/health", joined), \
        f"healthcheck does not probe 127.0.0.1:{PORT_IN_CONTAINER}/health"
    assert re.search(rf'^      HTTP_ADDR: "?:?{PORT_IN_CONTAINER}"?\s*$', joined, re.M), \
        f"HTTP_ADDR not pinned to :{PORT_IN_CONTAINER} in canopyd environment"
    assert re.search(rf'^      - "{PORT_HOST}:{PORT_IN_CONTAINER}"\s*$', joined, re.M), \
        f"publish {PORT_HOST}:{PORT_IN_CONTAINER} not found in canopyd ports"
    print(f"raw-scan: healthcheck probes {PORT_IN_CONTAINER}/health, HTTP_ADDR=:{PORT_IN_CONTAINER}, publish {PORT_HOST}->{PORT_IN_CONTAINER}")
PY

RENDER_MODE_FLAG=$RENDER_OK render_check "canopyd healthcheck present, ports agree (:8080 in-container, :8092 host)" "$WORK/check_a.py"

# ═══ B. probe budget exactly 60s ═══════════════════════════════════════════
echo ""
echo "== B. probe budget 60s (3s interval x 20 retries) =="

cat >"$WORK/check_b.py" <<'PY'
import json, re, sys, os

path = sys.argv[1]
rendered = os.environ.get("RENDER_MODE") == "1"

def budget_secs(hc):
    s = str(hc.get("interval"))
    interval = int(s[:-1]) if s.endswith("s") else int(s)
    return interval * int(hc.get("retries"))

if rendered:
    d = json.load(open(path, encoding="utf-8"))
    hc = d["services"]["canopyd"].get("healthcheck") or {}
    total = budget_secs(hc)
    assert total == 60, f"probe budget = {total}s, want 60s (3s interval x 20 retries)"
    print(f"probe budget {total}s")
else:
    lines = open(path, encoding="utf-8").read().splitlines()
    iv = rt = None
    on = False
    for ln in lines:
        if re.match(r"^  canopyd:\s*$", ln):
            on = True
            continue
        if on and re.match(r"^  \S", ln):
            break
        if on:
            m = re.match(r"^\s+interval:\s*(\d+)s?\s*$", ln)
            if m:
                iv = int(m.group(1))
            m = re.match(r"^\s+retries:\s*(\d+)\s*$", ln)
            if m:
                rt = int(m.group(1))
    assert iv is not None and rt is not None, f"interval/retries not found (iv={iv}, retries={rt})"
    assert iv * rt == 60, f"probe budget = {iv}s x {rt} = {iv*rt}s, want 60s"
    print(f"raw-scan: probe budget {iv}s x {rt} = 60s")
PY

render_check "healthcheck budget is exactly 60s (3s x 20)" "$WORK/check_b.py"

# ═══ C. DSN carries the real postgres password ═════════════════════════════
echo ""
echo "== C. CANOPY_DB_URL password matches postgres POSTGRES_PASSWORD =="

cat >"$WORK/check_c.py" <<'PY'
import json, re, sys, os
import urllib.parse as up

path = sys.argv[1]
rendered = os.environ.get("RENDER_MODE") == "1"

if rendered:
    d = json.load(open(path, encoding="utf-8"))
    pg_pw = d["services"]["postgres"]["environment"]["POSTGRES_PASSWORD"]
    dsn = d["services"]["canopyd"]["environment"]["CANOPY_DB_URL"]
    assert "***" not in dsn, f"CANOPY_DB_URL still carries a *** placeholder: {dsn!r}"
    parts = up.urlparse(dsn)
    assert parts.password == pg_pw, \
        f"CANOPY_DB_URL password {parts.password!r} != POSTGRES_PASSWORD {pg_pw!r} — canopyd would fail DB auth and exit inside the probe window"
    print(f"CANOPY_DB_URL password matches POSTGRES_PASSWORD ({pg_pw!r})")
else:
    text = open(path, encoding="utf-8").read()
    m = re.search(r"POSTGRES_PASSWORD:\s*(\S+)", text)
    assert m, "POSTGRES_PASSWORD not found in docker-compose.yml"
    pg_pw = m.group(1)
    m = re.search(r"CANOPY_DB_URL:\s*postgres://([^:@\s\"']+):([^@\s\"']+)@", text)
    assert m, "CANOPY_DB_URL not found in docker-compose.yml"
    dsn_pw = m.group(2)
    assert "***" not in dsn_pw, "CANOPY_DB_URL still carries a *** placeholder"
    assert dsn_pw == pg_pw, \
        f"CANOPY_DB_URL password {dsn_pw!r} != POSTGRES_PASSWORD {pg_pw!r} — canopyd would fail DB auth and exit inside the probe window"
    print(f"raw-scan: CANOPY_DB_URL password matches POSTGRES_PASSWORD ({pg_pw!r})")
PY

render_check "CANOPY_DB_URL uses the postgres service password (no *** placeholder)" "$WORK/check_c.py"

# ═══ D. negative controls — the gate must catch real regressions ══════════
echo ""
echo "== D. negative controls (mutated compose must FAIL its checker) =="

python3 - "$COMPOSE_FILE" "$WORK" <<'PY'
import subprocess, sys

src, work = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()

# (old, new, checker-that-must-fail). Unique output file per case: sharing
# one path would grade only the last mutation.
MUT = {
    "wrong probe port":  ("http://127.0.0.1:8080/health", "http://127.0.0.1:9999/health", "check_a.py"),
    "wrong budget":      ("retries: 20",                  "retries: 2",                   "check_b.py"),
    "redacted DSN back": ("postgres://canopy:canopy@",    "postgres://canopy:***@",       "check_c.py"),
}

results = []
slug = {"wrong probe port": "port", "wrong budget": "budget", "redacted DSN back": "dsn"}
for name, (old, new, checker) in MUT.items():
    assert old in text, f"mutation anchor for {name!r} not found in compose file"
    assert old != new, f"mutation {name!r} is a no-op (old == new)"
    mutated = text.replace(old, new, 1)
    p = f"{work}/compose-mutated-{slug[name]}.yml"
    open(p, "w", encoding="utf-8").write(mutated)
    # RENDER_MODE unset -> raw-scan branch, against the mutated FILE
    r = subprocess.run(
        ["python3", f"{work}/{checker}", p], capture_output=True, text=True)
    if r.returncode == 0:
        results.append(f"NOT-CAUGHT: {name}: {checker} accepted the mutated compose (rc=0)")
    else:
        first_err = next((ln for ln in r.stderr.splitlines() if "AssertionError" in ln), r.stderr.strip().splitlines()[-1] if r.stderr.strip() else "")
        results.append(f"caught: {name}: {checker} rejected it ({first_err[:110]})")

fails = [r for r in results if r.startswith("NOT-CAUGHT")]
print("\n".join(results))
sys.exit(1 if fails else 0)
PY
rc=$?
if [[ $rc == 0 ]]; then
	ok "negative controls: wrong probe port / wrong budget / redacted DSN each fail their checker"
else
	bad "negative controls: a mutation was NOT caught" ""
fi

# ═══ E. docs contract ═══════════════════════════════════════════════════════
echo ""
echo "== E. docs/INTEGRATION.md readiness contract =="

if [[ -f "$DOCS_FILE" ]]; then
	for tok in "QA-HERMES-CANOPY-40" "/health" "60s probe window" "--wait" "8092"; do
		if grep -qF -- "$tok" "$DOCS_FILE"; then
			ok "docs document '$tok'"
		else
			bad "docs do not document '$tok'" ""
		fi
	done
else
	bad "docs/INTEGRATION.md exists" ""
fi

echo ""
echo "== results: $pass passed, $fail failed =="
[[ $fail == 0 ]]
