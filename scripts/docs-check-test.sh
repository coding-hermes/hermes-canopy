#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CHECKER="$ROOT/scripts/docs-check.sh"
README="$ROOT/README.md"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/canopy-docs-check.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

cp "$README" "$WORK/README.md"

# ch:trace row=README-11 spec=none:no-docs-spec wave=none:serial test=scripts/docs-check-test.sh doc=README.md prompt=/tmp/hermes-canopy-2026-10-23-05-27-03-README-11-brief.txt evidence=commit witness=none:local-regression verdict=none:pending-foreman commit=none:pending-foreman memory=none:pending-foreman

run_check() {
  DOCS_CHECK_README="$WORK/README.md" "$CHECKER"
}

run_check
printf '%s\n' 'docs-check-test: clean PASS'

python3 - "$WORK/README.md" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
text = path.read_text(encoding="utf-8")
old = './bin/canopyd tree create "My Tree" --content'
new = './bin/canopyd tree create "My Tree" --definitely-fake-flag'
if text.count(old) < 1:
    raise SystemExit("mutation point not found")
path.write_text(text.replace(old, new, 1), encoding="utf-8")
PY

if run_check; then
  printf '%s\n' 'docs-check-test: mutated README unexpectedly PASSed' >&2
  exit 1
else
  printf '%s\n' 'docs-check-test: mutated README FAIL (expected)'
fi

cp "$README" "$WORK/README.md"
run_check
cmp -s "$README" "$WORK/README.md"
printf '%s\n' 'docs-check-test: clean PASS after restore'
