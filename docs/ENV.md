# Canopyd Environment Variable Reference (DOC-5)

Operator-facing reference for every environment variable the `canopyd` server and its CLI read. Derived mechanically from the source (grep over `os.Getenv` / `os.LookupEnv` in `internal/` and `cmd/`); each entry cites the read site.

Every variable below is read exactly once by the listed site. Test-only variables (not read by the server) are grouped at the end.

## Server

| Variable | Purpose | Default when unset | Read at |
|---|---|---|---|
| `HTTP_ADDR` | Listen address for the built-in HTTP server (`canopyd serve`), `host:port` form. Operators commonly run `:8091`; the CLI default target is `http://localhost:8091`. | `:8080` | internal/config/config.go:271 |
| `LOG_LEVEL` | Global log level (zerolog levels: `trace`, `debug`, `info`, `warn`, `error`, `fatal`, `panic`). An unparseable value falls back to `info`. | `info` | internal/config/config.go:274 |
| `LOG_FORMAT` | Log encoding. `json` = structured JSON on stderr; any other value = human-friendly console output. | `text` (console) | internal/config/config.go:277 |
| `CORS_ORIGIN` | Value for the `Access-Control-Allow-Origin` middleware. | `*` | internal/config/config.go:280 |
| `METRICS_ENABLED` | Enable the metrics endpoint. Truthy values are exactly `true` or `1`; anything else keeps metrics off. | `false` (off) | internal/config/config.go:292 |
| `CANOPY_TRUSTED_PROXIES` | Comma-separated CIDR prefixes of reverse proxies whose `X-Forwarded-For` header may be trusted for client IP resolution. Empty = trust no proxy (fail-closed). Entries are validated at startup; a malformed entry is a hard error. | unset (no proxy trusted) | internal/config/config.go:431 |

## Database

Precedence: when `CANOPY_DB_URL` is set (and parses as a `postgres://` URL), its components override every individual `DB_*` field. `DB_*` variables otherwise override the built-in defaults individually. The legacy `DB_*` variables remain supported.

| Variable | Purpose | Default when unset | Read at |
|---|---|---|---|
| `CANOPY_DB_URL` | Full PostgreSQL DSN (`postgres://user:password@host:port/dbname?sslmode=...`). Overrides all individual `DB_*` fields when set (BUG-021 fix). Only `sslmode` is honored from the query string. | unset | internal/config/config.go:367 |
| `CANOPY_DB_DRIVER` | Runtime database backend: `postgres` or `sqlite`. Any other value is a startup error (Validate). | `postgres` | internal/config/config.go:247 |
| `CANOPY_SQLITE_PATH` | SQLite database file path, used only when `CANOPY_DB_DRIVER=sqlite`. **Direction only:** SQLite-first is the declared direction (board row GAP-076) but is **not landed** in the live boot path; leaving the driver at `postgres` keeps the historical behavior. | `~/.canopy/canopy.sqlite` | internal/config/config.go:248 |
| `DB_HOST` | PostgreSQL host (legacy per-field override). | `localhost` | internal/config/config.go:251 |
| `DB_PORT` | PostgreSQL port (legacy per-field override; non-numeric values are ignored). | `5432` | internal/config/config.go:254 |
| `DB_USER` | PostgreSQL user (legacy per-field override). | `canopy` | internal/config/config.go:259 |
| `DB_PASSWORD` | PostgreSQL password (legacy per-field override). Prefer `CANOPY_DB_URL`; avoid committing this value anywhere. | `canopy` | internal/config/config.go:262 |
| `DB_NAME` | PostgreSQL database name (legacy per-field override). | `canopy` | internal/config/config.go:265 |
| `DB_SSLMODE` | TLS mode for the PostgreSQL connection (`disable`, `require`, `verify-full`, ...). (legacy per-field override) | `disable` | internal/config/config.go:268 |
| `DB_SCHEMA` | PostgreSQL schema injected as `search_path` in the assembled DSN. | `public` | internal/config/config.go:283 |
| `CANOPY_CARD_DATA_DIR` | Directory for per-card-type SQLite databases (cards do not use PostgreSQL or DuckDB). The override is used verbatim — nothing is joined onto it — and is re-resolved on every call. | `~/.hermes/canopy/cards` | internal/card/database.go:23 |

