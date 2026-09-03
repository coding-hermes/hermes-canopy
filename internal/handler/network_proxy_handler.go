package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
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
		writeError(w, 502, "NETWORK_ERROR", err.Error())
		return
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, networkProxyBodyLimit+1))
	if err != nil {
		writeError(w, 502, "NETWORK_ERROR", err.Error())
		return
	}
	if len(payload) > networkProxyBodyLimit {
		writeError(w, 413, "PAYLOAD_TOO_LARGE", "upstream response exceeds 10 MB")
		return
	}
	headers := make(map[string]string, len(resp.Header))
	for key, values := range resp.Header {
		headers[key] = strings.Join(values, ", ")
	}
	writeJSON(w, 200, map[string]any{"status": resp.StatusCode, "statusText": http.StatusText(resp.StatusCode), "headers": headers, "body": string(payload), "durationMs": time.Since(started).Milliseconds()})
}
