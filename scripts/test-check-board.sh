#!/usr/bin/env bash
#
# test-check-board.sh — hermetic regression coverage for scripts/check-board.sh
# (INFRA-005). Style follows scripts/test-check-deploy-staleness.sh: a temp
# scratch repo, pass/fail counters, PASS/FAIL lines, exit 0 only when every
# scenario passes. Nothing here touches the real board, the real hook, or the
# canopy repo's index — the checker is driven from a synthetic git repo whose
# index is mutated per scenario and restored byte-for-byte afterwards.
#
# Scenarios:
#   1. clean board index                 -> exit 0
#   2. duplicate task id staged          -> exit != 0, bytes restored
#   3. dangling depends_on staged        -> exit != 0, bytes restored
#   4. off-vocabulary status staged      -> exit != 0, bytes restored
#   5. -q is silent on success           -> exit 0, no output
#   6. unknown flag                      -> exit 3
#   7. staged deletion of tasks.jsonl    -> exit != 0
#   8. --staged-index really reads the INDEX: worktree poisoned, index clean
#      -> exit 0 (and the worktree byte-parity is asserted to prove the poison
#         was live; the checker must not have rewritten it)
#   9. checker leaves the repo untouched (index + worktree digest match)
#
# Exit 0 only when every scenario passes.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECKER="$HERE/check-board.sh"
WORK="$(mktemp -d /tmp/canopy-board-test.XXXXXX)"
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
	# shellcheck disable=SC2001  # matches the style of test-check-deploy-staleness.sh
	sed 's/^/    /' <<<"$2"
	echo "--------------"
}

# ── synthetic repo with a minimal valid board ────────────────────────────────
REPO="$WORK/repo"
mkdir -p "$REPO/.coding-hermes/board" "$REPO/scripts"
cp "$CHECKER" "$REPO/scripts/"
cp "$HERE/check-shell-yaml.sh" "$REPO/scripts/"
git init -q "$REPO"
git -C "$REPO" config user.email test@example.invalid
git -C "$REPO" config user.name board-test

BOARD="$REPO/.coding-hermes/board"

write_board() {
	# write_board <status> <depends-on-json>
	printf '%s\n' '{"project":"Board Checker Fixture","namespace":"board-checker-fixture","version":2,"ticks_total":1,"ticks_idle":0,"cooldown_s":7200,"updated_at":"2026-10-19T00:00:00Z"}' >"$BOARD/board.jsonl"
	printf '{"id":"FIX-001","title":"First fixture row","status":"%s","priority":"P2","depends_on":%s,"created_at":"2026-10-19T00:00:00Z"}\n' "$1" "$2" >"$BOARD/tasks.jsonl"
	printf '%s\n' '{"id":1,"task_id":null,"actor":"fixture","detail":"fixture seed","timestamp":"2026-10-19 00:00:00.000000","event_type":"task_created","tick_number":null}' >"$BOARD/events.jsonl"
}

write_board pending '[]'
git -C "$REPO" add -A >/dev/null 2>&1
git -C "$REPO" commit -q -m 'fixture: minimal valid board'

digest() {
	# digest of the index entries + worktree board files, relative to the repo
	(
		cd "$REPO" || exit 1
		git ls-files -s -- .coding-hermes/board/
		find .coding-hermes/board -type f -print0 | sort -z | while IFS= read -r -d '' f; do
			sha256sum "$f" | cut -d' ' -f1
		done
	) | sha256sum | cut -d' ' -f1
}

BEFORE_ALL="$(digest)"

run_checker() {
	# run_checker [extra args...] — the hook cds to the repo root before
	# invoking the checker (REPO_ROOT comes from `git rev-parse --show-toplevel`
	# of the CWD); the test drives the synthetic repo the same way.
	(
		cd "$REPO" || exit 2
		bash "$CHECKER" "$@"
	)
}

# ── scenario 1: clean staged/index board -> exit 0 ───────────────────────────
out="$(run_checker -q --staged-index 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then
	ok "1. clean index accepted (rc=0)"
else
	bad "1. clean index accepted (rc=0)" "$out (rc=$rc)"
fi

# ── scenario 2: duplicate task id staged -> nonzero, restored ────────────────
cp "$BOARD/tasks.jsonl" "$WORK/tasks.jsonl.bak"
row="$(head -1 "$BOARD/tasks.jsonl")"
{ printf '%s\n' "$row"; printf '%s\n' "$row"; } >"$BOARD/tasks.jsonl"
git -C "$REPO" add .coding-hermes/board/tasks.jsonl
out="$(run_checker -q --staged-index 2>&1)"
rc=$?
cp "$WORK/tasks.jsonl.bak" "$BOARD/tasks.jsonl"
git -C "$REPO" add .coding-hermes/board/tasks.jsonl
if [ "$rc" -ne 0 ]; then
	ok "2. duplicate task id rejected (rc=$rc)"
