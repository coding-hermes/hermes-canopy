#!/usr/bin/env bash
#
# test-mcp-canary-pin.sh (INT-CI-006) — the CI pin guard for the MCP SDK canary.
#
# scripts/mcp_sdk_canary.py refuses to run against an MCP SDK outside
# [MIN_MCP_VERSION, MAX_MCP_VERSION).  .github/workflows/build.yml installs the
# SDK that canary imports.  Those two facts live in different files, so an
# UNPINNED `pip install mcp` lets upstream release drift break master silently:
# mcp 2.3.0 was published, CI installed it, and the canary failed its own
# version gate at the END of a full build (GitHub Actions run 44789256392, job
# `build (ubuntu-latest, 1.25)`, step "MCP SDK canary":
#
#     ERROR: Unsupported MCP SDK 2.3.0
#     Supported versions: >=1.30.0,<1.31.0
#
# This guard compares the two sides, offline and in milliseconds:
#
#   canary side    MIN_MCP_VERSION / MAX_MCP_VERSION   -> supported window
#   workflow side  `pip install <specifier>` for mcp   -> installable window
#
# and fails when the installable window is not a SUBSET of the supported one.
# A bare `pip install mcp` (no specifier) always fails: it resolves to whatever
# upstream published last, which is exactly the drift this guards against.
#
# Usage:
#   bash scripts/test-mcp-canary-pin.sh
#   bash scripts/test-mcp-canary-pin.sh --file-workflow <path> [--file-canary <path>]
#
# The --file-* overrides exist so the red proof can point the guard at a MUTATED
# COPY of the workflow; the real files are never mutated to prove a failure.
#
# Exit codes: 0 PASS, 1 pin defect, 2 unreadable/unparseable input.
# No network, no venv, no pip execution, no writes.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
CANARY="$ROOT/scripts/mcp_sdk_canary.py"
WORKFLOW="$ROOT/.github/workflows/build.yml"

usage() {
	cat <<'EOF'
usage: test-mcp-canary-pin.sh [--file-workflow <path>] [--file-canary <path>]

  --file-workflow <path>  workflow to inspect (default: .github/workflows/build.yml)
  --file-canary <path>    canary script to inspect (default: scripts/mcp_sdk_canary.py)

Fails when the workflow's `pip install` specifier for the `mcp` SDK is not a
subset of the canary's supported range [MIN_MCP_VERSION, MAX_MCP_VERSION), or
when a bare `pip install mcp` (no specifier) is present.
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--file-workflow)
		[ $# -ge 2 ] || {
			echo "test-mcp-canary-pin: --file-workflow needs a path" >&2
			exit 2
		}
		WORKFLOW="$2"
		shift 2
		;;
	--file-workflow=*)
		WORKFLOW="${1#*=}"
		shift
		;;
	--file-canary)
		[ $# -ge 2 ] || {
			echo "test-mcp-canary-pin: --file-canary needs a path" >&2
			exit 2
		}
		CANARY="$2"
		shift 2
		;;
	--file-canary=*)
		CANARY="${1#*=}"
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "test-mcp-canary-pin: unknown argument: $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

input_error() {
	# Unreadable/unparseable input is not a pin defect and not a pass.
	echo "MCP-CANARY-PIN: ERROR ($1)" >&2
	exit 2
}

# ---------------------------------------------------------------------------
# Version helpers (dotted numeric, up to three components; normalized to X.Y.Z)
# ---------------------------------------------------------------------------

normalize_version() {
	local raw="$1" a b c rest
	[ -n "$raw" ] || return 1
	IFS=. read -r a b c rest <<<"$raw"
	[ -z "${rest:-}" ] || return 1
	[ -n "${a:-}" ] && [ -n "${b:-}" ] && [ -n "${c:-}" ] || return 1
	local part
	for part in "$a" "$b" "$c"; do
		[[ "$part" =~ ^[0-9]+$ ]] || return 1
	done
	printf '%s.%s.%s\n' "$((10#$a))" "$((10#$b))" "$((10#$c))"
}

# vercmp A B -> echoes -1, 0 or 1.  Both inputs must already be normalized.
vercmp() {
	local -a av bv
	local i
	IFS=. read -r -a av <<<"$1"
	IFS=. read -r -a bv <<<"$2"
	for i in 0 1 2; do
		if [ "$((10#${av[i]:-0}))" -gt "$((10#${bv[i]:-0}))" ]; then
			echo 1
			return 0
		fi
		if [ "$((10#${av[i]:-0}))" -lt "$((10#${bv[i]:-0}))" ]; then
			echo -1
			return 0
		fi
	done
	echo 0
}

# ---------------------------------------------------------------------------
# Canary side: MIN_MCP_VERSION / MAX_MCP_VERSION
# ---------------------------------------------------------------------------

