# SPEC-FTR-07 — Hermes Agent Gateway Integration

> **Status:** Spec | **Phase:** Post-MVP (FTR-07) | **Blocks:** BE-01 (Project Scaffold — HermesClient package), BE-11 (HTTP Router — agent forwarding), FE-01 (Project Scaffold — event type definitions)
> **References:** ARCHITECTURE.md (§5 Multi-Profile Rendering, §8 Agent Memory Integration), SPEC-API-01 (§3 SSE Wire Format, §7 Event Types), SPEC-API-05 (§2 Approval Events, §5 Approval Endpoints), SPEC-API-07 (§2 Error Types), SPEC-DM-04 (§3 Hermes Profile Integration), SPEC-PL-04 (§2 Dynamic Thinking Interface), SPEC-PL-03 (§2 App Card System), SPEC-FTR-01 (§5 API Endpoints), AGENTS.md (§Architecture, §Core Concepts), DuckBrain: /project/hermes-canopy/architecture/hermes-integration

---

## 1. Purpose

Define the exact implementation contract for the **Hermes Agent Gateway integration** — the boundary layer that makes canopyd a thin gateway between the Canopy frontend and the Hermes Agent Gateway API. A Go worker reading this document can implement the Hermes API client, event translator, profile router, and skill bridge without additional design decisions. A TypeScript worker can implement the frontend-side event consumer without guessing wire shapes.

Canopy does **not** run LLM inference, manage skills, route tools, or handle provider credentials. All agent intelligence is delegated to the Hermes Agent Gateway API at `http://127.0.0.1:8642` (local mode) or a remote endpoint. canopyd is the translation layer: it receives user requests from the frontend, translates them to Hermes API calls, streams results back through SSE, and translates tool outputs into Canopy Card events.

**Key insight:** The Hermes integration is not a library or embedded module — it is a **network boundary**. canopyd is a proxy + translator, not an agent runtime. Every communication goes through the Hermes Agent Gateway HTTP API. This keeps canopyd stateless with respect to agent execution, enables hot-swapping Hermes versions without rebuilding canopyd, and allows Canopy to work with any Hermes-compatible gateway (self-hosted, SaaS, or third-party).

A Go worker reading this document can implement:
- `HermesClient` — HTTP client for the Hermes Agent Gateway API
- `EventTranslator` — translates Hermes tool/skill outputs into Canopy Card events
- `ProfileRouter` — maps Canopy workspaces to active Hermes profiles
- `SkillBridge` — invokes Hermes skills and maps results to Cards
- `AgentSessionManager` — tracks Hermes session IDs across Canopy page reloads
- All HTTP handlers, SSE relay wiring, DDL tables for profile mapping, and the `--hermes-url` CLI flag

without making gateway-design decisions. The integration layer is the **boundary between Canopy's graph data model and Hermes's agent execution model**.

---

