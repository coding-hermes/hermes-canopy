#!/usr/bin/env bash
#
# test-check-deploy-staleness.sh (GAP-067): hermetic regression coverage for
# scripts/check-deploy-staleness.sh.
#
# All scenarios run against a synthetic git repository and a fake deployed
# artifact in a temp dir — nothing here touches the real service, the real
# install path, or the canopy repo. The fake artifact lives OUTSIDE the
# synthetic repo so it never shows up as worktree dirt.
#
# Covered controls:
#   1. current artifact                    -> exit 0, CURRENT evidence
#   2. stale artifact                      -> non-zero, STALE evidence
#   3. missing artifact                    -> non-zero, STALE evidence
#   4. stale + --deploy (clean repo)       -> fake deploy invoked, then CURRENT
#   5. dirty repo + --deploy               -> deploy NOT invoked, STALE_BLOCKED
#  6. stale + --deploy, no-op fake deploy -> still stale after deploy -> fail
#  7-11. --check-schema: embedded ==/>/< DB, missing probe binary, psql failure
# 12-13. STALE / STALE_BLOCKED append exactly one board alert line
# 14+. GAP-069 criterion 4: crash-loop restart-counter alert — baseline on
#      first run, below-threshold and counter-reset silence, >= threshold
#      alert with exact delta fields, corrupt state file, env threshold
#      override, and alert-on-the-stale-path.
# 21+. GAP-070: STALE_BLOCKED streak alert — a single refusal stays silent, a
#      second consecutive one writes a deploy_stale_blocked_alert board event
#      with its severity/reason, a CURRENT run resets the streak, the env
#      threshold override fires on the first refusal, stale-after-failed-deploy
#      (deploy command failed / still stale after deploy) alerts with its own
#      reason, and every exit code is unchanged throughout.
#
# Exit 0 only when every scenario passes.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECKER="$HERE/check-deploy-staleness.sh"
WORK="$(mktemp -d /tmp/canopy-stale-test.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
ART="$WORK/artifacts"
mkdir -p "$ART"

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
	sed 's/^/    /' <<<"$2"
	echo "--------------"
}

# mkrepo <dir>: synthetic git repo with the watched paths present.
mkrepo() {
	local repo="$1"
	git init -q -b main "$repo"
	git -C "$repo" config user.name tester
	git -C "$repo" config user.email tester@test
	mkdir -p "$repo/internal/x" "$repo/cmd" "$repo/migrations"
	echo "v1" >"$repo/internal/x/a.go"
	# Mirror the production .gitignore for the checker's own runtime state
	# (the crash-loop baseline and the GAP-070 stale streak). Without it that
	# file counts as untracked dirt and the --deploy scenarios could never
	# succeed — exactly why the real repo ignores it too.
	printf '%s\n' ".coding-hermes/deploy-check-state.json" >"$repo/.gitignore"
	# The board event log is TRACKED in production (the foreman commits it), so
	# it is tracked here too: the checker's own alert file must not be what
	# makes a scenario's worktree dirty, or a clean-tree scenario could never
	# reach the deploy gate.
	mkdir -p "$repo/.coding-hermes/board"
	: >"$repo/.coding-hermes/board/events.jsonl"
	git -C "$repo" add -A
	git -C "$repo" commit -q -m init
}

# touch_new_commit <repo>: append a watched-path change as a NEW commit so
# the source timestamp advances past any earlier artifact mtime.
touch_new_commit() {
	local repo="$1"
	echo "change-$(date +%s%N)" >"$repo/internal/x/a.go"
	git -C "$repo" add -A
	git -C "$repo" commit -q -m bump
}

# make_stale <path>: give the artifact an ancient mtime so it is stale against
# any repo whose HEAD is newer than epoch 1 (deterministic, no sleeps).
make_stale() {
	touch -d "@1" "$1"
}

# run_case <name> <want_rc> [env=..., ...] -- [checker args...]
# One env assignment may be EVIDENCE='<space-separated tokens>' that must all
# appear in the output. Clears the fake deploy marker first so "deploy
# invoked" is observable per case.
run_case() {
	local name="$1" want_rc="$2"
	shift 2
	local -a envs=()
	while [[ ${1:-} != "--" ]]; do
		envs+=("$1")
		shift
	done
	shift
	local -a cargs=("$@")

	local evidence=""
	local -a pass_envs=()
	local e
	for e in "${envs[@]}"; do
		if [[ "$e" == EVIDENCE=* ]]; then
			evidence="${e#EVIDENCE=}"
		else
			pass_envs+=("$e")
		fi
	done

	rm -f "$WORK/deploy-invoked"
	local out rc
	out="$(env ${pass_envs[@]+"${pass_envs[@]}"} bash "$CHECKER" ${cargs[@]+"${cargs[@]}"} 2>&1)"
	rc=$?

	if [[ "$rc" != "$want_rc" ]]; then
		bad "$name (want rc=$want_rc got rc=$rc)" "$out"
		return
	fi
	local tok
	for tok in $evidence; do
		if ! grep -q -- "$tok" <<<"$out"; then
			bad "$name (rc ok, evidence '$tok' missing)" "$out"
			return
		fi
	done
	ok "$name (rc=$rc)"
}

echo "== hermetic staleness checker tests (workdir: $WORK) =="

# ── 1. Current artifact → 0 ─────────────────────────────────────────────────
REPO="$WORK/cur"
mkrepo "$REPO"
: >"$ART/cur-canopyd"
run_case "current artifact exits 0 with CURRENT evidence" 0 \
	CANOPYD_STALE_REPO_ROOT="$REPO" \
	CANOPYD_STALE_PATH="$ART/cur-canopyd" \
	CANOPYD_STALE_THRESHOLD_S=86400 \
	EVIDENCE="CURRENT" --
CANOPYD_STALE_REPO_ROOT="$REPO" CANOPYD_STALE_PATH="$ART/cur-canopyd" \
	bash "$CHECKER" >"$WORK/thresh.out" 2>&1
