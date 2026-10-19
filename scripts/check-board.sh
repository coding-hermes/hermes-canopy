#!/usr/bin/env bash
#
# check-board.sh — the pre-commit board validation arm (INFRA-005).
#
# Invoked by the global hook (/home/kara/.hermes/hooks/pre-commit, mirrored in
# scripts/hooks/pre-commit) as:
#
#     bash scripts/check-board.sh -q --staged-index
#
# on EVERY commit, board files staged or not (DF-CRIER-389: an invalid board is
# a state, not a diff). Validation is delegated to `boardctl validate` — the
# canonical JSONL board validator — run against a SAFE TEMPORARY VIEW of the
# git INDEX, never against the working tree:
#
#   - staged/modified paths  -> the index blob (`git cat-file blob :path`)
#   - tracked, not in index  -> deletion staged: rejected below
#   - untracked              -> worktree copy (harmless fallback; the canonical
#                               board files are all tracked in this repo)
#
# The view lives in a `mktemp -d` scratch dir and is discarded on exit. The
# working tree and the tracked board files are never mutated, and no permanent
# board DB is created; boardctl validate itself was measured write-pure (file
# digests compared before/after every run during INFRA-005).
#
# WHAT FAILS THE COMMIT
#   - boardctl errors: duplicate task ids, dangling depends_on (via
#     --fail-on dangling-dep), off-vocabulary statuses, unreadable board.
#     Proven at the board's real shape: each mutation flips
#     `RESULT: OK (328 warning(s))` into `RESULT: FAIL (1 error(s), ...)` rc=1.
#   - a staged deletion of any canonical board file.
#   - boardctl missing / unusable: exit 2, never a silent skip.
#
# WHAT DOES NOT FAIL THE COMMIT
#   The current legacy board carries 328 warnings (key drift, null depends_on,
#   off-vocabulary priorities, free-form guard/ci prose). boardctl validate
#   tolerates that shape and reports it per line as warnings; warnings keep
#   exit 0. Only errors fail, and `--fail-on dangling-dep` promotes exactly the
#   dangling-dependency census to errors.
#
# Exit codes: 0 clean · 1 validation failure · 2 environment (boardctl, git,
# scratch dir) · 3 usage.
#
# Without --staged-index the checker validates the WORKING TREE board in place
# (boardctl -C <repo>) — the manual "is my tree clean?" mode. With the flag the
# index view above is authoritative, which is what the hook uses.

set -uo pipefail

usage() {
	echo "usage: $(basename "$0") [-q] [--staged-index] [-h]" >&2
}

quiet=0
staged_index=0
while [ $# -gt 0 ]; do
	case "$1" in
	-q)
		quiet=1
		;;
	--staged-index)
		staged_index=1
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "board-check: unknown argument: $1" >&2
		usage
		exit 3
		;;
	esac
	shift
done

# The canonical board files the hook's board arm covers. The hook classifies
# every .coding-hermes/board/* path; these three decide the whole board's
# validity (tasks + events are the data, board.jsonl is the header).
BOARD_DIR=".coding-hermes/board"
BOARD_PATHS="tasks.jsonl events.jsonl board.jsonl"

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || REPO_ROOT="$PWD"
cd "$REPO_ROOT" || exit 2

git rev-parse --git-dir >/dev/null 2>&1 || {
	echo "board-check: not a git repository: $REPO_ROOT" >&2
	exit 2
}

command -v boardctl >/dev/null 2>&1 || {
	echo "board-check: boardctl is REQUIRED for the commit gate but not on PATH" >&2
	exit 2
}

# HEAD tree, if the repo has commits yet (a fresh repo simply has no HEAD
# fallback; every file then comes from the index or the worktree).
head_tree="$(git rev-parse --verify -q HEAD 2>/dev/null || true)"

view="$(mktemp -d /tmp/canopy-board-check.XXXXXX)" || {
	echo "board-check: cannot create scratch dir for the index view" >&2
	exit 2
}
trap 'rm -rf "$view"' EXIT
mkdir -p "$view/$BOARD_DIR"

deleted=()
sources=()
for path in $BOARD_PATHS; do
	rel="$BOARD_DIR/$path"
	in_index=0
	git ls-files --error-unmatch -- "$rel" >/dev/null 2>&1 && in_index=1
	in_head=0
	if [ -n "$head_tree" ] && git cat-file -e "$head_tree:$rel" 2>/dev/null; then
		in_head=1
	fi
	if [ "$in_index" = 1 ]; then
		# Staged content wins: index blob, not the working tree.
		git cat-file blob ":$rel" >"$view/$rel" || exit 2
		sources+=("$path=staged")
	elif [ "$in_head" = 1 ]; then
		# Tracked in HEAD but absent from the index: a staged deletion.
		sources+=("$path=deleted")
		deleted+=("$rel")
	elif [ -f "$rel" ]; then
		# Untracked board file: no index claim exists, validate the worktree copy.
		cp "$rel" "$view/$rel" || exit 2
		sources+=("$path=worktree")
	else
		sources+=("$path=absent")
	fi
done

if [ "${#deleted[@]}" -gt 0 ]; then
	echo "board-check: REFUSED: staged deletion of canonical board file(s): ${deleted[*]}" >&2
	echo "board-check: the board is the foreman's state of record; restore it (git restore --staged --worktree <path>)" >&2
	exit 1
fi

# boardctl requires the header file to exist for topology A; an empty/absent
# header still validates (measured) but keep the view shape honest.
if [ ! -s "$view/$BOARD_DIR/board.jsonl" ]; then
	printf '\n' >"$view/$BOARD_DIR/board.jsonl"
fi

if [ "$staged_index" = 1 ]; then
	out="$(boardctl validate -C "$view" --fail-on dangling-dep 2>&1)"
else
	# Manual mode: validate the working tree board in place.
	out="$(boardctl validate -C "$REPO_ROOT/$BOARD_DIR" --fail-on dangling-dep 2>&1)"
fi
rc=$?

if [ "$rc" -eq 0 ]; then
	if [ "$quiet" = 0 ]; then
		summary="$(printf '%s\n' "$out" | grep -E '^RESULT:' | tail -1)"
		echo "board-check: PASS (${sources[*]}) ${summary:-}"
	fi
	exit 0
fi

if [ "$rc" -eq 2 ]; then
	# boardctl exits 2 for missing/unreadable inputs (measured: a view without
	# events.jsonl). Environment problem, not a row problem.
	echo "board-check: board/unreadable input error from boardctl (exit 2):" >&2
	printf '%s\n' "$out" | tail -20 >&2
	exit 2
fi

# rc=1: real validation errors (duplicates, dangling deps, bad statuses).
echo "board-check: FAILED — board validation errors in the staged/index view:" >&2
printf '%s\n' "$out" | grep -E '^\[error\]|^RESULT:' | tail -20 >&2
echo "board-check: fix the rows above (warnings are tolerated; errors are not) — do not bypass with --no-verify" >&2
exit 1
