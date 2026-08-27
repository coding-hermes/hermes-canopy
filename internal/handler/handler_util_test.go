// Package handler — unit tests for the lenient node-body decoder (GAP-053).
// Node endpoints (create/reply/fork/update) accept both camelCase and
// snake_case field names; genuinely unknown fields fail with the field named.
// These tests require no database.
package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// nodeBody decodes body into target via decodeNodeJSON and returns the error.
func nodeBody(t *testing.T, body string, target any) error {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	return decodeNodeJSON(r, target)
}

// replyRequest mirrors the anonymous request struct in handleReply/handleFork.
type replyRequest struct {
	Content       string          `json:"content"`
	ContentFormat string          `json:"content_format,omitempty"`
	NodeType      string          `json:"node_type,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

func TestDecodeNodeJSON_CamelCase(t *testing.T) {
	var req replyRequest
	err := nodeBody(t, `{"content":"hi","contentFormat":"markdown","nodeType":"message"}`, &req)
	if err != nil {
		t.Fatalf("camelCase body rejected: %v", err)
	}
	if req.Content != "hi" || req.ContentFormat != "markdown" || req.NodeType != "message" {
		t.Fatalf("camelCase fields not mapped: %+v", req)
	}
}

func TestDecodeNodeJSON_SnakeCase(t *testing.T) {
	var req replyRequest
	err := nodeBody(t, `{"content":"hi","content_format":"plain","node_type":"system"}`, &req)
	if err != nil {
		t.Fatalf("snake_case body rejected: %v", err)
	}
	if req.ContentFormat != "plain" || req.NodeType != "system" {
		t.Fatalf("snake_case fields not mapped: %+v", req)
	}
}

func TestDecodeNodeJSON_MixedCase(t *testing.T) {
	var req replyRequest
	err := nodeBody(t, `{"content":"hi","content_format":"plain","nodeType":"message"}`, &req)
	if err != nil {
		t.Fatalf("mixed-case body rejected: %v", err)
	}
	if req.ContentFormat != "plain" || req.NodeType != "message" {
		t.Fatalf("mixed-case fields not mapped: %+v", req)
	}
}

func TestDecodeNodeJSON_UnknownFieldNamesTheField(t *testing.T) {
	var req replyRequest
	err := nodeBody(t, `{"content":"hi","contentFormat":"markdown","bogus":true}`, &req)
	if err == nil {
		t.Fatal("unknown field accepted, want error")
	}
	msg := invalidNodeBodyMessage(err)
	if !strings.Contains(msg, "unknown field") || !strings.Contains(msg, "bogus") {
		t.Fatalf("error %q does not name the unknown field", msg)
	}
	if strings.Contains(msg, "must be valid JSON") {
		t.Fatalf("unknown field misreported as invalid JSON: %q", msg)
	}
}

func TestDecodeNodeJSON_MalformedJSON(t *testing.T) {
	var req replyRequest
	err := nodeBody(t, `{"content":`, &req)
	if err == nil {
		t.Fatal("malformed JSON accepted, want error")
	}
	if msg := invalidNodeBodyMessage(err); msg != "request body must be valid JSON" {
		t.Fatalf("malformed JSON message = %q, want generic validity message", msg)
	}
}

func TestDecodeNodeJSON_TypeMismatchNamesField(t *testing.T) {
	var req replyRequest
	err := nodeBody(t, `{"content":42}`, &req)
	if err == nil {
		t.Fatal("type mismatch accepted, want error")
	}
	msg := invalidNodeBodyMessage(err)
	if !strings.Contains(msg, "content") {
		t.Fatalf("type-error message %q does not name the field", msg)
	}
}

func TestDecodeNodeJSON_TreeCreateShapeFields(t *testing.T) {
	// Create-site shape: parent_id/edge_type plus their camelCase aliases.
	var req struct {
		ParentID      string          `json:"parent_id"`
		Content       string          `json:"content"`
		ContentFormat string          `json:"content_format,omitempty"`
		NodeType      string          `json:"node_type,omitempty"`
		EdgeType      string          `json:"edge_type,omitempty"`
		Metadata      json.RawMessage `json:"metadata,omitempty"`
	}
	err := nodeBody(t, `{"content":"hi","parentId":"e6c1c414-d6a2-4e10-8f4e-1a2b3c4d5e6f","edgeType":"reply"}`, &req)
	if err != nil {
		t.Fatalf("tree-create-style camelCase rejected: %v", err)
	}
	if req.ParentID != "e6c1c414-d6a2-4e10-8f4e-1a2b3c4d5e6f" || req.EdgeType != "reply" {
		t.Fatalf("parentId/edgeType not mapped: %+v", req)
	}
}

func TestDecodeNodeJSON_SnakeWinsOverCamel(t *testing.T) {
	var req replyRequest
	err := nodeBody(t, `{"content":"hi","content_format":"plain","contentFormat":"markdown"}`, &req)
	if err != nil {
		t.Fatalf("both-casings body rejected: %v", err)
	}
	if req.ContentFormat != "plain" {
		t.Fatalf("expected snake_case to win, got %q", req.ContentFormat)
	}
}

// decodeJSON (the shared strict decoder) must remain untouched: camelCase on
// non-node handlers still fails exactly as before.
func TestDecodeJSON_StrictUnchanged(t *testing.T) {
	var req replyRequest
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"content":"hi","contentFormat":"markdown"}`))
	if err := decodeJSON(r, &req); err == nil {
		t.Fatal("decodeJSON accepted camelCase; strict behavior changed")
	}
}

// Round-trip sanity: normalized re-encode must preserve raw metadata payloads
// (json.RawMessage fields pass through the map untouched).
func TestDecodeNodeJSON_MetadataRoundTrip(t *testing.T) {
	var req replyRequest
	raw := `{"content":"hi","metadata":{"k":"v","n":1},"contentFormat":"plain"}`
	if err := nodeBody(t, raw, &req); err != nil {
		t.Fatalf("metadata body rejected: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(req.Metadata, &meta); err != nil {
		t.Fatalf("metadata not preserved: %v (%s)", err, string(req.Metadata))
	}
	if meta["k"] != "v" || meta["n"] != float64(1) {
		t.Fatalf("metadata values wrong: %s", string(req.Metadata))
	}
}
