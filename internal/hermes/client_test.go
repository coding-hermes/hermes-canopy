package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient starts an httptest server with the given handler and returns
// a HermesClient pointed at it. cfgMutator may override client config
// (timeouts, retries, backoff) for the specific test.
func newTestClient(t *testing.T, handler http.HandlerFunc, cfgMutator func(*Config)) (HermesClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := DefaultConfig()
	cfg.BaseURL = srv.URL
	cfg.RetryBaseDelay = time.Millisecond // fast backoff for tests
	if cfgMutator != nil {
		cfgMutator(&cfg)
	}
	client, err := NewClient(cfg)
	require.NoError(t, err)
	return client, srv
}

func TestNewClientValidation(t *testing.T) {
	// Empty BaseURL is rejected.
	_, err := NewClient(Config{})
	require.Error(t, err)

	// Zero-valued fields fall back to DefaultConfig.
	client, err := NewClient(Config{BaseURL: "http://127.0.0.1:8642/"})
	require.NoError(t, err)
	hc, ok := client.(*httpHermesClient)
	require.True(t, ok)
	assert.Equal(t, "http://127.0.0.1:8642", hc.baseURL) // trailing slash trimmed
	assert.Equal(t, 3, hc.maxRetries)
	assert.Equal(t, time.Second, hc.retryBase)
	assert.Equal(t, 120*time.Second, hc.httpClient.Timeout)
}

func TestHealthSuccess(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/health", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"0.18.2","uptime_seconds":86400}`))
	}, nil)

	hs, err := client.Health(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ok", hs.Status)
	assert.Equal(t, "0.18.2", hs.Version)
	assert.Equal(t, int64(86400), hs.UptimeSec)
}

func TestHealthNon200(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}, nil)

	_, err := client.Health(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrHermesUnavailable), "want ErrHermesUnavailable, got %v", err)
	he, ok := IsHermesError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusInternalServerError, he.HTTPStatus)
	assert.True(t, he.IsRetryable)
}

func TestHealthRetryThenSuccess(t *testing.T) {
	var hits atomic.Int32
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","version":"0.18.2","uptime_seconds":10}`))
	}, nil)

	// Injected short delays: the startup probe schedule (1s/5s/15s) is
	// exercised via healthWithRetry with test-friendly delays.
	hc := client.(*httpHermesClient)
	hs, err := hc.healthWithRetry(context.Background(), []time.Duration{time.Millisecond, time.Millisecond})
	require.NoError(t, err)
	assert.Equal(t, "ok", hs.Status)
	assert.Equal(t, int32(3), hits.Load(), "server should have been hit 3 times (2 failures + 1 success)")
}

func TestHealthAllFail(t *testing.T) {
	var hits atomic.Int32
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}, nil)

	hc := client.(*httpHermesClient)
	_, err := hc.healthWithRetry(context.Background(), []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrHermesUnavailable))
	assert.Equal(t, int32(4), hits.Load(), "initial probe + 3 retries")
}

