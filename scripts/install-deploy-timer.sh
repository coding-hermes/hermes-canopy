#!/usr/bin/env bash
#
# install-deploy-timer.sh (GAP-067): render the tracked systemd user units
# (deploy/systemd/canopy-deploy-check.service/.timer — CONCRETE names, not
# templates: systemd refuses to enable a template timer against
# timers.target) into ~/.config/systemd/user/ and enable the daily staleness
# timer.
#
# Rendering approach: the tracked units are portable (%h placeholders only —
# no hardcoded /home/kara). The installer bakes THIS checkout's repo path into
# the rendered service via sed (repo path differs per checkout; %h alone
# cannot express it). The timer carries no repo path. The tracked units stay
# the source of truth. Rendering REPLACES any existing installed unit file
# (an old symlink or stale copy is unlinked first so a previous render or
# aborted verification can never shadow the fresh one).
#
# Dry run: CANOPYD_UNIT_INSTALL_DIR=<dir> bash scripts/install-deploy-timer.sh
# renders to <dir> instead of ~/.config/systemd/user and skips enable/reload
# (used by tests/verification; the rendered concrete units can be checked
# with systemd-analyze --user verify and
# systemctl --user enable --dry-run <dir>/canopy-deploy-check.timer — the
# latter must exit 0 BEFORE anything is installed or enabled for real).
#
# No systemd user session / no systemctl --user → enable is skipped with a
# notice (exit 0); render still happens so units can be inspected.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC_DIR="$REPO_ROOT/deploy/systemd"
UNIT_NAME="canopy-deploy-check.service"
TIMER_NAME="canopy-deploy-check.timer"
UNIT_DIR="${CANOPYD_UNIT_INSTALL_DIR:-$HOME/.config/systemd/user}"

fail() {
	echo "INSTALL FAILED: $*" >&2
	exit 1
}

[[ -f "$SRC_DIR/$UNIT_NAME" ]] || fail "missing $SRC_DIR/$UNIT_NAME"
[[ -f "$SRC_DIR/$TIMER_NAME" ]] || fail "missing $SRC_DIR/$TIMER_NAME"

mkdir -p "$UNIT_DIR"

RENDERED_SERVICE="$UNIT_DIR/$UNIT_NAME"
RENDERED_TIMER="$UNIT_DIR/$TIMER_NAME"

# Replace, never overlay: an existing unit file or verification symlink at
# these names would otherwise shadow or corrupt the fresh render.
rm -f "$RENDERED_SERVICE" "$RENDERED_TIMER"

# Bake the real repo path into the rendered unit (tracked unit uses %h only).
sed "s|%h/hermes-canopy|$REPO_ROOT|g" "$SRC_DIR/$UNIT_NAME" >"$RENDERED_SERVICE"
# Timer has no repo path; copy verbatim.
cp "$SRC_DIR/$TIMER_NAME" "$RENDERED_TIMER"
chmod 644 "$RENDERED_SERVICE" "$RENDERED_TIMER"

echo "rendered: $RENDERED_SERVICE"
echo "rendered: $RENDERED_TIMER"

# Structural sanity when systemd-analyze is available. Note: --user verify
# resolves units through the user's unit search path, so for a custom
# CANOPYD_UNIT_INSTALL_DIR it may also surface warnings about unrelated
# installed units — those are pre-existing and not caused by this render.
if command -v systemd-analyze >/dev/null 2>&1; then
	systemd-analyze --user verify "$RENDERED_SERVICE" "$RENDERED_TIMER" ||
		echo "NOTE: systemd-analyze verify reported issues above" >&2
fi

# ── Enable (skipped for dry-run dirs and session-less environments) ─────────
if [[ -n "${CANOPYD_UNIT_INSTALL_DIR:-}" ]]; then
	echo "dry run: CANOPYD_UNIT_INSTALL_DIR set — skipping daemon-reload/enable"
	exit 0
fi
if ! command -v systemctl >/dev/null 2>&1 || ! systemctl --user show-environment >/dev/null 2>&1; then
	echo "NOTE: no systemd user session available — units rendered but not enabled" >&2
	exit 0
fi

systemctl --user daemon-reload
systemctl --user enable --now "$TIMER_NAME"
echo "enabled: $TIMER_NAME (daily staleness check with auto-deploy)"
systemctl --user list-timers "$TIMER_NAME" --no-pager || true
