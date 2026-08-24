// Package hermes provides the Hermes Agent Gateway client for canopyd
// (SPEC-FTR-07 §3.1). canopyd is a thin proxy + translator, NOT an agent
// runtime: every communication with the Hermes Agent Gateway API
// (http://127.0.0.1:8642) goes through this client.
package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
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
	Role    string `json:"role"`                   // "user", "assistant", "system", "tool"
	Content string `json:"content,omitempty"`      // text content (for user/assistant)
	ToolID  string `json:"tool_call_id,omitempty"` // present when role="tool"

	// ToolCalls is present when role="assistant" and the assistant called a tool.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall represents a tool invocation by the Hermes agent.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
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
	TreeID        uuid.UUID             `json:"tree_id"`
	RootNodeID    uuid.UUID             `json:"root_node_id"`
	BudgetTokens  int                   `json:"budget_tokens"`          // max tokens for this context
	Nodes         []ContextManifestNode `json:"nodes"`                  // ordered by recency
	ActiveProfile string                `json:"active_profile"`         // e.g. "coding", "creative"
	ToolResults   []ToolResultSummary   `json:"tool_results,omitempty"` // summaries of prior tool results
}

// ContextManifestNode represents a single node in the context manifest.
type ContextManifestNode struct {
	NodeID    uuid.UUID `json:"node_id"`
	Role      string    `json:"role"` // "user", "assistant", "synthesis"
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
	SessionID      string     `json:"session_id"`
	ConversationID string     `json:"conversation_id,omitempty"`
	Content        string     `json:"content"` // assistant's text response
	ToolCalls      []ToolCall `json:"tool_calls,omitempty"`
	FinishReason   string     `json:"finish_reason"` // "stop", "tool_calls", "length", "content_filter"

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
	ProfileToken    string                `json:"-"`                         // Hermes auth token, never serialized
	ConversationID  string                `json:"conversation_id,omitempty"` // resumption
	Model           string                `json:"model,omitempty"`
	Messages        []ConversationMessage `json:"messages"`
	ContextManifest *ContextManifest      `json:"context_manifest,omitempty"`
	Stream          bool                  `json:"stream"` // SSE streaming enabled
	MaxTokens       int                   `json:"max_tokens,omitempty"`
	Temperature     float64               `json:"temperature,omitempty"`
}

// SSEDelta is a single SSE event from the Hermes streaming response.
type SSEDelta struct {
	Type    string `json:"type"` // "delta", "tool_call", "done", "error"
	Content string `json:"content,omitempty"`
	ToolID  string `json:"tool_id,omitempty"`

	// RawEvent is the original SSE event text, used for downstream translation.
	RawEvent string `json:"-"`
}

// HealthStatus represents the Hermes gateway health check response.
type HealthStatus struct {
	Status    string `json:"status"`  // "ok", "degraded", "unavailable"
	Version   string `json:"version"` // e.g. "0.18.2"
	UptimeSec int64  `json:"uptime_seconds"`
}

// ModelInfo describes a model available through the Hermes gateway.
type ModelInfo struct {
	ID                string `json:"id"`
	Provider          string `json:"provider"`
	ContextLen        int    `json:"context_length"`
	SupportsStreaming bool   `json:"supports_streaming"`
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
	HTTPStatus  int    `json:"http_status"`
	HermesCode  string `json:"hermes_code,omitempty"`
	Message     string `json:"message"`
	RetryAfter  int    `json:"retry_after_seconds,omitempty"` // from 429 responses
	IsRetryable bool   `json:"is_retryable"`

	// sentinel is the wrapped sentinel error (unexported implementation
	// detail). It is set by the client for transport-level failures (where
	// HTTPStatus is 0) and by mapHTTPError for HTTP failures. Unwrap returns
	// it so errors.Is(err, ErrAuthFailed) etc. work on mapped errors while
	// the error itself remains a *HermesError for errors.As.
	sentinel error
}

func (e *HermesError) Error() string {
	return fmt.Sprintf("hermes: %s (HTTP %d)", e.Message, e.HTTPStatus)
}