func TestSendMessageNonStreamSuccess(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/responses", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		_, _ = w.Write([]byte(`{
			"session_id":"sess-1",
			"conversation_id":"conv-1",
			"content":"Hello from Hermes",
			"finish_reason":"stop",
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"q\":\"go\"}"}}]
		}`))
	}, nil)

	resp, err := client.SendMessage(context.Background(), &SendMessageRequest{
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "sess-1", resp.SessionID)
	assert.Equal(t, "conv-1", resp.ConversationID)
	assert.Equal(t, "Hello from Hermes", resp.Content)
	assert.Equal(t, "stop", resp.FinishReason)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_1", resp.ToolCalls[0].ID)
	assert.Equal(t, "web_search", resp.ToolCalls[0].Function.Name)
	assert.Equal(t, `{"q":"go"}`, resp.ToolCalls[0].Function.Arguments)
}

func TestSendMessageAuthHeader(t *testing.T) {
	t.Run("token present", func(t *testing.T) {
		var gotAuth atomic.Value
		client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotAuth.Store(r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"session_id":"s","content":"ok","finish_reason":"stop"}`))
		}, nil)

		_, err := client.SendMessage(context.Background(), &SendMessageRequest{
			ProfileToken: "hprof_secret-token",
			Messages:     []ConversationMessage{{Role: "user", Content: "hi"}},
		})
		require.NoError(t, err)
		assert.Equal(t, "Bearer hprof_secret-token", gotAuth.Load())
	})

	t.Run("empty token sends no header", func(t *testing.T) {
		var gotAuth atomic.Value
		client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			gotAuth.Store(r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"session_id":"s","content":"ok","finish_reason":"stop"}`))
		}, nil)

		_, err := client.SendMessage(context.Background(), &SendMessageRequest{
			Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
		})
		require.NoError(t, err)
		assert.Equal(t, "", gotAuth.Load())
	})
}

func TestSendMessageRequestBody(t *testing.T) {
	treeID := uuid.New()
	rootID := uuid.New()
	nodeID := uuid.New()
	var gotBody map[string]any
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &gotBody))
		_, _ = w.Write([]byte(`{"session_id":"s","content":"ok","finish_reason":"stop"}`))
	}, nil)

	_, err := client.SendMessage(context.Background(), &SendMessageRequest{
		ProfileToken:   "tok",
		ConversationID: "conv-9",
		Model:          "deepseek-v4-flash",
		Messages:       []ConversationMessage{{Role: "user", Content: "hi"}},
		ContextManifest: &ContextManifest{
			TreeID:        treeID,
			RootNodeID:    rootID,
			BudgetTokens:  4000,
			ActiveProfile: "coding",
			Nodes:         []ContextManifestNode{{NodeID: nodeID, Role: "user", Content: "ctx", Timestamp: time.Unix(1700000000, 0)}},
		},
		Stream:      false,
		MaxTokens:   512,
		Temperature: 0.7,
	})
	require.NoError(t, err)

	// Wire contract: exact JSON field names, and the token must NEVER be serialized.
	assert.Equal(t, "conv-9", gotBody["conversation_id"])
	assert.Equal(t, "deepseek-v4-flash", gotBody["model"])
	assert.Equal(t, false, gotBody["stream"])
	assert.Equal(t, float64(512), gotBody["max_tokens"])
	assert.Equal(t, 0.7, gotBody["temperature"])
	_, hasToken := gotBody["profile_token"]
	assert.False(t, hasToken, "profile_token must not be serialized")

	manifest, ok := gotBody["context_manifest"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, treeID.String(), manifest["tree_id"])
	assert.Equal(t, rootID.String(), manifest["root_node_id"])
	assert.Equal(t, float64(4000), manifest["budget_tokens"])
	assert.Equal(t, "coding", manifest["active_profile"])
	nodes, ok := manifest["nodes"].([]any)
	require.True(t, ok)
	require.Len(t, nodes, 1)
}

func TestSendMessage401(t *testing.T) {
	var hits atomic.Int32
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"auth_failed","message":"token expired"}}`))
	}, nil)

	_, err := client.SendMessage(context.Background(), &SendMessageRequest{
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAuthFailed), "want ErrAuthFailed, got %v", err)
	he, ok := IsHermesError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusUnauthorized, he.HTTPStatus)
	assert.Equal(t, "auth_failed", he.HermesCode)
	assert.False(t, he.IsRetryable)
	assert.Equal(t, int32(1), hits.Load(), "401 must not be retried")
}

func TestSendMessage429(t *testing.T) {
	var hits atomic.Int32
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit","message":"slow down"}}`))
	}, func(cfg *Config) {
		cfg.MaxRetries = 1
	})

	_, err := client.SendMessage(context.Background(), &SendMessageRequest{
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimited), "want ErrRateLimited, got %v", err)
	he, ok := IsHermesError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusTooManyRequests, he.HTTPStatus)
	assert.Equal(t, 1, he.RetryAfter, "Retry-After header must be parsed")
	assert.True(t, he.IsRetryable)
	assert.Equal(t, int32(2), hits.Load(), "429 is retryable: initial + 1 retry")
}

func TestSendMessage500Retried(t *testing.T) {
	var hits atomic.Int32
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"session_id":"s","content":"recovered","finish_reason":"stop"}`))
	}, nil)

	resp, err := client.SendMessage(context.Background(), &SendMessageRequest{
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "recovered", resp.Content)
	assert.Equal(t, int32(3), hits.Load(), "2 failures + 1 success")
}

func TestSendMessageTimeout(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"session_id":"s","content":"late","finish_reason":"stop"}`))
	}, func(cfg *Config) {
		cfg.RequestTimeout = 50 * time.Millisecond
		cfg.MaxRetries = 1
	})

	start := time.Now()
	_, err := client.SendMessage(context.Background(), &SendMessageRequest{
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrHermesTimeout), "want ErrHermesTimeout, got %v", err)
	assert.Less(t, time.Since(start), 2*time.Second, "timeout test should fail fast")
}

