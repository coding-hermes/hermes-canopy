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
#   6. stale + --deploy, no-op fake deploy -> still stale after deploy -> fail
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

echo ""
echo "== results: $pass passed, $fail failed =="
[[ $fail == 0 ]]