// Unwrap returns the sentinel error this HermesError maps to, or nil.
// This makes errors.Is(err, ErrAuthFailed) etc. work on mapped errors.
func (e *HermesError) Unwrap() error { return e.sentinel }

// IsHermesError checks if an error is a typed HermesError.
func IsHermesError(err error) (*HermesError, bool) {
	var he *HermesError
	if errors.As(err, &he) {
		return he, true
	}
	return nil, false
}

// --- SSE relay types (SPEC-FTR-07 §3.6) ---

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
	Type        string         `json:"type"` // "hermes_event"
	WorkspaceID uuid.UUID      `json:"workspace_id"`
	ProfileName string         `json:"profile_name"`
	SessionID   string         `json:"session_id"`
	CardID      string         `json:"card_id,omitempty"`   // assigned by EventTranslator
	CardType    string         `json:"card_type,omitempty"` // resolved Card type
	Event       HermesSSEEvent `json:"event"`
}

// --- Version compatibility (SPEC-FTR-07 §2 decision 20, §8 Phase 1 item 10) ---

// HermesGatewayVersion is the minimum Hermes Agent Gateway API version this
// client is compatible with. canopyd checks the gateway's /v1/health version
// field against this at startup.
const HermesGatewayVersion = ">=0.18.0"

// ValidateVersion checks a gateway version string against HermesGatewayVersion
// (>=0.18.0). The parse is best-effort with no external semver dependency: a
// leading "v"/"V" is stripped, then the major.minor prefix is parsed. Any
// version that fails to parse, or that is older than 0.18.0, yields
// ErrVersionMismatch.
func ValidateVersion(version string) error {
	v := strings.TrimSpace(version)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	if v == "" {
		return fmt.Errorf("%w: empty version", ErrVersionMismatch)
	}
	var major, minor int
	if n, err := fmt.Sscanf(v, "%d.%d", &major, &minor); err != nil || n < 2 {
		return fmt.Errorf("%w: unparseable version %q", ErrVersionMismatch, version)
	}
	if major > 0 || (major == 0 && minor >= 18) {
		return nil
	}
	return fmt.Errorf("%w: version %q is older than %s", ErrVersionMismatch, version, HermesGatewayVersion)
}

// --- Client implementation ---

// httpHermesClient is the default HermesClient implementation. It wraps
// net/http and handles authentication, retries, and SSE streaming.
type httpHermesClient struct {
	baseURL    string
	httpClient *http.Client
	maxRetries int
	retryBase  time.Duration
}

// NewClient returns a HermesClient with the given configuration.
//
// Zero-valued fields are filled from DefaultConfig. An empty BaseURL is an
// error: the client must know where the gateway lives. (Deviation from the
// spec's single-value signature: validation needs an error return.)
func NewClient(cfg Config) (HermesClient, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("hermes: BaseURL is required")
	}
	def := DefaultConfig()
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = def.RequestTimeout
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = def.MaxRetries
	}
	if cfg.RetryBaseDelay <= 0 {
		cfg.RetryBaseDelay = def.RetryBaseDelay
	}
	if cfg.HealthCheckInterval <= 0 {
		cfg.HealthCheckInterval = def.HealthCheckInterval
	}
	return &httpHermesClient{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		httpClient: &http.Client{Timeout: cfg.RequestTimeout},
		maxRetries: cfg.MaxRetries,
		retryBase:  cfg.RetryBaseDelay,
	}, nil
}

// newRequest builds an HTTP request against the gateway base URL. The
// profile token is injected as "Authorization: Bearer <token>" only when
// non-empty, and is never logged or serialized (SendMessageRequest.ProfileToken
// and CreateConversation's token are json:"-" / plain parameters).
func (c *httpHermesClient) newRequest(ctx context.Context, method, path string, body io.Reader, token string, stream bool) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	return req, nil
}

// maxBackoff caps the exponential backoff delay at ~30s (SPEC-FTR-07 §8
// Phase 1 item 2).
const maxBackoff = 30 * time.Second