# Deterministic: capture BEFORE grepping — a piped `grep -q` can exit early
# on the `lag:` line and SIGPIPE-kill the checker mid-write, flipping rc via
# pipefail (intermittent red). Captured output cannot race.
# Assert BOTH the printed threshold (output channel) and the default:
# unsetting the env var must print exactly the built-in 86400s, proving the
# DEFAULT (not an inherited env value) is 86400.
grep -q "threshold 86400s" "$WORK/thresh.out" \
	&& ok "default threshold is 86400s" \
	|| bad "default threshold is not 86400s" "missing threshold line"
thresh_out="$(env -u CANOPYD_STALE_THRESHOLD_S \
	CANOPYD_STALE_REPO_ROOT="$REPO" CANOPYD_STALE_PATH="$ART/cur-canopyd" \
	bash "$CHECKER" 2>&1)"
grep -q "threshold 86400s" <<<"$thresh_out" \
	&& ok "built-in default threshold is 86400s (env unset)" \
	|| bad "built-in default threshold is not 86400s" "$thresh_out"

# ── 2. Stale artifact → non-zero + STALE evidence ───────────────────────────
REPO="$WORK/stale"
mkrepo "$REPO"
touch_new_commit "$REPO"
: >"$ART/stale-canopyd"
make_stale "$ART/stale-canopyd"
run_case "stale artifact exits 1 with STALE evidence" 1 \
	CANOPYD_STALE_REPO_ROOT="$REPO" \
	CANOPYD_STALE_PATH="$ART/stale-canopyd" \
	CANOPYD_STALE_THRESHOLD_S=86400 \
	EVIDENCE="STALE lag" --

# ── 3. Missing artifact → non-zero ──────────────────────────────────────────
run_case "missing artifact exits 1 with STALE evidence" 1 \
	CANOPYD_STALE_REPO_ROOT="$REPO" \
	CANOPYD_STALE_PATH="$ART/absent-binary" \
	CANOPYD_STALE_THRESHOLD_S=86400 \
	EVIDENCE="STALE missing" --

# ── 4. Stale + --deploy (clean) → deploy invoked, becomes CURRENT ───────────
REPO="$WORK/deploy"
mkrepo "$REPO"
touch_new_commit "$REPO"
: >"$ART/deploy-canopyd"
make_stale "$ART/deploy-canopyd"
run_case "stale + --deploy runs fake deploy and lands CURRENT" 0 \
	CANOPYD_STALE_REPO_ROOT="$REPO" \
	CANOPYD_STALE_PATH="$ART/deploy-canopyd" \
	CANOPYD_STALE_THRESHOLD_S=86400 \
	CANOPYD_DEPLOY_CMD="touch $ART/deploy-canopyd && touch $WORK/deploy-invoked" \
	CANOPYD_DEPLOY_DIR="$WORK" \
	EVIDENCE="CURRENT" -- --deploy
[[ -f "$WORK/deploy-invoked" ]] \
	&& ok "fake deploy command was actually invoked" \
	|| bad "fake deploy command was never invoked" ""

# ── 5. Dirty repo + --deploy → refuse, no deploy ────────────────────────────
REPO="$WORK/dirty"
mkrepo "$REPO"
touch_new_commit "$REPO"
: >"$ART/dirty-canopyd"
make_stale "$ART/dirty-canopyd"
echo "half-written worker code" >"$REPO/internal/x/wip.go"
run_case "dirty worktree + --deploy exits 2 STALE_BLOCKED, no deploy" 2 \
	CANOPYD_STALE_REPO_ROOT="$REPO" \
	CANOPYD_STALE_PATH="$ART/dirty-canopyd" \
	CANOPYD_STALE_THRESHOLD_S=86400 \
	CANOPYD_DEPLOY_CMD="touch $ART/dirty-canopyd" \
	CANOPYD_DEPLOY_DIR="$WORK" \
	EVIDENCE="STALE_BLOCKED NOT invoked" -- --deploy
[[ -f "$WORK/deploy-invoked" ]] \
	&& bad "deploy was invoked despite dirty worktree" "" \
	|| ok "deploy NOT invoked on dirty worktree"

# Untracked-only dirtiness must also refuse (porcelain covers it):
REPO="$WORK/untracked"
mkrepo "$REPO"
touch_new_commit "$REPO"
echo "stray" >"$REPO/internal/x/stray.txt"
run_case "untracked-only dirt also blocks --deploy" 2 \
	CANOPYD_STALE_REPO_ROOT="$REPO" \
	CANOPYD_STALE_PATH="$ART/dirty-canopyd" \
	CANOPYD_STALE_THRESHOLD_S=86400 \
	CANOPYD_DEPLOY_CMD="touch $ART/dirty-canopyd" \
	CANOPYD_DEPLOY_DIR="$WORK" \
	EVIDENCE="STALE_BLOCKED" -- --deploy
[[ -f "$WORK/deploy-invoked" ]] \
	&& bad "untracked dirt still triggered deploy" "" \
	|| ok "deploy NOT invoked on untracked-only dirt"

# ── 6. Deploy that does not fix staleness → fail ────────────────────────────
REPO="$WORK/stillstale"
mkrepo "$REPO"
touch_new_commit "$REPO"
: >"$ART/noop-canopyd"
make_stale "$ART/noop-canopyd"
run_case "no-op deploy leaves stale artifact → checker fails" 3 \
	CANOPYD_STALE_REPO_ROOT="$REPO" \
	CANOPYD_STALE_PATH="$ART/noop-canopyd" \
	CANOPYD_STALE_THRESHOLD_S=86400 \
	CANOPYD_DEPLOY_CMD="true" \
	CANOPYD_DEPLOY_DIR="$WORK" \
	EVIDENCE="still stale" -- --deploy

