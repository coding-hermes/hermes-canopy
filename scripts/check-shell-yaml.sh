#!/usr/bin/env bash
#
# check-shell-yaml.sh — syntax gate for staged shell scripts and GitHub
# workflow files. The global pre-commit hook (scripts/hooks/pre-commit) runs
# this on exactly the staged shell/workflow files and treats a MISSING checker
# as a hard failure, so the checker itself must exist for any .sh-carrying
# commit to land. Kept deliberately small and dependency-light:
#
#   *.sh / shebang'd scripts : `bash -n` — syntax parse only, never executed.
#   .github/workflows/*.yml  : `actionlint` when installed; otherwise a
#                              python3+PyYAML structural parse as the fallback
#                              (same pairing the tracked hook doctrine names).
#
# Exit 0 only when every named file passes.

set -uo pipefail

if [ $# -eq 0 ]; then
	echo "usage: $(basename "$0") <file> [<file>...]" >&2
	exit 2
fi

rc=0
have_actionlint=0
command -v actionlint >/dev/null 2>&1 && have_actionlint=1

yaml_fallback() {
	# Structural YAML parse; prints a diagnostic on failure.
	python3 - "$1" <<'PYEOF'
import sys

try:
    import yaml
except ImportError:
    print(f"pyyaml unavailable; skipped structural parse of {sys.argv[1]}")
    sys.exit(0)
try:
    yaml.safe_load(open(sys.argv[1]))
except Exception as exc:
    print(f"{sys.argv[1]}: invalid YAML: {exc}")
    sys.exit(1)
PYEOF
}

for f in "$@"; do
	if [ ! -f "$f" ]; then
		echo "check-shell-yaml: not a file: $f" >&2
		rc=1
		continue
	fi
	case "$f" in
	.github/workflows/*.yml | .github/workflows/*.yaml)
		if [ "$have_actionlint" = 1 ]; then
			if ! actionlint "$f"; then
				echo "check-shell-yaml: actionlint FAILED on $f" >&2
				rc=1
			fi
		elif ! out="$(yaml_fallback "$f")"; then
			echo "check-shell-yaml: YAML parse FAILED: ${out}" >&2
			rc=1
		elif [ -n "$out" ]; then
			echo "check-shell-yaml: note: $out"
		fi
		;;
	*)
		if ! bash -n "$f" 2>&1; then
			echo "check-shell-yaml: bash -n FAILED on $f" >&2
			rc=1
		fi
		;;
	esac
done

exit "$rc"
