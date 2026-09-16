
## Dogfood Findings (2026-09-16)
Verdict: PROMISING-BUT-ROUGH
Promise: {"entry_point":"Go single binary `canopyd` (HTTP/JSON REST API server on :8091 + SSE event hub + embedded migrations, API-only — does NOT serve the PWA); companion entry points are the React/TS Vite PWA on :5173 (separate static serve in prod), CLI subcommands in the same binary (`canopyd serve|tree

- [P0] CLI ignores DB_*/HTTP_ADDR and hardcodes :8091, writing into the LIVE instance's Postgres — Judge ran the CLI with DB_PORT=15440 expecting a scratch DB but 'tree create' silently wrote a tree named 'CLI Tree' into the host's live canopy Postgres (visible in the live PWA Trees view; it was no
- [P0] The documented production PWA path yields a dead, permanently unauthenticated UI — README says to serve frontend/dist with any static server, but same-origin /api/v1/* returns index.html → 'Unexpected token '\u003c'', \u003c!doctype ... is not valid JSON\ and 'Backend: unreachable'; no rever
- [P1] Documented defaults collide with the host's live service; no scratch-instance recipe exists — docker compose up -d 'does not give a working API': hermes-canopy-canopyd exits 1 crash-looping 'STALE BUILD ... binary_embedded_version=38 db_schema_version=46', and README claims compose serves :809
- [P1] Isolation is not real and docs/schema drift: fresh DB leaks host-wide stores, schema_version 38≠47, VITE_API_URL wrong — A brand-new database still listed 4 cards and recents from other instances via the global stores ~/.hermes/canopy/cards and ~/.canopy/files; the docs example shows /health schema_version 38 while the 
- [P1] Advertised MCP endpoint cannot be initialized and is absent from the README — POST /api/v1/mcp returns -32601 'Method not found: initialize' (also for notifications/initialized), so a standards-compliant MCP client cannot handshake — yet the entry_point promise advertises 'a JS
