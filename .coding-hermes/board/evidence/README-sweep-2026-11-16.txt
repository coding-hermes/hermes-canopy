
===== ARM 0 — identities =====

--- CMD: git -C /home/kara/hermes-canopy rev-parse HEAD
5d1a631b46c5c14c146af19423d0137ef67c69b4
rc=0

--- CMD: git -C /home/kara/hermes-canopy rev-parse origin/master
5d1a631b46c5c14c146af19423d0137ef67c69b4
rc=0

--- CMD: git -C /home/kara/hermes-canopy rev-list --left-right --count HEAD...origin/master
0	0
rc=0

--- CMD: sha256sum README.md (local) vs git cat-file -p origin/master:README.md
8765345cc80f391f8fc54b44841ebe4d694506ee1431569ecf62ed6df5b24d67  /home/kara/hermes-canopy/README.md
rc=0
8765345cc80f391f8fc54b44841ebe4d694506ee1431569ecf62ed6df5b24d67  -
rc=0

--- CMD: git -C /home/kara/hermes-canopy rev-parse --show-toplevel ; remote -v
/home/kara/hermes-canopy
rc=0
gitlab	git@gitlab.readydedis.com:coding-hermes/hermes-canopy.git (fetch)
gitlab	git@gitlab.readydedis.com:coding-hermes/hermes-canopy.git (push)
origin	git@github.com:coding-hermes/hermes-canopy.git (fetch)
origin	git@github.com:coding-hermes/hermes-canopy.git (push)
rc=0

--- CMD: git -C /home/kara/hermes-canopy ls-remote --symref origin HEAD
ref: refs/heads/master	HEAD
5d1a631b46c5c14c146af19423d0137ef67c69b4	HEAD
rc=0

--- CMD: git -C /home/kara/.hermes/stand-in/readme/hermes-canopy/probe/wt rev-parse HEAD (probe worktree = published tip)
5d1a631b46c5c14c146af19423d0137ef67c69b4
rc=0

===== ARM 1 — release boundary =====

--- CMD: git -C /home/kara/hermes-canopy tag --sort=-creatordate | head -3
v0.2.0
v0.1.0
rc1
rc=0

--- CMD: git -C /home/kara/hermes-canopy log -1 --format=%ci %h <tag> (per tag)
v0.1.0 2026-09-21 07:23:52 -0500 19036154
rc=0
v0.2.0 2026-10-20 22:38:15 -0500 65663f2c
rc=0
rc1 2026-09-16 01:53:53 -0500 0df39007
rc=0

--- CMD: git -C /home/kara/hermes-canopy rev-list --count v0.2.0..HEAD
186
rc=0

--- CMD: git -C /home/kara/hermes-canopy rev-list --count --no-merges v0.2.0..HEAD
173
rc=0

--- CMD: gh release list -R coding-hermes/hermes-canopy --limit 10
v0.1.0 — Canopy OS	Latest	v0.1.0	2026-09-21T12:37:02Z
rc=0

--- CMD: gh release view v0.2.0 -R coding-hermes/hermes-canopy
release not found
rc=1

--- CMD: git -C /home/kara/hermes-canopy ls-remote --tags origin
0df39007c43fa71841e34a753e2bb0f38e8300f3	refs/tags/rc1
62ba3a369da798af93f90f5d366ec41ca459fd02	refs/tags/v0.1.0
190361540864e3efd3517de92c35e9bc1e1c531d	refs/tags/v0.1.0^{}
8a5fbaa86c0551a5f1d6a4c2571b80c76ba0b444	refs/tags/v0.2.0
65663f2c9551528f6fa2d32bc849da0bc6c1ff15	refs/tags/v0.2.0^{}
rc=0

--- CMD: git -C /home/kara/hermes-canopy ls-remote --heads origin | grep -c refs/heads/gh-pages
0
rc=1

--- CMD: head -3 CHANGELOG.md
# Changelog

## [v0.2.0] - 2026-10-21
rc=0

===== ARM 2 — documented URL claims =====