# ── GAP-069: --check-schema battery ─────────────────────────────────────────
# Fake canopyd: a stub that prints a fixed "embedded" schema version and
# exits 0 — exactly the contract of the real `canopyd -print-schema-version`.
mk_fake_canopyd() {
	local path="$1" version="$2"
	cat >"$path" <<EOF
#!/usr/bin/env bash
if [[ "\${1:-}" == "-print-schema-version" ]]; then
	echo "$version"
	exit 0
fi
echo "unexpected argv: \$*" >&2
exit 9
EOF
	chmod +x "$path"
}

# fake psql: prints the fixed "DB" schema version for the known query,
# so the checker's comparison is fully deterministic.
mk_fake_psql() {
	local path="$1" version="$2"
	cat >"$path" <<EOF
#!/usr/bin/env bash
for a in "\$@"; do
	if [[ "\$a" == *"max(version)"* ]]; then
		echo "$version"
		exit 0
	fi
done
echo "fake psql: no query recognized" >&2
exit 18
EOF
	chmod +x "$path"
}

FAKE_PSQL="$WORK/fake-psql"
mk_fake_psql "$FAKE_PSQL" 46

# 7. embedded == db → 0 (current schema is fine)
SCHEMA_BIN="$WORK/canopyd-eq"
mk_fake_canopyd "$SCHEMA_BIN" 46
run_case "check-schema: embedded 46 == db 46 exits 0" 0 \
	CANOPYD_SCHEMA_PROBE_BIN="$SCHEMA_BIN" \
	CANOPYD_SCHEMA_PSQL="$FAKE_PSQL" \
	EVIDENCE="check-schema: binary embeds schema 46, DB is at schema 46 SCHEMA_OK" -- --check-schema

# 8. embedded > db → 0 (binary understands an OLDER database too)
SCHEMA_BIN="$WORK/canopyd-gt"
mk_fake_canopyd "$SCHEMA_BIN" 47
run_case "check-schema: embedded 47 > db 46 exits 0" 0 \
	CANOPYD_SCHEMA_PROBE_BIN="$SCHEMA_BIN" \
	CANOPYD_SCHEMA_PSQL="$FAKE_PSQL" \
	EVIDENCE="SCHEMA_OK" -- --check-schema

# 9. embedded < db → 1 with the STALE BUILD message
SCHEMA_BIN="$WORK/canopyd-lt"
mk_fake_canopyd "$SCHEMA_BIN" 42
run_case "check-schema: embedded 42 < db 46 exits 1 STALE BUILD" 1 \
	CANOPYD_SCHEMA_PROBE_BIN="$SCHEMA_BIN" \
	CANOPYD_SCHEMA_PSQL="$FAKE_PSQL" \
	EVIDENCE="STALE BUILD: binary embeds schema 42, DB is at 46 rebuild and redeploy" -- --check-schema

# 10. probe binary missing → 3 (operational error, never a silent pass)
run_case "check-schema: probe binary missing exits 3" 3 \
	CANOPYD_SCHEMA_PROBE_BIN="$WORK/absent-canopyd" \
	CANOPYD_SCHEMA_PSQL="$FAKE_PSQL" \
	EVIDENCE="check-schema: canopyd binary not found" -- --check-schema

# 11. psql failing → 3 (DB probe failure is an ERROR, never a silent pass)
SCHEMA_BIN="$WORK/canopyd-eq2"
mk_fake_canopyd "$SCHEMA_BIN" 46
run_case "check-schema: psql probe failure exits 3" 3 \
	CANOPYD_SCHEMA_PROBE_BIN="$SCHEMA_BIN" \
	CANOPYD_SCHEMA_PSQL="$WORK/absent-psql" \
	EVIDENCE="psql not available" -- --check-schema

# ── GAP-069: STALE detection appends exactly one board alert line ───────────
REPO="$WORK/alert"
mkrepo "$REPO"
touch_new_commit "$REPO"
ALERT_ART="$WORK/alert-artifacts"
mkdir -p "$ALERT_ART" "$REPO/.coding-hermes/board"
: >"$REPO/.coding-hermes/board/events.jsonl"
: >"$ALERT_ART/alert-canopyd"
make_stale "$ALERT_ART/alert-canopyd"
ALERT_OUT="$(env CANOPYD_STALE_REPO_ROOT="$REPO" \
	CANOPYD_STALE_PATH="$ALERT_ART/alert-canopyd" \
	CANOPYD_STALE_THRESHOLD_S=86400 \
	CANOPYD_SYSTEMCTL=/bin/true \
	CANOPYD_SERVICE_NAME=nonexistent-service \
	CANOPYD_SCHEMA_PSQL="$FAKE_PSQL" \
	CANOPYD_SCHEMA_PROBE_BIN="$WORK/canopyd-eq" \
	bash "$CHECKER" 2>&1)"
alert_rc=$?
if [[ "$alert_rc" == "1" ]] && grep -q "board alert appended" <<<"$ALERT_OUT"; then
	ok "stale run appended a board alert (rc=1)"
else
	bad "stale run did not append the board alert (rc=$alert_rc)" "$ALERT_OUT"
fi
python3 - "$REPO/.coding-hermes/board/events.jsonl" <<'PY'
import json, sys
lines = [l for l in open(sys.argv[1], encoding="utf-8") if l.strip()]
assert len(lines) == 1, f"expected exactly 1 event line, got {len(lines)}"
ev = json.loads(lines[0])
assert ev["event_type"] == "deploy_stale_alert", ev
assert ev["task_id"] == "GAP-069", ev
assert ev["actor"] == "deploy-staleness-check", ev
assert isinstance(ev["id"], int), ev
detail = json.loads(ev["detail"])
assert detail["reason"] == "STALE", detail
assert isinstance(detail["lag_seconds"], int), detail
assert detail["embedded_schema_version"] == 46, detail
assert detail["db_schema_version"] == 46, detail
assert detail["service_restarts"] == "unknown", detail
print("PASS: board alert line is well-formed JSONL with full GAP-069 detail")
PY
[[ $? == 0 ]] && ok "board alert line parses as the exact events.jsonl shape" \
	|| bad "board alert line failed shape validation" "$(cat "$REPO/.coding-hermes/board/events.jsonl")"

