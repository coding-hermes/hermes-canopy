#!/usr/bin/env bash
# test-bunker-qa-sync-tracked.sh — QA-HERMES-CANOPY-17 regression test.
#
# Extracts sync_repo() from the HOST script and drives it under a fake
# agent_ssh. The fake executes the remote command with a temporary HOME, so
# the tar stream is really extracted and committed locally instead of shipped.
#
# The normal run exercises the fixed function and then splices the saved old
# function (/tmp/qa17-old-sync_repo.txt) into a source copy. The fixed run must
# pass; the old run must fail because ignored junk is shipped and no size guard
# exists. Uses only bash + coreutils. Run from the repo root:
#   bash scripts/test-bunker-qa-sync-tracked.sh

set -u
SOURCE="${BUNKER_QA_SCRIPT:-$HOME/.hermes/scripts/bunker-qa.sh}"
OLD_FUNC=/tmp/qa17-old-sync_repo.txt
[ -f "$SOURCE" ] || { echo "FAIL: source script not found: $SOURCE" >&2; exit 1; }

FUNC=$(awk '/^sync_repo\(\) \{/{f=1} f{print} f&&/^\}$/{exit}' "$SOURCE")
[ -n "$FUNC" ] || { echo "FAIL: could not extract sync_repo from $SOURCE" >&2; exit 1; }

passed=0
failed=0
ok()  { echo "  ok: $1"; passed=$((passed+1)); }
bad() { echo "  FAIL: $1"; failed=$((failed+1)); }
check() { # name, got, want
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (want: [$3] got: [$2])"; fi
}

# Run one complete sync_repo body against the extracted function.
run_body() {
  passed=0
  failed=0
  local root repo remote_home bin payload rc run_stdout
  root=$(mktemp -d)
  repo="$root/repo"
  remote_home="$root/remote-home"
  bin="$root/bin"
  mkdir -p "$repo/tracked/sub" "$repo/dist" "$repo/.worktrees/ignored" "$remote_home" "$bin"

  printf 'tracked root\n' > "$repo/tracked.txt"
  printf 'tracked nested\n' > "$repo/tracked/sub/inside.txt"
  printf 'tracked space\n' > "$repo/tracked/sub/file with spaces.txt"
  printf 'tracked build output\n' > "$repo/dist/tracked-build.txt"
  printf '.worktrees/\n*.db\n' > "$repo/.gitignore"
  printf 'ignored worktree\n' > "$repo/.worktrees/ignored/junk.txt"
  printf 'ignored state\n' > "$repo/foo.db"
  printf 'untracked but shippable\n' > "$repo/ship-me.txt"
  git -C "$repo" init -q
  git -C "$repo" config user.name qa-test
  git -C "$repo" config user.email qa-test@example.invalid
  git -C "$repo" add -A
  git -C "$repo" commit -q -m fixture

  # The fake agent_ssh runs the exact remote command received by sync_repo.
  # HOME makes ~/qa-fixture land under this run's captured payload directory.
  printf '%s\n' '#!/usr/bin/env bash' \
    'set -u' \
    '[ "$#" -eq 1 ] || exit 2' \
    'HOME="$REMOTE_HOME" bash -c "$1"' > "$bin/agent_ssh"
  chmod +x "$bin/agent_ssh"

  (
    PATH="$bin:$PATH"
    export PATH HOME="$remote_home" REMOTE_HOME="$remote_home"
    eval "$FUNC"
    SYNC_ERR=
    sync_repo "$repo" fixture
    printf '%s\n' "$?" > "$root/sync.rc"
  ) > "$root/sync.out" 2> "$root/sync.err"
  rc=$(tr -d '\n' < "$root/sync.rc")
  run_stdout=$(tr -d '\n' < "$root/sync.out")
  payload="$remote_home/qa-fixture"

  check "sync succeeds from tracked-file list" "$rc" "0"
  check "sync_repo stdout remains empty after remote SYNC-OK" "$run_stdout" ""
  [ -d "$payload/.git" ] && ok "fake agent_ssh ran remote init/commit command" || bad "fake agent_ssh did not run remote init/commit command"

  for path in \
    .gitignore \
    tracked.txt \
    tracked/sub/inside.txt \
    'tracked/sub/file with spaces.txt' \
    dist/tracked-build.txt \
    ship-me.txt; do
    [ -f "$payload/$path" ] && ok "tracked/shippable file present: $path" || bad "tracked/shippable file missing: $path"
  done
  for path in .worktrees/ignored/junk.txt foo.db; do
    [ ! -e "$payload/$path" ] && ok "gitignored junk absent: $path" || bad "gitignored junk was shipped: $path"
  done

  (
    PATH="$bin:$PATH"
    export PATH HOME="$remote_home" REMOTE_HOME="$remote_home" BUNKER_QA_SYNC_MAX_BYTES=1
    eval "$FUNC"
    SYNC_ERR=
    sync_repo "$repo" oversize
    printf '%s\n' "$?" > "$root/oversize.rc"
  ) > "$root/oversize.out" 2> "$root/oversize.err"
  check "size guard returns failure" "$(tr -d '\n' < "$root/oversize.rc")" "1"
  grep -q 'SYNC-OVERSIZE:' "$root/oversize.err" \
    && ok "size guard emits visible SYNC-OVERSIZE" \
    || bad "size guard did not emit SYNC-OVERSIZE"
  [ ! -e "$remote_home/qa-oversize" ] \
    && ok "size guard stops before remote shipping" \
    || bad "size guard shipped an oversize payload"

  (
    PATH="$bin:$PATH"
    export PATH HOME="$remote_home" REMOTE_HOME="$remote_home" BUNKER_QA_SYNC_EXCLUDES='--exclude=ship-me.txt'
    eval "$FUNC"
    SYNC_ERR=
    sync_repo "$repo" extra-exclude
    printf '%s\n' "$?" > "$root/exclude.rc"
  ) > "$root/exclude.out" 2> "$root/exclude.err"
  check "extra tar exclude keeps SYNC-OK success" "$(tr -d '\n' < "$root/exclude.rc")" "0"
  [ ! -e "$remote_home/qa-extra-exclude/ship-me.txt" ] \
    && ok "BUNKER_QA_SYNC_EXCLUDES excludes a selected payload file" \
    || bad "BUNKER_QA_SYNC_EXCLUDES was ignored"

  rm -rf "$root"
  echo
  if [ "$failed" -eq 0 ]; then
    echo "PASS: $passed assertions"
    return 0
  fi
  echo "FAIL: $failed of $((passed+failed)) assertions failed." >&2
  return 1
}