--- CMD: curl -sS -D - -o /dev/null https://releases.canopy.app/stable/latest.json
HTTP/2 302 
server: openresty
date: Mon, 16 Nov 2026 19:03:04 GMT
content-type: text/html
content-length: 142
location: https://www.godaddy.com/forsale/canopy.app
x-frame-options: sameorigin

rc=0

--- CMD: curl -sSL -o /dev/null -w '%{http_code}' https://releases.canopy.app/stable/latest.json
final=403 url=https://www.godaddy.com/forsale/canopy.app
rc=0

--- CMD: curl -sSIL https://go.dev/dl/go1.25.11.linux-amd64.tar.gz
go-tarball=200
rc=0

--- CMD: raw.githubusercontent README screenshot URLs
canopy-hero.jpg=200 bytes=105123
rc=0
canopy-tree-view.png=200 bytes=123839
rc=0
canopy-live-gateway.png=200 bytes=96048
rc=0

--- CMD: live /health /version /metrics on :8091
{"status":"ok","service":"canopyd","schema_version":49,"embedded_migrations":49,"relay":{"mode":"air_gapped","status":"disabled","sessions":0},"version":"v0.2.0-172-g470bb96f-dirty","commit":"470bb96f","build_time":"2026-11-04T13:52:25Z"} rc=0
{"version":"v0.2.0-172-g470bb96f-dirty","commit":"470bb96f","build_time":"2026-11-04T13:52:25Z"}
 rc=0
metrics=200
rc=0

===== ARM 3 — built binary: version, help, config check, token mint/verify =====

--- CMD: /home/kara/.hermes/stand-in/readme/hermes-canopy/probe/wt/bin/canopyd -version
v0.2.0-186-g5d1a631b commit=5d1a631b built=2026-11-16T18:41:40Z
rc=0

--- CMD: /home/kara/.hermes/stand-in/readme/hermes-canopy/probe/wt/bin/canopyd --help
Usage: canopyd [serve] [flags]

Server mode (default). Configuration is environment-based — see
docs/INTEGRATION.md §4 and README "Environment Variables".

Subcommands:
  tree create <name> [--content <text>]  Create a new tree (root message required)
  tree list                 List all trees
  tree delete <id>          Delete a tree
  tree navigate <id>        Print tree structure as indented text
  session browse [flags]    List sessions from the Hermes state snapshot (6-hourly, read-only)
  session import [flags]    Import Hermes sessions from the state snapshot into trees
  session associations-backfill [flags]  Recompute association metadata for imported sessions
  topic <subcmd> [flags]    Topic detection: detect, proposals, config
  card export [flags]       Export the local card stores as deterministic JSONL (docs/CARD_EXPORT.md)
  db export-sqlite [flags] Copy core graph data from PostgreSQL into a new SQLite file
  token mint|verify [flags] Mint or verify a dev HS256 JWT (JWT_SECRET)
  config check [flags]      Validate configuration and print resolved values (no boot)
  context-accuracy [flags] Measure ancestry/topic selection accuracy
  serve [flags]             Start the API server (default mode; env-only config)

