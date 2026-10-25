#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CHECKER="$ROOT/scripts/docs-check.sh"
README="$ROOT/README.md"
DOCS="$ROOT/docs"
BINARY="$ROOT/bin/canopyd"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/canopy-docs-check.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

cp "$README" "$WORK/README.md"
cp -R "$DOCS" "$WORK/docs"

run_check() {
  DOCS_CHECK_README="$WORK/README.md" \
  DOCS_CHECK_DOCS_DIR="$WORK/docs" \
  CANOPYD_BIN="$BINARY" \
    "$CHECKER" "$@"
}

run_check
printf '%s\n' 'docs-check-test: clean PASS'

mv "$WORK/docs/API.md" "$WORK/docs/API.md.missing"
if run_check; then
  printf '%s\n' 'docs-check-test: missing indexed page unexpectedly PASSed' >&2
  exit 1
else
  printf '%s\n' 'docs-check-test: missing indexed page FAIL (expected)'
fi
mv "$WORK/docs/API.md.missing" "$WORK/docs/API.md"

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
  printf '%s\n' 'docs-check-test: missing README index link unexpectedly PASSed' >&2
  exit 1
else
  printf '%s\n' 'docs-check-test: missing README index link FAIL (expected)'
fi
cp "$README" "$WORK/README.md"

# DF-HERMES-CANOPY-96: an unknown trailing command must not be accepted by its parent.
python3 - "$WORK/README.md" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
s = p.read_text()
needle = "canopyd tree list"
assert needle in s
p.write_text(s.replace(needle, "canopyd tree lst", 1))
PY
if output=$(run_check 2>&1); then
  printf '%s\n' 'docs-check-test: unknown trailing subcommand unexpectedly PASSed' >&2
  exit 1
elif grep -q 'lst' <<<"$output"; then
  printf '%s\n' 'docs-check-test: unknown trailing subcommand FAIL names lst (expected)'
else
  printf '%s\n' "$output" >&2
  printf '%s\n' 'docs-check-test: unknown trailing subcommand failure omitted lst' >&2
  exit 1
fi
cp "$README" "$WORK/README.md"

# Blockquoted fences are valid Markdown examples and must be scanned.
python3 - "$WORK/README.md" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
s = p.read_text()
marker = "## "
insert = "> ```bash\n> canopyd db export-sqlite --sqlite-out /tmp/ghost.sqlite\n> ```\n\n"
pos = s.index(marker)
p.write_text(s[:pos] + insert + s[pos:])
PY
if output=$(run_check 2>&1); then
  printf '%s\n' 'docs-check-test: blockquoted invalid flag unexpectedly PASSed' >&2
  exit 1
elif grep -q -- '--sqlite-out' <<<"$output"; then
  printf '%s\n' 'docs-check-test: blockquoted fence FAIL names --sqlite-out (expected)'
else
  printf '%s\n' "$output" >&2
  printf '%s\n' 'docs-check-test: blockquoted fence failure omitted --sqlite-out' >&2
  exit 1
fi
cp "$README" "$WORK/README.md"

# DF-HERMES-CANOPY-96 ghost-env arm: a documented env assignment on a
# blockquoted command must correspond to a real production environment read;
# a ghost variable (e.g. CANOPY_GHOST_FENCE) must be named in the failure.
python3 - "$WORK/README.md" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
s = p.read_text()
marker = "## "
insert = ("> ```bash\n"
          ">   CANOPY_GHOST_FENCE=1 ./bin/canopyd tree list\n"
          "> ```\n\n")
pos = s.index(marker)
p.write_text(s[:pos] + insert + s[pos:])
PY
if output=$(run_check 2>&1); then
  printf '%s\n' 'docs-check-test: blockquoted ghost env assignment unexpectedly PASSed' >&2
  exit 1
elif grep -q 'CANOPY_GHOST_FENCE' <<<"$output"; then
  printf '%s\n' 'docs-check-test: blockquoted ghost env FAIL names CANOPY_GHOST_FENCE (expected)'
else
  printf '%s\n' "$output" >&2
  printf '%s\n' 'docs-check-test: blockquoted ghost env failure omitted CANOPY_GHOST_FENCE' >&2
  exit 1
fi
cp "$README" "$WORK/README.md"

# Prefix-like environment names must not satisfy an exact table row.
python3 - "$WORK/README.md" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
s = p.read_text()
needle = "| `CANOPY_DB_DRIVER` |"
line = next(x for x in s.splitlines() if x.startswith(needle))
p.write_text(s.replace(line, "| `CANOPY_DB` | unset | intentionally not a production variable |", 1))
PY
if output=$(run_check 2>&1); then
  printf '%s\n' 'docs-check-test: CANOPY_DB prefix false-positive unexpectedly PASSed' >&2
  exit 1
elif grep -q 'CANOPY_DB' <<<"$output"; then
  printf '%s\n' 'docs-check-test: exact environment name FAIL names CANOPY_DB (expected)'
else
  printf '%s\n' "$output" >&2
  printf '%s\n' 'docs-check-test: exact environment failure omitted CANOPY_DB' >&2
  exit 1
fi
cp "$README" "$WORK/README.md"

# Every local relative link must resolve, not only the canonical index link.
python3 - "$WORK/README.md" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
s = p.read_text()
s += "\n[missing docs page](docs/DOES-NOT-EXIST.md)\n"
p.write_text(s)
PY
if output=$(run_check 2>&1); then
  printf '%s\n' 'docs-check-test: broken relative docs link unexpectedly PASSed' >&2
  exit 1
elif grep -q 'DOES-NOT-EXIST.md' <<<"$output"; then
  printf '%s\n' 'docs-check-test: broken relative docs link FAIL names target (expected)'
else
  printf '%s\n' "$output" >&2
  printf '%s\n' 'docs-check-test: broken-link failure omitted target' >&2
  exit 1
fi

cp "$README" "$WORK/README.md"
run_check
cmp -s "$README" "$WORK/README.md"
printf '%s\n' 'docs-check-test: clean PASS after all negative arms'
