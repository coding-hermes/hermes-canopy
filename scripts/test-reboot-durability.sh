#!/usr/bin/env bash
#
# test-reboot-durability.sh (GAP-099): hermetic regression coverage for the
# canopy stack's reboot durability.
#
# The 2026-09-21 reboots left the live stack down 6h+ in a crash loop: the
# compose `postgres` service had no restart policy (container canopy-pg stayed
# dead, exited 255, RestartPolicy=no) and canopy-canopyd.service had
# Restart=always with no DB dependency gate, so it crash-looped ~690 exits/hour
# against a database that was not there yet. These tests pin BOTH durable fixes
# — the compose policy, and the unit's ExecStartPre DB bring-up — plus the
# unit-sync step that installs the unit from the repo.
#
# Nothing here touches the real service, the real user unit directory, real
# systemd, or a real container:
#   * the compose policy is read from the RENDERED config (read-only);
#   * the unit's ExecStartPre lines are extracted and executed against a stub
#     docker + a stub sleep, so the readiness poll's real semantics (exit 0 on
#     the first good probe, exit 1 after exactly 30 attempts) are exercised in
#     milliseconds with no container involved;
#   * the deploy script's unit-sync block is extracted verbatim between its
#     BEGIN/END markers and driven against a temp UNIT_DIR and a stub
#     systemctl, so "drift ⇒ atomic install + daemon-reload" is proven, not
#     grepped.
#
# Covered controls:
#   1. `docker compose config` renders restart: unless-stopped for postgres
#      (raw-file block scan as a labelled fallback when no daemon is reachable).
#   2-8. deploy/systemd/canopy-canopyd.service: live-unit essentials preserved,
#      CANOPY_DB_URL on 127.0.0.1:5437, GAP-099 comment, two ExecStartPre lines
#      with an absolute docker path, bounded pg_isready poll before ExecStart,
#      Restart=always kept.
#   9-11. ExecStartPre semantics under a stub docker: start is issued, a ready
#      DB short-circuits the poll, a never-ready DB exits 1 after 30 attempts.
#  12-17. deploy-canopyd.sh: step labels renumbered, unit sync runs before the
#      restart, drift installs + daemon-reloads, no drift is a no-op, a missing
#      live unit installs, a missing unit source fails loudly.
#  18-23. docs/INTEGRATION.md documents the reboot-durability contract.
#
# Env override: CANOPYD_LIVE_UNIT (default ~/.config/systemd/user/
# canopy-canopyd.service) — only used for the read-only essentials cross-check.
#
# Exit 0 only when every scenario passes.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"
UNIT_FILE="$REPO_ROOT/deploy/systemd/canopy-canopyd.service"
DEPLOY_SCRIPT="$REPO_ROOT/scripts/deploy-canopyd.sh"
COMPOSE_FILE="$REPO_ROOT/docker-compose.yml"
DOCS_FILE="$REPO_ROOT/docs/INTEGRATION.md"
LIVE_UNIT="${CANOPYD_LIVE_UNIT:-$HOME/.config/systemd/user/canopy-canopyd.service}"

WORK="$(mktemp -d /tmp/canopy-reboot-durability.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0
skip=0

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
skipped() {
	skip=$((skip + 1))
	echo "SKIP: $1"
}

echo "== reboot-durability tests (GAP-099) — workdir: $WORK =="

# ═══ A. compose postgres restart policy ════════════════════════════════════
echo ""
echo "== A. compose postgres restart policy =="

COMPOSE_JSON="$WORK/compose.json"
RENDER_OK=0
if command -v docker >/dev/null 2>&1 \
	&& docker compose -f "$COMPOSE_FILE" config --format json >"$COMPOSE_JSON" 2>"$WORK/compose.err" \
	&& [[ -s "$COMPOSE_JSON" ]]; then
	RENDER_OK=1
fi

if [[ $RENDER_OK == 1 ]]; then
	# The rendered config is what the daemon actually gets — the same evidence
	# the acceptance criterion asks for (`docker compose config`).
	out="$(python3 - "$COMPOSE_JSON" <<'PY' 2>&1
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
pg = d["services"]["postgres"]
assert pg.get("restart") == "unless-stopped", \
    f"postgres restart={pg.get('restart')!r}, want 'unless-stopped' (GAP-099)"