## 2. Design Decisions

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| 1 | Integration model | Network boundary via HTTP API at `http://127.0.0.1:8642` (local) or configurable remote URL | canopyd is a thin gateway, not an agent runtime. All LLM calls, tool execution, skill dispatch, and profile management happen inside Hermes. canopyd translates between the Canopy graph model and the Hermes API model. |
| 2 | Hermes API client location | `package gateway` in `internal/hermes/` — imported by `internal/http/` for handler wiring and `internal/card/` for event translation | Isolates all Hermes API interaction in one package. The HTTP layer routes agent requests to this package; the Card layer subscribes to event streams through it. No other package imports Hermes API types directly. |
| 3 | API versioning | URL-prefixed: `/v1/responses`, `/v1/conversations`, `/v1/models`, `/v1/profiles`. canopyd negotiates major version on startup via `/v1/health` probe | Hermes Gateway API evolves independently of canopyd. Major version prefix enables backward-incompatible changes without coordination. A failed version negotiation produces a startup error, not a silent runtime failure. |
| 4 | Agent call forwarding pattern | canopyd receives user message from frontend, wraps it with context manifest (from context compiler), sends `POST /v1/responses` to Hermes, streams SSE response back | The context compiler (which lives in canopyd — it traverses the DAG from DuckDB) pre-assembles the token-budgeted context manifest. The manifest is sent as part of the Hermes API request body, not as a separate endpoint. |
| 5 | Response streaming | SSE from Hermes → multiplexed through canopyd → SSE to frontend. canopyd adds Canopy-specific event metadata (card_type, context_hash, tree_id) as event fields | canopyd does not buffer or transform the stream body — it wraps the raw Hermes SSE events in Canopy SSE envelope fields. The frontend reads both the raw Hermes response and the Canopy metadata from the same stream. |
| 6 | Tool output → Card translation | `EventTranslator` reads Hermes tool call `result` fields and maps `file_read` → File Card, `code_result` → Code Card, `search_result` → Search Card, `skill_output` → skill-specific Card type. Unrecognized tool outputs become generic Info Cards. | One-to-one mapping between known Hermes tool types and Canopy Card types (SPEC-PL-03, SPEC-PL-04). The translation is stateless: given a tool result, produce a Card event. Unrecognized outputs degrade gracefully to Info Cards rather than erroring. |
| 7 | Profile routing model | canopyd maintains a `profile_route` table: `{workspace_id, profile_name, hermes_token, is_active}`. When a workspace is active, canopyd uses the mapped Hermes profile's token for all API calls. | A single canopyd instance may serve multiple workspaces, each mapped to a different Hermes profile (coding, creative, research). The profile routing happens at the workspace level, not per-message. `is_active` toggles which profile handles the workspace's agent requests. |
| 8 | Auth token propagation | Canopy session token → Hermes `Authorization: Bearer` header. canopyd validates Canopy token, then uses the mapped Hermes profile's token for the upstream call. Both tokens are opaque to canopyd. | canopyd does not interpret tokens — it extracts the Hermes profile token from the `profile_route` table and forwards it. Token validation is Hermes's responsibility. This keeps canopyd auth-agnostic but delegation-capable. |
| 9 | Session continuity | `AgentSessionManager` maps `{workspace_id, profile_name} → hermes_session_id`. On page reload, canopyd finds the existing session and passes `conversation` key to `POST /v1/responses`. On reconnect timeout (30min with no activity), session is archived. | Hermes sessions (`session_id`) persist across Canopy browser tab closes because the Hermes gateway is a separate process. canopyd tracks the mapping so reconnecting frontends resume the same agent session. 30min inactivity → session archived and a new one created on next message. |
| 10 | Error propagation model | Hermes API errors (HTTP 4xx/5xx) translated to Canopy Error Cards (SPEC-PL-04 §2 — Thinking/Tool Call subtypes). Rate limits (429) → warning card with retry-after hint. Token exhaustion (403) → re-auth card with login prompt. Model errors (500) → error card with raw error text. | All Hermes errors are visible to the user as Cards in the tree, not as console-only logs. This matches Canopy's principle that every model call has a visible context manifest and result. Error Cards carry structured fields for `error_type`, `retry_after_seconds`, and `raw_message`. |
| 11 | Hermes filesystem bridge | canopyd exposes `GET /api/v1/files/{path}` that proxies to Hermes `fs/read` tool output. File cards (SPEC-PL-02) reference these proxied URLs. File picker in Canopy frontend calls `GET /api/v1/files/search?q=` → Hermes filesystem search. | Canopy does not index or mirror the Hermes knowledge base. It is a thin proxy: the Hermes file system is the single source of truth. File URLs are transient (resolved per-view), preventing stale file references. |
| 12 | Skill invocation model | `SkillBridge` sends `POST /v1/responses` with `messages` containing a skill-invocation payload (tool_call type). Hermes executes the skill and returns results. canopyd translates the result to a Card event with the skill's return type. | Skills are Hermes-native — canopyd does not implement skill dispatch logic. It sends a request shaped like an agent tool-call and receives the result as a tool response. This works with any Hermes skill without canopyd knowing the skill's internals. |
| 13 | Context manifest assembly | Context compiler (in canopyd, uses DuckDB graph) produces a `ContextManifest` JSON: `{tree_id, root_node_id, budget_tokens, manifest_nodes: [{node_id, content, timestamp, role}], active_profile, recent_activity}`. This is sent in the Hermes API request body. | The context compiler is Canopy's core value — it transforms the DAG into a budgeted, auditable manifest. Hermes receives this manifest as the conversation context; it does not traverse the DAG itself. The manifest is rebuilt on every request because the tree state may have changed. |
| 14 | SSE multiplexing | canopyd maintains one SSE connection per active Hermes profile per workspace. Incoming Hermes SSE events are tagged with `{workspace_id, profile_name}` and broadcast to all frontend SSE clients subscribed to that workspace. | Multiple frontend tabs or devices share the same Hermes session (via `AgentSessionManager`). canopyd fans out a single Hermes SSE stream to all connected clients. This avoids opening multiple Hermes connections for the same workspace+profile. |
| 15 | Hermes health dependency | canopyd startup includes a `/v1/health` probe to the Hermes gateway. If Hermes is unreachable after 3 retries (1s, 5s, 15s exponential backoff), canopyd starts in **degraded mode**: serves PWA and local graph queries (DuckDB) but shows a "Hermes gateway unavailable" banner on all agent-request UI elements. | Canopy's graph and card UI still works offline — users can browse the tree, view nodes, read cards. Only agent interaction (asking questions, running skills, generating responses) is blocked. This graceful degradation is essential for the offline-first architecture (T1.4). |
| 16 | Conversation context persistence | Hermes API is stateless from canopyd's perspective. The `conversation` array (messages + context manifest) is reconstructed from the tree on every request. canopyd does NOT cache or store Hermes conversation state. | The tree IS the conversation state. Reconstructing the manifest every request ensures it reflects the latest graph state (new replies, approvals, forks). This aligns with Canopy's core principle: the graph is the single source of truth for conversation context. |
| 17 | Model selection | Model is selected per-workspace in Canopy settings, stored in a `workspace_model` table: `{workspace_id, model_name, provider}`. canopyd passes the selected model in the Hermes API request body. Default: Hermes default model (deepseek-v4-flash). | Users and workspaces may prefer different models for different contexts (fast vs thorough). The model selection is a workspace-level setting, not per-message, to keep decisions predictable. changing the model mid-conversation creates a new Hermes session. |
| 18 | Rate limit coordination | canopyd does NOT implement its own rate limiter for Hermes API calls. It relies on Hermes's per-profile rate limiting. If Hermes returns 429, canopyd relays the retry-after header to the frontend as a warning card. | Rate limiting is a Hermes-gateway concern. canopyd would be duplicating logic and potentially getting out of sync with Hermes's actual limits. The 429 relay gives users visibility into Hermes-side throttling. |
| 19 | Plugin/skill discovery | `SkillBridge` queries `GET /v1/models` or a future `GET /v1/skills` endpoint on Hermes to discover available skills. The result is cached for 5 minutes. Canopy sidebar renders available skills as action buttons. | Without discovery, users would need to know skill names to invoke them. Discovery enables the Canopy UI to dynamically populate "Available actions" from Hermes's registered skills. 5-minute cache balances freshness with API call frequency. |
| 20 | Gateway API version compatibility | canopyd declares a `HermesGatewayVersion` constant (`">=0.18.0"`) and checks it against Hermes's `/v1/health` response `version` field on startup. On mismatch, canopyd refuses to start and logs a clear upgrade message. | The Hermes Gateway API evolves. A canopyd built for v0.18.0+ may not work with v0.20.0+ if response shapes changed. The version check prevents silent API contract violations at runtime. The check runs once at startup, not per-request. |