func TestSendMessageStreamAggregation(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: delta\ndata: {\"content\":\"Hello\"}\n\n")
		_, _ = io.WriteString(w, "event: delta\ndata: {\"content\":\" world\"}\n\n")
		_, _ = io.WriteString(w, "event: tool_call\ndata: {\"tool_call_id\":\"call_1\",\"name\":\"web_search\",\"arguments\":\"{}\"}\n\n")
		_, _ = io.WriteString(w, "event: done\ndata: {\"session_id\":\"sess-9\",\"conversation_id\":\"conv-9\",\"finish_reason\":\"stop\"}\n\n")
	}, nil)

	resp, err := client.SendMessage(context.Background(), &SendMessageRequest{
		Stream:   true,
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello world", resp.Content)
	assert.Equal(t, "stop", resp.FinishReason)
	assert.Equal(t, "sess-9", resp.SessionID)
	assert.Equal(t, "conv-9", resp.ConversationID)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_1", resp.ToolCalls[0].ID)
	assert.Equal(t, "web_search", resp.ToolCalls[0].Function.Name)
	require.Len(t, resp.RawEvents, 4, "all raw SSE events must be captured")
	assert.Equal(t, "delta", resp.RawEvents[0].Event)
	assert.Equal(t, "done", resp.RawEvents[3].Event)
}

func TestStreamMessagesMultiEvent(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "text/event-stream", r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: delta\ndata: {\"content\":\"Hello\"}\n\n")
		_, _ = io.WriteString(w, "event: tool_call\ndata: {\"tool_call_id\":\"call_1\"}\n\n")
		_, _ = io.WriteString(w, "event: done\ndata: {}\n\n")
	}, nil)

	ch, body, err := client.StreamMessages(context.Background(), &SendMessageRequest{
		Stream:   true,
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	defer func() {
		require.NoError(t, body.Close())
	}()

	var deltas []SSEDelta
	for d := range ch {
		deltas = append(deltas, d)
	}
	require.Len(t, deltas, 3)
	assert.Equal(t, "delta", deltas[0].Type)
	assert.Equal(t, "Hello", deltas[0].Content)
	assert.Equal(t, "tool_call", deltas[1].Type)
	assert.Equal(t, "call_1", deltas[1].ToolID)
	assert.Equal(t, "done", deltas[2].Type)
}

func TestStreamMessagesMultilineData(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": this is a comment line\n")
		_, _ = io.WriteString(w, "event: delta\ndata: line one\ndata: line two\n\n")
		_, _ = io.WriteString(w, "event: done\ndata: {}\n\n")
	}, nil)

	ch, body, err := client.StreamMessages(context.Background(), &SendMessageRequest{
		Stream:   true,
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	defer body.Close()

	var deltas []SSEDelta
	for d := range ch {
		deltas = append(deltas, d)
	}
	require.Len(t, deltas, 2)
	assert.Equal(t, "delta", deltas[0].Type)
	assert.Equal(t, "line one\nline two", deltas[0].Content, "multi-line data joined with \\n, comments ignored")
	assert.Equal(t, "done", deltas[1].Type)
}

func TestStreamMessagesUnknownEventFallback(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: weird_event\ndata: {\"something\":\"else\"}\n\n")
		_, _ = io.WriteString(w, "event: done\ndata: {}\n\n")
	}, nil)

	ch, body, err := client.StreamMessages(context.Background(), &SendMessageRequest{
		Stream:   true,
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	defer body.Close()

	var deltas []SSEDelta
	for d := range ch {
		deltas = append(deltas, d)
	}
	require.Len(t, deltas, 2)
	assert.Equal(t, "delta", deltas[0].Type, "unknown event names fall back to delta")
	assert.Equal(t, `{"something":"else"}`, deltas[0].Content, "raw data preserved on fallback")
}

func TestStreamMessagesServerErrorMidStream(t *testing.T) {
	// The server sends a delta and a tool_call, then closes the connection
	// WITHOUT a done event: the stream is incomplete, so the client must
	// emit one "error" delta and then close the channel.
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: delta\ndata: {\"content\":\"partial\"}\n\n")
		_, _ = io.WriteString(w, "event: tool_call\ndata: {\"tool_call_id\":\"call_1\"}\n\n")
	}, nil)

	ch, body, err := client.StreamMessages(context.Background(), &SendMessageRequest{
		Stream:   true,
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	defer body.Close()

	var deltas []SSEDelta
	for d := range ch {
		deltas = append(deltas, d)
	}
	require.Len(t, deltas, 3, "2 events + 1 error delta")
	assert.Equal(t, "delta", deltas[0].Type)
	assert.Equal(t, "tool_call", deltas[1].Type)
	assert.Equal(t, "error", deltas[2].Type, "mid-stream close without done must emit an error delta")
}

func TestCreateConversationSuccess(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/conversations", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "Bearer hprof_tok", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		_, _ = w.Write([]byte(`{"conversation_id":"conv-42"}`))
	}, nil)

	id, err := client.CreateConversation(context.Background(), "hprof_tok", &ContextManifest{
		TreeID:        uuid.New(),
		RootNodeID:    uuid.New(),
		BudgetTokens:  4000,
		ActiveProfile: "coding",
	})
	require.NoError(t, err)
	assert.Equal(t, "conv-42", id)
}

