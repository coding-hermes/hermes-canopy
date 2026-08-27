#!/usr/bin/env bash
#
# Deploy canopyd (GAP-052): build → install → restart → health poll → smoke.
#
# Closes the stale-binary deploy gap: the systemd user unit
# canopy-canopyd.service runs /home/kara/bin/canopyd, which until now was only
# updated by manual copy — a HEAD build was never what systemd executed (the
# live stack 404'd the entire /api/v1/gateway surface for ~7h because of it).
#
# Steps:
#   1. make build            (repo root → bin/canopyd)
#   2. install atomically    (cp to temp next to target, then mv — mv within
#                            the same filesystem is atomic, so systemd never
#                            execs a half-written binary)
#   3. systemctl --user restart canopy-canopyd
#   4. poll GET /health until 200 (max ~30s)
#   5. run scripts/smoke-gateway.sh (gateway surface presence + contract)
#
# Idempotent: safe to re-run any number of times.
# Environment overrides: INSTALL_PATH (default /home/kara/bin/canopyd),
# SERVICE_NAME (default canopy-canopyd), CANOPY_BASE_URL / CANOPY_JWT_SECRET
# (passed through to the smoke test).
#
# Exit non-zero on any failure.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_PATH="${CANOPYD_INSTALL_PATH:-/home/kara/bin/canopyd}"
SERVICE_NAME="${CANOPYD_SERVICE_NAME:-canopy-canopyd}"
HEALTH_TIMEOUT=30

fail() {
	echo "DEPLOY FAILED: $*" >&2
	exit 1
}

echo "Deploying canopyd from $REPO_ROOT"

# ── 1. Build from HEAD ────────────────────────────────────────────────────
cd "$REPO_ROOT"
echo "[1/5] make build"
make build || fail "make build failed"
[[ -x bin/canopyd ]] || fail "build did not produce an executable bin/canopyd"

# ── 2. Atomic install to the systemd unit's exec path ────────────────────
echo "[2/5] install bin/canopyd -> $INSTALL_PATH"
INSTALL_DIR="$(dirname "$INSTALL_PATH")"
mkdir -p "$INSTALL_DIR"
TMP_PATH="$(mktemp "$INSTALL_DIR/.canopyd.XXXXXX")"
trap 'rm -f "$TMP_PATH"' EXIT
if ! cp bin/canopyd "$TMP_PATH"; then
	fail "copy to $TMP_PATH failed"
fi
chmod 755 "$TMP_PATH"
if ! mv -f "$TMP_PATH" "$INSTALL_PATH"; then
	fail "atomic mv $TMP_PATH -> $INSTALL_PATH failed"
fi
trap - EXIT
echo "      installed: $(ls -la "$INSTALL_PATH")"

# ── 3. Restart the systemd user service ───────────────────────────────────
echo "[3/5] systemctl --user restart $SERVICE_NAME"
systemctl --user restart "$SERVICE_NAME" || fail "systemctl --user restart $SERVICE_NAME failed (is the user session D-Bus up?)"

# ── 4. Poll /health until the new binary is serving ──────────────────────
echo "[4/5] poll /health (max ${HEALTH_TIMEOUT}s)"
deadline=$((SECONDS + HEALTH_TIMEOUT))
until curl -s -o /dev/null --max-time 2 http://localhost:8091/health; do
	if ((SECONDS >= deadline)); then
		echo "--- journalctl (last 20 lines) ---" >&2
		journalctl --user -u "$SERVICE_NAME" -n 20 --no-pager >&2 || true
		fail "canopyd did not become healthy within ${HEALTH_TIMEOUT}s (check journalctl --user -u $SERVICE_NAME)"
	fi
	sleep 1
done
echo "      canopyd healthy"

# ── 5. Gateway surface smoke test ─────────────────────────────────────────
echo "[5/5] gateway smoke test (scripts/smoke-gateway.sh)"
bash "$REPO_ROOT/scripts/smoke-gateway.sh" || fail "post-deploy gateway smoke test failed"

echo ""
echo "DEPLOY OK: $(systemctl --user show -p ActiveState --value "$SERVICE_NAME") since $(systemctl --user show -p ActiveEnterTimestamp --value "$SERVICE_NAME")"
