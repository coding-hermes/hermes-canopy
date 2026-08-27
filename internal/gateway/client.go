// Package gateway implements a client for the Hermes gateway runs API
// (hermes gateway run — the api_server process) plus the Canopy-side run
// registry that turns gateway state into live dashboard data.
//
// The client adopts the hermes-webui gateway-client pattern
// (api/runner_client.py + api/gateway_chat.py): POST /v1/runs starts a run,
// GET /v1/runs/{id} polls status, GET /v1/runs/{id}/events streams SSE
// lifecycle events, POST /v1/runs/{id}/stop interrupts, and
// POST /v1/runs/{id}/approval resolves pending approvals. All requests carry
// an `Authorization: Bearer <api key>` header.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the default Hermes gateway api_server address, matching
// the hermes-webui default (http://127.0.0.1:8642).
const DefaultBaseURL = "http://127.0.0.1:8642"

// Client is a small JSON-over-HTTP client for the Hermes gateway runs API.
// It is transport-only: no run maps, queues, or cancellation state live here
// (mirrors hermes-webui's runner_client.py design).
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithHTTPClient overrides the underlying HTTP client (tests, custom timeouts).
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) { c.http = hc }
}

// NewClient constructs a gateway client. baseURL defaults to DefaultBaseURL
// when empty; apiKey may be empty (gateway without auth).
func NewClient(baseURL, apiKey string, opts ...ClientOption) (*Client, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("gateway: invalid base_url %q: %w", baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("gateway: base_url must be http(s); got scheme %q", u.Scheme)
	}
	c := &Client{
		baseURL: strings.TrimSuffix(base, "/"),
		apiKey:  strings.TrimSpace(apiKey),
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// BaseURL returns the configured base URL (useful for status surfacing).
func (c *Client) BaseURL() string { return c.baseURL }

// Health checks the gateway /v1/health endpoint. Returns nil when the
// gateway answers OK.
func (c *Client) Health(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("gateway health: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway health: HTTP %d", resp.StatusCode)
	}
	return nil
}

// StartRunRequest mirrors the gateway's POST /v1/runs body. The gateway
// accepts an OpenAI-style `input` (string or message list); we always send
// the string form, optionally with instructions / session scope / model.
type StartRunRequest struct {
	Input        string `json:"input"`
	Instructions string `json:"instructions,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	Model        string `json:"model,omitempty"`
}

// RunRef is the immediate result of starting a run (HTTP 202).
type RunRef struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

// StartRun creates a real Hermes agent run via POST /v1/runs.
func (c *Client) StartRun(ctx context.Context, req StartRunRequest) (RunRef, error) {
	var out RunRef
	err := c.doJSON(ctx, http.MethodPost, "/v1/runs", req, &out, http.StatusAccepted)
	return out, err
}

// RunStatus is the pollable run state from GET /v1/runs/{id}. Extra fields
// from the gateway (usage, output, error, last_event, created_at, model,
// session_id) are captured in Extra.
type RunStatus struct {
	Status    string `json:"status"`
	RunID     string `json:"run_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Model     string `json:"model,omitempty"`
	// Extra holds any additional gateway-provided status fields.
	Extra map[string]any `json:"-"`
}

// GetRun polls a run's status via GET /v1/runs/{id}.
func (c *Client) GetRun(ctx context.Context, runID string) (RunStatus, error) {
	var out RunStatus
	err := c.doJSON(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(runID), nil, &out, http.StatusOK)
	if err != nil {
		// 404 (run_not_found) is a normal terminal state for swept runs.
		if IsNotFound(err) {
			return RunStatus{Status: "not_found", RunID: runID}, nil
		}
		return out, err
	}
	out.RunID = runID
	return out, nil
}

// StopRun interrupts a running agent via POST /v1/runs/{id}/stop.
func (c *Client) StopRun(ctx context.Context, runID string) (RunRef, error) {
	var out RunRef
	err := c.doJSON(ctx, http.MethodPost, "/v1/runs/"+url.PathEscape(runID)+"/stop", map[string]any{}, &out, http.StatusOK)
	if err != nil {
		return out, err
	}
	if out.RunID == "" {
		out.RunID = runID
	}
	return out, nil
}

// RespondApproval resolves a pending run approval via
// POST /v1/runs/{id}/approval. choice is one of: once, session, always, deny.
func (c *Client) RespondApproval(ctx context.Context, runID, approvalID, choice string) error {
	body := map[string]any{"choice": choice}
	if approvalID != "" {
		body["approval_id"] = approvalID
	}
	var out map[string]any
	return c.doJSON(ctx, http.MethodPost, "/v1/runs/"+url.PathEscape(runID)+"/approval", body, &out, http.StatusOK)
}

// RespondClarify sends a clarification response. The live gateway api_server
// does not expose a clarifications route (hermes-webui's runner backend
// does); kept for contract parity with the hermes-webui client pattern.
func (c *Client) RespondClarify(ctx context.Context, runID, clarifyID, response string) error {
	var out map[string]any
	return c.doJSON(ctx, http.MethodPost,
		"/v1/runs/"+url.PathEscape(runID)+"/clarifications/"+url.PathEscape(clarifyID)+"/respond",
		map[string]any{"response": response}, &out, http.StatusOK)
}

// QueueMessage queues a follow-up message for a running agent. The live
// gateway api_server does not expose a messages route (hermes-webui's runner
// backend does); kept for contract parity with the hermes-webui client
// pattern.
func (c *Client) QueueMessage(ctx context.Context, runID, message string, mode string) error {
	if mode == "" {
		mode = "queue"
	}
	var out map[string]any
	return c.doJSON(ctx, http.MethodPost,
		"/v1/runs/"+url.PathEscape(runID)+"/messages",
		map[string]any{"message": message, "mode": mode}, &out, http.StatusOK)
}

// UpdateGoal sets a session goal. The live gateway api_server does not expose
// a session goal route (hermes-webui's runner backend does); kept for
// contract parity with the hermes-webui client pattern.
func (c *Client) UpdateGoal(ctx context.Context, sessionID, action, text string) error {
	var out map[string]any
	return c.doJSON(ctx, http.MethodPost,
		"/v1/sessions/"+url.PathEscape(sessionID)+"/goal",
		map[string]any{"action": action, "text": text}, &out, http.StatusOK)
}

// ObserveRun opens the SSE event stream for a run via
// GET /v1/runs/{id}/events. The caller owns the returned body and must close
// it. The gateway emits `data: {json}` lines plus `: keepalive` comments and
// closes the stream with a final comment once the run finishes.
func (c *Client) ObserveRun(ctx context.Context, runID string) (io.ReadCloser, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(runID)+"/events", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway observe %s: %w", runID, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("gateway observe %s: HTTP %d: %s", runID, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}

// ─── transport helpers ────────────────────────────────────────────────────

// APIError is a non-2xx gateway response with an OpenAI-style error body.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gateway: HTTP %d: %s", e.Status, e.Body)
}

// IsNotFound reports whether err is a gateway 404 (run_not_found).
func IsNotFound(err error) bool {
	ae, ok := err.(*APIError)
	return ok && ae.Status == http.StatusNotFound
}

func (c *Client) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("gateway: marshal request: %w", err)
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return nil, fmt.Errorf("gateway: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hermes-canopy-gateway-client")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return req, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any, wantStatus int) error {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("gateway %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("gateway %s %s: read body: %w", method, path, err)
	}
	if resp.StatusCode != wantStatus {
		return &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("gateway %s %s: decode response: %w", method, path, err)
	}
	return nil
}
