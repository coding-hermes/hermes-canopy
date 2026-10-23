#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CHECKER="$ROOT/scripts/docs-check.sh"
README="$ROOT/README.md"
DOCS="$ROOT/docs"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/canopy-docs-check.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

cp "$README" "$WORK/README.md"
cp -R "$DOCS" "$WORK/docs"

# DOC-11: the compatibility index is intentionally a link, not a duplicate index.
run_check() {
  DOCS_CHECK_README="$WORK/README.md" \
  DOCS_CHECK_DOCS_DIR="$WORK/docs" \
    "$CHECKER"
}

run_check
printf '%s\n' 'docs-check-test: clean PASS'

# Removing an indexed page must fail deterministically.
mv "$WORK/docs/API.md" "$WORK/docs/API.md.missing"
if run_check; then
  printf '%s\n' 'docs-check-test: missing indexed page unexpectedly PASSed' >&2
  exit 1
else
  printf '%s\n' 'docs-check-test: missing indexed page FAIL (expected)'
fi
mv "$WORK/docs/API.md.missing" "$WORK/docs/API.md"
run_check
printf '%s\n' 'docs-check-test: clean PASS after page restore'

# Removing the required README entry must also fail deterministically.
python3 - "$WORK/README.md" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
text = path.read_text(encoding="utf-8")
needle = "[docs/README.md](docs/README.md)"
if text.count(needle) != 1:
    raise SystemExit(f"expected one canonical README link, found {text.count(needle)}")
path.write_text(text.replace(needle, "documentation index", 1), encoding="utf-8")
PY

if run_check; then
  printf '%s\n' 'docs-check-test: missing README link unexpectedly PASSed' >&2
  exit 1
else
  printf '%s\n' 'docs-check-test: missing README link FAIL (expected)'
fi
cp "$README" "$WORK/README.md"
run_check
cmp -s "$README" "$WORK/README.md"
printf '%s\n' 'docs-check-test: clean PASS after README restore'
