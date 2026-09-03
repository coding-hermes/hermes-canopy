package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func proxyRequest(t *testing.T, h *NetworkProxyHandler, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/network-proxy", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	h.Proxy(w, req)
	return w
}

func TestNetworkProxyHTTPSAndHeaderStrippingAndSuccess(t *testing.T) {
	var cookie, authorization, custom string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, authorization, custom = r.Header.Get("Cookie"), r.Header.Get("Authorization"), r.Header.Get("X-Custom")
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "ok")
	})
	h := NewNetworkProxyHandler()
	h.client = &http.Client{Transport: handlerTransport{handler: upstream}}
	if w := proxyRequest(t, h, map[string]any{"url": "http://example.com"}); w.Code != 400 || !strings.Contains(w.Body.String(), "INVALID_URL") {
		t.Fatalf("HTTP URL response = %d %s", w.Code, w.Body.String())
	}
	w := proxyRequest(t, h, map[string]any{"url": "https://upstream.test", "method": "POST", "headers": map[string]string{"Cookie": "secret", "Authorization": "Bearer secret", "X-Custom": "kept"}, "body": "hello"})
	if w.Code != 200 {
		t.Fatalf("proxy response = %d %s", w.Code, w.Body.String())
	}
	if cookie != "" || authorization != "" || custom != "kept" {
		t.Fatalf("forwarded headers cookie=%q authorization=%q custom=%q", cookie, authorization, custom)
	}
	var got struct {
		Status     int               `json:"status"`
		StatusText string            `json:"statusText"`
		Body       string            `json:"body"`
		Headers    map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != 201 || got.StatusText != "Created" || got.Body != "ok" || got.Headers["X-Upstream"] != "yes" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestNetworkProxyBodyCap(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.CopyN(w, zeroReader{}, networkProxyBodyLimit+1)
	})
	h := NewNetworkProxyHandler()
	h.client = &http.Client{Transport: handlerTransport{handler: upstream}}
	w := proxyRequest(t, h, map[string]any{"url": "https://upstream.test"})
	if w.Code != 413 || !strings.Contains(w.Body.String(), "PAYLOAD_TOO_LARGE") {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type handlerTransport struct{ handler http.Handler }

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.handler.ServeHTTP(w, req)
	return w.Result(), nil
}
