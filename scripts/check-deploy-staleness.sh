#!/usr/bin/env bash
#
# check-deploy-staleness.sh (GAP-067): is the deployed canopyd artifact older
# than the newest committed change that can affect it?
#
# Compares the mtime of the installed canopyd binary against the committer
# date of the newest commit touching any watched path (internal/, cmd/,
# migrations/, go.mod, go.sum). Prints deployed/source timestamps, lag in
# seconds, and the relevant source commit.
#
# Exit codes (stable, documented):
#   0 CURRENT         — deployed artifact is within the stale threshold
#   1 STALE           — deployed artifact is over the threshold (or absent)
#   2 STALE_BLOCKED   — stale + --deploy, but the repo worktree is dirty
#                       (tracked changes); auto-deploy refused for safety.
#                       A half-written worker tree must never be deployed.
#   3 ERROR           — operational error (bad repo, unreadable paths, failed
#                       re-check after deploy)
#
# Environment overrides (tests never touch the real service):
#   CANOPYD_STALE_REPO_ROOT   repo to compare against
#                             (default: this script's repo root)
#   CANOPYD_STALE_PATH        installed artifact to check
#                             (default: /home/kara/bin/canopyd)
#   CANOPYD_STALE_THRESHOLD_S staleness threshold in seconds (default: 86400)
#   CANOPYD_DEPLOY_CMD        --deploy command (default: make deploy)
#   CANOPYD_DEPLOY_DIR        working directory for the deploy command
#                             (default: repo root)
#
# --deploy mode: only when stale, run the deploy command (the existing atomic
# `make deploy` path — build → install → restart → health poll → smoke), then
# re-run the staleness check; fail if the artifact is still stale afterwards.
# Never runs from a tracked-dirty worktree (STALE_BLOCKED).

set -uo pipefail

EXIT_CURRENT=0
EXIT_STALE=1
EXIT_STALE_BLOCKED=2
EXIT_ERROR=3

WATCH_PATHS=("internal" "cmd" "migrations" "go.mod" "go.sum")
DEFAULT_THRESHOLD=86400
DEFAULT_DEPLOYED_PATH="/home/kara/bin/canopyd"
DEFAULT_DEPLOY_CMD="make deploy"

MODE="check"
if [[ "${1:-}" == "--deploy" ]]; then
	MODE="deploy"
elif [[ -n "${1:-}" ]]; then
	echo "usage: $0 [--deploy]" >&2
	exit "$EXIT_ERROR"
fi

fail_error() {
	echo "STALE_CHECK ERROR: $*" >&2
	exit "$EXIT_ERROR"
}

REPO_ROOT="${CANOPYD_STALE_REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
DEPLOYED_PATH="${CANOPYD_STALE_PATH:-$DEFAULT_DEPLOYED_PATH}"
THRESHOLD_S="${CANOPYD_STALE_THRESHOLD_S:-$DEFAULT_THRESHOLD}"
DEPLOY_CMD="${CANOPYD_DEPLOY_CMD:-$DEFAULT_DEPLOY_CMD}"
DEPLOY_DIR="${CANOPYD_DEPLOY_DIR:-$REPO_ROOT}"

# ── Repo sanity ──────────────────────────────────────────────────────────────
[[ -d "$REPO_ROOT/.git" || -f "$REPO_ROOT/.git" ]] || fail_error "not a git repo: $REPO_ROOT"

if ! git -C "$REPO_ROOT" rev-parse HEAD >/dev/null 2>&1; then
	fail_error "cannot resolve HEAD in $REPO_ROOT"
fi

# ── Newest committed change on the watched paths ────────────────────────────
SRC_COMMIT="$(git -C "$REPO_ROOT" log -1 --format=%H -- "${WATCH_PATHS[@]}")"
[[ -n "$SRC_COMMIT" ]] || fail_error "no commits touch watched paths: ${WATCH_PATHS[*]}"

# %ct = committer date, epoch seconds.
SRC_TS="$(git -C "$REPO_ROOT" log -1 --format=%ct -- "${WATCH_PATHS[@]}")"
[[ "$SRC_TS" =~ ^[0-9]+$ ]] || fail_error "cannot parse commit timestamp for $SRC_COMMIT"
SRC_HUMAN="$(date -u -d "@$SRC_TS" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -r "$SRC_TS" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || echo "epoch=$SRC_TS")"

# ── Deployed artifact ────────────────────────────────────────────────────────
if [[ ! -f "$DEPLOYED_PATH" ]]; then
	echo "STALE: deployed binary missing: $DEPLOYED_PATH"
	echo "source: commit $SRC_COMMIT ($SRC_HUMAN)"
	if [[ "$MODE" == "deploy" ]]; then
		# A missing binary is also a reason to deploy; fall through to the
		# deploy gate below with staleness flag set.
		NEEDS_DEPLOY=1
	else
		exit "$EXIT_STALE"
	fi
else
	DEP_TS="$(stat -c %Y "$DEPLOYED_PATH" 2>/dev/null)" \
		|| fail_error "cannot stat deployed binary: $DEPLOYED_PATH"
	LAG=$((SRC_TS - DEP_TS))
	DEP_HUMAN="$(date -u -d "@$DEP_TS" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -r "$DEP_TS" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || echo "epoch=$DEP_TS")"

	echo "deployed: $DEPLOYED_PATH ($DEP_HUMAN)"
	echo "source:   $SRC_COMMIT ($SRC_HUMAN)"
	echo "watched:  ${WATCH_PATHS[*]}"
	echo "lag:      ${LAG}s (threshold ${THRESHOLD_S}s)"

	if ((LAG <= THRESHOLD_S)); then
		echo "CURRENT: deployed canopyd is up to date"
		exit "$EXIT_CURRENT"
	fi
	echo "STALE: deployed canopyd is ${LAG}s behind source"
	NEEDS_DEPLOY=1
fi

# ── Not deploying → plain STALE result ──────────────────────────────────────
if [[ "$MODE" != "deploy" ]]; then
	exit "$EXIT_STALE"
fi

# ── Safety gate: never auto-deploy a tracked-dirty worktree ─────────────────
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain 2>/dev/null)" ]]; then
	echo "STALE_BLOCKED: worktree has tracked/untracked changes; refusing to auto-deploy" >&2
	echo "STALE_BLOCKED: commit or stash, then re-run $0 --deploy" >&2
	echo "STALE_BLOCKED: deploy command NOT invoked" >&2
	exit "$EXIT_STALE_BLOCKED"
fi

# ── Deploy via the existing atomic path ─────────────────────────────────────
echo "stale → deploying via: $DEPLOY_CMD (cwd: $DEPLOY_DIR)"
if ! (cd "$DEPLOY_DIR" && $DEPLOY_CMD); then
	fail_error "deploy command failed: $DEPLOY_CMD"
fi
echo "deploy command finished; re-checking staleness"

# Re-run this script WITHOUT --deploy: a stale result (exit 1) means the
# deploy did not actually refresh the artifact, so the automation must fail.
"$0"
rc=$?
if ((rc != 0)); then
	fail_error "deployed artifact still stale after deploy (re-check rc=$rc)"
fi
exit "$EXIT_CURRENT"