[ -f "$CANARY" ] || input_error "canary script not found: $CANARY"

# canary_tuple NAME -> echoes the X.Y.Z form of `NAME = (X, Y, Z)`; non-zero
# when the assignment is missing or is not a three-integer tuple.
canary_tuple() {
	local name="$1" raw p
	local -a parts
	raw="$(grep -E "^${name}[[:space:]]*=" "$CANARY" | head -n 1)"
	[ -n "$raw" ] || return 1
	raw="${raw#*(}"
	raw="${raw%%)*}"
	IFS=',' read -r -a parts <<<"$raw"
	[ "${#parts[@]}" -eq 3 ] || return 1
	local out=""
	for p in "${parts[@]}"; do
		p="${p//[[:space:]]/}"
		[[ "$p" =~ ^[0-9]+$ ]] || return 1
		out="${out:+$out.}${p}"
	done
	printf '%s\n' "$out"
}

MIN_RAW="$(canary_tuple MIN_MCP_VERSION)" || input_error "cannot parse MIN_MCP_VERSION = (X, Y, Z) from $CANARY"
MAX_RAW="$(canary_tuple MAX_MCP_VERSION)" || input_error "cannot parse MAX_MCP_VERSION = (X, Y, Z) from $CANARY"

MIN="$(normalize_version "$MIN_RAW")" || input_error "cannot parse MIN_MCP_VERSION from $CANARY"
MAX="$(normalize_version "$MAX_RAW")" || input_error "cannot parse MAX_MCP_VERSION from $CANARY"
[ "$(vercmp "$MIN" "$MAX")" = "-1" ] || input_error "canary window is empty in $CANARY: MIN $MIN >= MAX $MAX"

WINDOW=">=${MIN},<${MAX}"

# ---------------------------------------------------------------------------
# Workflow side: every `pip install` requirement naming the mcp SDK
# ---------------------------------------------------------------------------

[ -f "$WORKFLOW" ] || input_error "workflow not found: $WORKFLOW"

# One entry per install site: "<line>:<requirement text>" with an empty
# requirement text standing for a BARE install (no specifier at all).
sites=()
lineno=0
while IFS= read -r line || [ -n "$line" ]; do
	lineno=$((lineno + 1))
	# Comments cannot install anything; do not fail on documented examples.
	stripped="${line#"${line%%[![:space:]]*}"}"
	case "$stripped" in \#*) continue ;; esac
	# `pip install` where `pip` is its own token (optionally a path: .../bin/pip).
	[[ "$line" =~ (^|[^[:alnum:]_.-])pip[[:space:]]+install([[:space:]]|$) ]] || continue
	args="${line#*install}"
	read -r -a toks <<<"$args"
	for tok in "${toks[@]}"; do
		tok="${tok//\"/}"
		tok="${tok//\'/}"
		# mcp, mcp[cli], or mcp followed by a version operator. Anything else
		# (mcp-cli, mcpo, ...) is a different distribution and is ignored.
		[[ "$tok" =~ ^mcp(\[[^]]*\])?((>=|<=|==|!=|~=|>|<|=)[^[:space:]]*)?$ ]] || continue
		sites+=("${lineno}:${BASH_REMATCH[2]:-}")
	done
done <"$WORKFLOW"

[ "${#sites[@]}" -gt 0 ] || input_error "no \`pip install\` site for the mcp SDK found in $WORKFLOW"

# ---------------------------------------------------------------------------
# Coverage: installable window must be a subset of [MIN, MAX)
# ---------------------------------------------------------------------------

failures=()
REASONS=()

add_reason() { REASONS+=("$1"); }

