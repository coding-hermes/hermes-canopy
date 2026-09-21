#!/usr/bin/env bash
#
# Deploy canopyd (GAP-052): build → schema gate → install → unit sync →
# restart → health poll → smoke.
#
# Closes the stale-binary deploy gap: the systemd user unit
# canopy-canopyd.service runs /home/kara/bin/canopyd, which until now was only
# updated by manual copy — a HEAD build was never what systemd executed (the
# live stack 404'd the entire /api/v1/gateway surface for ~7h because of it).
#
# Steps:
#   0. make build            (repo root → bin/canopyd)
#   1. PRE-DEPLOY SCHEMA GATE (GAP-069): check-deploy-staleness.sh
#                            --check-schema compares bin/canopyd's embedded
#                            migration version against the live DB's
#                            schema_migrations max (read-only SELECT). A stale
#                            binary would fail its own in-process stale-build
#                            guard AFTER restart and crash-loop the service
#                            (2026-09-12: schema-46 DB + schema-42 binary =
#                            ~37h outage) — so the mismatch aborts the deploy
#                            HERE, before install or restart.
#   2. install atomically    (cp to temp next to target, then mv — mv within
#                            the same filesystem is atomic, so systemd never
#                            execs a half-written binary)
#   3. UNIT SYNC (GAP-099)   if deploy/systemd/canopy-canopyd.service differs
#                            from ~/.config/systemd/user/canopy-canopyd.service
#                            (or the live copy is missing), install the repo
#                            copy atomically and `systemctl --user
#                            daemon-reload`. That unit carries the ExecStartPre
#                            DB bring-up — the 2026-09-21 reboot fix is worth
#                            nothing if the RUNNING unit is still the old one.
#                            Deliberately BEFORE the restart below: the restart
#                            must exec the unit we just installed.
#   4. systemctl --user restart canopy-canopyd
#   5. poll GET /health until 200 (max ~30s)
#   6. run scripts/smoke-gateway.sh (gateway surface presence + contract)
#
# Idempotent: safe to re-run any number of times.
# Environment overrides: INSTALL_PATH (default /home/kara/bin/canopyd),
# SERVICE_NAME (default canopy-canopyd), CANOPY_BASE_URL / CANOPY_JWT_SECRET
# (passed through to the smoke test), CANOPYD_SCHEMA_DB_URL (schema-gate
# target DB, default the shared :5437 canopy-pg), CANOPYD_UNIT_SOURCE (default
# deploy/systemd/canopy-canopyd.service), CANOPYD_UNIT_DIR (default
# ~/.config/systemd/user), CANOPYD_SYSTEMCTL (default systemctl; the unit-sync
# step calls it with --user daemon-reload).
#
# Exit non-zero on any failure.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_PATH="${CANOPYD_INSTALL_PATH:-/home/kara/bin/canopyd}"
SERVICE_NAME="${CANOPYD_SERVICE_NAME:-canopy-canopyd}"
# GAP-099: tracked unit file (source of truth) and the live user unit it is
# synced to. Configurable so tests can drive the sync against a temp dir.
UNIT_SOURCE="${CANOPYD_UNIT_SOURCE:-$REPO_ROOT/deploy/systemd/canopy-canopyd.service}"
UNIT_DIR="${CANOPYD_UNIT_DIR:-$HOME/.config/systemd/user}"
UNIT_PATH="$UNIT_DIR/$SERVICE_NAME.service"
SYSTEMCTL="${CANOPYD_SYSTEMCTL:-systemctl}"
HEALTH_TIMEOUT=30

fail() {
	echo "DEPLOY FAILED: $*" >&2
	exit 1
}

echo "Deploying canopyd from $REPO_ROOT"

# ── 0. Build from HEAD ────────────────────────────────────────────────────
cd "$REPO_ROOT"
echo "[0/6] make build"
make build || fail "make build failed"
[[ -x bin/canopyd ]] || fail "build did not produce an executable bin/canopyd"

