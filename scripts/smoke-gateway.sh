#!/usr/bin/env bash
#
# Post-deploy gateway smoke test for canopyd (GAP-052).
#
# Verifies the live Hermes gateway surface (GAP-050) is PRESENT on a freshly
# deployed canopyd. This is the check that was missing when a stale binary
# 404'd the entire /api/v1/gateway surface for ~7h while the stack looked
# healthy (GET /health was 200, E2E had zero gateway coverage).
#
# Checks (read-only — never creates a gateway run):
#   1. GET /health                        → 200 (canopyd is up)
#   2. GET /api/v1/gateway/status (JWT)   → 200, JSON, handler contract shape
#   3. GET /api/v1/gateway/runs   (JWT)   → 200, JSON, {"runs":[...]}
#
# Contract (internal/handler/gateway_handler.go + gateway_handler_test.go):
#   /status returns 200 EVEN when the upstream Hermes gateway is unreachable —
#   the body then reports connected=false with an error string. A 404 on the
#   gateway surface means the deployed binary predates GAP-050 → FAIL.
#
# Environment overrides:
#   CANOPY_BASE_URL   (default http://localhost:8091)
#   CANOPY_JWT_SECRET (default dev-secret-change-me — the dev JWT secret)
#
# Exit 0 only when all checks pass.

set -euo pipefail

BASE_URL="${CANOPY_BASE_URL:-http://localhost:8091}"
JWT_SECRET="${CANOPY_JWT_SECRET:-dev-secret-change-me}"
DEV_USER_ID="00000000-0000-0000-0000-000000000001" # dev user from the Vite proxy (BUG-003)

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

# base64url encode stdin (single line, no padding) via openssl.
b64url() {
	openssl base64 -A | tr '+/' '-_' | tr -d '='
}

# Mint a short-lived HS256 dev JWT (same claims pattern as
# scripts/canopy-tm03-verify.mjs).
mint_jwt() {
	local now header payload sig
	now="$(date +%s)"
	header="$(printf '{"alg":"HS256","typ":"JWT"}' | b64url)"
	payload="$(printf '{"sub":"%s","exp":%s}' "$DEV_USER_ID" $((now + 300)) | b64url)"
	sig="$(printf '%s.%s' "$header" "$payload" \
		| openssl dgst -sha256 -hmac "$JWT_SECRET" -binary \
		| openssl base64 -A | tr '+/' '-_' | tr -d '=')"
	printf '%s.%s.%s' "$header" "$payload" "$sig"
}

# Assert a response body parses as JSON and contains a required key.
# Usage: assert_json <body> <required-substring> <label>
assert_json() {
	local body="$1" needle="$2" label="$3"
	if ! printf '%s' "$body" | python3 -c 'import sys,json; json.load(sys.stdin)' >/dev/null 2>&1; then
		fail "$label body is not valid JSON: $(printf '%.200s' "$body")"
	fi
	if [[ "$body" != *"$needle"* ]]; then
		fail "$label body missing expected \"$needle\": $(printf '%.200s' "$body")"
	fi
}

echo "Gateway smoke test — target: $BASE_URL"

# ── 1. canopyd is up ──────────────────────────────────────────────────────
health_code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$BASE_URL/health" || true)"
if [[ "$health_code" != "200" ]]; then
	fail "GET $BASE_URL/health returned '$health_code' (expected 200) — canopyd is not up"
fi
echo "OK  GET /health -> 200"

# ── 2. gateway/status exists and matches the handler contract ────────────
JWT="$(mint_jwt)"
status_body="$(curl -s --max-time 10 -H "Authorization: Bearer $JWT" "$BASE_URL/api/v1/gateway/status" || true)"
status_code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -H "Authorization: Bearer $JWT" "$BASE_URL/api/v1/gateway/status" || true)"
if [[ "$status_code" == "404" ]]; then
	fail "GET /api/v1/gateway/status returned 404 — the gateway surface is MISSING (stale canopyd binary? redeploy with make deploy)"
fi
if [[ "$status_code" != "200" ]]; then
	fail "GET /api/v1/gateway/status returned $status_code (expected 200; handler returns 200 with connected=false even when the upstream gateway is down): $(printf '%.200s' "$status_body")"
fi
assert_json "$status_body" '"connected"' "gateway/status"
assert_json "$status_body" '"base_url"' "gateway/status"
echo "OK  GET /api/v1/gateway/status -> 200 (JSON, contract shape)"

# ── 3. gateway/runs exists and returns the runs envelope ─────────────────
runs_body="$(curl -s --max-time 10 -H "Authorization: Bearer $JWT" "$BASE_URL/api/v1/gateway/runs" || true)"
runs_code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 -H "Authorization: Bearer $JWT" "$BASE_URL/api/v1/gateway/runs" || true)"
if [[ "$runs_code" == "404" ]]; then
	fail "GET /api/v1/gateway/runs returned 404 — the gateway surface is MISSING (stale canopyd binary? redeploy with make deploy)"
fi
if [[ "$runs_code" != "200" ]]; then
	fail "GET /api/v1/gateway/runs returned $runs_code (expected 200): $(printf '%.200s' "$runs_body")"
fi
assert_json "$runs_body" '"runs"' "gateway/runs"
echo "OK  GET /api/v1/gateway/runs -> 200 (JSON, runs envelope)"

echo "GATEWAY SMOKE TEST PASSED"