check_spec() { # $1 = specifier text (e.g. ">=1.30.0,<1.31.0"); fills REASONS
	local spec="$1"
	local lower="" upper="" lower_strict=0 upper_strict=0 exact=""
	local -a clauses
	local clause op ver norm

	REASONS=()

	IFS=',' read -r -a clauses <<<"$spec"
	for clause in "${clauses[@]}"; do
		clause="${clause//[[:space:]]/}"
		[ -n "$clause" ] || continue
		if [[ "$clause" =~ ^(>=|<=|==|!=|~=|>|<|=)(.+)$ ]]; then
			op="${BASH_REMATCH[1]}"
			ver="${BASH_REMATCH[2]}"
		else
			add_reason "unsupported version clause '${clause}' in 'mcp${spec}'"
			return 0
		fi
		if ! norm="$(normalize_version "$ver")"; then
			add_reason "unparseable version '${ver}' in 'mcp${spec}' (expected X.Y.Z)"
			return 0
		fi
		case "$op" in
		">=")
			if [ -z "$lower" ] || [ "$(vercmp "$norm" "$lower")" = "1" ]; then
				lower="$norm"
				lower_strict=0
			fi
			;;
		">")
			if [ -z "$lower" ] || [ "$(vercmp "$norm" "$lower")" -ge 0 ]; then
				lower="$norm"
				lower_strict=1
			fi
			;;
		"<=")
			if [ -z "$upper" ] || [ "$(vercmp "$norm" "$upper")" = "-1" ]; then
				upper="$norm"
				upper_strict=0
			fi
			;;
		"<")
			if [ -z "$upper" ] || [ "$(vercmp "$norm" "$upper")" -le 0 ]; then
				upper="$norm"
				upper_strict=1
			fi
			;;
		"==" | "=")
			exact="$norm"
			;;
		"~=")
			# PEP 440 compatible release on a 3-part version: >=X.Y.Z, <X.(Y+1).0
			local -a parts
			IFS=. read -r -a parts <<<"$norm"
			if [ -z "$lower" ] || [ "$(vercmp "$norm" "$lower")" = "1" ]; then
				lower="$norm"
				lower_strict=0
			fi
			local tilde_upper
			tilde_upper="${parts[0]}.$((10#${parts[1]} + 1)).0"
			if [ -z "$upper" ] || [ "$(vercmp "$tilde_upper" "$upper")" = "-1" ]; then
				upper="$tilde_upper"
				upper_strict=1
			fi
			;;
		"!=")
			# Exclusions cannot widen the installable set; coverage is unaffected.
			;;
		esac
	done

	if [ -n "$exact" ]; then
		if [ "$(vercmp "$exact" "$MIN")" = "-1" ]; then
			add_reason "pinned version ${exact} is below the canary minimum ${MIN}"
		fi
		if [ "$(vercmp "$exact" "$MAX")" -ge 0 ]; then
			add_reason "pinned version ${exact} is at or above the canary maximum ${MAX} (exclusive)"
		fi
		return 0
	fi

	if [ -z "$lower" ]; then
		add_reason "no lower bound: pip may install an older unsupported SDK (canary needs >=${MIN})"
	elif [ "$(vercmp "$lower" "$MIN")" = "-1" ]; then
		add_reason "lower bound ${lower} is below the canary minimum ${MIN}: pip could install a version the canary rejects"
	fi
	if [ -z "$upper" ]; then
		add_reason "no upper bound: pip resolves the newest release, which may be outside <${MAX} (this is how mcp 2.3.0 broke master)"
	elif [ "$upper_strict" = "1" ]; then
		if [ "$(vercmp "$upper" "$MAX")" = "1" ]; then
			add_reason "upper bound ${upper} is above the canary maximum ${MAX} (exclusive): versions the canary rejects are installable"
		fi
	else
		if [ "$(vercmp "$upper" "$MAX")" -ge 0 ]; then
			add_reason "inclusive upper bound ${upper} reaches the canary maximum ${MAX} (exclusive): that version is not supported"
		fi
	fi
	if [ -n "$lower" ] && [ -n "$upper" ] && [ "$(vercmp "$lower" "$upper")" -ge 0 ]; then
		add_reason "empty installable window: lower ${lower} (strict=${lower_strict}) is not below upper ${upper} (strict=${upper_strict})"
	fi
	return 0
}

specs_seen=()
for site in "${sites[@]}"; do
	line_no="${site%%:*}"
	spec="${site#*:}"
	if [ -z "$spec" ]; then
		failures+=("${WORKFLOW}:${line_no} -> bare \`pip install mcp\` with no version specifier; it resolves to the newest upstream release at CI time")
		continue
	fi
	check_spec "$spec"
	if [ "${#REASONS[@]}" -gt 0 ]; then
		joined="$(printf '%s\n' "${REASONS[@]}" | paste -sd ';' -)"
		joined="${joined//;/; }"
		failures+=("${WORKFLOW}:${line_no} -> mcp${spec}: ${joined}")
		continue
	fi
	specs_seen+=("mcp${spec}")
done

if [ "${#failures[@]}" -gt 0 ]; then
	echo "MCP-CANARY-PIN: FAIL (workflow install does not cover the canary window ${WINDOW})"
	for f in "${failures[@]}"; do
		echo "  workflow: ${f}"
	done
	echo "  canary:   ${CANARY} -> MIN ${MIN} (inclusive), MAX ${MAX} (exclusive)"
	echo "  fix:      pin the install to a specifier inside that window, e.g. /tmp/mcp45-venv/bin/pip install 'mcp${WINDOW}'"
	exit 1
fi

echo "MCP-CANARY-PIN: PASS (install ${specs_seen[*]} covers canary ${MIN}..${MAX})"
exit 0