assert d["services"]["canopyd"].get("restart") == "unless-stopped", \
    "canopyd lost its restart policy"
print("rendered postgres restart=%s canopyd restart=%s" % (pg["restart"], d["services"]["canopyd"]["restart"]))
PY
)"
	rc=$?
	if [[ $rc == 0 ]]; then
		ok "docker compose config: postgres restart=unless-stopped (rendered)"
	else
		bad "docker compose config: postgres restart policy wrong" "$out"
	fi
else
	# No daemon reachable: fall back to a raw block scan, labelled so a run
	# without docker is visible (never a silent pass).
	out="$(python3 - "$COMPOSE_FILE" <<'PY' 2>&1
import re, sys
lines = open(sys.argv[1], encoding="utf-8").read().splitlines()
def block(name):
    out, on = [], False
    for ln in lines:
        if re.match(r"^  %s:\s*$" % re.escape(name), ln):
            on = True
            continue
        if on and re.match(r"^  \S", ln):
            break
        if on:
            out.append(ln)
    return out
body = block("postgres")
assert body, "postgres service block not found in docker-compose.yml"
assert any(re.match(r"^\s{4}restart:\s*unless-stopped\s*$", ln) for ln in body), \
    "postgres block declares no 'restart: unless-stopped'"
print("raw-scan postgres block declares restart: unless-stopped")
PY
)"
	rc=$?
	if [[ $rc == 0 ]]; then
		ok "docker-compose.yml: postgres restart=unless-stopped (raw scan; docker daemon unavailable)"
	else
		bad "docker-compose.yml: postgres restart policy missing" "$out"
	fi
fi

# ═══ B. the tracked unit file ══════════════════════════════════════════════
echo ""
echo "== B. deploy/systemd/canopy-canopyd.service =="

if [[ ! -f "$UNIT_FILE" ]]; then
	bad "tracked unit file deploy/systemd/canopy-canopyd.service exists" "$(ls -la "$REPO_ROOT/deploy/systemd" 2>&1)"
else
	ok "tracked unit file deploy/systemd/canopy-canopyd.service exists"
	UNIT_OUT="$(python3 - "$UNIT_FILE" "$LIVE_UNIT" <<'PY' 2>&1
import os, re, sys

unit_path, live_path = sys.argv[1], sys.argv[2]
text = open(unit_path, encoding="utf-8").read()
code = [l for l in text.splitlines() if not l.lstrip().startswith("#")]
joined = "\n".join(code)


def check(name, cond):
    print(("PASS: " if cond else "FAIL: ") + name)


check("unit keeps the [Unit]/[Service]/[Install] sections",
      all(s in joined for s in ("[Unit]", "[Service]", "[Install]")))
check("unit keeps Type=simple / WorkingDirectory / EnvironmentFile",
      "Type=simple" in joined
      and "WorkingDirectory=/home/kara/hermes-canopy" in joined
      and "EnvironmentFile=/home/kara/.hermes/.env" in joined)

envs = set(re.findall(r"^Environment=(\S.*)$", joined, re.M))
for want in ("HTTP_ADDR=:8091", "CANOPY_DEV=false", "METRICS_ENABLED=true",
             "HERMES_WEBUI_GATEWAY_BASE_URL=http://127.0.0.1:8642"):
    check("unit keeps Environment=%s" % want, want in envs)

db = [e for e in envs if e.startswith("CANOPY_DB_URL=")]
check("CANOPY_DB_URL targets 127.0.0.1:5437 (host instance, not the compose network)",
      len(db) == 1 and "@127.0.0.1:5437/" in db[0] and db[0].endswith("sslmode=disable"))
check("ExecStart runs the installed host binary",
      "ExecStart=/home/kara/bin/canopyd serve" in joined)
check("Restart=always + RestartSec=5 kept as the last resort",
      "Restart=always" in joined and "RestartSec=5" in joined)

# ── GAP-099 hardening ──────────────────────────────────────────────────────
check("GAP-099 comment names the incident (2026-09-21 reboots)",
      "GAP-099" in text and "2026-09-21" in text)