---

## 3. Go Interface Definitions

The following package is syntactically compilable Go. All types are defined in `package hermes`. This is the only package that directly communicates with the Hermes Agent Gateway API. No other package in canopyd imports Hermes API types directly.

### 3.1 Hermes Client — Core Gateway Interface

```go
package hermes

import (
    "context"
    "io"
    "net/http"
    "time"

    "github.com/google/uuid"
)

// --- Configuration ---

// Config holds all configuration for the Hermes Gateway client.
type Config struct {
    // BaseURL is the Hermes Agent Gateway base URL (e.g. "http://127.0.0.1:8642").
    BaseURL string `json:"base_url"`

    // Timeout for individual HTTP requests to the Hermes gateway.
    RequestTimeout time.Duration `json:"request_timeout,omitempty"`

    // MaxRetries is the maximum number of retries for transient Hermes errors.
    MaxRetries int `json:"max_retries,omitempty"`

    // RetryBaseDelay is the base delay for exponential backoff.
    RetryBaseDelay time.Duration `json:"retry_base_delay,omitempty"`

    // HealthCheckInterval is how often to check Hermes gateway health.
    HealthCheckInterval time.Duration `json:"health_check_interval,omitempty"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
    return Config{
        BaseURL:             "http://127.0.0.1:8642",
        RequestTimeout:      120 * time.Second,
        MaxRetries:          3,
        RetryBaseDelay:      time.Second,
        HealthCheckInterval: 30 * time.Second,
    }
}

// --- Core Types ---