# 12. STALE_BLOCKED also appends (dirty worktree path)
REPO="$WORK/alert2"
mkrepo "$REPO"
touch_new_commit "$REPO"
mkdir -p "$REPO/.coding-hermes/board"
: >"$REPO/.coding-hermes/board/events.jsonl"
: >"$ALERT_ART/alert2-canopyd"
make_stale "$ALERT_ART/alert2-canopyd"
echo "half-written" >"$REPO/internal/x/wip.go"
BLOCKED_OUT="$(env CANOPYD_STALE_REPO_ROOT="$REPO" \
	CANOPYD_STALE_PATH="$ALERT_ART/alert2-canopyd" \
	CANOPYD_STALE_THRESHOLD_S=86400 \
	CANOPYD_SYSTEMCTL=/bin/true \
	CANOPYD_SERVICE_NAME=nonexistent-service \
	bash "$CHECKER" --deploy 2>&1)"
blocked_rc=$?
if [[ "$blocked_rc" == "2" ]] && grep -q "reason=STALE_BLOCKED" <<<"$BLOCKED_OUT"; then
	ok "STALE_BLOCKED appended a board alert (rc=2)"
else
	bad "STALE_BLOCKED did not append a board alert (rc=$blocked_rc)" "$BLOCKED_OUT"
fi

# ── GAP-069 criterion 4: crash-loop restart-counter alert ───────────────────
# fake systemctl: prints the NRestarts value recorded in a file, so a test can
# move the counter between runs with no real service anywhere in sight. Same
# stubbing idea as mk_fake_psql above: match the argv the checker uses.
# Third arg (optional, GAP-070): a file holding the unit's is-active answer, so
# the stale-blocked severity probe is drivable too (systemd's own exit status
# is mirrored: 0 when active, 3 otherwise).
mk_fake_systemctl() {
	local path="$1" nrfile="$2" activefile="${3:-}"
	cat >"$path" <<EOF
#!/usr/bin/env bash
for a in "\$@"; do
	if [[ "\$a" == "NRestarts" ]]; then
		cat "$nrfile" 2>/dev/null || echo 0
		exit 0
	fi
	if [[ "\$a" == "is-active" ]]; then
		state="\$(cat "$activefile" 2>/dev/null)"
		state="\${state:-inactive}"
		echo "\$state"
		[[ "\$state" == "active" ]] && exit 0
		exit 3
	fi
done
echo "fake systemctl: unrecognized invocation: \$*" >&2
exit 17
EOF
	chmod +x "$path"
}

# crashloop_run <repo> <artifact> <nrestarts> [extra env assignments...]
# One checker run (staleness mode) with a stub systemctl reporting <nrestarts>;
# prints combined output, caller captures rc from the assignment.
CRASHLOOP_NRFILE="$WORK/fake-nrestarts"
FAKE_SYSTEMCTL="$WORK/fake-systemctl"
mk_fake_systemctl "$FAKE_SYSTEMCTL" "$CRASHLOOP_NRFILE"
crashloop_run() {
	local repo="$1" art="$2" n="$3"
	shift 3
	echo "$n" >"$CRASHLOOP_NRFILE"
	env CANOPYD_STALE_REPO_ROOT="$repo" \
		CANOPYD_STALE_PATH="$art" \
		CANOPYD_STALE_THRESHOLD_S=86400 \
		CANOPYD_SYSTEMCTL="$FAKE_SYSTEMCTL" \
		CANOPYD_SERVICE_NAME=canopy-canopyd \
		CANOPYD_SCHEMA_PSQL="$FAKE_PSQL" \
		"$@" \
		bash "$CHECKER" 2>&1
}

# crashloop_events <repo>: number of crash-loop alert lines on that board.
crashloop_events() {
	local f="$1/.coding-hermes/board/events.jsonl"
	if [[ ! -f "$f" ]]; then
		echo 0
		return
	fi
	grep -c 'deploy_crashloop_alert' "$f" || true
}

# state_nrestarts <repo>: baseline value the checker persisted (or ERROR:...).
state_nrestarts() {
	python3 - "$1/.coding-hermes/deploy-check-state.json" <<'PY'
import json, sys
try:
    with open(sys.argv[1], encoding="utf-8") as f:
        print(json.load(f).get("nrestarts"))
except Exception as e:
    print("ERROR:%s" % e)
PY
}

# mk_crashloop_repo <dir>: fresh synthetic repo + empty board, artifact CURRENT
# (created after the last commit, so staleness is NOT in play).
mk_crashloop_repo() {
	local repo="$1" art="$2"
	mkrepo "$repo"
	mkdir -p "$repo/.coding-hermes/board"
	: >"$repo/.coding-hermes/board/events.jsonl"
	: >"$art"
}

# 14. First run with NO state file -> baseline recorded, no alert, rc 0.
REPO="$WORK/cl-fresh"
CL_ART="$WORK/artifacts/cl-fresh-canopyd"
mk_crashloop_repo "$REPO" "$CL_ART"
OUT="$(crashloop_run "$REPO" "$CL_ART" 7)"
rc=$?
if [[ "$rc" == "0" ]] && grep -q "no usable prior state" <<<"$OUT"; then
	ok "crashloop: first run records baseline, rc unchanged (rc=0)"
else
	bad "crashloop: first run did not record a baseline cleanly (rc=$rc)" "$OUT"
fi
if [[ "$(crashloop_events "$REPO")" == "0" ]] && [[ "$(state_nrestarts "$REPO")" == "7" ]]; then
	ok "crashloop: no prior state -> zero alerts, baseline persisted as 7"