pres = re.findall(r"^ExecStartPre=(.*)$", joined, re.M)
check("exactly two ExecStartPre lines (container start + readiness poll)",
      len(pres) == 2)
start = [p for p in pres if re.match(r"^\S+ start canopy-pg$", p)]
poll = [p for p in pres if "pg_isready" in p]
check("ExecStartPre starts the canopy-pg container by ABSOLUTE docker path",
      len(start) == 1 and start[0].startswith("/")
      and os.access(start[0].split()[0], os.X_OK))
check("ExecStartPre readiness poll runs through /bin/sh -c and exits non-zero on timeout",
      len(poll) == 1 and poll[0].startswith("/bin/sh -c ")
      and poll[0].rstrip().rstrip("'").endswith("exit 1"))
check("readiness poll is bounded (30 attempts, 1s apart) and probes pg_isready -U canopy",
      len(poll) == 1 and "seq 1 30" in poll[0] and "sleep 1" in poll[0]
      and "pg_isready -U canopy" in poll[0])
check("poll uses the same absolute docker path as the start line",
      len(start) == 1 and len(poll) == 1 and start[0].split()[0] in poll[0])
check("ExecStartPre lines precede ExecStart (canopyd never races a booting DB)",
      "ExecStartPre=" in joined and joined.index("ExecStartPre=") < joined.index("ExecStart="))

# ── cross-check against the LIVE unit (read-only) ──────────────────────────
if os.path.isfile(live_path):
    live = [l for l in open(live_path, encoding="utf-8").read().splitlines()
            if l.strip() and not l.lstrip().startswith("#")]
    keys = ("Type=", "WorkingDirectory=", "EnvironmentFile=", "Environment=", "ExecStart=")
    live_essentials = [l for l in live if l.startswith(keys)]
    absent = [l for l in live_essentials if l not in code]
    check("repo unit carries all %d Environment=/ExecStart= essentials of the live unit"
          % len(live_essentials), not absent)
    if absent:
        print("FAIL:   (missing from the repo copy: %s)" % absent)
else:
    print("SKIP: live unit %s absent — live-essentials cross-check not run" % live_path)
PY
)"
	while IFS= read -r line; do
		case "$line" in
		PASS:*) ok "${line#PASS: }" ;;
		SKIP:*) skipped "${line#SKIP: }" ;;
		FAIL:*) bad "${line#FAIL: }" "$UNIT_OUT" ;;
		*) : ;;
		esac
	done <<<"$UNIT_OUT"
fi

# ═══ C. ExecStartPre semantics (stub docker, stub sleep) ═══════════════════
echo ""
echo "== C. ExecStartPre semantics =="

STUB_BIN="$WORK/stubbin"
mkdir -p "$STUB_BIN"
DOCKER_CALLS="$WORK/docker.calls"
PGREADY_FILE="$WORK/pgready"
SYSTEMCTL_CALLS="$WORK/systemctl.calls"
export SYSTEMCTL_CALLS

cat >"$STUB_BIN/docker" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$DOCKER_CALLS"
if [[ "\${1:-}" == "exec" ]]; then
	exit "\$(cat "$PGREADY_FILE" 2>/dev/null || echo 1)"
fi
exit 0
EOF
cat >"$STUB_BIN/sleep" <<'EOF'
#!/usr/bin/env bash
# no-op: keeps the 30-attempt poll instant under test
exit 0
EOF
chmod +x "$STUB_BIN/docker" "$STUB_BIN/sleep"

PRE_LINES=()
if [[ -f "$UNIT_FILE" ]]; then
	mapfile -t PRE_LINES < <(sed -n 's/^ExecStartPre=//p' "$UNIT_FILE")
fi