## Auth

| Variable | Purpose | Default when unset | Read at |
|---|---|---|---|
| `JWT_SECRET` | HS256 signing secret for dev-mode JWTs. **The default is a public dev value — set a real secret before exposing the server.** | `dev-secret-change-me` (dev only) | internal/config/config.go:286 |
| `REFERENCE_SELECTION_SECRET` | HMAC secret for preflight reference-selection tokens (SPEC-PL-06 §14.1). Empty falls back to `JWT_SECRET`. | falls back to `JWT_SECRET` | internal/config/config.go:289 |
| `CANOPY_OWNER_ID` | Owner UUID used by `canopyd session` import/update commands. Must parse as a UUID; an invalid value exits with an error. | `00000000-0000-0000-0000-000000000001` (dev user) | cmd/canopyd/session_cmd.go:158, cmd/canopyd/session_cmd.go:267 |
| `CANOPY_TOKEN` | Bearer token the `canopyd` CLI attaches as `Authorization: Bearer <token>` on API requests. Server-side JWTs are minted per the README "Authentication (dev mode)" section. | unset (CLI proceeds without auth and prints a warning) | cmd/canopyd/cli.go:350 |
| `CANOPY_SERVER_URL` | API base URL the `canopyd` CLI targets (absolute `http(s)://host:port`). An invalid value is refused; if any server-only variable (`HTTP_ADDR`, `CANOPY_DB_URL`, `DB_*`) is set without it, the CLI refuses to guess the target. | `http://localhost:8091` | cmd/canopyd/cli.go:270 |

## Context compiler

Numeric knobs accept an integer; out-of-range or non-numeric values silently keep the default (except `CONTEXT_MODEL_WINDOWS`, which fails startup loudly on a malformed entry).

| Variable | Purpose | Default when unset | Read at |
|---|---|---|---|
| `CONTEXT_MAX_ANCESTORS` | Maximum ancestor nodes folded into a compiled context payload. Values `<= 0` are ignored. | `50` | internal/config/config.go:295 |
| `CONTEXT_MAX_REFS` | Soft maximum #references resolved per compilation; the hard cap is 2x this value. Values `<= 0` are ignored. | `5` | internal/config/config.go:300 |
| `CONTEXT_DEFAULT_BUDGET` | Flat default token budget for a compiled context payload. Values `<= 0` are ignored. | `8000` | internal/config/config.go:305 |
| `CONTEXT_BUDGET_PERCENT` | Percentage of the selected model's context window used as the default budget (replaces the flat budget only when the model's window is known). Accepted range 0–100; `0` disables the window-derived path. | `60` | internal/config/config.go:315 |
| `CONTEXT_RETRIEVAL_MAX` | Maximum topic-search results the retrieved tier may fold into a payload. Accepted range 0–50; `0` disables the tier. | `0` (tier disabled) | internal/config/config.go:324 |
| `CONTEXT_MODEL_WINDOWS` | Locally declared context windows for models the gateway reports no window for. Format: comma-separated `model=window` pairs, e.g. `CONTEXT_MODEL_WINDOWS="Hermes Agent=200000,probe-big=128000"` (a model id may contain `=`; the last `=` separates). Later pairs replace earlier ones; a window the gateway reports always wins. A malformed pair is a **startup error**. | unset (no overrides) | internal/config/config.go:334 |

## Plugins

| Variable | Purpose | Default when unset | Read at |
|---|---|---|---|
| `PLUGIN_MAX_SIZE` | Maximum plugin bundle size in bytes (plugin sandbox, GAP-002 §4.1). Values `<= 0` are ignored at parse time; a negative value on a programmatically-built config is a startup error. | `1048576` (1 MiB) | internal/config/config.go:339 |

## Integrations

### Hermes gateway