// doWithRetry executes fn, retrying transient failures with exponential
// backoff (RetryBaseDelay * 2^attempt, capped at maxBackoff). Transient
// failures are network errors, HTTP 5xx, and 429 (whose Retry-After header
// is honored). 4xx (except 429) and caller context cancellation are never
// retried. After MaxRetries retries the last mapped error is returned.
func (c *httpHermesClient) doWithRetry(ctx context.Context, fn func(ctx context.Context) (*http.Response, error)) (*http.Response, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		resp, err := fn(ctx)
		if err != nil {
			if resp != nil {
				_ = resp.Body.Close()
			}
			if ctx.Err() != nil {
				// Caller canceled or deadline exceeded: never retry.
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return nil, fmt.Errorf("%w: %v", ErrHermesTimeout, ctx.Err())
				}
				return nil, ctx.Err()
			}
			lastErr = mapNetworkError(err)
		} else if !isRetryableStatus(resp.StatusCode) {
			return resp, nil
		} else {
			// Retryable status: capture the body for the error message,
			// close the connection, and back off.
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			lastErr = mapHTTPError(resp.StatusCode, resp.Header, raw)
		}

		if attempt >= c.maxRetries {
			return nil, lastErr
		}
		delay := c.backoffDelay(attempt, lastErr)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("%w: %v", ErrHermesTimeout, ctx.Err())
			}
			return nil, ctx.Err()
		}
	}
}

// isRetryableStatus reports whether an HTTP status is worth retrying:
// 429 (rate limit) and 5xx. All other 4xx are permanent.
func isRetryableStatus(status int) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	return status >= 500
}

// backoffDelay computes the wait before retry attempt N: RetryBaseDelay *
// 2^attempt, capped at maxBackoff. A 429's Retry-After (when present) is
// honored by waiting at least that long.
func (c *httpHermesClient) backoffDelay(attempt int, lastErr error) time.Duration {
	exp := attempt
	if exp > 30 {
		exp = 30
	}
	delay := c.retryBase * time.Duration(1<<exp)
	if delay > maxBackoff {
		delay = maxBackoff
	}
	if he, ok := IsHermesError(lastErr); ok && he.HTTPStatus == http.StatusTooManyRequests && he.RetryAfter > 0 {
		ra := time.Duration(he.RetryAfter) * time.Second
		if ra > delay {
			delay = ra
		}
		if delay > maxBackoff {
			delay = maxBackoff
		}
	}
	return delay
}

// mapHTTPError converts a non-2xx HTTP response into a typed *HermesError
// wrapping the appropriate sentinel (SPEC-FTR-07 §2 decision 10, §8 item 5):
//
//	401/403 → ErrAuthFailed (not retryable)
//	404 with model/session context in the body → ErrModelUnavailable / ErrSessionExpired
//	429     → ErrRateLimited (retryable, Retry-After honored)
//	5xx     → ErrHermesUnavailable (retryable)
//
// A 404 without model/session context maps to a plain HermesError (no
// sentinel): the endpoint is missing, which is neither an auth, model,
// session, nor availability failure. Non-JSON bodies are used verbatim as
// the message.
func mapHTTPError(status int, header http.Header, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	// Prefer structured fields from a JSON error body.
	var hermesCode string
	if len(body) > 0 {
		var parsed struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &parsed) == nil {
			if parsed.Error.Message != "" {
				msg = parsed.Error.Message
			} else if parsed.Message != "" {
				msg = parsed.Message
			}
			if parsed.Error.Code != "" {
				hermesCode = parsed.Error.Code
			} else {
				hermesCode = parsed.Code
			}
		}
	}

	he := &HermesError{HTTPStatus: status, HermesCode: hermesCode, Message: msg}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		he.sentinel = ErrAuthFailed
		he.IsRetryable = false
	case status == http.StatusTooManyRequests:
		he.sentinel = ErrRateLimited
		he.IsRetryable = true
		he.RetryAfter = parseRetryAfter(header)
	case status == http.StatusNotFound:
		lower := strings.ToLower(msg + " " + hermesCode)
		switch {
		case strings.Contains(lower, "model"):
			he.sentinel = ErrModelUnavailable
			he.IsRetryable = false
		case strings.Contains(lower, "session"):
			he.sentinel = ErrSessionExpired
			he.IsRetryable = false
		default:
			he.IsRetryable = false // no sentinel: endpoint missing
		}
	case status >= 500:
		he.sentinel = ErrHermesUnavailable
		he.IsRetryable = true
	default:
		he.IsRetryable = false
	}
	return he
}