else
	bad "2. duplicate task id rejected" "$out (rc=0)"
fi

# ── scenario 3: dangling depends_on staged -> nonzero, restored ──────────────
write_board pending '["NO-SUCH-TASK-ZZ"]'
git -C "$REPO" add .coding-hermes/board/tasks.jsonl
out="$(run_checker -q --staged-index 2>&1)"
rc=$?
write_board pending '[]'
git -C "$REPO" add .coding-hermes/board/tasks.jsonl
if [ "$rc" -ne 0 ]; then
	ok "3. dangling depends_on rejected (rc=$rc)"
else
	bad "3. dangling depends_on rejected" "$out (rc=0)"
fi

# ── scenario 4: off-vocabulary status staged -> nonzero, restored ────────────
write_board banana '[]'
git -C "$REPO" add .coding-hermes/board/tasks.jsonl
out="$(run_checker -q --staged-index 2>&1)"
rc=$?
write_board pending '[]'
git -C "$REPO" add .coding-hermes/board/tasks.jsonl
if [ "$rc" -ne 0 ]; then
	ok "4. off-vocabulary status rejected (rc=$rc)"
else
	bad "4. off-vocabulary status rejected" "$out (rc=0)"
fi

# ── scenario 5: -q is silent on success ──────────────────────────────────────
out="$(run_checker -q --staged-index 2>&1)"
rc=$?
if [ "$rc" -eq 0 ] && [ -z "$out" ]; then
	ok "5. -q silent on success"
else
	bad "5. -q silent on success" "rc=$rc output=[$out]"
fi

# ── scenario 6: unknown flag -> exit 3 ───────────────────────────────────────
run_checker --bogus-flag >/dev/null 2>&1
rc=$?
if [ "$rc" -eq 3 ]; then
	ok "6. unknown flag exits 3"
else
	bad "6. unknown flag exits 3" "rc=$rc"
fi

# ── scenario 7: staged deletion of a canonical board file ────────────────────
mv "$BOARD/tasks.jsonl" "$WORK/tasks.jsonl.deleted"
git -C "$REPO" rm -q --cached .coding-hermes/board/tasks.jsonl >/dev/null 2>&1
out="$(run_checker -q --staged-index 2>&1)"
rc=$?
mv "$WORK/tasks.jsonl.deleted" "$BOARD/tasks.jsonl"
git -C "$REPO" add .coding-hermes/board/tasks.jsonl
if [ "$rc" -ne 0 ]; then
	ok "7. staged board deletion refused (rc=$rc)"
else
	bad "7. staged board deletion refused" "$out (rc=0)"
fi

# ── scenario 8: --staged-index reads the INDEX, not the poisoned worktree ────
cp "$BOARD/tasks.jsonl" "$WORK/tasks.jsonl.bak2"
row="$(head -1 "$BOARD/tasks.jsonl")"
{ printf '%s\n' "$row"; printf '%s\n' "$row"; } >"$BOARD/tasks.jsonl"
poisoned="$(sha256sum "$BOARD/tasks.jsonl" | cut -d' ' -f1)"
clean="$(git -C "$REPO" rev-parse HEAD:.coding-hermes/board/tasks.jsonl 2>/dev/null)"
clean_blob="$(git -C "$REPO" cat-file blob "$clean" 2>/dev/null | sha256sum | cut -d' ' -f1)"
if [ "$poisoned" != "$clean_blob" ]; then
	out="$(run_checker -q --staged-index 2>&1)"
	rc=$?
	after="$(sha256sum "$BOARD/tasks.jsonl" | cut -d' ' -f1)"
	if [ "$rc" -eq 0 ]; then
		if [ "$after" = "$poisoned" ]; then
			ok "8. index view wins over poisoned worktree (checker left it untouched)"
		else
			bad "8. index view wins over poisoned worktree" "checker rewrote the worktree"
		fi
	else
		bad "8. index view wins over poisoned worktree" "$out (rc=$rc)"
	fi
else
	bad "8. index view wins over poisoned worktree" "worktree poison not live (premise failed)"
fi
cp "$WORK/tasks.jsonl.bak2" "$BOARD/tasks.jsonl"

# ── scenario 9: checker leaves the repo untouched ────────────────────────────
AFTER_ALL="$(digest)"
if [ "$BEFORE_ALL" = "$AFTER_ALL" ]; then
	ok "9. repo untouched by the whole run (index + worktree digest equal)"
else
	bad "9. repo untouched by the whole run" "digest changed: $BEFORE_ALL -> $AFTER_ALL"
fi

echo
echo "board checker: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