| Variable | Purpose | Default when unset | Read at |
|---|---|---|---|
| `HERMES_WEBUI_GATEWAY_BASE_URL` | Base URL of the Hermes gateway client (GAP-050). | `http://127.0.0.1:8642` | internal/config/config.go:354 |
| `HERMES_WEBUI_GATEWAY_API_KEY` | API key for the gateway. Takes precedence over the fallback below, so a canopyd running next to `hermes gateway run` needs no extra configuration. | unset (see fallback) | internal/config/config.go:357 |
| `API_SERVER_KEY` | Fallback API key used only when `HERMES_WEBUI_GATEWAY_API_KEY` is unset. | unset | internal/config/config.go:359 |

### NATS

| Variable | Purpose | Default when unset | Read at |
|---|---|---|---|
| `CANOPY_NATS_URL` | NATS server URL. When set, canopyd loads transport settings from PostgreSQL and registers the NATS transport adapter at boot; when empty, NATS is not used. | unset (NATS disabled) | internal/config/config.go:410 |
| `CANOPY_NATS_CREDS` | Path to NATS credentials file passed to the client alongside `CANOPY_NATS_URL`. | unset | internal/config/config.go:411 |

### WebRTC

| Variable | Purpose | Default when unset | Read at |
|---|---|---|---|
| `CANOPY_WEBRTC_ENABLED` | Explicit opt-in for the production Pion WebRTC transport (signaling rides the existing SSE control connection). Registered only when the value is exactly `1`. | unset (WebRTC off) | internal/transport/webrtc_production.go:16 |

### File storage / gateway state

| Variable | Purpose | Default when unset | Read at |
|---|---|---|---|
| `CANOPY_FILE_ROOT` | Root directory for uploaded file bytes (SPEC-PL-02 §7.6); objects land at `<root>/files/<aa>/<sha256>`. | `~/.canopy/files` | internal/config/config.go:416 |
| `CANOPY_GATEWAY_STATE_FILE` | Path of the gateway run-registry JSONL file. The override names the file itself (not a directory), is used verbatim, and is re-resolved on every call. | `~/.hermes/canopy/gateway/runs.jsonl` | internal/gateway/service.go:130 |

## Test infrastructure (not read by the server)

These variables are consumed by the test/benchmark harness (`internal/testutil`) and CI, never by `canopyd` itself.

| Variable | Purpose | Default when unset | Read at |
|---|---|---|---|
| `CANOPY_SKIP_INTEGRATION` | Any value: integration tests skip unconditionally, before any DB gate or probe. | unset (tests run) | internal/testutil/integration.go:280 |
| `CANOPY_ADMIN_DB_URL` | PostgreSQL admin URL for test setup/teardown (creates/drops per-run databases). Preferred override; an explicit value bypasses the shared-DB gate. | `postgres://canopy:***@localhost:5437/postgres?sslmode=disable` | internal/testutil/integration.go:105 |
| `CANOPY_TEST_DB_URL` | PostgreSQL server URL tests target; selects the server only — each pool still gets its own uniquely named database. Fallback for the admin URL; explicit value bypasses the shared-DB gate. | unset (falls back to `CANOPY_ADMIN_DB_URL`, then the default above) | internal/testutil/integration.go:108 |
| `CANOPY_TEST_ALLOW_SHARED_DB` | `1`: explicitly opt in to running tests against the default (production-shared) PostgreSQL on :5437. | unset (implicit shared default is refused) | internal/testutil/integration.go:158 |
| `CANOPY_REQUIRE_DB` | `1` (CI mode): integration tests FAIL instead of skip when PostgreSQL is unreachable, and the shared-DB gate is satisfied (CI provisions its own container). | unset (unreachable DB = skip) | internal/testutil/integration.go:161, internal/testutil/integration.go:291 |
| `CANOPY_DATABASE_URL` | PG requirement marker for the integration **benchmark** harness (INT-05): benchmarks expect a running PostgreSQL here. Read by test convention, not by production code. | `localhost:5437` | internal/handler/benchmark_integration_test.go:27 (comment; consumed by the harness) |