// ConversationMessage represents a single message in the Hermes conversation format.
type ConversationMessage struct {
    Role    string `json:"role"`             // "user", "assistant", "system", "tool"
    Content string `json:"content,omitempty"` // text content (for user/assistant)
    ToolID  string `json:"tool_call_id,omitempty"` // present when role="tool"

    // ToolCalls is present when role="assistant" and the assistant called a tool.
    ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall represents a tool invocation by the Hermes agent.
type ToolCall struct {
    ID       string         `json:"id"`
    Type     string         `json:"type"`
    Function ToolCallFunction `json:"function"`
}

// ToolCallFunction describes the function being called.
type ToolCallFunction struct {
    Name      string `json:"name"`
    Arguments string `json:"arguments"` // raw JSON arguments string
}

// ContextManifest is the token-budgeted DAG traversal assembled by canopyd's
// context compiler. It is sent as part of the Hermes API request body so the
// agent has full context of the conversation tree.
type ContextManifest struct {
    TreeID       uuid.UUID              `json:"tree_id"`
    RootNodeID   uuid.UUID              `json:"root_node_id"`
    BudgetTokens int                    `json:"budget_tokens"`    // max tokens for this context
    Nodes        []ContextManifestNode  `json:"nodes"`            // ordered by recency
    ActiveProfile string                `json:"active_profile"`  // e.g. "coding", "creative"
    ToolResults  []ToolResultSummary    `json:"tool_results,omitempty"` // summaries of prior tool results
}

// ContextManifestNode represents a single node in the context manifest.
type ContextManifestNode struct {
    NodeID    uuid.UUID `json:"node_id"`
    Role      string    `json:"role"`     // "user", "assistant", "synthesis"
    Content   string    `json:"content"`
    Timestamp time.Time `json:"timestamp"`
}

// ToolResultSummary is a condensed summary of a prior tool execution result.
type ToolResultSummary struct {
    ToolName string `json:"tool_name"`
    Summary  string `json:"summary"`
    CardType string `json:"card_type,omitempty"` // Canopy Card type, if translated
}

// HermesResponse represents the full response from a Hermes API call,
// including the streamed result and any tool calls made.
type HermesResponse struct {
    SessionID    string      `json:"session_id"`
    ConversationID string    `json:"conversation_id,omitempty"`
    Content      string      `json:"content"`      // assistant's text response
    ToolCalls    []ToolCall  `json:"tool_calls,omitempty"`
    FinishReason string      `json:"finish_reason"` // "stop", "tool_calls", "length", "content_filter"

    // RawEvents stores the unprocessed SSE events from the Hermes stream.
    // Used by EventTranslator for Card creation.
    RawEvents []HermesSSEEvent `json:"-"`
}

// --- HermesClient Interface ---

// HermesClient is the primary interface for all Hermes Agent Gateway communication.
// Implementations wrap net/http and handle authentication, retries, and SSE streaming.
type HermesClient interface {
    // SendMessage sends a user message to the Hermes gateway and returns the
    // fully processed response. Streams SSE events internally; the returned
    // HermesResponse contains the aggregated text + tool calls.
    SendMessage(ctx context.Context, req *SendMessageRequest) (*HermesResponse, error)

    // StreamMessages sends a message and returns an SSE event channel for
    // real-time streaming. Each event carries a delta, tool call, or completion marker.
    // The caller must drain the channel and call Close on the returned ReadCloser.
    StreamMessages(ctx context.Context, req *SendMessageRequest) (<-chan SSEDelta, io.ReadCloser, error)

    // CreateConversation creates a new Hermes conversation with initial messages.
    // Returns the conversation ID for subsequent SendMessage calls.
    CreateConversation(ctx context.Context, profileToken string, manifest *ContextManifest) (string, error)

    // Health checks whether the Hermes gateway is reachable and responsive.
    Health(ctx context.Context) (*HealthStatus, error)

    // ListModels returns available models from the Hermes gateway.
    ListModels(ctx context.Context) ([]ModelInfo, error)

    // ListSkills returns available skills/plugins registered with the Hermes gateway.
    ListSkills(ctx context.Context) ([]SkillInfo, error)
}

// SendMessageRequest wraps all parameters needed to send a message to the Hermes gateway.
type SendMessageRequest struct {
    ProfileToken    string             `json:"-"`                  // Hermes auth token, never serialized
    ConversationID  string             `json:"conversation_id,omitempty"` // resumption
    Model           string             `json:"model,omitempty"`
    Messages        []ConversationMessage `json:"messages"`
    ContextManifest *ContextManifest   `json:"context_manifest,omitempty"`
    Stream          bool               `json:"stream"`            // SSE streaming enabled
    MaxTokens       int                `json:"max_tokens,omitempty"`
    Temperature     float64            `json:"temperature,omitempty"`
}

// SSEDelta is a single SSE event from the Hermes streaming response.
type SSEDelta struct {
    Type    string `json:"type"`    // "delta", "tool_call", "done", "error"
    Content string `json:"content,omitempty"`
    ToolID  string `json:"tool_id,omitempty"`

    // RawEvent is the original SSE event text, used for downstream translation.
    RawEvent string `json:"-"`
}

// HealthStatus represents the Hermes gateway health check response.
type HealthStatus struct {
    Status    string `json:"status"`     // "ok", "degraded", "unavailable"
    Version   string `json:"version"`    // e.g. "0.18.2"
    UptimeSec int64  `json:"uptime_seconds"`
}

// ModelInfo describes a model available through the Hermes gateway.
type ModelInfo struct {
    ID         string `json:"id"`
    Provider   string `json:"provider"`
    ContextLen int    `json:"context_length"`
    SupportsStreaming bool `json:"supports_streaming"`
}

// SkillInfo describes a skill/plugin registered with the Hermes gateway.
type SkillInfo struct {
    Name        string   `json:"name"`
    Description string   `json:"description"`
    Permissions []string `json:"permissions"` // required capabilities
}

// --- Errors ---

var (
    ErrHermesUnavailable = errors.New("hermes: gateway is unavailable")
    ErrHermesTimeout     = errors.New("hermes: request timed out")
    ErrAuthFailed        = errors.New("hermes: authentication failed — token expired or invalid")
    ErrRateLimited       = errors.New("hermes: rate limit exceeded")
    ErrModelUnavailable  = errors.New("hermes: requested model is not available")
    ErrSessionExpired    = errors.New("hermes: session has expired")
    ErrVersionMismatch   = errors.New("hermes: gateway API version is incompatible")
)

// HermesError wraps errors returned by the Hermes gateway with structured metadata.
type HermesError struct {
    HTTPStatus      int    `json:"http_status"`
    HermesCode      string `json:"hermes_code,omitempty"`
    Message         string `json:"message"`
    RetryAfter      int    `json:"retry_after_seconds,omitempty"` // from 429 responses
    IsRetryable     bool   `json:"is_retryable"`
}

func (e *HermesError) Error() string {
    return fmt.Sprintf("hermes: %s (HTTP %d)", e.Message, e.HTTPStatus)
}

// IsHermesError checks if an error is a typed HermesError.
func IsHermesError(err error) (*HermesError, bool) {
    var he *HermesError
    if errors.As(err, &he) {
        return he, true
    }
    return nil, false
}
```

### 3.2 Event Translator — Tool Output → Canopy Card Events

```go
package hermes

// EventTranslator translates Hermes tool/skill outputs into Canopy Card events.
// The translation is stateless: given a tool result, return a Card event.
type EventTranslator interface {
    // TranslateToolResult reads a Hermes tool result and produces a CardEvent.
    // Returns ErrUnknownTool if the tool type is not recognised.
    TranslateToolResult(ctx context.Context, toolResult *ToolResult) (*CardEvent, error)

    // TranslateError translates a HermesError into an ErrorCard event for the frontend.
    TranslateError(ctx context.Context, err *HermesError) (*CardEvent, error)

    // TranslateStreamDelta translates an SSEDelta into a partial Card update.
    // Used for streaming responses where re-rendering is progressive.
    TranslateStreamDelta(ctx context.Context, delta *SSEDelta, currentCardID string) (*CardEvent, error)
}

// ToolResult represents the output of a single Hermes tool execution.
type ToolResult struct {
    ToolName string `json:"tool_name"`    // e.g. "file_read", "code_result", "web_search"
    CallID   string `json:"call_id"`      // matches the ToolCall that produced this result
    Content  string `json:"content"`      // raw text output
    Data     []byte `json:"data,omitempty"` // structured JSON data (if available)
    MIMEType string `json:"mime_type,omitempty"`
    Duration time.Duration `json:"duration"` // how long the tool took to execute
}

// CardEvent is a Canopy Card event that the frontend renders as a Card node.
// These are sent through the Canopy SSE stream wrapped in SSE envelope.
type CardEvent struct {
    CardType   string          `json:"card_type"`           // "file", "code", "search", "thinking", "tool_call", "error"
    CardID     string          `json:"card_id"`             // unique ID for dedup and updates
    NodeID     uuid.UUID       `json:"node_id"`             // the Canopy tree node this card belongs to
    Subtype    string          `json:"subtype,omitempty"`   // per-card-type: e.g. "execution" for code, "thinking" for thinking
    Title      string          `json:"title"`               // user-visible title
    Content    string          `json:"content,omitempty"`   // markdown body
    Data       json.RawMessage `json:"data,omitempty"`      // structured card-specific data
    Actions    []CardAction    `json:"actions,omitempty"`   // interactive buttons (approve, retry, cancel)
    Status     string          `json:"status,omitempty"`    // "running", "completed", "error", "pending"
    ErrorInfo  *CardErrorInfo  `json:"error_info,omitempty"`
    Timestamp  time.Time       `json:"timestamp"`
}

// CardAction is an interactive action the user can take on a Card.
type CardAction struct {
    Label   string `json:"label"`   // "Approve", "Retry", "Cancel", "View File"
    Handler string `json:"handler"` // event handler name, dispatched by frontend
    Icon    string `json:"icon,omitempty"`
}

// CardErrorInfo carries structured error information for Error Cards.
type CardErrorInfo struct {
    ErrorType   string `json:"error_type"`             // "rate_limit", "auth", "model", "tool", "unknown"
    Description string `json:"description"`            // human-readable description
    RetryAfter  int    `json:"retry_after_seconds,omitempty"`
    RawMessage  string `json:"raw_message,omitempty"`  // original Hermes error message (for debugging)
}

// ErrUnknownTool is returned when EventTranslator cannot map a tool type to a Card type.
var ErrUnknownTool = errors.New("hermes: unknown tool type — cannot translate to card event")
```

### 3.3 Profile Router — Workspace ↔ Hermes Profile Mapping

```go
package hermes

// ProfileRouter maps Canopy workspaces to Hermes profiles.
// The mapping is persisted in the profile_route table.
type ProfileRouter interface {
    // GetActiveProfile returns the active Hermes profile for a workspace.
    // Returns ErrNoProfileMapping if no profile is mapped.
    GetActiveProfile(ctx context.Context, workspaceID uuid.UUID) (*ProfileMapping, error)

    // SetActiveProfile maps a workspace to a Hermes profile.
    SetActiveProfile(ctx context.Context, workspaceID uuid.UUID, profileName string, profileToken string) error

    // ListProfiles returns all profile mappings for a workspace.
    ListProfiles(ctx context.Context, workspaceID uuid.UUID) ([]ProfileMapping, error)

    // RemoveProfile removes a profile mapping from a workspace.
    RemoveProfile(ctx context.Context, workspaceID uuid.UUID, profileName string) error

    // GetProfileToken returns the stored Hermes auth token for a profile.
    // Tokens are stored encrypted at rest.
    GetProfileToken(ctx context.Context, workspaceID uuid.UUID, profileName string) (string, error)

    // ListAvailableProfiles queries the Hermes gateway for available profiles.
    ListAvailableProfiles(ctx context.Context) ([]AvailableProfile, error)
}

// ProfileMapping associates a Canopy workspace with a Hermes profile.
type ProfileMapping struct {
    WorkspaceID     uuid.UUID `json:"workspace_id"`
    ProfileName     string    `json:"profile_name"`     // e.g. "coding", "creative", "research"
    DisplayName     string    `json:"display_name"`     // e.g. "Coding (deepseek-v4-pro)"
    IsActive        bool      `json:"is_active"`        // currently handling this workspace
    ModelPreference string    `json:"model_preference,omitempty"` // optional model override
    MappedAt        time.Time `json:"mapped_at"`
    LastUsedAt      time.Time `json:"last_used_at"`
}

// AvailableProfile represents a profile accessible through the Hermes gateway.
type AvailableProfile struct {
    Name        string `json:"name"`
    DisplayName string `json:"display_name"`
    DefaultModel string `json:"default_model"`
}

// Errors
var ErrNoProfileMapping = errors.New("hermes: no profile mapped for this workspace")
```

### 3.4 Skill Bridge — Hermes Skill Invocation

```go
package hermes

// SkillBridge invokes Hermes skills and maps results to Canopy Cards.
type SkillBridge interface {
    // ListSkills returns all skills available through the active Hermes profile.
    ListSkills(ctx context.Context, profileToken string) ([]SkillInfo, error)

    // InvokeSkill invokes a named skill with the given parameters.
    // Returns the skill's output translated into one or more CardEvents.
    InvokeSkill(ctx context.Context, req *InvokeSkillRequest) ([]CardEvent, error)

    // GetSkillStatus checks the current status/health of a skill.
    GetSkillStatus(ctx context.Context, skillName string, profileToken string) (*SkillStatus, error)
}

// InvokeSkillRequest wraps parameters for invoking a Hermes skill.
type InvokeSkillRequest struct {
    ProfileToken string            `json:"-"`
    SkillName    string            `json:"skill_name"`
    Parameters   map[string]any    `json:"parameters"`
    Context      *ContextManifest  `json:"context,omitempty"`
    Timeout      time.Duration     `json:"timeout,omitempty"`
}

// SkillStatus represents the runtime status of a skill.
type SkillStatus struct {
    Name    string `json:"name"`
    Healthy bool   `json:"healthy"`
    Version string `json:"version,omitempty"`
    Message string `json:"message,omitempty"`
}

// Errors
var (
    ErrSkillNotFound   = errors.New("hermes: skill not found")
    ErrSkillTimeout    = errors.New("hermes: skill execution timed out")
    ErrSkillPermission = errors.New("hermes: skill requires permissions not granted")
)
```

### 3.5 Agent Session Manager

```go
package hermes

// AgentSessionManager tracks Hermes session IDs across Canopy page reloads.
// Sessions are persisted in the agent_session table.
type AgentSessionManager interface {
    // GetSession returns the active Hermes session ID for a workspace+profile.
    // Returns ("", nil) if no active session exists.
    GetSession(ctx context.Context, workspaceID uuid.UUID, profileName string) (string, error)

    // SetSession records or updates the active session ID.
    SetSession(ctx context.Context, workspaceID uuid.UUID, profileName string, sessionID string) error

    // ArchiveSession marks a session as archived (30min+ inactivity).
    // Archived sessions are not resumed — a new session is created on the next message.
    ArchiveSession(ctx context.Context, workspaceID uuid.UUID, profileName string) error

    // CleanupStaleSessions runs a goroutine that archives sessions with no activity
    // beyond the configured TTL (default: 30 minutes).
    CleanupStaleSessions(ctx context.Context, checkInterval time.Duration, ttl time.Duration)
}

// AgentSession represents a tracked Hermes agent session in canopyd.
type AgentSession struct {
    WorkspaceID  uuid.UUID  `json:"workspace_id"`
    ProfileName  string     `json:"profile_name"`
    SessionID    string     `json:"session_id"`
    ModelName    string     `json:"model_name,omitempty"`
    Status       string     `json:"status"`        // "active", "archived"
    LastActivity time.Time  `json:"last_activity"`
    CreatedAt    time.Time  `json:"created_at"`
}
```

### 3.6 Hermes SSE Event Types (for relay translation)

```go
package hermes

// HermesSSEEvent represents a raw SSE event from the Hermes gateway stream.
// canopyd relays these through the Canopy SSE stream, wrapping them in a
// Canopy SSE envelope with workspace_id, profile_name, and card metadata.
type HermesSSEEvent struct {
    Event string `json:"event"` // "delta", "tool_call", "tool_result", "done", "error"
    Data  string `json:"data"`  // raw event data JSON
    ID    string `json:"id,omitempty"`
}

// EventEnvelope wraps a Hermes SSE event for Canopy SSE relay.
// canopyd adds these fields before forwarding to the frontend.
type EventEnvelope struct {
    Type        string    `json:"type"`         // "hermes_event"
    WorkspaceID uuid.UUID `json:"workspace_id"`
    ProfileName string    `json:"profile_name"`
    SessionID   string    `json:"session_id"`
    CardID      string    `json:"card_id,omitempty"`     // assigned by EventTranslator
    CardType    string    `json:"card_type,omitempty"`   // resolved Card type
    Event       HermesSSEEvent `json:"event"`
}
```

---

## 4. SSE Events — Hermes Gateway Relay

canopyd opens an SSE connection to the Hermes Gateway API at `POST /v1/responses` (with `stream: true`). Each SSE event from Hermes is wrapped in a Canopy SSE envelope and forwarded to all frontend SSE clients subscribed to the relevant workspace.

### 4.1 Canopy SSE Envelope

The Canopy SSE format extends the standard SSE protocol (SPEC-API-01 §3) with Hermes-specific fields:

```
event: hermes_event
data: {
  "type": "hermes_event",
  "workspace_id": "a1b2c3d4-...",
  "profile_name": "coding",
  "session_id": "20260723_003644_8725da",
  "card_id": "card_abc123",
  "card_type": "thinking",
  "event": {
    "event": "delta",
    "data": "{\"content\":\"Let me think about this...\"}",
    "id": "evt_001"
  }
}
```

### 4.2 Hermes SSE Event → Canopy Event Mapping

| Hermes SSE Event | Canopy Event Type | Card Type | Notes |
|------------------|------------------|-----------|-------|
| `delta` (text content) | `hermes_event` | `thinking` | Text deltas from the agent. Rendered as streaming Thinking Card. |
| `tool_call` | `hermes_event` | `tool_call` | Agent requested a tool execution. Rendered as Tool Call Card with approve/deny. |
| `tool_result` | `hermes_event` | per-tool mapping | Tool output translated by EventTranslator. |
| `done` | `hermes_event` | `thinking` (completed) | Agent finished generating. Thinking Card marked complete. |
| `error` | `hermes_event` | `error` | Hermes error translated by EventTranslator. |
| `rate_limit` | `hermes_event` | `error` (rate_limit subtype) | 429 from upstream provider. Includes retry-after. |

### 4.3 Hermes Tool → Canopy Card Type Mapping

| Hermes Tool Name | Canopy Card Type | Card Subtype | Structured Data |
|-----------------|-----------------|--------------|-----------------|
| `file_read` | `file` | — | path, size, mime_type, content_preview |
| `code_result` | `code` | execution | stdout, stderr, exit_code, duration, language |
| `web_search` | `search` | web | query, results_count, source_urls |
| `web_extract` | `file` | web_content | url, title, content_preview, extracted_at |
| `skill:*` | `card` | skill_`<name>` | skill-specific structured data |
| `image_generate` | `file` | image | prompt, image_url, model_used |
| `text_to_speech` | `file` | audio | text, duration_sec, audio_url |
| *unknown* | `info` | generic | raw tool result text |

---

## 5. API Endpoints — Hermes Agent Forwarding

canopyd exposes HTTP endpoints that proxy or wrap Hermes API calls for the frontend.

### 5.1 Agent Request

```
POST /api/v1/agent/send
```

Forwards a user message to the Hermes gateway. canopyd assembles the context manifest from DuckDB, maps the active profile, and calls `HermesClient.SendMessage()`.

**Request:**
```json
{
  "workspace_id": "a1b2c3d4-...",
  "message": "Show me the files in /projects/canopy",
  "model": "deepseek-v4-flash",
  "stream": true
}
```

**Response (SSE stream):** Hermes events wrapped in Canopy SSE envelopes (see §4.1).

### 5.2 Skill Invocation

```
POST /api/v1/agent/skills/{skill_name}/invoke
```

Invokes a specific Hermes skill and returns the result as one or more Cards.

**Request:**
```json
{
  "workspace_id": "a1b2c3d4-...",
  "parameters": {
    "query": "What's new in Go 1.25?",
    "max_results": 5
  }
}
```

**Response:**
```json
{
  "cards": [
    {
      "card_type": "search",
      "card_id": "card_abc123",
      "node_id": "n1-...",
      "title": "Web Search Results",
      "content": "Go 1.25 introduces...",
      "data": { "results_count": 5 },
      "status": "completed"
    }
  ],
  "session_id": "20260723_003644_8725da"
}
```

### 5.3 Profile Management

```
GET /api/v1/profiles
```

Lists available profiles from the Hermes gateway.

```
GET /api/v1/workspaces/{workspace_id}/profiles
```

Lists profile mappings for a workspace.

```
POST /api/v1/workspaces/{workspace_id}/profiles
```

Maps a Hermes profile to a workspace.

**Request:**
```json
{
  "profile_name": "coding",
  "profile_token": "hprof_...",
  "set_active": true
}
```

```
DELETE /api/v1/workspaces/{workspace_id}/profiles/{profile_name}
```

Removes a profile mapping from a workspace.

### 5.4 Hermes Health

```
GET /api/v1/hermes/health
```

Returns the Hermes gateway health status, proxied from the Hermes `/v1/health` endpoint.

**Response:**
```json
{
  "status": "ok",
  "version": "0.18.2",
  "uptime_seconds": 86400,
  "hermes_url": "http://127.0.0.1:8642"
}
```

### 5.5 Model Selection

```
GET /api/v1/models
```

Returns available models from the Hermes gateway (proxied from `GET /v1/models`).

```
GET /api/v1/workspaces/{workspace_id}/model
POST /api/v1/workspaces/{workspace_id}/model
```

Get/set the model preference for a workspace.

### 5.6 Hermes Filesystem Proxy

```
GET /api/v1/files/{path}
```

Proxies a file read from the Hermes filesystem. The file content is served as-is with the correct MIME type. Supports range requests for partial reads.

```
GET /api/v1/files/search?q={query}
```

Searches the Hermes filesystem by name or content.

### 5.7 Conversation Session

```
POST /api/v1/agent/session/new
```

Creates a new Hermes conversation session for a workspace+profile combination.

**Request:**
```json
{
  "workspace_id": "a1b2c3d4-...",
  "profile_name": "coding",
  "model": "deepseek-v4-flash"
}
```

---

## 6. DDL — Profile & Session Storage

These tables live in the Canopy PostgreSQL database alongside the tree data.

### 6.1 Profile Route Table

```sql
CREATE TABLE profile_route (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    profile_name    VARCHAR(64) NOT NULL,    -- "coding", "creative", "research"
    display_name    VARCHAR(128) NOT NULL DEFAULT '',
    is_active       BOOLEAN NOT NULL DEFAULT false,
    model_preference VARCHAR(64),            -- optional model override
    profile_token_encrypted BYTEA,           -- encrypted Hermes auth token
    mapped_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(workspace_id, profile_name)
);

CREATE INDEX idx_profile_route_active ON profile_route(workspace_id) WHERE is_active = true;
```

### 6.2 Agent Session Table

```sql
CREATE TABLE agent_session (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    profile_name    VARCHAR(64) NOT NULL,
    hermes_session_id VARCHAR(256) NOT NULL,
    model_name      VARCHAR(64),
    status          VARCHAR(16) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    last_activity   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(workspace_id, profile_name, status)
);

CREATE INDEX idx_agent_session_active ON agent_session(workspace_id, profile_name) WHERE status = 'active';
CREATE INDEX idx_agent_session_stale ON agent_session(last_activity) WHERE status = 'active';
```

### 6.3 Workspace Model Preference

```sql
CREATE TABLE workspace_model (
    workspace_id UUID PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    model_name   VARCHAR(64) NOT NULL DEFAULT 'deepseek-v4-flash',
    provider     VARCHAR(64) NOT NULL DEFAULT 'deepseek',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

---

## 7. Edge Cases & Test Scenarios

| # | Edge Case | Expected Behaviour | Verification |
|---|-----------|-------------------|--------------|
| 1 | Hermes gateway is down at canopyd startup | canopyd starts in degraded mode: serves PWA, graph queries, cards. Shows "Hermes gateway unavailable" banner. All agent-request UI calls return 503. | Start canopyd with no Hermes gateway running. Verify PWA loads, graph renders, "Send Message" returns 503 banner. |
| 2 | Hermes gateway goes down during an active agent session | In-flight SSE stream drops. canopyd detects broken connection on next health check (30s interval). Frontend shows "Connection lost — retrying" overlay with retry button. Agent session is preserved (Hermes keeps it alive). | Kill Hermes canopyd while agent is streaming. Verify frontend shows retry overlay within 30s. Restart Hermes, verify retry succeeds and session resumes. |
| 3 | Profile token expires mid-session | Hermes returns 403 with `auth_failed`. canopyd translates to Error Card with "Session expired — re-authenticate" message. Frontend shows re-auth dialog. | Set a Hermes profile token with 60s TTL. Send message, wait 61s, send another. Verify Error Card appears with auth prompt. |
| 4 | Rate limit hit on upstream provider | Hermes returns 429 with `retry_after_seconds`. canopyd translates to Error Card of type `rate_limit` with retry-after hint. | Configure Hermes with aggressive rate limits. Send burst of 20 messages. Verify rate_limit Error Card appears with correct retry-after. |
| 5 | Unknown tool type returned by Hermes | `EventTranslator.TranslateToolResult` returns `ErrUnknownTool`. canopyd creates a generic Info Card with raw tool result text. | Mock a Hermes response with an unrecognised `tool_call` name. Verify Info Card appears with raw content. |
| 6 | Page reload during active SSE stream | Frontend reconnects to Canopy SSE endpoint. canopyd's `AgentSessionManager` finds the existing Hermes session. New SSE stream picks up from where the old one left off (Hermes event IDs). | Open Canopy, start an agent message, reload the page before it finishes. Verify the session is still active, response continues, and the context manifest includes previous messages. |
| 7 | User opens 3 tabs to the same workspace | canopyd fans out a single Hermes SSE connection to all 3 tabs (SSE multiplexing, §2 Decision 14). Each tab receives the same events independently. | Open workspace in 3 browser tabs. Send a message. Verify all 3 tabs show the same streaming response. |
| 8 | Workspace has no mapped profile | `ProfileRouter.GetActiveProfile` returns `ErrNoProfileMapping`. canopyd returns 400 with "No Hermes profile configured for this workspace" when any agent request is made. Frontend shows profile setup wizard. | Create a new workspace, attempt to send a message. Verify 400 response with profile-setup prompt. |
| 9 | Model name invalid for selected profile | Hermes returns 400 with `model_not_found`. canopyd translates to Error Card with "Model unavailable — switch to a different model" message. | Set workspace model to a non-existent model. Send message. Verify error card with model-switch prompt. |
| 10 | Context manifest exceeds token budget | Context compiler enforces token budget locally. If the assembled manifest exceeds the budget, it is trimmed by recency (most recent nodes kept, oldest dropped). The `budget_tokens` field reflects the actual budget used. | Create a tree with 200+ long messages simulating a large conversation. Verify context manifest is trimmed to budget and the frontend shows a "Context window: 4000/8000 tokens" indicator. |
| 11 | Hermes gateway upgrade during operation | canopyd's startup version check passed at boot. If Hermes restarts with a new version, the next `SendMessage` call may fail with a parsed response error. canopyd catches this, logs a warning, and shows "Hermes gateway updated — reload may be required" banner. | Update canopyd binary, restart Hermes. Send a message. Verify warning banner appears, then retry succeeds (version-compatible path). |
| 12 | Concurrent skill invocations on the same workspace | Each skill invocation creates a separate Hermes API call with a shared session. Hermes serializes tool calls within a session. canopyd queues concurrent requests per workspace+profile. | Fire 3 skill invocations simultaneously on the same workspace. Verify they execute sequentially (not interleaved) and each returns a distinct Card. |
| 13 | File read from Hermes filesystem with large file (>10MB) | canopyd proxies the read with HTTP range requests. Frontend renders a "Large file — preview truncated" indicator with a download link. | Request a 20MB file path. Verify response is chunked, and frontend shows truncated preview + download link. |
| 14 | Agent session archived during extended idle | `AgentSessionManager.CleanupStaleSessions` runs, finds no activity >30min, archives the session. Next message creates a fresh Hermes session. Frontend shows "New session started — previous context archived" note. | Open workspace, send a message. Wait 31min without activity. Send another message. Verify a new session is created and the archived notice appears. |
| 15 | Profile token rotation | `profile_token_encrypted` updated via `POST /api/v1/workspaces/{id}/profiles` without changing the profile mapping. Old token becomes invalid immediately. | Update profile token while an active session exists. Send another message. Verify request uses new token and succeeds (Hermes authenticates the new token). |
| 16 | Multiple profiles mapped to same workspace | Only one profile can be `is_active` per workspace. Toggling active profile closes the current Hermes session and opens a new one with the new profile's token. Previous profile's session is archived. | Map "coding" and "creative" profiles to the same workspace. Toggle active from coding → creative. Verify a new Hermes session is created (new session_id) and the coding session is archived. |
| 17 | Hermes returns malformed SSE data | canopyd's SSE parser fails gracefully: logs the malformed event, skips it, and continues processing the rest of the stream. The frontend receives a "partial response — some data was lost" info card. | Inject a malformed SSE event via mock Hermes server. Verify canopyd logs the error, skips the event, and the frontend shows the informational card. |

---

## 8. Implementation Plan

### Phase 1: Core `HermesClient` (3-5 days)

1. **Create `internal/hermes/` package** with config, types, and `HermesClient` interface
2. **Implement `httpHermesClient`** — HTTP client with retries (exponential backoff), auth header injection, SSE stream parsing, and health check
3. **Implement `Health()` method** — startup probe with 3 retries (1s, 5s, 15s backoff)
4. **Implement streaming support** — `StreamMessages()` returns `<chan SSEDelta>` and `io.ReadCloser`
5. **Implement error mapping** — HTTP 4xx/5xx → typed `HermesError` with structured fields
6. **Tests**: mock Hermes server, 20+ test cases covering success, streaming, timeouts, rate limits, auth failures, version mismatch

### Phase 2: Context Compiler & Event Translator (3-5 days)

1. **Build context compiler** — traverses DuckDB graph, produces `ContextManifest` with token budget enforcement, recency ordering, and profile injection
2. **Build `EventTranslator`** — tool name → Card type mapping table, `TranslateToolResult()`, `TranslateError()`, `TranslateStreamDelta()`
3. **Implement Card creation helpers** — `NewFileCard()`, `NewCodeCard()`, `NewSearchCard()`, `NewThinkingCard()`, `NewToolCallCard()`, `NewErrorCard()`
4. **Tests**: mock Hermes tool outputs for each known type, verify correct Card type and structure. Unknown tool type → Info Card fallback

### Phase 3: Profile Router & Session Manager (2-3 days)

1. **Implement `ProfileRouter`** — reads/writes `profile_route` table, token encryption (AES-256-GCM with server key), active profile toggling
2. **Implement `AgentSessionManager`** — session CRUD within `agent_session` table, stale cleanup goroutine, archive on inactivity
3. **Implement profile management API endpoints** — GET/POST/DELETE `/api/v1/workspaces/{id}/profiles`
4. **Tests**: profile CRUD, token encryption round-trip, session lifecycle (create → archive → resume), stale cleanup timing

### Phase 4: HTTP Wiring & Frontend Integration (4-6 days)

1. **Wire `POST /api/v1/agent/send`** — assembles context manifest, maps active profile, calls `HermesClient.StreamMessages()`, wraps SSE events in Canopy envelopes, broadcasts to workspace SSE hub
2. **Wire `POST /api/v1/agent/skills/{name}/invoke`** — calls `SkillBridge.InvokeSkill()`, returns Card array
3. **Wire proxy endpoints** — filesystem proxy (`GET /api/v1/files/...`), health proxy, model listing
4. **Implement hermetic mode fallback** — canopyd starts without Hermes, shows degraded-mode banner
5. **Frontend:** Event type definitions (TypeScript), SSE event consumer that handles `hermes_event` type, Card renderer for each Card type, reconnection logic with session resumption
6. **E2E tests:** canopyd + fake Hermes server, end-to-end message flow (frontend → canopyd → fake Hermes → SSE stream → frontend)