The tree/session/topic subcommands are HTTP clients of a running canopyd;
they target CANOPY_SERVER_URL (default http://localhost:8091).

Flags:
  -version                print version and exit
  -relay-mode             air_gapped, self_hosted, or saas (default air_gapped)
  -relay-listen           relay listener address
  -relay-connect          upstream relay address
  -max-relay-sessions     maximum concurrent sessions
  -relay-heartbeat        heartbeat interval
  -relay-drain-timeout    graceful drain timeout
  -relay-tls-enabled      enable TLS for relay connections
  -relay-tls-cert         TLS certificate file path
  -relay-tls-key          TLS key file path
  -relay-tls-ca           TLS CA certificate file path
  -relay-tls-mutual       require mutual TLS
  -update-interval        signed update check interval (0 disables update checks)
  -channel                update channel: stable, beta, or nightly
  -update-manifest-url    signed update manifest URL (env CANOPY_UPDATE_MANIFEST_URL)
  -update-public-key-file path to a hex-encoded Ed25519 public key (env CANOPY_UPDATE_PUBLIC_KEY_FILE)
  -h, --help  print this help and exit

Key environment variables:
  HTTP_ADDR       listen address (default :8080)
  CANOPY_DB_URL   postgres:// DSN (overrides all DB_* fields)
  CANOPY_DB_DRIVER postgres (default) or sqlite
  CANOPY_SQLITE_PATH SQLite file path (default ~/.canopy/canopy.sqlite)
  DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME, DB_SCHEMA, DB_SSLMODE
  JWT_SECRET      HS256 signing secret (default dev-secret-change-me)
  LOG_LEVEL, LOG_FORMAT, METRICS_ENABLED, CORS_ORIGIN
  CANOPY_UPDATE_MANIFEST_URL update manifest URL (overrides the built-in default)
  CANOPY_UPDATE_PUBLIC_KEY_FILE path to a hex-encoded Ed25519 public key (overrides the embedded key)
rc=0

--- CMD: /home/kara/.hermes/stand-in/readme/hermes-canopy/probe/wt/bin/canopyd config check
config: resolved (no server boot, no database opened)
driver=postgres
DB target: postgres host=localhost port=5432 database=canopy (built-in default; no DB_PORT / CANOPY_DB_URL set)
WARNING: port 5432 is NOT the documented development port: the compose stack, Makefile and every project script use :5437 (docker-compose.yml maps 5437:5432). Set DB_PORT=5437 or CANOPY_DB_URL to select that instance.
env=dev production=false http_addr=:8080
log_level=info log_format=text metrics_enabled=false
cors_origin=(disabled — no cross-origin requests admitted; set CORS_ORIGIN to opt in)
jwt_secret=dev-default (INSECURE outside local dev — set JWT_SECRET; `canopyd token mint` signs with it)
serve=allowed (configuration accepted)
rc=0

--- CMD: token mint -> token verify
minted_len=223
valid sub=00000000-0000-0000-0000-000000000001 exp=2026-11-17T19:03:07Z expires_in=24h0m0s
rc=0

--- CMD: token verify on a malformed token
invalid: token verify: token is malformed: could not JSON decode header: invalid character '\u009e' looking for beginning of value
rc=1

===== ARM 4 — README auth snippets produce a token the binary accepts =====

--- CMD: README python3 snippet (lines 263-274) -> token verify
py_len=188
valid sub=00000000-0000-0000-0000-000000000001 exp=2026-11-17T19:03:07Z expires_in=24h0m0s
rc=0

--- CMD: README openssl snippet (lines 277-281) -> token verify
valid sub=00000000-0000-0000-0000-000000000001 exp=2026-11-17T19:03:07Z expires_in=24h0m0s
rc=0

--- CMD: README node snippet (lines 284-294) -> token verify
node_len=188
valid sub=00000000-0000-0000-0000-000000000001 exp=2026-11-17T19:03:07Z expires_in=24h0m0s
rc=0

===== ARM 5 — session subcommand contract (README §Hermes session source) =====

--- CMD: /home/kara/.hermes/stand-in/readme/hermes-canopy/probe/wt/bin/canopyd session browse (no snapshot dir on this host)
rc=1
Error: session: snapshot directory unavailable: /home/kara/.hermes/state-backups does not exist. It is populated by a scheduled Hermes state-backup job (typically ~/.hermes/scripts/backup-state.sh or backup-tiered.sh), which writes state_<Y

--- CMD: /home/kara/.hermes/stand-in/readme/hermes-canopy/probe/wt/bin/canopyd session import --limit 5 --dry-run
rc=1
Error: session: snapshot directory unavailable: /home/kara/.hermes/state-backups does not exist. It is populated by a scheduled Hermes state-backup job (typically ~/.hermes/scripts/backup-state.sh or backup-tiered.sh), which writes state_<Y

===== ARM 6 — deploy-timer pre-flight (README lines 1132-1136) =====

--- CMD: CANOPYD_UNIT_INSTALL_DIR=/var/tmp/hermes-scratch/tmp.FyPGcQp2td bash scripts/install-deploy-timer.sh
rendered: /var/tmp/hermes-scratch/tmp.FyPGcQp2td/canopy-deploy-check.service
rendered: /var/tmp/hermes-scratch/tmp.FyPGcQp2td/canopy-deploy-check.timer
/home/kara/.config/systemd/user/hermes-gateway.service.d/provider-env.conf:2: Unknown key 'Comment' in section [Unit], ignoring.
/home/kara/.config/systemd/user/eduos-ttl-heartbeat.service:3: Invalid URL, ignoring: Stopgap
/home/kara/.config/systemd/user/eduos-ttl-heartbeat.service:3: Invalid URL, ignoring: for
/home/kara/.config/systemd/user/eduos-ttl-heartbeat.service:3: Invalid URL, ignoring: bunkerd
/home/kara/.config/systemd/user/eduos-ttl-heartbeat.service:3: Invalid URL, ignoring: 0.1.2
/home/kara/.config/systemd/user/eduos-ttl-heartbeat.service:3: Invalid URL, ignoring: TTL-shrink
/home/kara/.config/systemd/user/eduos-ttl-heartbeat.service:3: Invalid URL, ignoring: behavior
/home/kara/.config/systemd/user/eduos-ttl-heartbeat.service:3: Invalid URL, ignoring: (see
/home/kara/.config/systemd/user/eduos-ttl-heartbeat.service:3: Invalid URL, ignoring: DF-BUNKER-35)
/home/kara/.config/systemd/user/duckbrain-http.service.d/10-restart-always.conf:6: Unknown key 'StartLimitIntervalSec' in section [Service], ignoring.
/usr/lib/systemd/user/spice-vdagent.service:23: Unknown key 'StandardError' in section [Install], ignoring.
dry run: CANOPYD_UNIT_INSTALL_DIR set — skipping daemon-reload/enable
install_rc=0

--- CMD: systemd-analyze --user verify <rendered units>
verify_rc=0
/usr/lib/systemd/user/spice-vdagent.service:23: Unknown key 'StandardError' in section [Install], ignoring.

--- CMD: systemctl --user enable --dry-run <rendered timer>
dryrun_rc=1
Failed to enable unit: File '/home/kara/.config/systemd/user/canopy-deploy-check.timer' already exists

===== ARM 7 — reader-facing files: existence, markers, links, anchors =====

--- CMD: referenced files exist
OK   .env.example
OK   deploy/reference-proxy.py
OK   deploy/nginx.canopy.conf
OK   deploy/Caddyfile
OK   scripts/scratch-instance.sh
OK   scripts/check-deploy-staleness.sh
OK   scripts/install-deploy-timer.sh
OK   scripts/smoke-gateway.sh
OK   docs/traceability-doctrine.md
OK   docs/README.md
OK   docs/API.md
OK   docs/INTEGRATION.md
OK   docs/SCRATCH_INSTANCE.md
OK   docs/SQLITE-RUNTIME.md
OK   docs/CARD_EXPORT.md
OK   docs/DESKTOP-WAILS.md
OK   docs/E2E-EVIDENCE.md
OK   docs/ENV.md
OK   deploy/grafana/dashboard.json
OK   internal/updater/release_public_key.txt
OK   cmd/release-tool
OK   LICENSE

--- CMD: ch:trace markers committed inside README.md
934:`ch:trace row=DF-HERMES-CANOPY-72 spec=specs/SPEC-FTR-06.md#48 wave=none:serial test=internal/updater/updater_test.go::TestDefaultManifestURL doc=README.md prompt=/tmp/hermes-canopy-2026-10-19-22-09-14-DF-HERMES-CANOPY-72-rework.txt evidence=scripts/validate-release-manifest-publication.py witness=none:worker-local-unpublished verdict=verdict:a44b1cc7:prior-incomplete commit=see-final-worker-report memory=/project/hermes-canopy/foreman-tick-2026-10-19`
1280:<!-- ch:trace row=README-16 spec=none:no-feature-spec wave=none:serial-worktree-hermes-canopy-2026-10-26-06-37-35 test=Makefile:docs-check+docs-check-test;cmd/canopyd/context_accuracy_cmd.go::runContextAccuracyCmd doc=README.md prompt=/tmp/hermes-canopy-2026-10-26-06-37-35-README-16-brief.txt evidence=README.md witness=path@origin:README.md verdict=none:post-merge-rejudge-pending commit=9d9396d5efe16ff925fc77e91d2aa99314d2cf8b memory=none:not-written -->
rc=0

--- CMD: worker closeout HTML comments committed inside README.md
1281:<!-- STATUS: complete -->
1282:<!-- TIER: T3; exact CLI help, README grep, make build, docs-check, docs-check-test observed -->
1283:<!-- EVIDENCE: README.md; docs-check output; docs-check-test output -->
rc=0

--- CMD: anchor checker (skill script) over README.md at repo root
BROKEN /home/kara/hermes-canopy/README.md:472: #references
anchors_checked=12 broken=1
rc=1

--- CMD: is the flagged '#references' token a markdown link anywhere?
rc=1 (1 = no link form; the checker hit is a table cell)

===== ARM 8 — Makefile target coverage vs README table =====

--- CMD: all targets defined in Makefile
all
build
build-embed
build-embed-darwin-amd64
build-embed-darwin-arm64
build-embed-linux-amd64
build-embed-linux-arm64
build-embed-windows-amd64
build-ui
build-wails
clean
deploy
docker
docs-check
docs-check-test
install-deploy-timer
lint
run
test
test-chaos-disconnect
test-proxy
test-short
test-wails-desktop-contract
tidy
validate-release-manifest-publication
validate-wails-desktop
vet

--- CMD: targets listed in README 'Makefile Targets' table (lines 1140-1155)
build
deploy
install-deploy-timer
run
test
test-short
test-chaos-disconnect
vet
lint
validate-wails-desktop
test-wails-desktop-contract
build-wails
clean
docker

--- CMD: counts
makefile_targets=27
readme_table_targets=14
undocumented_targets=all build-embed build-embed-darwin-amd64 build-embed-darwin-arm64 build-embed-linux-amd64 build-embed-linux-arm64 build-embed-windows-amd64 build-ui docs-check docs-check-test test-proxy tidy validate-release-manifest-publication 

===== ARM 9 — .env.example vs canonical docs/ENV.md =====

--- CMD: keys read by FromEnv helpers in internal/config/config.go
API_SERVER_KEY
CANOPY_DB_DRIVER
CANOPY_DB_URL
CANOPY_ENV
CANOPY_FILE_ROOT
CANOPY_NATS_CREDS
CANOPY_NATS_URL
CANOPY_PROFILE_ROUTER_SECRET
CANOPY_SQLITE_PATH
CANOPY_TRUSTED_PROXIES
CONTEXT_MODEL_WINDOWS
CORS_ORIGIN
DB_HOST
DB_NAME
DB_PASSWORD
DB_SCHEMA
DB_SSLMODE
DB_USER
HERMES_WEBUI_GATEWAY_API_KEY
HERMES_WEBUI_GATEWAY_BASE_URL
HTTP_ADDR
JWT_SECRET
LOG_FORMAT
LOG_LEVEL
METRICS_ENABLED
REFERENCE_SELECTION_SECRET

--- CMD: .env.example self-description (line 5)
# Canonical complete reference: docs/ENV.md (keep this example in sync).

--- CMD: helper-read keys absent from .env.example
MISSING-IN-ENVEXAMPLE CANOPY_FILE_ROOT
MISSING-IN-ENVEXAMPLE CONTEXT_MODEL_WINDOWS

--- CMD: those same keys in docs/ENV.md
50:Numeric knobs accept an integer; out-of-range or non-numeric values silently keep the default (except `CONTEXT_MODEL_WINDOWS`, which fails startup loudly on a malformed entry).
59:| `CONTEXT_MODEL_WINDOWS` | Locally declared context windows for models the gateway reports no window for. Format: comma-separated `model=window` pairs, e.g. `CONTEXT_MODEL_WINDOWS="Hermes Agent=200000,probe-big=128000"` (a model id may contain `=`; the last `=` separates). Later pairs replace earlier ones; a window the gateway reports always wins. A malformed pair is a **startup error**. | unset (no overrides) | internal/config/config.go:440 (`FromEnv()`; parsed in `parseContextModelWindows()`) |
94:| `CANOPY_FILE_ROOT` | Root directory for uploaded file bytes (SPEC-PL-02 §7.6); objects land at `<root>/files/<aa>/<sha256>`. | `~/.canopy/files` | internal/config/config.go:522 (`FromEnv()`) |

--- CMD: helper-read keys absent from README.md

===== ARM 10 — docs gate scope =====

--- CMD: make docs-check (in probe worktree)
make: Entering directory '/home/kara/.hermes/stand-in/readme/hermes-canopy/probe/wt'
bash scripts/docs-check.sh
docs-check: PASS (18 docs pages, 17 canopyd commands, 14 Makefile targets, 37 environment variables)
make: Leaving directory '/home/kara/.hermes/stand-in/readme/hermes-canopy/probe/wt'
rc=0

--- CMD: make docs-check-test (in probe worktree)
docs-check: FAIL: README.md must link the canonical documentation index exactly once (found 0)
docs-check-test: missing README index link FAIL (expected)
docs-check-test: unknown trailing subcommand FAIL names lst (expected)
docs-check-test: blockquoted fence FAIL names --sqlite-out (expected)
docs-check-test: blockquoted ghost env FAIL names CANOPY_GHOST_FENCE (expected)
docs-check-test: exact environment name FAIL names CANOPY_DB (expected)
docs-check-test: broken relative docs link FAIL names target (expected)
docs-check: PASS (18 docs pages, 17 canopyd commands, 14 Makefile targets, 37 environment variables)
docs-check-test: clean PASS after all negative arms
make: Leaving directory '/home/kara/.hermes/stand-in/readme/hermes-canopy/probe/wt'
rc=0

--- CMD: does the docs gate read .env.example at all?
0
rc=1

===== END OF TRANSCRIPT =====

===== ARM 11 — expensive arms executed this sweep (logs captured live) =====

--- CMD: make -C /home/kara/.hermes/stand-in/readme/hermes-canopy/probe/wt build (re-run; incremental, confirms the documented build target)
make: Entering directory '/home/kara/.hermes/stand-in/readme/hermes-canopy/probe/wt'
go build -ldflags="-X main.version=v0.2.0-186-g5d1a631b -X main.commit=5d1a631b -X main.buildTime=2026-11-16T19:03:12Z" -o bin/canopyd ./cmd/canopyd
make: Leaving directory '/home/kara/.hermes/stand-in/readme/hermes-canopy/probe/wt'
rc=0
binary: -rwxrwxr-x 1 kara kara 44262150 Nov 16 14:03 /home/kara/.hermes/stand-in/readme/hermes-canopy/probe/wt/bin/canopyd
binary_sha256: f835e6da5ac0e7303d0169a2009705f2f1a547b3ffa5b2600416bd7b3f90f5a6
binary_version: v0.2.0-186-g5d1a631b commit=5d1a631b built=2026-11-16T19:03:12Z

--- CMD: bash scripts/scratch-instance.sh (documented, CANOPY_SCRATCH_PORT=8094; full teardown)
log: /home/kara/.hermes/stand-in/readme/hermes-canopy/probe/probe/scratch-instance-run.log sha256=797e7053603aebd8a3e08a4b562d31d52782c7518d7e81846883ec3147a46579
scratch instance (DF-HERMES-CANOPY-8)
  repo:         /home/kara/.hermes/stand-in/readme/hermes-canopy/probe/wt
  HEAD:         5d1a631b
  database:     canopy_scratch_473_d3437181 (PostgreSQL 127.0.0.1:5437)
  api:          http://127.0.0.1:8094 (loopback only)
  scratch root: /tmp/canopy-scratch-473-TSD2ft
  HOME:         /tmp/canopy-scratch-473-TSD2ft/home  (card store + gateway registry)
  file root:    /tmp/canopy-scratch-473-TSD2ft/file-root
  gateway:      http://127.0.0.1:9 (closed port — scratch never submits a live run)

== preflight
port 8094 free
PostgreSQL ready
database name canopy_scratch_473_d3437181 unused
live instance: pid=532795 health={"status":"ok","service":"canopyd","schema_version":49,"embedded_migrations":49,"relay":{"mode":"air_gapped","status":"disabled","sessions":0},"version":"v0.2.0-172-g470bb96f-dirty","commit":"470bb96f","build_time":"2026-11-04T13:52:25Z"}

== build from HEAD
built version dev commit=unknown built=unknown

== create database canopy_scratch_473_d3437181
created (the scratch server runs the embedded migrations on first boot)

== start scratch server
pid 3635353 → log /tmp/canopy-scratch-473-TSD2ft/canopyd.log

== readiness
health: status=ok schema_version=49 embedded_migrations=49
schema matches embedded migrations (no STALE BUILD)
scratch server log confirms its own file root: /tmp/canopy-scratch-473-TSD2ft/file-root
minted dev JWT (not printed)

== CLI create/list against the scratch API
    Tree created successfully.
      ID:          63dbbb0b-000f-4844-b1c8-b103aef05dfb
      Title:       scratch-473-135352
      Root Node:   01a1e5b5-e268-75e1-87cb-f743cefb3c00
created tree 63dbbb0b-000f-4844-b1c8-b103aef05dfb (root node 01a1e5b5-e268-75e1-87cb-f743cefb3c00)
CLI tree list shows the created tree
negative control: CLI refused without CANOPY_SERVER_URL (exit 1, no request sent)

== authenticated API + isolation checks
scratch API: 1 tree(s), includes 63dbbb0b-000f-4844-b1c8-b103aef05dfb
live instance does NOT see the scratch tree (writes stayed in canopy_scratch_473_d3437181)
scratch API rejects unauthenticated reads (401)

== fresh non-graph stores
card create 201; card store is scratch-local: /tmp/canopy-scratch-473-TSD2ft/home/.hermes/canopy/cards (2 db file(s))
gateway run registry path for this instance: /tmp/canopy-scratch-473-TSD2ft/home/.hermes/canopy/gateway/runs-8094.jsonl
scratch /api/v1/gateway/status → HTTP 200 (endpoint is http://127.0.0.1:9 — no live run submitted)

== checks
PASS: build, readiness, schema match, auth, create/list, refusal control, live non-contamination
next: stopping the scratch server and dropping only canopy_scratch_473_d3437181

== cleanup
dropped database: canopy_scratch_473_d3437181
removed root:     /tmp/canopy-scratch-473-TSD2ft

== post-cleanup verification
OK: database canopy_scratch_473_d3437181 dropped
OK: scratch root removed
OK: port 8094 released
OK: live /health unchanged ({"status":"ok","service":"canopyd","schema_version":49,"embedded_migrations":49,"relay":{"mode":"air_gapped","status":"disabled","sessions":0},"version":"v0.2.0-172-g470bb96f-dirty","commit":"470bb96f","build_time":"2026-11-04T13:52:25Z"})
OK: live canopyd pid unchanged (532795) — never restarted
OK: PostgreSQL database list unchanged (no live DB created/dropped/modified)
OK: shared $HOME stores unchanged (cards/, files/)
EXIT=0

--- CMD: make vet / make docs-check / make docs-check-test (captured)
log: /home/kara/.hermes/stand-in/readme/hermes-canopy/probe/probe/make-gates.log sha256=369f2e406c9ae96a39af5845a85a38f389e730a294f785c825b847bdfab6ae3f
## make vet
go vet ./...
go: downloading github.com/go-viper/mapstructure/v2 v2.2.1
go: downloading golang.org/x/exp v0.0.0-20250128182459-e0ece0dbea4c
vet_rc=0
## make docs-check
bash scripts/docs-check.sh
docs-check: PASS (18 docs pages, 17 canopyd commands, 14 Makefile targets, 37 environment variables)
docs_check_rc=0
## make docs-check-test
docs-check-test: missing indexed page FAIL (expected)
docs-check: FAIL: README.md must link the canonical documentation index exactly once (found 0)
docs-check-test: missing README index link FAIL (expected)
docs-check-test: unknown trailing subcommand FAIL names lst (expected)
docs-check-test: blockquoted fence FAIL names --sqlite-out (expected)
docs-check-test: blockquoted ghost env FAIL names CANOPY_GHOST_FENCE (expected)
docs-check-test: exact environment name FAIL names CANOPY_DB (expected)
docs-check-test: broken relative docs link FAIL names target (expected)
docs-check: PASS (18 docs pages, 17 canopyd commands, 14 Makefile targets, 37 environment variables)
docs-check-test: clean PASS after all negative arms
docs_check_test_rc=0
DONE

--- CMD: make test-short (Go unit suite, captured)
log: /home/kara/.hermes/stand-in/readme/hermes-canopy/probe/probe/test-short.log sha256=7e65cc3ce4cdb0d4eec11b8e0702179dae5fc9e7c7dcfb93692981fa2348206f
## make test-short
ok  	github.com/coding-hermes/hermes-canopy/internal/deploycheck	0.037s
ok  	github.com/coding-hermes/hermes-canopy/internal/dist	0.034s
ok  	github.com/coding-hermes/hermes-canopy/internal/federation	0.025s
ok  	github.com/coding-hermes/hermes-canopy/internal/fileviewer	0.023s
ok  	github.com/coding-hermes/hermes-canopy/internal/gateway	0.415s
ok  	github.com/coding-hermes/hermes-canopy/internal/handler	8.332s
ok  	github.com/coding-hermes/hermes-canopy/internal/hermes	1.334s
ok  	github.com/coding-hermes/hermes-canopy/internal/mls	0.018s
ok  	github.com/coding-hermes/hermes-canopy/internal/plugin	0.037s
ok  	github.com/coding-hermes/hermes-canopy/internal/reference	0.009s
ok  	github.com/coding-hermes/hermes-canopy/internal/relay	2.214s
ok  	github.com/coding-hermes/hermes-canopy/internal/retrieval	0.007s
ok  	github.com/coding-hermes/hermes-canopy/internal/search	0.026s
ok  	github.com/coding-hermes/hermes-canopy/internal/server	1.896s
ok  	github.com/coding-hermes/hermes-canopy/internal/service	0.062s
ok  	github.com/coding-hermes/hermes-canopy/internal/session	7.904s
ok  	github.com/coding-hermes/hermes-canopy/internal/sse	6.659s
ok  	github.com/coding-hermes/hermes-canopy/internal/sync	0.013s
ok  	github.com/coding-hermes/hermes-canopy/internal/telemetry	0.068s
ok  	github.com/coding-hermes/hermes-canopy/internal/testutil	0.007s
ok  	github.com/coding-hermes/hermes-canopy/internal/transport	10.446s
ok  	github.com/coding-hermes/hermes-canopy/internal/updater	0.027s
ok  	github.com/coding-hermes/hermes-canopy/internal/wailsbridge	0.966s
ok  	github.com/coding-hermes/hermes-canopy/internal/webui	0.017s
ok  	github.com/coding-hermes/hermes-canopy/migrations	0.083s
test_short_rc=0
DONE

--- CMD: hilo graph warm (README §Code Quality), run in the probe worktree
8162
vfs_state: coverage.json edges.jsonl graph.db 

===== END OF EXPENSIVE ARMS =====
