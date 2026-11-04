# hermes-canopy — observability spec

Status: specification of the TARGET state, with the measured CURRENT state recorded first.
Audit date: 2026-11-03. Repo HEAD at audit: `e0bfc6ef`.
Author: fleet observability audit (`monitoring` lane). No service code was changed by this audit.

> **Amendment (CAN-MET-01, 2026-11-04):** the rename this spec's §4 called for has
> LANDED. Every product series is now emitted under the `canopy_` namespace:
> `request_total` → `canopy_http_requests_total`,
> `request_duration_seconds` → `canopy_http_request_duration_seconds`,
> `active_connections` → `canopy_active_connections`,
> `tree_count` → `canopy_tree_count` (now wired, see D2),
> `node_count` → `canopy_node_count` (now wired, see D2),
> `resume_duration_seconds` → `canopy_resume_duration_seconds`,
> `resume_started_total` → `canopy_resume_started_total`.
> New: `canopy_http_unmatched_requests_total{method=...}` counts requests that
> match no chi route; the path label on the request series is always the chi
> route pattern and folds to `unmatched` for unmatched requests — the raw
> `r.URL.Path` fallback is deleted (D3's residual defect closed).
> Where §4's target table below named different renames
> (`canopy_http_inflight_requests`, `canopy_domain_trees`, `canopy_domain_nodes`),
> the shipped names above win; the table is amended to match.
> §2 below records the PRE-rename measurement exactly as the audit found it —
> it is a historical record of the state at `e0bfc6ef`, not the current wire.

---

## 1. What this service is, and what "healthy" means

`canopyd` is a single long-running Go HTTP daemon (`cmd/canopyd/main.go`, entry `main()` at
`cmd/canopyd/main.go:236-246` sets up zerolog) that serves:

- a JSON REST API under `/api/v1` (`internal/server/server.go:325`: `r.Route("/api/v1", ...)` +
  JWT auth middleware), backed by Postgres via `pgxpool` (`internal/db/db.go:95`);
- a GraphQL-ish node/tree domain (trees, nodes, edges, topics, cards, profiles, MLS) — e.g.
  `internal/handler/node_handler.go:102-109`, `internal/service/tree_service.go`;
- long-lived SSE streams (`internal/sse/sse_handler.go:36-82`) for tree events and federation
  events;
- an optional peer relay hub (`internal/relay/hub.go:37`, `:416`) with TLS cert rotation
  (`internal/relay/keyrotation.go`) and per-peer rate limiting (`internal/federation/ratelimit.go`);
- a transport layer with pluggable adapters — SSE, WebSocket, WebRTC, NATS, relay
  (`internal/transport/`), each with connect/reconnect/message counters held in process memory
  (`internal/transport/metrics.go:13-22`);
- a proxy surface onto an upstream **Hermes gateway** (`internal/gateway/client.go:79`,
  `internal/gateway/service.go:288-320`), whose runs are held in an in-memory map
  (`internal/gateway/service.go:115`, `runs map[string]*RunRecord`);
- an embedded SPA on the same port when built with `-tags embedui` (`internal/webui/spa.go:30`).

"Healthy" therefore has five distinct meanings, and today only the first is observable:

1. **Up and DB-consistent** — process listening, schema version equals the binary's embedded
   migration version. *Observed today* via `GET /health`
   (`internal/server/server.go:737-766`), which returns `schema_version`,
   `embedded_migrations`, relay `mode/status/sessions`, and build identity.
2. **Doing work** — requests are being served, at what rate, at what latency, with what error
   share. *Not observable at fleet level* (see §3.1 — the endpoint exists but nothing scrapes it,
   and the labels are wrong).
3. **Long-lived streams staying connected** — SSE/WebSocket connections, reconnects, drops.
   *Not observable* — the counters exist in memory and are only served over an
   authenticated per-request JSON endpoint (§3.3).
4. **Peer relay healthy** — sessions vs. limit, queue depth, TLS cert expiry, drops.
   *Only partly observable*, and only behind JWT (§3.3).
5. **Upstream gateway reachable** — the Hermes gateway that every AI run depends on. *Not
   observable at all* — a gateway outage is silent to Prometheus (§3.4).

---

## 2. Measured current state (verified, not inferred)

> The static scan that seeded this audit reported `endpoint_metrics=3 promhttp=1 own_counter_or_gauge=2`.
> That under-counted. Verified reality below.

**Endpoint exists and is live right now.**

```
$ curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8091/metrics
200
$ curl -s http://127.0.0.1:8091/metrics | grep -c '^# HELP'
47          # of which 40 are go_*/process_* runtime families; 7 are product families
```

**Product metric families actually served** (`curl http://127.0.0.1:8091/metrics`):

| family | type | declared at | live value at audit |
|---|---|---|---|
| `request_duration_seconds` | histogram, labels `method`,`path` | `internal/telemetry/metrics.go:50-57` | 4 series |
| `request_total` | counter, labels `method`,`path`,`status` | `internal/telemetry/metrics.go:58-64` | 4 series |
| `active_connections` | gauge | `internal/telemetry/metrics.go:65-70` | `1` |
| `tree_count` | gauge | `internal/telemetry/metrics.go:71-76` | **`0`** |
| `node_count` | gauge | `internal/telemetry/metrics.go:77-82` | **`0`** |
| `resume_duration_seconds` | histogram | `internal/telemetry/metrics.go:39-48` | count `0` |
| `resume_started_total` | counter | `internal/telemetry/metrics.go:84-89` | `0` |

**Defects in what already exists** (each is a board row, §5):

- **D1 — not scraped.** No `job_name` in the monitoring stack's `prometheus.yml` mentions
  canopy, and no `remote_write` push exists in `canopyd`. Verified:
  `grep -rn "canopy" <monitoring>/prometheus` → no output. A green `/metrics` that nobody
  scrapes is worth exactly zero at 3am.
- **D2 — `tree_count` and `node_count` are permanent zeros.** They are registered by `promauto`
  (`internal/telemetry/metrics.go:71-82`) and **never written**: the only writers in the repo are
  unrelated struct fields (`grep -rn "TreeCount\.\|NodeCount\.Set" --include=*.go` → no output).
  The shipped Grafana dashboard (`deploy/grafana/dashboard.json`, panels `expr: "tree_count"`,
  `expr: "node_count"`) therefore renders two flat-zero gauges forever.
- **D3 — the `path` label falls back to the raw URL.**
  `internal/telemetry/middleware.go:32-36` uses `r.URL.Path` when chi has no route pattern. chi sets no pattern for **unmatched** routes,
  so every 404 becomes its own label value. Verified live: after one request to
  `/api/v1/nonexistent-path-xyz` the series
  `request_total{method="GET",path="/api/v1/*",status="401"}` appeared — note the raw path was
  NOT the literal sentinel: chi's mount pattern `/api/v1/*` replaced it. So the honest finding is
  **chi's catch-all pattern collapses unmatched paths into one bucket rather than exploding
  cardinality**; the residual defect is that all unmatched traffic (404 route probes, and
  in this case a 401 from the auth middleware) is indistinguishable in the series, and the
  fallback to a raw `r.URL.Path` remains one router change away from becoming a cardinality bomb.
  Treated as a hardening item (OBS-1 step 3), not as a live cardinality bomb.
- **D4 — no `status` on the latency histogram.** `request_duration_seconds` carries only
  `method`,`path` (`internal/telemetry/metrics.go:56`), so latency cannot be split by 2xx/5xx
  and error responses pollute the SLO percentile.
- **D5 — `/metrics` itself is rate-limited and self-measured.** The rate limiter runs before the
  metrics middleware (`internal/server/server.go:295` vs `:299`) and exempts only
  `/health`,`/healthz`,`/version` (`internal/handler/auth.go:161-167`); `/metrics` is not in that
  list. Under load, a scraper can be 429'd, and the scrape itself adds a `request_total` sample
  (`path="/metrics"` is visible in the live dump). Twelve 10s-interval scrapers from one IP would
  hit the 100/s burst? No — but a busy host plus a slow scrape can, and a 429 on `/metrics`
  makes the scrape target flap.
- **D6 — no process-exporter gap**: `go_*`/`process_*` families ARE present (40 of 47), so Go
  runtime visibility is the one thing that already works and needs no work. (There is no
  `prometheus.NewProcessCollector`/`NewGoCollector` call in the repo; they come from
  `promhttp.Handler()`'s default gatherer — fine, but it means Go runtime metrics and product
  metrics share one registry and one endpoint policy.)
- **D7 — `resume_*` is deployed but idle.** `resume_started_total == 0` with a live server and
  3 observed `/health` hits. The resume window only opens on authenticated, tree-scoped reads
  (`internal/telemetry/resume.go:284`), so 0 is plausible — but it is also indistinguishable
  from "wired but broken". Its health needs a positive-control query (§6.3).

---

## 3. Blind spots that actually hurt (state that exists in code, and is never emitted)

### 3.1 Request-level work volume and error rate, fleet-wide
*Answerable today*: via `request_total` — **but only if scraped** (D1) and only if labels are
bounded (D3). An operator asking "did canopy get slower or just quieter?" has no answer either
way in Prometheus.

### 3.2 Latency of the expensive paths
`internal/handler/network_proxy_handler.go:87` computes `time.Since(started).Milliseconds()` and
puts it **only in the HTTP response body** (`durationMs`). Same for the transport latency probe
(`internal/handler/transport_handler.go:239`). Those are exactly the two places where a slow
upstream turns into a slow product, and neither emits a metric.

### 3.3 Transport / stream health — counters exist, are not scrapeable
`internal/transport/metrics.go:13-22` keeps `connectAttempts`, `connectSuccesses`,
`connectFailures`, `messagesSent`, `messagesReceived`, `reconnects`, `lastTransition`,
`relayEnqueues`, `relayPolls`, `relayDrops` as atomics. The **only** consumer is
`internal/handler/transport_handler.go:65` (`transport.Metrics().Snapshot()`), served by
`GET /api/v1/transports/health` (`:46-48`) — **JWT-authenticated**. Also:
- `ConnectionManager.ConnectionCount(tt)` (`internal/transport/connection_manager.go:437-450`)
  is a ready-made gauge with no caller;
- `RelayService.QueueDepth()` (`internal/transport/relay_service.go:162-170`) — the in-memory
  peer queue depth — has exactly one caller, the same JWT handler;
- `RelayHub.ActiveSessions()` (`internal/relay/hub.go:416`) is surfaced **only** inside the
  `/health` JSON body as `relay.sessions` (`internal/relay/relay_service.go:204` via
  `internal/server/server.go:750-756`) and **inside an SSE broadcast frame**
  (`internal/relay/relay_service.go:205-209`, `relay_status`) — i.e. pushed to connected
  dashboards, not to a time-series store. Session-loss history is unrecoverable;
- the federation Postgres relay has a real, expensive `QueueDepth(ctx, peerID)` query
  (`internal/federation/relay.go:113`, cap `MaxPeerQueueDepth = 10000` at `:26`) surfaced only
  by the JWT-gated `internal/handler/federation_handler.go:104-112`.

So the cheapest, highest-value wins in this product are **re-exporting things that already
exist** as Prometheus series, not writing new instrumentation.

### 3.4 Upstream gateway reachability — completely blind
`Client.Health(ctx)` (`internal/gateway/client.go:79-94`) exists and returns a typed error on
non-200. Nothing counts it. If the Hermes gateway goes down, canopy keeps answering 2xx (it is
"up") while every AI run fails silently from Prometheus's point of view. This is the single
most likely 3am page that nobody will receive.

### 3.5 In-memory run map growth
`internal/gateway/service.go:115` (`runs map[string]*RunRecord`) is populated on every
`StartRun` (`:366`) — and there is no eviction in the file. A leak here is invisible: memory
grows, latency grows, and the only symptom is eventually `go_memstats_alloc_bytes` — attributing
that to the run map takes an hour of `pprof` archaeology. A `len(s.runs)` gauge removes that hour.

### 3.6 Rate-limit rejections
`RateLimit` (`internal/handler/middleware.go:107-133`) returns `429 RATE_LIMITED` on refusal and
counts nothing. A client hammering canopy looks, in `request_total`, identical to a client being
served — the 429s land in `request_total{status="429"}` *only where a route matched*; refusals
short-circuit before the handler but after the metrics middleware, so they do appear — but there
is no dedicated, alertable counter, and no visibility into **how many IPs** are being limited
(the limiter's `visits` map, `internal/handler/middleware.go:66`, is unbounded and never pruned).

### 3.7 DB pool saturation
`pgxpool` stats (`pgxpool.Stat()`) are never read anywhere in the repo
(`grep -rn "pgxpool.New" internal/db` → only construction at `internal/db/db.go:95`). Pool
exhaustion presents as latency spikes on every endpoint at once, with no metric that says so.

### 3.8 Data growth / retention
The only retention control found is a transport-event prune
(`internal/db/transport_repo.go:301`). Snapshot rows (`internal/db/snapshot_repo.go:150-217`)
carry full `SnapshotData`; nothing counts them. "Is the DB growing without bound?" is
answerable only with `psql`.

### 3.9 CLI lanes (cardexport, deploycheck, updater)
`internal/cardexport/export.go:129` and `internal/updater/updater.go:129/331` are
short-lived operations, not daemons. They must **not** grow a scrape target — see §7.

---

## 4. Metric catalogue (target state)

Convention: `canopy_<subsystem>_<thing>_<unit>`. Labels are strictly low-cardinality:
`method`, the route pattern (chi **pattern** only, never a raw path — shipped as label
`path` by CAN-MET-01; OBS-1 relabels it `route`), `status_class` (`2xx`/`3xx`/`4xx`/`5xx`, OBS-1),
`transport`, `state`, `peer_class`. **Never** a user id, tree id, node id, run id, prompt, URL,
IP, or timestamp in a label.

### Keep (already exist — renamed to the `canopy_` namespace by CAN-MET-01)

| name | type | unit | labels | question answered |
|---|---|---|---|---|
| `canopy_http_request_duration_seconds` *(rename of `request_duration_seconds`)* | histogram | s | `method`,`path` (chi route pattern only) | how slow are responses? |
| `canopy_http_requests_total` *(rename of `request_total`)* | counter | requests | `method`,`path` (chi route pattern only),`status` | how much work, and what share fails? |
| `canopy_active_connections` *(rename of `active_connections`)* | gauge | requests | — | is the server under concurrent pressure right now? |
| `canopy_http_unmatched_requests_total` | counter | requests | `method` | is something probing or has a client gone stale? (this replaces the raw-path fallback from D3) |
| `canopy_tree_count` *(rename of `tree_count`)* | gauge | trees | — | how much data do we hold? (wired by CAN-MET-01 — D2) |
| `canopy_node_count` *(rename of `node_count`)* | gauge | nodes | — | how much data do we hold? (wired by CAN-MET-01 — D2) |
| `canopy_resume_duration_seconds` | histogram | s | — | does "resume in <30s" still hold? |
| `canopy_resume_started_total` | counter | resumes | — | are resumes happening at all? |

Renaming is safe: the Grafana dashboard in this repo is the only consumer
(`deploy/grafana/dashboard.json`), it is deployed nowhere yet (no panel of it exists on the
monitor host), and the rename and the JSON update land in the same commit
(CAN-MET-01). The earlier draft of this table named
`canopy_http_inflight_requests` / `canopy_domain_trees` / `canopy_domain_nodes`
as the renames; the shipped names (above) keep the original series' own words
under the `canopy_` prefix instead, which is what the CAN-MET-01 board row
specified. A `status_class` label split (2xx/3xx/4xx/5xx) remains OBS-1 scope
and is deliberately NOT in the rename commit.

### Add — P0 (minimal viable: §5 row OB-1)

| name | type | unit | labels | question answered |
|---|---|---|---|---|
| `canopy_build_info` | gauge = 1 | — | `version`,`commit` | which build is running in production? |
| `canopy_upstream_gateway_up` | gauge 0/1 | — | — | **is the Hermes gateway reachable?** (the 3am page) |
| `canopy_upstream_gateway_check_errors_total` | counter | checks | `kind` (`timeout`/`non_200`/`conn_refused`) | is the gateway flapping or down? |

### Add — P1 (depth: §5 rows OB-2, OB-3)

| name | type | unit | labels | question answered |
|---|---|---|---|---|
| `canopy_transport_connections` | gauge | connections | `transport`,`state` | are peers connected, and over which transport? |
| `canopy_transport_connect_total` | counter | attempts | `transport`,`result` (`ok`/`fail`) | are connects succeeding? |
| `canopy_transport_reconnects_total` | counter | reconnects | `transport` | is a link flapping? |
| `canopy_transport_messages_total` | counter | messages | `transport`,`direction` (`in`/`out`) | how much traffic, and is it one-way? |
| `canopy_relay_sessions` | gauge | sessions | — | are relay sessions being lost? |
| `canopy_relay_queue_depth` | gauge | messages | `peer_class` (`memory`/`db`) | is a queue backing up? |
| `canopy_relay_enqueue_drops_total` | counter | messages | — | are we dropping work? (from `relayDrops`) |
| `canopy_federation_queue_depth` | gauge | events | — | per-peer federation backlog (sum over peers) |
| `canopy_ratelimited_total` | counter | requests | `route` | is a client being throttled, and where? |
| `canopy_db_pool_acquired` / `canopy_db_pool_total` | gauge | conns | — | is the DB pool saturated? |
| `canopy_gateway_runs_in_flight` | gauge | runs | `state` | is the run map growing unbounded? |

### Add — P2 (depth: §5 rows OB-4, OB-5)

| name | type | unit | labels | question answered |
|---|---|---|---|---|
| `canopy_upstream_proxy_duration_seconds` | histogram | s | `outcome` (`ok`/`error`) | how slow is the upstream proxy path? (today `durationMs` is response-body-only, `internal/handler/network_proxy_handler.go:87`) |
| `canopy_context_compile_duration_seconds` | histogram | s | — | how long does context compilation take? |
| `canopy_export_duration_seconds` | histogram | s | — | how long does a card export take? |
| `canopy_db_largest_table_rows` | gauge | rows | `table` (fixed allowlist: `nodes`,`edges`,`snapshots`,`transport_events`) | is the DB growing without bound? |
| `canopy_relay_cert_expiry_seconds` | gauge | s | — | when does the relay TLS cert expire? |
| `canopy_relay_key_rotations_total` | counter | rotations | — | is rotation actually happening? |

### Deliberately NOT instrumented

- **Per-user / per-tree / per-node labels** — unbounded cardinality, and in a multi-tenant
  product it is also a privacy leak when the scrape store is shared. Aggregate only.
- **Label values derived from URLs** — this is D3; the fix is to drop the sample into
  `canopy_http_unmatched_requests_total{method=...}` instead.
- **The rate limiter's IP set** (`internal/handler/middleware.go:66`) — an IP is a label the
  audit forbids, and it is unbounded. Emit only counts.
- **Prompt / message / model text** — never.
- **Per-request user id in the resume metric** — `internal/telemetry/resume.go:53-56` already
  keys the tracker by user id in memory; that key must never become a label.

---

## 5. Exposure mechanism

**Primary: a `/metrics` endpoint on the existing HTTP server — already there, keep it.**
`internal/server/server.go:316-319` registers `promhttp.Handler()` when `metrics != nil`;
`cmd/canopyd/main.go:602-606` constructs it only when `METRICS_ENABLED` is `true`/`1`
(`internal/config/config.go`, default off). Both production entry points already set it:
`docker-compose.yml:77` (`METRICS_ENABLED: "true"`) and
`deploy/systemd/canopy-canopyd.service:54` (`Environment=METRICS_ENABLED=true`). The library is
the official `github.com/prometheus/client_golang v1.24.1` (`go.mod:14`). **No new dependency,
no new port, no new route.**

Three required changes to the existing exposure:

1. **`metrics` should default ON** for a daeon whose whole cost of exposure is one route. The
   current fail-off default means any deployment that forgets the env var is invisible. Keep the
   env var as an *opt-out* (`METRICS_ENABLED=false`) and document the flip in README's env table.
2. **Exempt `/metrics` from the rate limiter** — add it to `isPublicPath`
   (`internal/handler/auth.go:161-167`). A scraped endpoint must never be 429'd (D5).
3. **Keep the metrics middleware registered BEFORE the rate limiter** so a 429 is attributed to
   its route rather than vanishing. The current order already does this correctly —
   `telemetry.MetricsMiddleware` is registered at `internal/server/server.go:298-300`, the rate
   limiter at `:295`; verified in source. Do not regress it.

**Secondary: a textfile completion record for CLI lanes.** `cardexport`, `deploycheck`,
`canopy-deploy-check.timer` (`deploy/systemd/canopy-deploy-check.timer`) and the updater are
short-lived. They must **not** be scraped. The right answer is node_exporter's
`--collector.textfile.directory` (the fleet runs node/cAdvisor already): each run writes
`canopy_lane_last_success_timestamp_seconds` and `canopy_lane_runs_total` into a `.prom` file.
Do not build a pushgateway for this — it adds a component for two metrics.

**Not used: push/remote_write from canopyd.** It is a long-lived, scrape-able HTTP server on a
known port; push would duplicate scrape semantics for no benefit.

---

## 6. Collection path

- **Scrape target**: `canopy-server` / systemd `canopyd` on `HTTP_ADDR` (default `:8091`,
  `deploy/systemd/canopy-canopyd.service:52`).
- **Scrape job** (to be added by the monitoring lane, NOT by this repo):
  `job_name: canopy`, `metrics_path: /metrics`, `interval: 15s`, `timeout: 10s`,
  `static_configs: [{targets: ["<canopy-host>:8091"], labels: {product: "hermes-canopy", service: "canopyd", host: "<host>"}}]`.
- **Labels on every series**: `host`, `product=hermes-canopy`, `service=canopyd`, plus the
  target's `job=canopy`.
- **What the fleet agent already does**: nothing for this product. There is no `canopy*` entry
  in the Prometheus config and no agent-side push (`grep -rn "canopy" <monitoring>/prometheus`
  → no output). The scrape job is net-new.
- **Cardinality budget**: the route-pattern label (shipped as `path` since CAN-MET-01;
  OBS-1 will relabel it `route`) is bounded by the set of mounted chi patterns (order of
  ~130 route entries per the docs corpus; not machine-counted in this audit — see §9) plus one
  `unmatched` bucket; `status_class` (OBS-1) adds 5; `transport` adds ~5. Estimated ceiling on the order
  of a few thousand series worst case, realistically <300 because most routes see one method and
  one status class. This is an estimate and must be re-measured after OB-1 lands
  (`prometheus_tsdb_head_series` delta).

---

## 7. Verification — what a reviewer runs to prove each metric is live

Definition of **done** for any row below: the command in its `ACCEPTANCE:` clause returns the
named series with a **non-zero, moving** value, and the series carries
`product="hermes-canopy"`.

```bash
# 0. endpoint is up and serves product families (no auth needed)
curl -sf http://<canopy-host>:8091/metrics | grep -c '^canopy_'
# DONE: >= 3

# 1. build identity
curl -sf http://<canopy-host>:8091/metrics | grep '^canopy_build_info'
# DONE: one series, value 1, version/commit matching `canopyd version`

# 2. gateway reachability (the P0 page)
curl -sf http://<canopy-host>:8091/metrics | grep '^canopy_upstream_gateway_up '
# DONE: value 1. Negative control: stop the gateway -> value 0 within 15s,
# and canopy_upstream_gateway_check_errors_total increases.

# 3. request volume and error share (PromQL, in Grafana/explore)
#    (shipped labels are method/path/status; the status_class split lands with OBS-1)
sum(rate(canopy_http_requests_total[5m])) by (status)
# DONE: non-zero for 2xx on a served host; 5xx present only when errors actually occur.

# 4. latency SLO
histogram_quantile(0.95, sum(rate(canopy_http_request_duration_seconds_bucket[5m])) by (le))
# DONE: a bounded, non-empty value; alarm threshold set in §8.

# 5. no cardinality bomb (regression guard for D3)
curl -sf http://<canopy-host>:8091/api/v1/<random-garbage> -o /dev/null
curl -sf http://<canopy-host>:8091/metrics | grep 'canopy_http_unmatched_requests_total'
# DONE: the unmatched request lands in canopy_http_unmatched_requests_total{method=...},
# and NO series exists whose path label contains the literal garbage string
# (shipped label name is `path`; `route` arrives with the OBS-1 relabel).

# 6. tree/node gauges are real (regression guard for D2)
curl -sf http://<canopy-host>:8091/metrics | grep -E '^canopy_(tree|node)_count '
# DONE: value > 0 on any host that has data; and a query after creating one tree
# moves the gauge by exactly +1 within one refresh tick (30s).

# 7. transport/relay (P1)
sum(canopy_transport_connections) by (transport)
sum by (peer_class) (canopy_relay_queue_depth)
# DONE: connection count matches `GET /api/v1/transports/health` (JWT) at the same instant.

# 8. resume metric is wired, not dead (regression guard for D7)
rate(canopy_resume_started_total[1h]) > 0
# DONE: non-zero on a host with real users within one day; if it stays 0 while
# authenticated tree reads happen, the wiring is broken, not the users.

# 9. scrape health
up{job="canopy"} == 1
scrape_samples_scraped{job="canopy"}
# DONE: up==1 for 24h with no gaps > 2 intervals.
```

---

## 8. Alerting intent (for the monitoring lane — not implemented here)

> The `status_class` label in these expressions is OBS-1 scope; against the
> CAN-MET-01 wire substitute `status=~"5.."`.

| alert | expression | why it is the one that matters |
|---|---|---|
| `CanopyDown` | `up{job="canopy"} == 0` for 2m | process gone |
| `CanopyGatewayUnreachable` | `canopy_upstream_gateway_up == 0` for 5m | product is up but every AI action fails |
| `CanopyErrorRateHigh` | `sum(rate(canopy_http_requests_total{status_class="5xx"}[5m])) / sum(rate(canopy_http_requests_total[5m])) > 0.05` for 10m | user-visible failure |
| `CanopyLatencySLOBreach` | p95 `> 2s` for 10m | slow, not down |
| `CanopyRelayQueueBacklog` | `canopy_relay_queue_depth > 1000` for 10m | work queued and not draining |
| `CanopyDBPoolSaturated` | `canopy_db_pool_acquired / canopy_db_pool_total > 0.9` for 5m | imminent total stall |

---

## 9. What this audit could NOT verify

- **Scrape reachability from `fleet-monitor`** — no network scraping access was available; the
  statements about the absence of a scrape job are from grep of the monitoring tree on this host,
  not from a live Prometheus config API call.
- **Whether the Grafana dashboard in `deploy/grafana/dashboard.json` is loaded anywhere** — no
  dashboard mentioning `canopy` was found on the monitor host, but Grafana's own API was not
  queried.
- **The `resume_*` metric's correctness under real user traffic** — it read `0` at audit, which
  is consistent with either "no resumes yet" or "wired wrong". §7.8 is the probe that separates
  the two; it was not run (no authenticated session).
- **The exact set of chi route patterns** — the repo contains a route-parity test
  (`internal/server/route_parity_test.go`) but the dumped pattern list was not reproduced here,
  so the cardinality ceiling in §6 is an estimate, not a measurement.
- **Whether `metrics` middleware short-circuits before the rate limiter in the deployed binary** —
  verified in source (`internal/server/server.go:295` before `:299`), and consistent with the
  live dump (a `429`-capable ordering), but not exercised, because driving the limiter to
  saturation would have altered the live service's traffic.

---

## 10. Board rows filed from this spec

| row | priority | complexity | scope |
|---|---|---|---|
| `OBS-1` | P1 | 3 | minimal viable: `canopy_build_info`, `canopy_upstream_gateway_up`, unmatched-request counter, `status_class` on latency, real tree/node gauges, `/metrics` rate-limiter exemption, the `canopy` scrape job |
| `OBS-2` | P2 | 3 | transport/relay counters that already exist in memory (`internal/transport/metrics.go`, `internal/relay/hub.go:416`) |
| `OBS-3` | P2 | 2 | DB pool, federation queue backlog, rate-limit rejections |
| `OBS-4` | P3 | 2 | upstream-proxy / context-compile / export duration histograms |
| `OBS-5` | P3 | 2 | table row counts (allowlist) and relay TLS cert lifetime |
| `OBS-6` | P3 | 2 | CI guard: every declared metric must have a writer; no raw URL in a label |

Each row's `reasoning` carries the WHY / WHAT / `ACCEPTANCE:` clause, and each row's
`acceptance_criteria` field restates the acceptance as a numbered list of runnable commands and
PromQL expressions.

---

## 10. Board rows filed from this spec

| row | priority | complexity | scope |
|---|---|---|---|
| `OBS-1` | P1 | 3 | minimal viable: `canopy_build_info`, `canopy_upstream_gateway_up`, unmatched-request counter, `status_class` on latency, real tree/node gauges, `/metrics` rate-limiter exemption, the `canopy` scrape job |
| `OBS-2` | P2 | 3 | transport/relay counters that already exist in memory (`internal/transport/metrics.go:13-22`, `internal/relay/hub.go:416`) |
| `OBS-3` | P2 | 2 | DB pool, federation queue backlog, rate-limit rejections |
| `OBS-4` | P3 | 2 | upstream-proxy / context-compile / export duration histograms |
| `OBS-5` | P3 | 2 | table row counts (fixed allowlist) and relay TLS certificate lifetime |
| `OBS-6` | P3 | 2 | CI guard: every declared metric must have a writer; no raw URL in any label |

Each row's `reasoning` follows WHY -> WHAT -> `ACCEPTANCE:`, and each row's
`acceptance_criteria` field restates the acceptance as a numbered list of runnable curl/PromQL
commands. Every row carries `lane: monitoring` and points at this file.