else
	bad "crashloop: no-prior-state run alerted or mis-persisted (events=$(crashloop_events "$REPO") state=$(state_nrestarts "$REPO"))" "$OUT"
fi

# 15. Climb below threshold, then an unchanged counter -> still silent, state
#     tracks reality both times.
OUT="$(crashloop_run "$REPO" "$CL_ART" 20)" # delta 13 < 50
rc=$?
OUT2="$(crashloop_run "$REPO" "$CL_ART" 20)" # delta 0
rc2=$?
if [[ "$rc" == "0" ]] && [[ "$rc2" == "0" ]] && grep -q "delta 13 < threshold 50" <<<"$OUT"; then
	ok "crashloop: below-threshold climb is silent and rc unchanged (rc=0)"
else
	bad "crashloop: below-threshold climb misbehaved (rc=$rc rc2=$rc2)" "$OUT"
fi
if [[ "$(crashloop_events "$REPO")" == "0" ]] && [[ "$(state_nrestarts "$REPO")" == "20" ]]; then
	ok "crashloop: sub-threshold runs keep zero alerts and a current baseline (20)"
else
	bad "crashloop: sub-threshold run alerted or stale baseline (events=$(crashloop_events "$REPO") state=$(state_nrestarts "$REPO"))" "$OUT2"
fi

# 16. Counter RESET (systemd restarted the unit) -> no alert, re-baseline.
OUT="$(crashloop_run "$REPO" "$CL_ART" 3)" # delta -17
rc=$?
if [[ "$rc" == "0" ]] && [[ "$(crashloop_events "$REPO")" == "0" ]] && [[ "$(state_nrestarts "$REPO")" == "3" ]]; then
	ok "crashloop: counter reset (20 -> 3) re-baselines silently"
else
	bad "crashloop: counter reset misbehaved (rc=$rc events=$(crashloop_events "$REPO") state=$(state_nrestarts "$REPO"))" "$OUT"
fi

# 17. Climb >= threshold -> exactly ONE deploy_crashloop_alert, rc unchanged,
#     baseline advanced, detail carries the exact delta fields.
OUT="$(crashloop_run "$REPO" "$CL_ART" 75)" # delta 72 >= 50
rc=$?
if [[ "$rc" == "0" ]] && grep -q "CRASHLOOP" <<<"$OUT" && grep -q "board alert appended" <<<"$OUT"; then
	ok "crashloop: climb >= threshold alerts on stdout and rc unchanged (rc=0)"
else
	bad "crashloop: climb >= threshold did not alert (rc=$rc)" "$OUT"
fi
if [[ "$(crashloop_events "$REPO")" == "1" ]] && [[ "$(state_nrestarts "$REPO")" == "75" ]]; then
	ok "crashloop: exactly one alert line appended, baseline advanced to 75"
else
	bad "crashloop: alert count/baseline wrong (events=$(crashloop_events "$REPO") state=$(state_nrestarts "$REPO"))" "$OUT"
fi
python3 - "$REPO/.coding-hermes/board/events.jsonl" <<'PY'
import json, re, sys
lines = [l for l in open(sys.argv[1], encoding="utf-8") if l.strip()]
evs = [json.loads(l) for l in lines]
crashes = [e for e in evs if e.get("event_type") == "deploy_crashloop_alert"]
assert len(crashes) == 1, f"expected exactly 1 crash-loop event, got {len(crashes)}: {evs}"
ev = crashes[0]
assert ev["task_id"] == "GAP-069", ev
assert ev["actor"] == "deploy-staleness-check", ev
assert isinstance(ev["id"], int), ev
assert re.match(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$", ev["timestamp"]), ev
d = json.loads(ev["detail"])
assert d["restart_before"] == 3, d
assert d["restart_after"] == 75, d
assert d["delta"] == 72, d
assert d["service"] == "canopy-canopyd", d
assert re.match(r"^[0-9a-f]{40}$", d["source_commit"]), d
assert d["threshold"] == 50, d
print("PASS: crash-loop alert line is well-formed JSONL with exact delta fields")
PY
[[ $? == 0 ]] && ok "crashloop: alert line carries restart_before/after, delta, service, src commit" \
	|| bad "crashloop: alert line failed shape validation" "$(cat "$REPO/.coding-hermes/board/events.jsonl")"

# 18. Missing/corrupt state file -> baseline recorded, no alert, rc unchanged.
REPO="$WORK/cl-corrupt"
CL_ART2="$WORK/artifacts/cl-corrupt-canopyd"
mk_crashloop_repo "$REPO" "$CL_ART2"
printf 'this is not json{{{\n' >"$REPO/.coding-hermes/deploy-check-state.json"
OUT="$(crashloop_run "$REPO" "$CL_ART2" 99)"
rc=$?
if [[ "$rc" == "0" ]] && grep -q "no usable prior state" <<<"$OUT" && [[ "$(crashloop_events "$REPO")" == "0" ]]; then
	ok "crashloop: corrupt state file re-baselines with no alert, rc unchanged (rc=0)"
else
	bad "crashloop: corrupt state file misbehaved (rc=$rc)" "$OUT"
fi
[[ "$(state_nrestarts "$REPO")" == "99" ]] && ok "crashloop: corrupt state replaced by a valid baseline (99)" \
	|| bad "crashloop: corrupt state was not replaced (state=$(state_nrestarts "$REPO"))" ""

# 19. CANOPYD_CRASHLOOP_THRESHOLD overrides the default.
REPO="$WORK/cl-thresh"
CL_ART3="$WORK/artifacts/cl-thresh-canopyd"
mk_crashloop_repo "$REPO" "$CL_ART3"
OUT="$(crashloop_run "$REPO" "$CL_ART3" 1000)"
OUT="$(crashloop_run "$REPO" "$CL_ART3" 1003 CANOPYD_CRASHLOOP_THRESHOLD=5)" # delta 3 < 5
rc=$?
OUT2="$(crashloop_run "$REPO" "$CL_ART3" 1009 CANOPYD_CRASHLOOP_THRESHOLD=5)" # delta 6 >= 5
rc2=$?
if [[ "$rc" == "0" ]] && [[ "$rc2" == "0" ]] \
	&& [[ "$(crashloop_events "$REPO")" == "1" ]] \
	&& grep -q "delta 6 >= threshold 5" <<<"$OUT2"; then
	ok "crashloop: CANOPYD_CRASHLOOP_THRESHOLD=5 gates a delta-6 climb (rc unchanged)"
else
	bad "crashloop: threshold override misbehaved (rc=$rc rc2=$rc2 events=$(crashloop_events "$REPO"))" "$OUT2"
fi

# 20. A crash-loop is reported even on the STALE exit path, alongside the
#     staleness alert, with the exit code unchanged (rc=1).
REPO="$WORK/cl-stale"
mkrepo "$REPO"
mkdir -p "$REPO/.coding-hermes/board"
: >"$REPO/.coding-hermes/board/events.jsonl"
touch_new_commit "$REPO"
CL_ART4="$WORK/artifacts/cl-stale-canopyd"
: >"$CL_ART4"
make_stale "$CL_ART4"
OUT="$(crashloop_run "$REPO" "$CL_ART4" 0)"
OUT="$(crashloop_run "$REPO" "$CL_ART4" 60)" # delta 60 >= 50, artifact still stale
rc=$?
types_seen="$(python3 - "$REPO/.coding-hermes/board/events.jsonl" <<'PY'
import json, sys
print(",".join(sorted({json.loads(l)["event_type"] for l in open(sys.argv[1], encoding="utf-8") if l.strip()})))
PY
)"
if [[ "$rc" == "1" ]] && [[ "$types_seen" == "deploy_crashloop_alert,deploy_stale_alert" ]]; then
	ok "crashloop: stale run emits BOTH alerts and keeps rc=1"
