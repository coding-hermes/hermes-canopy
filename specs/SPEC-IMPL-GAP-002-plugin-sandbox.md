# SPEC-IMPL-GAP-002 — Plugin Sandbox Implementation Spec

> **Task:** GAP-002 — Plugin sandbox (Critical, Cpx 5)
> **Status:** Implementation-ready (this spec)
> **Sources:** AGENTS.md Core Concepts §4 + Plugin Sandbox (§Architecture), SPEC-PL-01 §2/§3/§4/§7/§9/§10/§12, DuckBrain architecture/plugin-spec-complete (2026-07-22 final decisions)
> **Worker target:** A worker reading this spec must produce correct, compilable code with zero clarifying questions.

## 1. Purpose

Create `internal/plugin/` — the backend half of Canopy's plugin sandbox. It registers plugins (manifest-as-comment-block + source JS), stores them in PostgreSQL, enforces capability-scoped permissions on every API call, and serves plugin source + sandbox bootstrap to the frontend. The frontend renders each plugin in a sandboxed iframe (`sandbox="allow-scripts"` + CSP) with a `postMessage`-based `canopy` API shim — this spec covers the backend package plus the minimal frontend sandbox component. This is the "Sandboxed iframes + CSP + capability-scoped APIs" MVP promise from AGENTS.md.

**MVP scope boundary (from AGENTS.md "Deferred"):** Versioning/rollback, hot-reload SSE, plugin marketplace UI, arbitrary JS plugins from the internet, calendar/network API implementations are POST-MVP. This task delivers: register/list/source/install + sandbox host + permission gate. The DB schema (SPEC-PL-01 §3) is implemented in full — it is the foundation for post-MVP features — but only the MVP endpoints are wired.

## 2. Interface (exact — worker copies these)

```go
package plugin

// Permission is a capability granted to a plugin at install time.
type Permission string

const (
    PermissionDataRead       Permission = "data_read"
    PermissionDataWrite      Permission = "data_write"
    PermissionNotification   Permission = "notification"
    PermissionCalendarRead   Permission = "calendar_read"
    PermissionCalendarWrite  Permission = "calendar_write"
    PermissionNetworkRequest Permission = "network_request"
)

// AllPermissions is the canonical set a plugin may declare.
var AllPermissions = []Permission{ /* all 6 above */ }

func ValidPermission(p Permission) bool

// PluginStatus is the lifecycle status of a plugin_registry row.
type PluginStatus string // "active" | "disabled" | "archived"

// PluginRenderType determines UI mounting.
type PluginRenderType string // "card" | "embed" | "background"

// PluginManifest is the parsed manifest embedded in the plugin source header.
type PluginManifest struct {
    Name         string           `json:"name"`
    Version      string           `json:"version"`       // semver
    Description  string           `json:"description"`
    Permissions  []Permission     `json:"permissions"`
    RenderType   PluginRenderType `json:"render_type"`
    EntryPoint   string           `json:"entry_point"`   // e.g. "main"
    IconURL      string           `json:"icon_url,omitempty"`
    AuthorName   string           `json:"author_name,omitempty"`
    MinCanopyVer string           `json:"min_canopy_version,omitempty"`
}

// Plugin is a row in plugin_registry.
type Plugin struct {
    ID              uuid.UUID     `db:"id"                  json:"id"`
    Name            string        `db:"name"                json:"name"`
    Slug            string        `db:"slug"                json:"slug"`
    Version         string        `db:"version"             json:"version"`
    Description     string        `db:"description"         json:"description"`
    AuthorProfileID uuid.UUID     `db:"author_profile_id"   json:"authorProfileId"`
    Permissions     []Permission  `db:"permissions"         json:"permissions"`
    ManifestJSON    []byte        `db:"manifest_json"       json:"manifest"`
    SourceJS        string        `db:"source_js"           json:"-"`
    SourceSHA256    string        `db:"source_sha256"       json:"sourceSha256"`
    SourceByteSize  int           `db:"source_byte_size"    json:"sourceByteSize"`
    IconURL         string        `db:"icon_url"            json:"iconUrl"`
    Status          PluginStatus  `db:"status"              json:"status"`
    CreatedAt       time.Time     `db:"created_at"          json:"createdAt"`
    UpdatedAt       time.Time     `db:"updated_at"          json:"updatedAt"`
}

// PluginInstance is a per-tree/per-user install of a plugin.
type PluginInstance struct {
    ID                  uuid.UUID   `db:"id"                  json:"id"`
    PluginID            uuid.UUID   `db:"plugin_id"           json:"pluginId"`
    TreeID              *uuid.UUID  `db:"tree_id"             json:"treeId,omitempty"`
    ProfileID           uuid.UUID   `db:"profile_id"          json:"profileId"`
    InstanceName        string      `db:"instance_name"       json:"instanceName"`
    Settings            []byte      `db:"settings"            json:"settings"`
    GrantedPermissions  []Permission `db:"granted_permissions" json:"grantedPermissions"`
    Status              string      `db:"status"              json:"status"` // "active" | "paused" | "uninstalled"
    InvokeCount         int         `db:"invoke_count"        json:"invokeCount"`
    CreatedAt           time.Time   `db:"created_at"          json:"createdAt"`
}

// ---- Repo interface (implemented with pgx; pattern: internal/db/PGNodeRepo) ----

type Repo interface {
    Register(ctx context.Context, p *Plugin) (*Plugin, error)
    GetByID(ctx context.Context, id uuid.UUID) (*Plugin, error)
    GetActiveByName(ctx context.Context, name string) (*Plugin, error) // status='active'
    List(ctx context.Context, limit, offset int) ([]Plugin, int, error)
    Install(ctx context.Context, inst *PluginInstance) (*PluginInstance, error)
    GetInstance(ctx context.Context, id uuid.UUID) (*PluginInstance, error)
    ListInstances(ctx context.Context, profileID uuid.UUID, treeID *uuid.UUID) ([]PluginInstance, error)
    UpdateInstanceStatus(ctx context.Context, id uuid.UUID, status string) error
    IncrementInvokeCount(ctx context.Context, id uuid.UUID) error
}

// ---- Service ----

type Service interface {
    Register(ctx context.Context, manifest PluginManifest, sourceJS string, authorID uuid.UUID) (*Plugin, error)
    Get(ctx context.Context, id uuid.UUID) (*Plugin, error)
    List(ctx context.Context, limit, offset int) ([]Plugin, int, error)
    Install(ctx context.Context, pluginID uuid.UUID, treeID *uuid.UUID, profileID uuid.UUID, granted []Permission) (*PluginInstance, error)
    // CheckPermission validates an api_call from a plugin instance. Returns
    // the resolved method result via dispatch, or a typed error.
    CheckPermission(ctx context.Context, instanceID uuid.UUID, method string) error
    GetSource(ctx context.Context, id uuid.UUID) (string, string, error) // (sourceJS, sha256)
}
```