// mapNetworkError converts a transport-level failure into a typed error.
// Client timeouts (http.Client.Timeout) and context deadlines map to
// ErrHermesTimeout; everything else (connection refused, DNS, reset) maps to
// ErrHermesUnavailable. Both are retryable.
func mapNetworkError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return &HermesError{Message: err.Error(), IsRetryable: true, sentinel: ErrHermesTimeout}
	}
	return &HermesError{Message: err.Error(), IsRetryable: true, sentinel: ErrHermesUnavailable}
}

// parseRetryAfter parses the Retry-After header: integer seconds, or an
// HTTP-date. Returns 0 when absent or unparseable.
func parseRetryAfter(header http.Header) int {
	raw := header.Get("Retry-After")
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
		return secs
	}
	if t, err := http.ParseTime(raw); err == nil {
		if d := time.Until(t); d > 0 {
			return int(d.Seconds() + 0.5) // round up
		}
	}
	return 0
}

// Health performs a single health check against GET /v1/health.
//
// It does NOT retry: it is the "check right now" semantic used by the
// periodic HealthCheckInterval probe. The startup probe (SPEC-FTR-07 §8
// Phase 1 item 3: 3 retries with 1s, 5s, 15s backoff) is HealthWithRetry.
// Non-200 → ErrHermesUnavailable.
func (c *httpHermesClient) Health(ctx context.Context) (*HealthStatus, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/health", nil, "", false)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, mapNetworkError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, mapHTTPError(resp.StatusCode, resp.Header, raw)
	}
	var hs HealthStatus
	if err := json.NewDecoder(resp.Body).Decode(&hs); err != nil {
		return nil, fmt.Errorf("hermes: health: decode response: %w", err)
	}
	return &hs, nil
}

// healthRetryDelays is the startup probe backoff schedule from SPEC-FTR-07
// §8 Phase 1 item 3: 3 retries with 1s, 5s, 15s backoff.
var healthRetryDelays = []time.Duration{time.Second, 5 * time.Second, 15 * time.Second}

// HealthWithRetry is the startup probe: it retries Health up to 3 times with
// the spec's 1s/5s/15s backoff schedule before giving up. canopyd uses this
// at boot to decide between normal and degraded mode (§2 decision 15).
func (c *httpHermesClient) HealthWithRetry(ctx context.Context) (*HealthStatus, error) {
	return c.healthWithRetry(ctx, healthRetryDelays)
}