# ── 1. Pre-deploy schema gate (GAP-069, read-only) ────────────────────────
# Compare the JUST-BUILT binary's embedded migration version against the
# live DB. embedded < db ⇒ the binary predates the schema it would serve;
# its own stale-build guard would kill it after restart (crash-loop). Abort
# here instead — nothing has been installed or restarted yet.
echo "[1/6] schema gate: check-deploy-staleness.sh --check-schema"
rc=0
bash "$REPO_ROOT/scripts/check-deploy-staleness.sh" --check-schema || rc=$?
if ((rc != 0)); then
	fail "pre-deploy schema check failed (rc=$rc) — refusing to deploy a binary that predates the database schema (GAP-069)"
fi

# ── 2. Atomic install to the systemd unit's exec path ────────────────────
echo "[2/6] install bin/canopyd -> $INSTALL_PATH"
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

# ── 3. Sync the systemd user unit (GAP-099) ──────────────────────────────
# The repo copy is the source of truth for what systemd runs. It carries the
# ExecStartPre= DB bring-up that stops a reboot from becoming a 6h crash loop
# (~690 exits/h against a stopped canopy-pg, 2026-09-21). A drifted live copy
# means that fix is not installed, so re-install it here — atomically, same
# cp+mv discipline as the binary — and daemon-reload BEFORE the restart below.
# BEGIN GAP-099 unit sync (extracted verbatim by scripts/test-reboot-durability.sh)
echo "[3/6] sync $UNIT_SOURCE -> $UNIT_PATH"
[[ -f "$UNIT_SOURCE" ]] || fail "unit source $UNIT_SOURCE is missing — refusing to deploy without the reboot fix (GAP-099 ExecStartPre DB bring-up)"
mkdir -p "$UNIT_DIR"
if [[ -f "$UNIT_PATH" ]] && cmp -s "$UNIT_SOURCE" "$UNIT_PATH"; then
	echo "      live unit is current with the repo copy (no daemon-reload needed)"
else
	if [[ -f "$UNIT_PATH" ]]; then
		echo "      live unit drifted from the repo copy — reinstalling"
	else
		echo "      unit not installed yet — installing"
	fi
	UNIT_TMP="$(mktemp "$UNIT_DIR/.$SERVICE_NAME.XXXXXX")"
	trap 'rm -f "$UNIT_TMP"' EXIT
	if ! cp "$UNIT_SOURCE" "$UNIT_TMP"; then
		fail "copy $UNIT_SOURCE -> $UNIT_TMP failed"
	fi
	chmod 644 "$UNIT_TMP"
	if ! mv -f "$UNIT_TMP" "$UNIT_PATH"; then
		fail "atomic mv $UNIT_TMP -> $UNIT_PATH failed"
	fi
	trap - EXIT
	"$SYSTEMCTL" --user daemon-reload || fail "$SYSTEMCTL --user daemon-reload failed"
	echo "      installed: $UNIT_PATH ($(wc -l <"$UNIT_PATH") lines)"
fi
# END GAP-099 unit sync

# ── 4. Restart the systemd user service ───────────────────────────────────
echo "[4/6] systemctl --user restart $SERVICE_NAME"
systemctl --user restart "$SERVICE_NAME" || fail "systemctl --user restart $SERVICE_NAME failed (is the user session D-Bus up?)"

# ── 5. Poll /health until the new binary is serving ──────────────────────
echo "[5/6] poll /health (max ${HEALTH_TIMEOUT}s)"
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

# ── 6. Gateway surface smoke test ─────────────────────────────────────────
echo "[6/6] gateway smoke test (scripts/smoke-gateway.sh)"
bash "$REPO_ROOT/scripts/smoke-gateway.sh" || fail "post-deploy gateway smoke test failed"

echo ""
echo "DEPLOY OK: $(systemctl --user show -p ActiveState --value "$SERVICE_NAME") since $(systemctl --user show -p ActiveEnterTimestamp --value "$SERVICE_NAME")"