**Manifest extraction rule (SPEC-PL-01 §5.3):** The manifest lives as a comment block at the TOP of the plugin JS file:

```javascript
/**
 * @canopy-manifest
 * {
 *   "name": "csv-viewer",
 *   "version": "1.0.0",
 *   "description": "View CSV cards",
 *   "permissions": ["data_read"],
 *   "render_type": "card",
 *   "entry_point": "main"
 * }
 * @end-canopy-manifest
 */
```

`ParseManifest(source string) (*PluginManifest, error)` must: find the `@canopy-manifest` ... `@end-canopy-manifest` block, extract the JSON, validate required fields (name, version semver, description, permissions ⊆ AllPermissions, render_type ∈ {card,embed,background}, entry_point non-empty). Reject malformed JSON with `ErrInvalidManifest`, missing fields with `ErrManifestValidationFailed`.

**Permission gate (SPEC-PL-01 §7.4/§7.5):**

```go
// MethodToPermission maps an API method to its required permission.
// Returns "" for unknown methods.
func MethodToPermission(method string) Permission

// table:
// data.query        -> data_read
// data.mutate       -> data_write
// notify            -> notification
// calendar.query    -> calendar_read
// calendar.create   -> calendar_write
// network.fetch     -> network_request
```

**Sandbox bootstrap builder (SPEC-PL-01 §7.1/§7.3):**

```go
// BuildSrcDoc renders the sandboxed iframe document for a plugin.
// nonce is a per-session random hex string; parentOrigin is the host origin.
func BuildSrcDoc(p *Plugin, instanceID uuid.UUID, nonce, parentOrigin string) string
```

Must produce a `<!doctype html>` doc with:
- `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src none; img-src data: https:; font-src data:;">`
- `#root` div
- The `canopy` shim (SPEC-PL-01 §7.3 verbatim, with PLUGIN_ID/INSTANCE_ID/NONCE/PARENT_ORIGIN/ENTRY_POINT substituted)
- The plugin source evaluated in an IIFE after the shim

## 3. Data Model

Three tables from SPEC-PL-01 §3, verbatim DDL (renumbered to fit this repo):