func TestCreateConversation401(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"auth_failed","message":"invalid token"}}`))
	}, nil)

	_, err := client.CreateConversation(context.Background(), "bad-token", &ContextManifest{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAuthFailed), "want ErrAuthFailed, got %v", err)
	he, ok := IsHermesError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, he.HTTPStatus)
	assert.False(t, he.IsRetryable)
}

func TestListModelsSuccess(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		_, _ = w.Write([]byte(`{"models":[
			{"id":"deepseek-v4-flash","provider":"deepseek","context_length":128000,"supports_streaming":true},
			{"id":"gpt-5.6-sol","provider":"openai","context_length":200000,"supports_streaming":true},
			{"id":"kimi-k3","provider":"kimi","context_length":131072,"supports_streaming":false}
		]}`))
	}, nil)

	models, err := client.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 3)
	assert.Equal(t, "deepseek-v4-flash", models[0].ID)
	assert.Equal(t, "deepseek", models[0].Provider)
	assert.Equal(t, 128000, models[0].ContextLen)
	assert.True(t, models[0].SupportsStreaming)
	assert.False(t, models[2].SupportsStreaming)
}

func TestListModels500Retried(t *testing.T) {
	var hits atomic.Int32
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"models":[]}`))
	}, nil)

	models, err := client.ListModels(context.Background())
	require.NoError(t, err)
	assert.Empty(t, models)
	assert.Equal(t, int32(3), hits.Load())
}

func TestListSkillsSuccess(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/skills", r.URL.Path)
		_, _ = w.Write([]byte(`{"skills":[
			{"name":"web_search","description":"Search the web","permissions":["network"]},
			{"name":"file_read","description":"Read files","permissions":["fs.read"]}
		]}`))
	}, nil)

	skills, err := client.ListSkills(context.Background())
	require.NoError(t, err)
	require.Len(t, skills, 2)
	assert.Equal(t, "web_search", skills[0].Name)
	assert.Equal(t, []string{"network"}, skills[0].Permissions)
}

func TestListSkills404(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
	}, nil)

	// Older gateways may not expose /v1/skills: 404 → empty slice, nil error.
	skills, err := client.ListSkills(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, skills)
	assert.Empty(t, skills)
}

func TestValidateVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		wantErr bool
	}{
		{"exact minimum", "0.18.0", false},
		{"patch above minimum", "0.18.2", false},
		{"v prefix", "v0.18.2", false},
		{"major one", "1.0.0", false},
		{"major two", "2.5.1", false},
		{"below minimum", "0.17.9", true},
		{"empty", "", true},
		{"garbage", "not-a-version", true},
		{"major only", "0", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateVersion(tt.version)
			if tt.wantErr {
				assert.True(t, errors.Is(err, ErrVersionMismatch), "want ErrVersionMismatch, got %v", err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestIsHermesError(t *testing.T) {
	t.Run("typed error", func(t *testing.T) {
		he := &HermesError{HTTPStatus: 500, Message: "boom", IsRetryable: true}
		got, ok := IsHermesError(he)
		require.True(t, ok)
		assert.Same(t, he, got)
	})

	t.Run("wrapped sentinel", func(t *testing.T) {
		he := &HermesError{HTTPStatus: 401, Message: "nope", sentinel: ErrAuthFailed}
		wrapped := fmt.Errorf("send message: %w", he)
		got, ok := IsHermesError(wrapped)
		require.True(t, ok)
		assert.Same(t, he, got)
		assert.True(t, errors.Is(wrapped, ErrAuthFailed), "errors.Is must unwrap to the sentinel")
	})

	t.Run("plain error", func(t *testing.T) {
		_, ok := IsHermesError(errors.New("plain"))
		assert.False(t, ok)
	})

	t.Run("nil", func(t *testing.T) {
		_, ok := IsHermesError(nil)
		assert.False(t, ok)
	})
}

func TestHermesErrorString(t *testing.T) {
	he := &HermesError{HTTPStatus: 429, Message: "slow down"}
	assert.Equal(t, "hermes: slow down (HTTP 429)", he.Error())
}

func TestParseRetryAfter(t *testing.T) {
	h := http.Header{}
	assert.Equal(t, 0, parseRetryAfter(h))
	h.Set("Retry-After", "5")
	assert.Equal(t, 5, parseRetryAfter(h))
	h.Set("Retry-After", "not-a-number")
	assert.Equal(t, 0, parseRetryAfter(h))
}

func TestSendMessage404ModelNotFound(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"model_not_found","message":"model does not exist"}}`))
	}, nil)

	_, err := client.SendMessage(context.Background(), &SendMessageRequest{
		Model:    "nonexistent-model",
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrModelUnavailable), "want ErrModelUnavailable, got %v", err)
	he, ok := IsHermesError(err)
	require.True(t, ok)
	assert.Equal(t, http.StatusNotFound, he.HTTPStatus)
}

func TestSendMessage404SessionExpired(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"session_not_found","message":"session has expired"}}`))
	}, nil)

	_, err := client.SendMessage(context.Background(), &SendMessageRequest{
		ConversationID: "stale-conv",
		Messages:       []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSessionExpired), "want ErrSessionExpired, got %v", err)
}