if [[ ${#PRE_LINES[@]} -ne 2 ]]; then
	bad "could not extract 2 ExecStartPre lines to exercise (got ${#PRE_LINES[@]})" "$(grep -c ExecStartPre "$UNIT_FILE" 2>/dev/null)"
else
	REAL_DOCKER="$(sed -n 's/^ExecStartPre=\(\S*\) start canopy-pg$/\1/p' "$UNIT_FILE" | head -1)"
	STUB_DOCKER="$STUB_BIN/docker"
	if [[ -n "$REAL_DOCKER" && -x "$STUB_DOCKER" ]]; then
		ok "unit's ExecStartPre docker path read for exercise: $REAL_DOCKER"
	else
		bad "could not read the docker path out of ExecStartPre" "$(grep ExecStartPre "$UNIT_FILE")"
	fi

	START_IDX=0
	POLL_IDX=0
	for i in "${!PRE_LINES[@]}"; do
		if [[ "${PRE_LINES[$i]}" == *pg_isready* ]]; then
			POLL_IDX=$((i + 1))
		else
			START_IDX=$((i + 1))
		fi
	done

	# run_pre <index>: execute one ExecStartPre line exactly as systemd would
	# (its argv, with the real docker path swapped for the stub so no container
	# is ever touched) and print the combined output.
	run_pre() {
		local idx="$1" cmd
		cmd="${PRE_LINES[$((idx - 1))]}"
		if [[ -n "$REAL_DOCKER" ]]; then
			cmd="${cmd//$REAL_DOCKER/$STUB_DOCKER}"
		fi
		PATH="$STUB_BIN:$PATH" bash -c "$cmd" 2>&1
	}

	if [[ $START_IDX -gt 0 && $POLL_IDX -gt 0 ]]; then
		# C1. the container start is issued.
		: >"$DOCKER_CALLS"
		out="$(run_pre "$START_IDX")"
		rc=$?
		if [[ $rc == 0 ]] && grep -qx 'start canopy-pg' "$DOCKER_CALLS"; then
			ok "ExecStartPre[$START_IDX] brings canopy-pg up (docker start, rc=0)"
		else
			bad "ExecStartPre[$START_IDX] did not issue 'docker start canopy-pg' (rc=$rc)" "$out $(cat "$DOCKER_CALLS" 2>/dev/null)"
		fi

		# C2. a ready database short-circuits the poll on the first probe.
		echo 0 >"$PGREADY_FILE"
		: >"$DOCKER_CALLS"
		out="$(run_pre "$POLL_IDX")"
		rc=$?
		execs="$(grep -c '^exec ' "$DOCKER_CALLS" || true)"
		if [[ $rc == 0 ]] && [[ "$execs" == "1" ]]; then
			ok "ExecStartPre[$POLL_IDX] returns 0 on the first successful pg_isready (1 probe, no wait)"
		else
			bad "readiness poll did not short-circuit on a ready DB (rc=$rc probes=$execs)" "$out"
		fi

		# C3. a database that never becomes ready → exit 1 after exactly 30
		# attempts, so systemd retries the start instead of exec'ing canopyd.
		echo 1 >"$PGREADY_FILE"
		: >"$DOCKER_CALLS"
		out="$(run_pre "$POLL_IDX")"
		rc=$?
		execs="$(grep -c '^exec ' "$DOCKER_CALLS" || true)"
		if [[ $rc == 1 ]] && [[ "$execs" == "30" ]]; then
			ok "ExecStartPre[$POLL_IDX] gives up after exactly 30 attempts with exit 1 (no crash-loop into a dead DB)"
		else
			bad "readiness poll timeout behaviour wrong (rc=$rc want 1, probes=$execs want 30)" "$out"
		fi
	else
		bad "could not identify the start/poll ExecStartPre lines (start=$START_IDX poll=$POLL_IDX)" "${PRE_LINES[*]}"
	fi
fi

# ═══ D. deploy-canopyd.sh unit sync ════════════════════════════════════════
echo ""
echo "== D. deploy-canopyd.sh unit sync =="

if [[ ! -f "$DEPLOY_SCRIPT" ]]; then
	bad "scripts/deploy-canopyd.sh exists" ""
else
	labels="$(grep -o '^echo "\[[0-9]/[0-9]\]' "$DEPLOY_SCRIPT" | sed 's/.*\[\(.*\)\]/\1/' | tr '\n' ' ')"
	want="0/6 1/6 2/6 3/6 4/6 5/6 6/6 "
	if [[ "$labels" == "$want" ]]; then
		ok "deploy step labels renumbered 0..6 with the unit sync inserted"
	else
		bad "deploy step labels not renumbered as expected" "got: '$labels' want: '$want'"
	fi

	B="$(grep -n 'BEGIN GAP-099 unit sync' "$DEPLOY_SCRIPT" | cut -d: -f1 | head -1)"
	E="$(grep -n 'END GAP-099 unit sync' "$DEPLOY_SCRIPT" | cut -d: -f1 | head -1)"
	RESTART="$(grep -n '^systemctl --user restart "\$SERVICE_NAME"' "$DEPLOY_SCRIPT" | cut -d: -f1 | head -1)"

	if [[ -n "$B" && -n "$E" && "$B" -lt "$E" ]]; then
		sed -n "$((B + 1)),$((E - 1))p" "$DEPLOY_SCRIPT" >"$WORK/unit-sync.sh"
		block_lines="$(wc -l <"$WORK/unit-sync.sh")"
		if [[ "$block_lines" -ge 15 ]]; then
			ok "extracted the $block_lines-line GAP-099 unit-sync block verbatim"
		else
			bad "extracted unit-sync block is too short to be the real thing ($block_lines lines)" "$(cat "$WORK/unit-sync.sh")"
		fi
		if grep -qF -- 'cmp -s "$UNIT_SOURCE" "$UNIT_PATH"' "$WORK/unit-sync.sh" \
			&& grep -qF -- '--user daemon-reload' "$WORK/unit-sync.sh" \
			&& grep -qF -- 'mv -f "$UNIT_TMP" "$UNIT_PATH"' "$WORK/unit-sync.sh"; then
			ok "unit sync compares the repo copy, installs atomically, then daemon-reloads"
		else
			bad "unit sync is missing the compare / atomic install / daemon-reload shape" "$(cat "$WORK/unit-sync.sh")"
		fi
	else
		bad "no BEGIN/END GAP-099 unit sync markers in deploy-canopyd.sh" "$(grep -n 'GAP-099' "$DEPLOY_SCRIPT")"
	fi

	if [[ -n "$RESTART" && -n "$B" && "$B" -lt "$RESTART" ]]; then
		ok "unit sync runs BEFORE the service restart (line $B < $RESTART)"
	else
		bad "unit sync is not before the restart step (sync=$B restart=$RESTART)" ""
	fi

	# Behavioural: drive the extracted block with a temp UNIT_DIR and a stub
	# systemctl. The real unit dir and real systemd are never involved.
	cat >"$WORK/run-unit-sync.sh" <<'EOF'
#!/usr/bin/env bash
# Harness for the extracted GAP-099 unit-sync block: fail() and the unit
# variables the deploy script defines are supplied here, everything else is the
# shipped code under test.
set -euo pipefail
fail() { echo "DEPLOY FAILED: $*" >&2; exit 1; }
UNIT_DIR="$1"
UNIT_SOURCE="$2"
SYSTEMCTL="$3"
SERVICE_NAME="${4:-canopy-canopyd}"
UNIT_PATH="$UNIT_DIR/$SERVICE_NAME.service"
source "$5"
EOF
	cat >"$WORK/stub-systemctl" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$SYSTEMCTL_CALLS"
exit 0
EOF
	chmod +x "$WORK/run-unit-sync.sh" "$WORK/stub-systemctl"

	SYNC_BLOCK="$WORK/unit-sync.sh"
	STUB_SYSTEMCTL="$WORK/stub-systemctl"

	if [[ -s "$SYNC_BLOCK" ]]; then
		# D1. drift → the repo copy replaces the live unit, daemon-reload runs.
		D1="$WORK/d1"
		mkdir -p "$D1"
		printf '[Service]\nExecStart=/old/binary\n' >"$D1/canopy-canopyd.service"
		: >"$SYSTEMCTL_CALLS"
		out="$(bash "$WORK/run-unit-sync.sh" "$D1" "$UNIT_FILE" "$STUB_SYSTEMCTL" canopy-canopyd "$SYNC_BLOCK" 2>&1)"
		rc=$?
		if [[ $rc == 0 ]] && cmp -s "$UNIT_FILE" "$D1/canopy-canopyd.service"; then
			ok "drift: the drifted live unit was replaced by the repo copy (rc=0)"
		else
			bad "drift: live unit was not replaced (rc=$rc)" "$out"
		fi
		if grep -q -- '--user daemon-reload' "$SYSTEMCTL_CALLS"; then
			ok "drift: systemctl --user daemon-reload was invoked"
		else
			bad "drift: no daemon-reload after installing the unit" "$(cat "$SYSTEMCTL_CALLS" 2>/dev/null)"
		fi
		mode="$(stat -c '%a' "$D1/canopy-canopyd.service" 2>/dev/null)"
		if [[ "$mode" == "644" ]] && ! compgen -G "$D1/.canopy-canopyd.*" >/dev/null; then
			ok "drift: installed unit is chmod 644 with no temp file left behind"
		else
			bad "drift: installed unit mode/leftovers wrong (mode=$mode)" "$(ls -la "$D1")"
		fi

		# D2. no drift → no install, no daemon-reload.
		D2="$WORK/d2"
		mkdir -p "$D2"
		cp "$UNIT_FILE" "$D2/canopy-canopyd.service"
		: >"$SYSTEMCTL_CALLS"
		out="$(bash "$WORK/run-unit-sync.sh" "$D2" "$UNIT_FILE" "$STUB_SYSTEMCTL" canopy-canopyd "$SYNC_BLOCK" 2>&1)"
		rc=$?
		if [[ $rc == 0 ]] && grep -q 'already current\|current with the repo copy' <<<"$out" \
			&& [[ ! -s "$SYSTEMCTL_CALLS" ]]; then
			ok "no drift: current live unit → no reinstall and no daemon-reload (rc=0)"
		else
			bad "no drift: sync did excess work (rc=$rc calls='$(cat "$SYSTEMCTL_CALLS" 2>/dev/null)')" "$out"
		fi

		# D3. live unit missing entirely → install + daemon-reload.
		D3="$WORK/d3"
		mkdir -p "$D3"
		: >"$SYSTEMCTL_CALLS"
		out="$(bash "$WORK/run-unit-sync.sh" "$D3" "$UNIT_FILE" "$STUB_SYSTEMCTL" canopy-canopyd "$SYNC_BLOCK" 2>&1)"
		rc=$?
		if [[ $rc == 0 ]] && cmp -s "$UNIT_FILE" "$D3/canopy-canopyd.service" \
			&& grep -q -- '--user daemon-reload' "$SYSTEMCTL_CALLS"; then
			ok "missing live unit: installed from the repo copy and daemon-reloaded (rc=0)"
		else
			bad "missing live unit: not installed (rc=$rc)" "$out"
		fi

		# D4. missing repo source → loud failure, nothing installed. A silently
		# skipped unit sync is exactly how the old unit stays running.
		D4="$WORK/d4"
		mkdir -p "$D4"
		: >"$SYSTEMCTL_CALLS"
		out="$(bash "$WORK/run-unit-sync.sh" "$D4" "$WORK/absent.service" "$STUB_SYSTEMCTL" canopy-canopyd "$SYNC_BLOCK" 2>&1)"
		rc=$?
		if [[ $rc == 1 ]] && grep -q 'unit source' <<<"$out" && [[ ! -e "$D4/canopy-canopyd.service" ]]; then
			ok "missing unit source: fails loudly (rc=1) and installs nothing"
		else
			bad "missing unit source: did not fail loudly (rc=$rc)" "$out"
		fi
	fi
fi

# ═══ E. documented contract ════════════════════════════════════════════════
echo ""
echo "== E. docs/INTEGRATION.md reboot-durability contract =="

if [[ -f "$DOCS_FILE" ]]; then
	for tok in 'restart: unless-stopped' 'docker start canopy-pg' 'ExecStartPre' 'deploy/systemd/canopy-canopyd.service' 'GAP-099'; do
		if grep -qF -- "$tok" "$DOCS_FILE"; then
			ok "docs document '$tok'"
		else
			bad "docs do not document '$tok'" ""
		fi
	done
	if grep -qF -- 'docker update --restart unless-stopped canopy-pg' "$DOCS_FILE"; then
		ok "docs give the manual recovery command for a stopped canopy-pg"
	else
		bad "docs omit the manual recovery command" ""
	fi
else
	bad "docs/INTEGRATION.md exists" ""
fi

echo ""
echo "== results: $pass passed, $fail failed, $skip skipped =="
[[ $fail == 0 ]]