// healthWithRetry is the testable core of HealthWithRetry; the delay schedule
// is injectable so tests can use short delays.
func (c *httpHermesClient) healthWithRetry(ctx context.Context, delays []time.Duration) (*HealthStatus, error) {
	var lastErr error
	for i := 0; ; i++ {
		hs, err := c.Health(ctx)
		if err == nil {
			return hs, nil
		}
		lastErr = err
		if i >= len(delays) {
			return nil, lastErr
		}
		select {
		case <-time.After(delays[i]):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// SendMessage sends a message to POST /v1/responses. When req.Stream is
// true, the SSE streaming path is used and deltas are aggregated into the
// returned HermesResponse (Content = concatenated delta content, ToolCalls
// from tool_call events, FinishReason/SessionID/ConversationID from the done
// event, RawEvents populated). Otherwise the JSON response body is parsed
// directly.
func (c *httpHermesClient) SendMessage(ctx context.Context, req *SendMessageRequest) (*HermesResponse, error) {
	if req == nil {
		return nil, errors.New("hermes: send message: nil request")
	}
	if req.Stream {
		return c.sendMessageStream(ctx, req)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("hermes: send message: encode request: %w", err)
	}
	resp, err := c.doWithRetry(ctx, func(ctx context.Context) (*http.Response, error) {
		r, err := c.newRequest(ctx, http.MethodPost, "/v1/responses", bytes.NewReader(body), req.ProfileToken, false)
		if err != nil {
			return nil, err
		}
		return c.httpClient.Do(r)
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, mapHTTPError(resp.StatusCode, resp.Header, raw)
	}
	var out HermesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("hermes: send message: decode response: %w", err)
	}
	return &out, nil
}

// sendMessageStream aggregates a streaming response into a HermesResponse.
func (c *httpHermesClient) sendMessageStream(ctx context.Context, req *SendMessageRequest) (*HermesResponse, error) {
	ch, body, err := c.StreamMessages(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()

	out := &HermesResponse{}
	for delta := range ch {
		out.RawEvents = append(out.RawEvents, parseRawEvent(delta.RawEvent))
		switch delta.Type {
		case "delta":
			out.Content += delta.Content
		case "tool_call":
			if tc, ok := parseToolCallFromRaw(delta.RawEvent); ok {
				out.ToolCalls = append(out.ToolCalls, tc)
			}
		case "done":
			// The done event's data may carry session/conversation/finish metadata.
			ev := parseRawEvent(delta.RawEvent)
			if ev.Data != "" {
				var done struct {
					SessionID      string `json:"session_id"`
					ConversationID string `json:"conversation_id"`
					FinishReason   string `json:"finish_reason"`
				}
				if json.Unmarshal([]byte(ev.Data), &done) == nil {
					if done.SessionID != "" {
						out.SessionID = done.SessionID
					}
					if done.ConversationID != "" {
						out.ConversationID = done.ConversationID
					}
					if done.FinishReason != "" {
						out.FinishReason = done.FinishReason
					}
				}
			}
		case "error":
			// The gateway reported a stream-level error; no sentinel fits.
			return nil, &HermesError{Message: "stream error: " + delta.Content, IsRetryable: false}
		}
	}
	return out, nil
}

// StreamMessages POSTs to /v1/responses with stream:true and parses the SSE
// body incrementally, sending SSEDelta values on the returned channel. The
// returned io.ReadCloser is the response body; the caller MUST Close it. The
// channel is closed when the stream ends, or after one "error" delta on
// stream failure.
//
// The initial POST is not retried: a streaming response is a long-lived
// connection, and retrying mid-stream is meaningless. Callers that need
// retry semantics for the connection itself should re-invoke StreamMessages.
func (c *httpHermesClient) StreamMessages(ctx context.Context, req *SendMessageRequest) (<-chan SSEDelta, io.ReadCloser, error) {
	if req == nil {
		return nil, nil, errors.New("hermes: stream messages: nil request")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("hermes: stream messages: encode request: %w", err)
	}
	httpReq, err := c.newRequest(ctx, http.MethodPost, "/v1/responses", bytes.NewReader(body), req.ProfileToken, true)
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, nil, mapNetworkError(err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		return nil, nil, mapHTTPError(resp.StatusCode, resp.Header, raw)
	}

	ch := make(chan SSEDelta, 16)
	bodyRC := newCloseAwareBody(resp.Body)
	go c.streamLoop(ctx, bodyRC, ch)
	return ch, bodyRC, nil
}

// streamLoop reads the SSE body and forwards mapped deltas until EOF, a read
// error, caller Close, or context cancellation. The channel is always closed
// on exit. A single "error" delta is emitted first when the stream ends
// without a "done" event (server dropped the connection mid-stream) — unless
// the server already sent an "error" event, or the stop was caller-initiated
// (body Close or context cancellation).
func (c *httpHermesClient) streamLoop(ctx context.Context, body *closeAwareBody, ch chan<- SSEDelta) {
	defer close(ch)
	defer func() { _ = body.Close() }()

	// If the caller's context is canceled, close the body to unblock the SSE
	// read and end the stream (prevents goroutine leaks).
	stopWatcher := make(chan struct{})
	defer close(stopWatcher)
	go func() {
		select {
		case <-ctx.Done():
			_ = body.Close()
		case <-stopWatcher:
		}
	}()

	sawDone := false
	sawError := false
	err := parseSSE(body, func(ev HermesSSEEvent, raw string) {
		if ev.Event == "done" {
			sawDone = true
		}
		if ev.Event == "error" {
			sawError = true
		}
		ch <- mapSSEEvent(ev, raw)
	})
	if err == nil && sawDone {
		return // clean end: the stream completed with a done event
	}
	if body.closed.Load() || ctx.Err() != nil {
		return // caller-initiated stop, not a stream failure
	}
	if !sawError {
		// The server dropped the connection before the stream completed:
		// emit one error delta before closing the channel.
		msg := "stream ended unexpectedly"
		if err != nil {
			msg = err.Error()
		}
		ch <- SSEDelta{Type: "error", Content: msg}
	}
}

// CreateConversation creates a new Hermes conversation via POST
// /v1/conversations with the manifest in the body and the profile token as
// the Bearer credential. Returns the conversation ID from the response
// ("conversation_id", falling back to "id").
func (c *httpHermesClient) CreateConversation(ctx context.Context, profileToken string, manifest *ContextManifest) (string, error) {
	if manifest == nil {
		return "", errors.New("hermes: create conversation: nil manifest")
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("hermes: create conversation: encode manifest: %w", err)
	}
	resp, err := c.doWithRetry(ctx, func(ctx context.Context) (*http.Response, error) {
		r, err := c.newRequest(ctx, http.MethodPost, "/v1/conversations", bytes.NewReader(body), profileToken, false)
		if err != nil {
			return nil, err
		}
		return c.httpClient.Do(r)
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", mapHTTPError(resp.StatusCode, resp.Header, raw)
	}
	var out struct {
		ConversationID string `json:"conversation_id"`
		ID             string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("hermes: create conversation: decode response: %w", err)
	}
	if out.ConversationID != "" {
		return out.ConversationID, nil
	}
	return out.ID, nil
}

// ListModels returns available models from GET /v1/models.
func (c *httpHermesClient) ListModels(ctx context.Context) ([]ModelInfo, error) {
	raw, err := c.getWithRetry(ctx, "/v1/models")
	if err != nil {
		return nil, err
	}
	models, err := decodeList[ModelInfo](raw, "models")
	if err != nil {
		return nil, fmt.Errorf("hermes: list models: %w", err)
	}
	return models, nil
}

// ListSkills returns available skills from GET /v1/skills. A 404 is treated
// as "no skills" (empty slice, nil error): older gateways may not expose the
// endpoint (SPEC-FTR-07 §2 decision 19).
func (c *httpHermesClient) ListSkills(ctx context.Context) ([]SkillInfo, error) {
	resp, err := c.doWithRetry(ctx, func(ctx context.Context) (*http.Response, error) {
		r, err := c.newRequest(ctx, http.MethodGet, "/v1/skills", nil, "", false)
		if err != nil {
			return nil, err
		}
		return c.httpClient.Do(r)
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return []SkillInfo{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, mapHTTPError(resp.StatusCode, resp.Header, raw)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("hermes: list skills: read response: %w", err)
	}
	skills, err := decodeList[SkillInfo](raw, "skills")
	if err != nil {
		return nil, fmt.Errorf("hermes: list skills: %w", err)
	}
	return skills, nil
}

// getWithRetry performs a retrying GET and returns the response body bytes
// for a 200, or a mapped error otherwise.
func (c *httpHermesClient) getWithRetry(ctx context.Context, path string) ([]byte, error) {
	resp, err := c.doWithRetry(ctx, func(ctx context.Context) (*http.Response, error) {
		r, err := c.newRequest(ctx, http.MethodGet, path, nil, "", false)
		if err != nil {
			return nil, err
		}
		return c.httpClient.Do(r)
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, mapHTTPError(resp.StatusCode, resp.Header, raw)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// decodeList decodes a list response that is either a bare JSON array or an
// object wrapping the array under the given key (e.g. {"models": [...]}).
func decodeList[T any](raw []byte, key string) ([]T, error) {
	var out []T
	if err := json.Unmarshal(raw, &out); err == nil {
		return out, nil
	}
	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, errors.New("decode list response: not a JSON array or object")
	}
	items, ok := wrapped[key]
	if !ok {
		return nil, fmt.Errorf("decode list response: missing %q field", key)
	}
	if err := json.Unmarshal(items, &out); err != nil {
		return nil, fmt.Errorf("decode list response: %q field: %w", key, err)
	}
	return out, nil
}