func TestSendMessageNonJSONErrorBody(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream exploded"))
	}, nil)

	_, err := client.SendMessage(context.Background(), &SendMessageRequest{
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrHermesUnavailable))
	he, ok := IsHermesError(err)
	require.True(t, ok)
	assert.Equal(t, "upstream exploded", he.Message, "non-JSON body text used as message")
}

func TestStreamMessagesHTTPError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"auth_failed","message":"bad token"}}`))
	}, nil)

	ch, body, err := client.StreamMessages(context.Background(), &SendMessageRequest{
		Stream:   true,
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAuthFailed))
	assert.Nil(t, ch)
	assert.Nil(t, body)
}

func TestStreamMessagesContextCanceled(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: delta\ndata: {\"content\":\"one\"}\n\n")
		_, _ = io.WriteString(w, "event: delta\ndata: {\"content\":\"two\"}\n\n")
		_, _ = io.WriteString(w, "event: delta\ndata: {\"content\":\"three\"}\n\n")
		_, _ = io.WriteString(w, "event: done\ndata: {}\n\n")
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	ch, body, err := client.StreamMessages(ctx, &SendMessageRequest{
		Stream:   true,
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	// Drain one delta, then cancel: the stream must end without an error
	// delta (caller-initiated stop is not a stream failure).
	first := <-ch
	assert.Equal(t, "delta", first.Type)
	cancel()
	_ = body.Close()

	var deltas []SSEDelta
	for d := range ch {
		deltas = append(deltas, d)
	}
	for _, d := range deltas {
		assert.NotEqual(t, "error", d.Type, "caller cancellation must not emit an error delta")
	}
}

func TestStreamMessagesBodyCloseIdempotent(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: done\ndata: {}\n\n")
	}, nil)

	ch, body, err := client.StreamMessages(context.Background(), &SendMessageRequest{
		Stream:   true,
		Messages: []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	for range ch {
	}
	require.NoError(t, body.Close())
	require.NoError(t, body.Close(), "double Close must be safe")
}

func TestSendMessageNilRequest(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be hit")
	}, nil)
	_, err := client.SendMessage(context.Background(), nil)
	require.Error(t, err)
}

func TestCreateConversationNilManifest(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be hit")
	}, nil)
	_, err := client.CreateConversation(context.Background(), "tok", nil)
	require.Error(t, err)
}

func TestListModelsBareArray(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"m1","provider":"p1","context_length":100,"supports_streaming":true}]`))
	}, nil)

	models, err := client.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "m1", models[0].ID)
}

func TestHealthWithRetryContextCanceled(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	hc := client.(*httpHermesClient)
	_, err := hc.healthWithRetry(ctx, []time.Duration{time.Hour})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

func TestStreamMessagesRequestJSON(t *testing.T) {
	// The streaming request body must carry the same wire contract as the
	// non-streaming one (stream:true, no profile_token), and the token must
	// ride in the Authorization header.
	var gotBody map[string]any
	var gotAuth string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(raw, &gotBody))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: done\ndata: {}\n\n")
	}, nil)

	ch, body, err := client.StreamMessages(context.Background(), &SendMessageRequest{
		ProfileToken: "tok",
		Stream:       true,
		Messages:     []ConversationMessage{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	for range ch {
	}
	_ = body.Close()

	assert.Equal(t, true, gotBody["stream"])
	assert.Equal(t, "Bearer tok", gotAuth)
	_, hasToken := gotBody["profile_token"]
	assert.False(t, hasToken, "profile_token must not be serialized in streaming requests")
}
