package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/telemetry"
)

const networkProxyBodyLimit = 10 * 1024 * 1024

type NetworkProxyHandler struct{ client *http.Client }

func NewNetworkProxyHandler() *NetworkProxyHandler {
	return &NetworkProxyHandler{client: &http.Client{Timeout: 30 * time.Second}}
}

type networkProxyRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

func (h *NetworkProxyHandler) Proxy(w http.ResponseWriter, r *http.Request) {
	var input networkProxyRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "request body must be valid JSON")
		return
	}
	target, err := url.Parse(input.URL)
	if err != nil || target.Scheme != "https" || target.Host == "" {
		writeError(w, 400, "INVALID_URL", "url must be an absolute HTTPS URL")
		return
	}
	method := input.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if len(input.Body) > 0 {
		if input.Body[0] == '"' {
			var value string
			if json.Unmarshal(input.Body, &value) == nil {
				body = strings.NewReader(value)
			}
		} else {
			body = strings.NewReader(string(input.Body))
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		writeError(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	for key, value := range input.Headers {
		if strings.EqualFold(key, "Cookie") || strings.EqualFold(key, "Authorization") {
			continue
		}
		req.Header.Set(key, value)
	}
	started := time.Now()
	resp, err := h.client.Do(req)
	if err != nil {
		// The one place a proxied call can lose to the clock: classify it as
		// timeout rather than a plain error so "gateway is slow" and "gateway
		// is broken" are separable on canopy_upstream_proxy_duration_seconds.
		telemetry.ObserveUpstreamProxyDuration(proxyErrorOutcome(ctx, err), time.Since(started).Seconds())
		writeError(w, 502, "NETWORK_ERROR", err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, networkProxyBodyLimit+1))
	if err != nil {
		telemetry.ObserveUpstreamProxyDuration(telemetry.UpstreamProxyOutcomeError, time.Since(started).Seconds())
		writeError(w, 502, "NETWORK_ERROR", err.Error())
		return
	}
	if len(payload) > networkProxyBodyLimit {
		telemetry.ObserveUpstreamProxyDuration(telemetry.UpstreamProxyOutcomeError, time.Since(started).Seconds())
		writeError(w, 413, "PAYLOAD_TOO_LARGE", "upstream response exceeds 10 MB")
		return
	}
	headers := make(map[string]string, len(resp.Header))
	for key, values := range resp.Header {
		headers[key] = strings.Join(values, ", ")
	}
	elapsed := time.Since(started)
	telemetry.ObserveUpstreamProxyDuration(proxyOutcome(resp.StatusCode), elapsed.Seconds())
	writeJSON(w, 200, map[string]any{"status": resp.StatusCode, "statusText": http.StatusText(resp.StatusCode), "headers": headers, "body": string(payload), "durationMs": time.Since(started).Milliseconds()})
}

// proxyOutcome maps the upstream status Canopy relays to its caller onto the
// closed outcome set of canopy_upstream_proxy_duration_seconds: a 2xx/3xx
// answer is "ok", anything else (a gateway 4xx/5xx) is "error". It reads the
// status the handler already writes into the response body — never a URL, user
// id, tree id or node id, which would mint one series per request.
func proxyOutcome(status int) string {
	if status >= 200 && status < 400 {
		return telemetry.UpstreamProxyOutcomeOK
	}
	return telemetry.UpstreamProxyOutcomeError
}

// proxyErrorOutcome classifies a failed upstream call: an exhausted deadline is
// "timeout" (Canopy gave up at its own 30s budget), anything else is "error".
func proxyErrorOutcome(ctx context.Context, err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return telemetry.UpstreamProxyOutcomeTimeout
	}
	return telemetry.UpstreamProxyOutcomeError
}