- `migrations/000022_plugin_registry.up.sql` + `.down.sql` — SPEC-PL-01 §3.1 (plugin_registry table + 5 indexes + constraints + partial unique index on active)
- `migrations/000023_plugin_instances.up.sql` + `.down.sql` — SPEC-PL-01 §3.2 (plugin_instances + unique install index)
- `migrations/000024_plugin_audit_log.up.sql` + `.down.sql` — SPEC-PL-01 §3.3 (plugin_audit_log + indexes)

Copy the DDL from SPEC-PL-01 §3.1-3.4 EXACTLY, changing only the migration number prefix (000080→000022, 000081→000023, 000082→000024). Migrations are auto-embedded via `//go:embed *.sql` — no registration needed.

## 4. Wiring

### 4.1 Config (internal/config/config.go)

```go
// Plugin sandbox
PluginMaxSize int // PLUGIN_MAX_SIZE, default 1048576 (1MB)
```

`FromEnv()` reads `PLUGIN_MAX_SIZE` with that default; negative → error at startup; zero → default.

### 4.2 HTTP routes (internal/server/server.go)

```go
pluginSvc := plugin.NewService(pluginRepo, cfg.PluginMaxSize)
r.Route("/api/v1/plugins", func(r chi.Router) {
    r.Use(authMW)
    r.Post("/register", handler.NewPluginHandler(pluginSvc).Register)
    r.Get("/", handler.NewPluginHandler(pluginSvc).List)
    r.Get("/{plugin_id}", handler.NewPluginHandler(pluginSvc).Get)
    r.Get("/{plugin_id}/source", handler.NewPluginHandler(pluginSvc).GetSource)
    r.Post("/{plugin_id}/install", handler.NewPluginHandler(pluginSvc).Install)
    r.Get("/instances", handler.NewPluginHandler(pluginSvc).ListInstances)
    r.Post("/instances/{instance_id}/pause", handler.NewPluginHandler(pluginSvc).PauseInstance)
    r.Post("/instances/{instance_id}/resume", handler.NewPluginHandler(pluginSvc).ResumeInstance)
})
```

Handler pattern: copy `internal/handler/graph_handler.go` (NewXHandler(x) with Routes()).

Route contracts:
- `POST /api/v1/plugins/register` — body `{source: "<full plugin JS with manifest header>"}`; auth required; 201 `{plugin}`; 400 `INVALID_MANIFEST`/`MANIFEST_VALIDATION_FAILED`; 413 `PLUGIN_TOO_LARGE`; 422 `INVALID_PERMISSION`
- `GET /api/v1/plugins?limit=20&offset=0` — 200 `{plugins: [...], total: N}`
- `GET /api/v1/plugins/{id}` — 200 `{plugin}` (no source); 404
- `GET /api/v1/plugins/{id}/source` — 200 `text/javascript` body + `X-Source-SHA256` header; 404
- `POST /api/v1/plugins/{id}/install` — body `{treeId?: string, grantedPermissions: ["data_read"]}`; 201 `{instance}`; 409 `PLUGIN_ALREADY_INSTALLED`; 403 `PERMISSION_NOT_DECLARED`; 410 `PLUGIN_DISABLED`/`PLUGIN_ARCHIVED`
- `POST /api/v1/plugins/instances/{id}/pause` — 200; `POST .../resume` — 200
- `GET /api/v1/plugins/instances` — 200 `{instances: [...]}` (scoped to caller profile + optional `?treeId=`)

### 4.3 main.go wiring

- Build `pluginRepo` (pgx pool — same pool as nodeRepo) and `pluginSvc` in main.go
- Pass `pluginSvc` into `server.New(...)` (add parameter) — update all call sites (main.go + server tests) in the same commit

### 4.4 Frontend (minimal — frontend/src/components/plugin/PluginSandbox.tsx)

One React component, ~150 lines:

- Props: `{ plugin: Plugin, instanceId: string, onError?: (e: {code, message}) => void }`
- Renders `<iframe sandbox="allow-scripts" referrerpolicy="no-referrer" srcDoc={doc} />` where `doc` comes from a client-side mirror of `BuildSrcDoc` (build the srcDoc in the component; the `canopy` shim is the same JS)
- Host side: `window.addEventListener('message', handler)` that:
  - validates `event.origin === window.location.origin` (host page origin)
  - validates `msg.nonce` against the nonce generated for this instance (useRef)
  - validates `msg.target === 'host'`
  - on `ready`: send `init` with `{pluginId, instanceId, manifest, grantedPermissions, theme}`
  - on `api_call`: `methodToPermission` check against grantedPermissions; if allowed, `console.debug` + resolve with a stub result (real data APIs are post-MVP); if denied, return `{error: {code: 'PERMISSION_DENIED', message}}`
  - on `error`: call `onError`