# An explicitly supplied source is the one-shot mode used by the red-proof
# invocation (and by reviewers who want to run the body against a source copy).
if [ -n "${BUNKER_QA_SCRIPT:-}" ] || [ "${QA17_RED_PROOF_RUN:-0}" = 1 ]; then
  run_body
  exit $?
fi

[ -s "$OLD_FUNC" ] || {
  echo "FAIL: saved old sync_repo is missing or empty: $OLD_FUNC" >&2
  exit 1
}

echo "== sync_repo tracked-only payload regression (QA-HERMES-CANOPY-17) =="
echo "[1] fixed source: $SOURCE"
if run_body; then
  echo "GREEN RESULT: PASS"
else
  echo "GREEN RESULT: FAIL" >&2
  exit 1
fi

# Replace only sync_repo() in a copy of the current host script with the
# function captured before the fix. This proves the test is not vacuous.
old_source=$(mktemp)
trap 'rm -f "$old_source"' EXIT
awk -v old_file="$OLD_FUNC" '
  BEGIN {
    n = 0
    while ((getline line < old_file) > 0) old[++n] = line
    close(old_file)
  }
  /^sync_repo\(\) \{/ {
    for (i = 1; i <= n; i++) print old[i]
    replacing = 1
    next
  }
  replacing && /^\}$/ { replacing = 0; next }
  !replacing { print }
' "$SOURCE" > "$old_source"

[ -s "$old_source" ] || {
  echo "FAIL: could not build old-source red-proof copy" >&2
  exit 1
}

echo "[2] old source red-proof: $old_source"
if BUNKER_QA_SCRIPT="$old_source" QA17_RED_PROOF_RUN=1 bash "$0"; then
  echo "RED-PROOF RESULT: UNEXPECTED PASS (old sync_repo did not fail the regression)" >&2
  exit 1
else
  echo "RED-PROOF RESULT: expected FAIL against saved old sync_repo"
fi

echo "PASS: fixed run green; old run red as required"
exit 0