else
	bad "crashloop: stale run alert set wrong (rc=$rc types='$types_seen')" "$OUT"
fi

# ── GAP-070: STALE_BLOCKED streak alert ─────────────────────────────────────
# A `--deploy` run that refuses to deploy (dirty worktree) is the correct call
# and a terrible thing to leave silent: the 2026-09-13 tick exited 2 and nobody
# looked again for 24h. These cases drive the streak counter, the threshold
# gate, the board event and its severity field, the reset — and prove the exit
# codes are untouched.
SB_ACTIVE_FILE="$WORK/fake-active"
FAKE_SYSTEMCTL_SB="$WORK/fake-systemctl-sb"
mk_fake_systemctl "$FAKE_SYSTEMCTL_SB" "$CRASHLOOP_NRFILE" "$SB_ACTIVE_FILE"

# blocked_run <repo> <artifact> <nrestarts> <active-state> [extra env...]
# One `--deploy` checker run against a stub systemctl that answers BOTH the
# restart counter and is-active. Prints combined output; caller reads rc.
blocked_run() {
	local repo="$1" art="$2" n="$3" active="$4"
	shift 4
	echo "$n" >"$CRASHLOOP_NRFILE"
	printf '%s\n' "$active" >"$SB_ACTIVE_FILE"
	env CANOPYD_STALE_REPO_ROOT="$repo" \
		CANOPYD_STALE_PATH="$art" \
		CANOPYD_STALE_THRESHOLD_S=86400 \
		CANOPYD_SYSTEMCTL="$FAKE_SYSTEMCTL_SB" \
		CANOPYD_SERVICE_NAME=canopy-canopyd \
		CANOPYD_SCHEMA_PSQL="$FAKE_PSQL" \
		"$@" \
		bash "$CHECKER" --deploy 2>&1
}

# blocked_alerts <repo>: deploy_stale_blocked_alert lines on that board.
blocked_alerts() {
	local f="$1/.coding-hermes/board/events.jsonl"
	if [[ ! -f "$f" ]]; then
		echo 0
		return
	fi
	grep -c 'deploy_stale_blocked_alert' "$f" || true
}

# state_key <repo> <key>: one value out of the shared state file (or ERROR:...).
state_key() {
	python3 - "$1/.coding-hermes/deploy-check-state.json" "$2" <<'PY'
import json, sys
try:
    with open(sys.argv[1], encoding="utf-8") as f:
        print(json.load(f).get(sys.argv[2]))
except Exception as e:
    print("ERROR:%s" % e)
PY
}

# mk_stale_blocked_repo <dir> <artifact> [--dirty]
# Fresh synthetic repo whose artifact is STALE; --dirty adds the untracked
# half-written file that makes --deploy refuse.
mk_stale_blocked_repo() {
	local repo="$1" art="$2" dirty="${3:-}"
	mkrepo "$repo"
	touch_new_commit "$repo"
	: >"$art"
	make_stale "$art"
	if [[ "$dirty" == "--dirty" ]]; then
		echo "half-written worker code" >"$repo/internal/x/wip.go"
	fi
	return 0
}

# 21. First refusal: below threshold. No alert, streak 1, rc still 2 — and a
#     foreign key seeded into the shared state file survives the run, proving
#     the streak writer is a read-modify-write, not a clobber of the GAP-069
#     restart baseline.
REPO="$WORK/sb-first"
SB_ART1="$WORK/artifacts/sb-first-canopyd"
mk_stale_blocked_repo "$REPO" "$SB_ART1" --dirty
printf '{"canary": "keep-me"}\n' >"$REPO/.coding-hermes/deploy-check-state.json"
OUT="$(blocked_run "$REPO" "$SB_ART1" 0 inactive)"
rc=$?
if [[ "$rc" == "2" ]] && [[ "$(blocked_alerts "$REPO")" == "0" ]] \
	&& [[ "$(state_key "$REPO" stale_blocked_streak)" == "1" ]] \
	&& [[ "$(state_key "$REPO" canary)" == "keep-me" ]] \
	&& grep -q "1/2 consecutive un-remediated stale runs (STALE_BLOCKED)" <<<"$OUT"; then
	ok "stale-blocked: one refusal is below threshold — rc 2, no alert, streak 1"