- `useEffect` cleanup: remove listener, send `destroy` to iframe, revoke nothing (srcDoc iframes are GC'd)
- Nonce: `crypto.randomUUID()` per mount

## 5. Register Algorithm (exact, ordered)

1. **Size check:** `len(source) > cfg.PluginMaxSize` → `ErrPluginTooLarge` (413).
2. **Parse manifest** via `ParseManifest(source)`. Errors: `ErrInvalidManifest` (400) / `ErrManifestValidationFailed` (400).
3. **Validate permissions:** any permission ∉ AllPermissions → `ErrInvalidPermission` (422).
4. **Slug derivation:** lowercase name, spaces→`-`, strip non `[a-z0-9-]` (SPEC-PL-01 §3.1 `chk_plugin_slug`).
5. **Compute SHA-256** of source (hex).
6. **Check existing:** `GetActiveByName(name)` — if a row with same (name, version) exists → return existing row (idempotent, 200-ish semantics: worker returns 200 with existing plugin, no duplicate insert — matches SPEC-PL-01 §12.1 test 7). If same name but different version → this is an update; per MVP scope, archive old active row, insert new row with `previous_version_id` = old id (SPEC-PL-01 §3.1 fields exist; keep it simple: set old.status='archived', new row inserted, `is_root_version` = old row was nil).
7. **Insert** row (author_profile_id from JWT via `UserIDFromContext`).
8. **Audit:** append `registered` entry to plugin_audit_log.

## 6. Error Catalog

| Error | Condition | HTTP | Notes |
|-------|-----------|------|-------|
| `ErrInvalidManifest` | manifest block missing / JSON malformed | 400 | `INVALID_MANIFEST` |
| `ErrManifestValidationFailed` | required field missing/bad (semver, render_type, entry_point) | 400 | details in body |
| `ErrInvalidPermission` | unknown permission string | 422 | `INVALID_PERMISSION` |
| `ErrPluginTooLarge` | source > PluginMaxSize | 413 | `PLUGIN_TOO_LARGE` |
| `ErrPluginNotFound` | Get/GetSource/install by unknown id | 404 | |
| `ErrPluginDisabled` | status='disabled' | 410 | `PLUGIN_DISABLED` |
| `ErrPluginArchived` | status='archived' | 410 | `PLUGIN_ARCHIVED` |
| `ErrAlreadyInstalled` | unique install violated | 409 | `PLUGIN_ALREADY_INSTALLED` |
| `ErrPermissionNotDeclared` | granted ∉ plugin.Permissions | 403 | `PERMISSION_NOT_DECLARED` |
| `ErrDatabaseUnavailable` | DB down sentinel | 503 | reuse `service.ErrDatabaseUnavailable` mapping |
| Internal repo error | any other | 500 | generic "internal server error"; real error via zerolog (BUG-020 pattern) |

## 7. Edge Cases

- **Duplicate register (same name+version):** idempotent — return existing row, no duplicate, no new audit entry.
- **Update same name new version:** old row archived, new active, previous_version_id linked.
- **Install twice same (plugin, tree, profile):** partial unique index `WHERE status != 'uninstalled'` enforces → catch constraint violation → `ErrAlreadyInstalled`.
- **Install with permission not declared:** 403.
- **Tree-scoped vs global:** treeID NULL = global; per-tree instance takes precedence in frontend (document only, no precedence logic in backend MVP).
- **Pause/resume:** only status transitions; paused instance blocks api_call via CheckPermission (status != active → error `PLUGIN_DISABLED` style; return `ErrInstanceNotActive`).
- **Concurrent registers:** rely on `uq_plugin_name_version` unique constraint; on violation return existing row.
- **Empty permissions:** valid (manifest may declare `[]` — plugin runs with no API access).
- **Unicode in source:** store as-is (UTF-8); SHA-256 over raw bytes.
- **Source size boundary:** exactly 1MB passes; 1MB+1 rejects.

## 8. Testing (exact scenarios — all in internal/plugin/)

Use a stub Repo (no PG) for service tests: `stubRepo` with in-memory maps. Repo itself gets 6 integration-style tests (SKIP without PG, `SkipIfNoDB` pattern from testutil).

| # | Scenario | Expected |
|---|----------|----------|
| 1 | Register valid plugin (10KB JS + manifest) | Plugin returned; sha256 correct; is_root_version=true; audit registered entry |
| 2 | Register malformed manifest JSON | ErrInvalidManifest; no row |
| 3 | Register missing entry_point | ErrManifestValidationFailed |
| 4 | Register bad semver ("1.2") | ErrManifestValidationFailed |
| 5 | Register unknown permission ("quantum_compute") | ErrInvalidPermission |
| 6 | Register 2MB source | ErrPluginTooLarge (413) |
| 7 | Register duplicate (name, version) | Existing row returned; no new row; no new audit entry |
| 8 | Register same name, new version | Old archived; new active; previous_version_id linked |
| 9 | Install plugin to tree | Instance created; status active; grantedPermissions set |
| 10 | Install twice same (plugin, tree, profile) | ErrAlreadyInstalled |
| 11 | Install with permission not declared | ErrPermissionNotDeclared |
| 12 | Install disabled plugin | ErrPluginDisabled |
| 13 | Pause then resume instance | Status active→paused→active |
| 14 | CheckPermission allowed method | nil error |
| 15 | CheckPermission denied method | PERMISSION_DENIED typed error |
| 16 | CheckPermission unknown method | ErrAPINotFound |
| 17 | CheckPermission on paused instance | ErrInstanceNotActive |
| 18 | ParseManifest happy path | Manifest fields parsed correctly |
| 19 | ParseManifest no block | ErrInvalidManifest |
| 20 | MethodToPermission table | data.query→data_read, network.fetch→network_request, foo→"" |
| 21 | BuildSrcDoc contains CSP + sandbox attrs | doc has `default-src 'none'`, `connect-src none`, shim present, source present, nonce substituted |
| 22 | BuildSrcDoc nonce uniqueness | two calls, different nonce → different docs |
| 23 | List pagination | limit/offset respected; total correct |
| 24 | GetSource | source + sha256 returned; matches registered |

Handler tests (`internal/handler/plugin_handler_test.go`, stub service): register 201, register 400 bad manifest, register 413 oversized, install 201, install 409 duplicate, get 404 unknown, source 200 + sha header, unauth 401.

## 9. Hilo Impact

- **Depends on:** `internal/db` (pool pattern), `internal/handler` (handler pattern), `internal/server` (routing), `internal/config` (env vars), `internal/service` (ErrDatabaseUnavailable)
- **Depends on this (after wiring):** `internal/server/server.go` (routes), `cmd/canopyd/main.go` (construction), frontend `PluginSandbox.tsx`, future: PL-02 built-in viewers, PL-03 card actions, PL-05 calendar
- **Blast radius:** LOW-MEDIUM — new package + 3 migrations + additive wiring. `server.New` signature changes (one new param) — update call sites in the same commit. Migration 000022-000024 apply cleanly on top of 000020 (GAP-001's 000021 may or may not exist by then — migrations are ordered, so if 000021 exists this lands after it; if not, sequence still works).

## 10. Acceptance Criteria (from board GAP-002)

1. `internal/plugin/` package exists: models, manifest.go (ParseManifest), permissions.go (MethodToPermission + gate), repo.go (pgx), service.go, sandbox.go (BuildSrcDoc)
2. Migrations 000022/000023/000024 (plugin_registry, plugin_instances, plugin_audit_log) from SPEC-PL-01 verbatim
3. `POST /api/v1/plugins/register` stores plugin with SHA-256 integrity; size cap 1MB
4. Permission gate: `CheckPermission(instanceID, method)` enforces granted_permissions
5. `GET /api/v1/plugins/{id}/source` serves source with `X-Source-SHA256` header
6. Install/pause/resume endpoints work with per-tree scoping
7. All 24 service tests + 6 handler tests pass (plus 6 PG-skipped repo tests)
8. Frontend `PluginSandbox.tsx` renders sandboxed iframe with CSP + nonce + postMessage host (build + tsc clean)
9. `go build ./...`, `go vet ./...`, `gofmt` clean
10. `gitreins guard` passes
11. No new TODOs; no changes to existing package behavior except additive server.New param

## 11. Out of Scope (explicitly NOT in this task)

- Hot-reload via SSE (`plugin_updated` broadcast) — PL-01 post-MVP
- Rollback endpoint + version history UI — PL-01 post-MVP
- Real data.query/data.mutate/calendar/network implementations — post-MVP (frontend stubs with console.debug)
- Plugin marketplace / discovery UI — post-MVP
- Multi-author name conflict UX — post-MVP (DB allows; UI warning later)
- Built-in file viewers (PL-02)
- Quota/rate-limit engine (checkQuota in SPEC-PL-01 §7.4) — post-MVP
