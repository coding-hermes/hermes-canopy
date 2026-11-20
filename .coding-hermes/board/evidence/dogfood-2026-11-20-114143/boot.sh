#!/usr/bin/env bash
# Dogfood 2026-11-20: boot an isolated canopyd scratch instance with the LIVE
# Hermes gateway (:8642) and metrics enabled. Never prints secrets.
set -u
REPO=/home/kara/hermes-canopy
PORT=18093
BASE="http://127.0.0.1:$PORT"
PG_HOST=127.0.0.1; PG_PORT=5437; PG_USER=canopy; PG_PASSWORD=canopy
RAND=$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')
DB="canopy_scratch_df20b_${RAND}"
ROOT=/tmp/canopy-df19
mkdir -p "$ROOT/home" "$ROOT/files"
export PGPASSWORD="$PG_PASSWORD"
psql -w -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='$DB'" | grep -q 1 && { echo "FATAL db exists"; exit 1; }
psql -w -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d postgres -c "CREATE DATABASE $DB" >/dev/null || { echo "FATAL createdb"; exit 1; }
# gateway key: sourced, never echoed
GATEWAY_KEY=$(grep -E '^API_SERVER_KEY=' ~/.hermes/.env | head -1 | cut -d= -f2-)
[ -n "$GATEWAY_KEY" ] || { echo "FATAL no gateway key found"; exit 1; }
env -i \
  PATH="$PATH" \
  HOME="$ROOT/home" \
  HTTP_ADDR="127.0.0.1:$PORT" \
  DB_HOST="$PG_HOST" DB_PORT="$PG_PORT" DB_USER="$PG_USER" \
  DB_PASSWORD="$PG_PASSWORD" DB_NAME="$DB" DB_SSLMODE=disable \
  CANOPY_FILE_ROOT="$ROOT/files" \
  HERMES_WEBUI_GATEWAY_BASE_URL="http://127.0.0.1:8642" \
  HERMES_WEBUI_GATEWAY_API_KEY="$GATEWAY_KEY" \
  METRICS_ENABLED=true \
  LOG_LEVEL=info LOG_FORMAT=json \
  "$ROOT/bin/canopyd" serve >"$ROOT/canopyd.log" 2>&1 &
PID=$!
echo "$PID" > "$ROOT/server.pid"
echo "booted pid $PID db $DB"
READY=0
for i in $(seq 1 40); do
  kill -0 "$PID" 2>/dev/null || { echo "FATAL server exited"; tail -5 "$ROOT/canopyd.log" | sed 's/^/  /'; exit 1; }
  curl -s --max-time 2 "$BASE/health" >/dev/null 2>&1 && { READY=1; break; }
  sleep 1
done
[ "$READY" = 1 ] || { echo "FATAL not ready"; tail -10 "$ROOT/canopyd.log" | sed 's/^/  /'; exit 1; }
echo "--- health ---"
curl -s --max-time 5 "$BASE/health"
echo
grep -qF "\"file_root\":\"$ROOT/files\"" "$ROOT/canopyd.log" && echo "file_root OK: isolated"
grep -q '8642' "$ROOT/canopyd.log" && echo "gateway base: live :8642 wired"
echo "$DB" > "$ROOT/db.name"