else
	bad "stale-blocked: first refusal misbehaved (rc=$rc alerts=$(blocked_alerts "$REPO") streak=$(state_key "$REPO" stale_blocked_streak) canary=$(state_key "$REPO" canary))" "$OUT"
fi

# 22. Second consecutive refusal: alert fires, rc unchanged, severity is the
#     outage class because the stub unit is NOT active while the artifact is
#     stale.
OUT="$(blocked_run "$REPO" "$SB_ART1" 0 inactive)"
rc=$?
if [[ "$rc" == "2" ]] && [[ "$(blocked_alerts "$REPO")" == "1" ]] \
	&& [[ "$(state_key "$REPO" stale_blocked_streak)" == "2" ]] \
	&& grep -q "STALE_BLOCKED_ALERT: 2 consecutive un-remediated stale runs (STALE_BLOCKED, severity=stale_refused_to_start, threshold 2)" <<<"$OUT"; then
	ok "stale-blocked: second consecutive refusal alerts with rc unchanged (rc=2)"
else
	bad "stale-blocked: second refusal did not alert (rc=$rc alerts=$(blocked_alerts "$REPO") streak=$(state_key "$REPO" stale_blocked_streak))" "$OUT"
fi
python3 - "$REPO/.coding-hermes/board/events.jsonl" <<'PY'
import json, re, sys
lines = [l for l in open(sys.argv[1], encoding="utf-8") if l.strip()]
evs = [json.loads(l) for l in lines]
blocked = [e for e in evs if e.get("event_type") == "deploy_stale_blocked_alert"]
assert len(blocked) == 1, f"expected exactly 1 stale-blocked alert, got {len(blocked)}: {evs}"
ev = blocked[0]
assert ev["task_id"] == "GAP-070", ev
assert ev["actor"] == "deploy-staleness-check", ev
assert isinstance(ev["id"], int), ev
assert re.match(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$", ev["timestamp"]), ev
d = json.loads(ev["detail"])
assert d["reason"] == "STALE_BLOCKED", d
assert d["severity"] == "stale_refused_to_start", d
assert d["outage_class"] is True, d
assert d["consecutive_runs"] == 2, d
assert d["threshold"] == 2, d
assert isinstance(d["lag_seconds"], int), d
assert re.match(r"^[0-9a-f]{40}$", d["source_commit"]), d
assert d["deployed_binary"].endswith("sb-first-canopyd"), d
print("PASS: stale-blocked alert line is well-formed JSONL with severity/reason")
PY
[[ $? == 0 ]] && ok "stale-blocked: alert line carries reason, severity, outage_class, streak and lag" \
	|| bad "stale-blocked: alert line failed shape validation" "$(cat "$REPO/.coding-hermes/board/events.jsonl")"

# 23. A run that finds the artifact CURRENT clears the streak (rc 0), and the
#     next refusal starts over at 1 with no repeat alert — the alert re-arms.
touch "$SB_ART1"
OUT="$(blocked_run "$REPO" "$SB_ART1" 0 inactive)"
rc=$?
if [[ "$rc" == "0" ]] && [[ "$(state_key "$REPO" stale_blocked_streak)" == "0" ]] \
	&& [[ "$(blocked_alerts "$REPO")" == "1" ]] \
	&& grep -q "stale-blocked streak reset to 0 (was 2)" <<<"$OUT"; then
	ok "stale-blocked: a CURRENT run resets the streak to 0 (no extra alert)"
else
	bad "stale-blocked: CURRENT run did not reset the streak (rc=$rc streak=$(state_key "$REPO" stale_blocked_streak) alerts=$(blocked_alerts "$REPO"))" "$OUT"
fi
make_stale "$SB_ART1"
OUT="$(blocked_run "$REPO" "$SB_ART1" 0 inactive)"
rc=$?
if [[ "$rc" == "2" ]] && [[ "$(state_key "$REPO" stale_blocked_streak)" == "1" ]] \
	&& [[ "$(blocked_alerts "$REPO")" == "1" ]] \
	&& grep -q "1/2 consecutive" <<<"$OUT"; then
	ok "stale-blocked: after a reset the next refusal restarts at 1 (alert re-armed)"
else
	bad "stale-blocked: post-reset refusal misbehaved (rc=$rc streak=$(state_key "$REPO" stale_blocked_streak) alerts=$(blocked_alerts "$REPO"))" "$OUT"
fi

# 24. CANOPYD_STALE_BLOCKED_THRESHOLD=1 fires on the FIRST refusal (rc still 2)
#     and reports stale_but_serving because the unit IS active; a following
#     refusal above the threshold does not repeat the alert.
REPO="$WORK/sb-thresh1"
SB_ART2="$WORK/artifacts/sb-thresh1-canopyd"
mk_stale_blocked_repo "$REPO" "$SB_ART2" --dirty
OUT="$(blocked_run "$REPO" "$SB_ART2" 0 active CANOPYD_STALE_BLOCKED_THRESHOLD=1)"
rc=$?
if [[ "$rc" == "2" ]] && [[ "$(blocked_alerts "$REPO")" == "1" ]] \
	&& grep -q "severity=stale_but_serving" <<<"$OUT"; then
	ok "stale-blocked: threshold override 1 alerts on the first refusal, rc unchanged (rc=2)"
else
	bad "stale-blocked: threshold override misbehaved (rc=$rc alerts=$(blocked_alerts "$REPO"))" "$OUT"
fi
python3 - "$REPO/.coding-hermes/board/events.jsonl" <<'PY'
import json, sys
evs = [json.loads(l) for l in open(sys.argv[1], encoding="utf-8") if l.strip()]
blocked = [e for e in evs if e.get("event_type") == "deploy_stale_blocked_alert"]
assert len(blocked) == 1, blocked
d = json.loads(blocked[0]["detail"])
assert d["severity"] == "stale_but_serving", d
assert d["outage_class"] is False, d
assert d["consecutive_runs"] == 1, d
assert d["threshold"] == 1, d
print("PASS: active-unit refusal is reported stale_but_serving, not outage-class")
PY
[[ $? == 0 ]] && ok "stale-blocked: active unit → severity stale_but_serving / outage_class false" \
	|| bad "stale-blocked: serving-severity event failed shape validation" "$(cat "$REPO/.coding-hermes/board/events.jsonl")"
OUT="$(blocked_run "$REPO" "$SB_ART2" 0 active CANOPYD_STALE_BLOCKED_THRESHOLD=1)"
rc=$?
if [[ "$rc" == "2" ]] && [[ "$(blocked_alerts "$REPO")" == "1" ]] \
	&& grep -q "alert already raised at the threshold, no repeat" <<<"$OUT"; then
	ok "stale-blocked: streak past the threshold does not repeat the alert (rc=2)"
else
	bad "stale-blocked: alert repeated past the threshold (rc=$rc alerts=$(blocked_alerts "$REPO"))" "$OUT"
fi

# 25. Stale-after-failed-deploy: the deploy DID run (clean tree, gate passed)
#     and the artifact is still stale — an outage-class stale with its own
#     reason. Two flavours: the deploy command failing, and a no-op deploy that
#     leaves the artifact stale (rc 3 both times, unchanged).
REPO="$WORK/sb-deployfail"
SB_ART3="$WORK/artifacts/sb-deployfail-canopyd"
mk_stale_blocked_repo "$REPO" "$SB_ART3" # clean tree: the gate must let this through
OUT="$(blocked_run "$REPO" "$SB_ART3" 0 inactive CANOPYD_STALE_BLOCKED_THRESHOLD=1 \
	CANOPYD_DEPLOY_CMD=false CANOPYD_DEPLOY_DIR="$WORK")"
rc=$?
if [[ "$rc" == "3" ]] && [[ "$(blocked_alerts "$REPO")" == "1" ]] \
	&& grep -q "deploy command failed" <<<"$OUT"; then
	ok "stale-blocked: failed deploy command alerts (DEPLOY_FAILED) and keeps rc=3"
else
	bad "stale-blocked: failed deploy command misbehaved (rc=$rc alerts=$(blocked_alerts "$REPO"))" "$OUT"
fi
python3 - "$REPO/.coding-hermes/board/events.jsonl" <<'PY'
import json, sys
evs = [json.loads(l) for l in open(sys.argv[1], encoding="utf-8") if l.strip()]
blocked = [e for e in evs if e.get("event_type") == "deploy_stale_blocked_alert"]
assert len(blocked) == 1, blocked
d = json.loads(blocked[0]["detail"])
assert d["reason"] == "DEPLOY_FAILED", d
assert d["severity"] == "stale_refused_to_start", d
assert d["outage_class"] is True, d
print("PASS: failed deploy is reported as an outage-class stale, reason DEPLOY_FAILED")
PY
[[ $? == 0 ]] && ok "stale-blocked: DEPLOY_FAILED event names its reason and outage class" \
	|| bad "stale-blocked: DEPLOY_FAILED event failed shape validation" "$(cat "$REPO/.coding-hermes/board/events.jsonl")"

REPO="$WORK/sb-afterdeploy"
SB_ART4="$WORK/artifacts/sb-afterdeploy-canopyd"
mk_stale_blocked_repo "$REPO" "$SB_ART4" # clean tree, deploy succeeds but fixes nothing
OUT="$(blocked_run "$REPO" "$SB_ART4" 0 inactive CANOPYD_STALE_BLOCKED_THRESHOLD=1 \
	CANOPYD_DEPLOY_CMD=true CANOPYD_DEPLOY_DIR="$WORK")"
rc=$?
if [[ "$rc" == "3" ]] && [[ "$(blocked_alerts "$REPO")" == "1" ]] \
	&& grep -q "still stale after deploy" <<<"$OUT"; then
	ok "stale-blocked: no-op deploy leaving the artifact stale alerts (rc=3)"
else
	bad "stale-blocked: still-stale-after-deploy misbehaved (rc=$rc alerts=$(blocked_alerts "$REPO"))" "$OUT"
fi
python3 - "$REPO/.coding-hermes/board/events.jsonl" <<'PY'
import json, sys
evs = [json.loads(l) for l in open(sys.argv[1], encoding="utf-8") if l.strip()]
blocked = [e for e in evs if e.get("event_type") == "deploy_stale_blocked_alert"]
assert len(blocked) == 1, blocked
d = json.loads(blocked[0]["detail"])
assert d["reason"] == "STALE_AFTER_DEPLOY", d
assert d["severity"] == "stale_refused_to_start", d
print("PASS: still-stale-after-deploy is reported with reason STALE_AFTER_DEPLOY")
PY
[[ $? == 0 ]] && ok "stale-blocked: STALE_AFTER_DEPLOY event carries the deploy-specific reason" \
	|| bad "stale-blocked: STALE_AFTER_DEPLOY event failed shape validation" "$(cat "$REPO/.coding-hermes/board/events.jsonl")"

# 26. A nonsense threshold is an operational error (exit 3), never a silent
#     no-op — same discipline as CANOPYD_CRASHLOOP_THRESHOLD.
run_case "invalid CANOPYD_STALE_BLOCKED_THRESHOLD exits 3" 3 \
	CANOPYD_STALE_REPO_ROOT="$WORK/sb-first" \
	CANOPYD_STALE_PATH="$SB_ART1" \
	CANOPYD_STALE_BLOCKED_THRESHOLD=0 \
	EVIDENCE="CANOPYD_STALE_BLOCKED_THRESHOLD not a positive integer" --

echo ""
echo "== results: $pass passed, $fail failed =="
[[ $fail == 0 ]]
