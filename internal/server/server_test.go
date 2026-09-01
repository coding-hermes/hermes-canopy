package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	healthHandler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", body["status"])
	}
	if body["service"] != "canopyd" {
		t.Errorf("expected service 'canopyd', got %v", body["service"])
	}
	// DF-HERMES-CANOPY-1: health now reports schema/embedded versions.
	for _, key := range []string{"schema_version", "embedded_migrations"} {
		if _, ok := body[key]; !ok {
			t.Errorf("health response missing %q", key)
		}
	}
}

func TestVersionHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	w := httptest.NewRecorder()

	versionHandler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["version"] != "dev" {
		t.Errorf("expected version 'dev', got %q", body["version"])
	}
}

func TestCorsMiddleware(t *testing.T) {
	mw := corsMiddleware("*")
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("sets CORS headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		resp := w.Result()
		if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("expected Access-Control-Allow-Origin '*', got %q", resp.Header.Get("Access-Control-Allow-Origin"))
		}
		if resp.Header.Get("Access-Control-Allow-Methods") == "" {
			t.Error("expected Access-Control-Allow-Methods to be set")
		}
		if resp.Header.Get("Access-Control-Allow-Headers") == "" {
			t.Error("expected Access-Control-Allow-Headers to be set")
		}
	})

	t.Run("handles OPTIONS preflight", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		resp := w.Result()
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("expected 204 No Content for OPTIONS, got %d", resp.StatusCode)
		}
	})

	t.Run("respects custom origin", func(t *testing.T) {
		customMW := corsMiddleware("http://example.com")
		customHandler := customMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		customHandler.ServeHTTP(w, req)

		resp := w.Result()
		if resp.Header.Get("Access-Control-Allow-Origin") != "http://example.com" {
			t.Errorf("expected Access-Control-Allow-Origin 'http://example.com', got %q", resp.Header.Get("Access-Control-Allow-Origin"))
		}
	})
}
