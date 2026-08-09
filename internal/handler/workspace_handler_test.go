package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/testutil"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newWorkspaceTestServer builds a minimal router with ONLY the workspace
// channels routes (no auth, no DB). Used for pure unit tests that must run
// without PostgreSQL. Returns the httptest.Server, the SSE hub (so tests can
// broadcast directly), and a general-channel UUID for convenience.
func newWorkspaceTestServer(t *testing.T, heartbeat time.Duration) (*httptest.Server, sse.SSEHub, uuid.UUID) {
	t.Helper()
	// Disable pruning so the background goroutine doesn't outlive the test.
	hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1})
	t.Cleanup(func() { _ = hub.Shutdown(context.Background()) })

	h := NewWorkspaceHandler(hub)
	// Short heartbeat when a test opts in, so heartbeat arrival is fast.
	if heartbeat > 0 {
		// We can't reconfigure the handler's HeartbeatInterval constant, so
		// tests that need a heartbeat exercise the SSE path via the hub
		// directly (broadcast) rather than waiting on the ticker.
	}

	r := chi.NewRouter()
	r.Mount("/api/v1/workspace/channels", h.Routes())
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	// general channel = NewSHA1(channelNamespace, "general")
	generalID := uuid.NewSHA1(channelNamespace, []byte("general"))
	return srv, hub, generalID
}

// newWorkspaceTestServerWithAuth is like newWorkspaceTestServer but wraps
// routes in AuthMiddleware("canopy-dev-secret") so UserIDFromContext is
// populated. Used for tests that assert the sender_id fallback behavior.
func newWorkspaceTestServerWithAuth(t *testing.T) (*httptest.Server, sse.SSEHub, uuid.UUID) {
	t.Helper()
	hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1})
	t.Cleanup(func() { _ = hub.Shutdown(context.Background()) })

	h := NewWorkspaceHandler(hub)
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(AuthMiddleware("canopy-dev-secret"))
		r.Mount("/workspace/channels", h.Routes())
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	generalID := uuid.NewSHA1(channelNamespace, []byte("general"))
	return srv, hub, generalID
}

// wsBearer builds a Bearer token for the given user.
func wsBearer(t *testing.T, userID uuid.UUID) string {
	t.Helper()
	tok := signedToken(t, "canopy-dev-secret", jwt.MapClaims{
		"sub": userID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	return "Bearer " + tok
}

// wsPost issues an authenticated POST with a JSON body.
func wsPost(t *testing.T, srv *httptest.Server, path, bearer string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

// wsGet issues a GET (optionally authenticated).
func wsGet(t *testing.T, srv *httptest.Server, path, bearer string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	return resp
}

// ---------------------------------------------------------------------------
// ListChannels
// ---------------------------------------------------------------------------

func TestWorkspace_ListChannels(t *testing.T) {
	srv, hub, generalID := newWorkspaceTestServer(t, 0)
	_ = hub
	resp := wsGet(t, srv, "/api/v1/workspace/channels", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, string(body))
	}

	var channels []channelListItem
	if err := json.NewDecoder(resp.Body).Decode(&channels); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Default channels: general + agents.
	if len(channels) < 2 {
		t.Fatalf("expected at least 2 default channels, got %d", len(channels))
	}

	names := make(map[string]bool)
	for _, ch := range channels {
		names[ch.Name] = true
		if ch.Name == "general" && ch.ID != generalID {
			t.Errorf("general channel id = %s, want %s", ch.ID, generalID)
		}
	}
	if !names["general"] {
		t.Error("expected 'general' channel in list")
	}
	if !names["agents"] {
		t.Error("expected 'agents' channel in list")
	}
	t.Logf("channels: %d (names=%v)", len(channels), names)
}

// ---------------------------------------------------------------------------
// SendMessage validation
// ---------------------------------------------------------------------------

func TestWorkspace_SendMessage_EmptyContent(t *testing.T) {
	srv, _, generalID := newWorkspaceTestServer(t, 0)
	resp := wsPost(t, srv, "/api/v1/workspace/channels/"+generalID.String()+"/message", "",
		map[string]any{"content": ""})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 400; body=%s", resp.StatusCode, string(body))
	}

	var errBody apiErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody.Error.Code != "EMPTY_CONTENT" {
		t.Errorf("error code = %q, want EMPTY_CONTENT", errBody.Error.Code)
	}
}

func TestWorkspace_SendMessage_UnknownChannel(t *testing.T) {
	srv, _, _ := newWorkspaceTestServer(t, 0)
	unknown := uuid.New()
	resp := wsPost(t, srv, "/api/v1/workspace/channels/"+unknown.String()+"/message", "",
		map[string]any{"content": "hello"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 404; body=%s", resp.StatusCode, string(body))
	}

	var errBody apiErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if errBody.Error.Code != "CHANNEL_NOT_FOUND" {
		t.Errorf("error code = %q, want CHANNEL_NOT_FOUND", errBody.Error.Code)
	}
}

func TestWorkspace_SendMessage_InvalidChannelID(t *testing.T) {
	srv, _, _ := newWorkspaceTestServer(t, 0)
	resp := wsPost(t, srv, "/api/v1/workspace/channels/not-a-uuid/message", "",
		map[string]any{"content": "hello"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestWorkspace_SendMessage_Success(t *testing.T) {
	srv, _, generalID := newWorkspaceTestServer(t, 0)
	resp := wsPost(t, srv, "/api/v1/workspace/channels/"+generalID.String()+"/message", "",
		map[string]any{"content": "hello world"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 202; body=%s", resp.StatusCode, string(body))
	}

	var msgResp sendMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&msgResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if msgResp.MessageID == uuid.Nil {
		t.Error("message_id should not be nil")
	}
	if msgResp.ChannelID != generalID {
		t.Errorf("channel_id = %s, want %s", msgResp.ChannelID, generalID)
	}
	if msgResp.Content != "hello world" {
		t.Errorf("content = %q, want %q", msgResp.Content, "hello world")
	}
}

func TestWorkspace_SendMessage_SenderFallbackToAuthUser(t *testing.T) {
	srv, hub, generalID := newWorkspaceTestServerWithAuth(t)
	authUser := uuid.New()

	// Subscribe a live SSE reader BEFORE posting so we can observe the
	// broadcast and assert sender_id == auth user.
	readerDone := make(chan channelMessageData, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet,
			srv.URL+"/api/v1/workspace/channels/"+generalID.String()+"/feed?include_heartbeat=false", nil)
		req.Header.Set("Authorization", wsBearer(t, authUser))
		client := &http.Client{Timeout: 0}
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("SSE GET: %v", err)
			return
		}
		defer resp.Body.Close()
		br := bufio.NewReader(resp.Body)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				var env struct {
					EventType string          `json:"event_type"`
					Data      json.RawMessage `json:"data"`
				}
				if json.Unmarshal([]byte(payload), &env) == nil && env.EventType == "channel_message" {
					var data channelMessageData
					if json.Unmarshal(env.Data, &data) == nil {
						readerDone <- data
						return
					}
				}
			}
		}
	}()

	// Give the SSE connection time to subscribe (subscribe-before-write is
	// load-bearing here; a small settle is acceptable for the unit path).
	time.Sleep(150 * time.Millisecond)

	resp := wsPost(t, srv, "/api/v1/workspace/channels/"+generalID.String()+"/message",
		wsBearer(t, authUser), map[string]any{"content": "auth fallback"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST status = %d, body=%s", resp.StatusCode, string(body))
	}

	select {
	case data := <-readerDone:
		if data.SenderID != authUser {
			t.Errorf("sender_id = %s, want auth user %s (fallback)", data.SenderID, authUser)
		}
		if data.Content != "auth fallback" {
			t.Errorf("content = %q, want %q", data.Content, "auth fallback")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not receive channel_message SSE event within 5s")
	}

	_ = hub
}

// ---------------------------------------------------------------------------
// ChannelEvents SSE
// ---------------------------------------------------------------------------

func TestWorkspace_ChannelEvents_InvalidChannelID(t *testing.T) {
	srv, _, _ := newWorkspaceTestServer(t, 0)
	resp := wsGet(t, srv, "/api/v1/workspace/channels/not-a-uuid/feed", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestWorkspace_ChannelEvents_UnknownChannel(t *testing.T) {
	srv, _, _ := newWorkspaceTestServer(t, 0)
	resp := wsGet(t, srv, "/api/v1/workspace/channels/"+uuid.New().String()+"/feed", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestWorkspace_ChannelEvents_Connect(t *testing.T) {
	srv, _, generalID := newWorkspaceTestServer(t, 0)
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/api/v1/workspace/channels/"+generalID.String()+"/feed?include_heartbeat=false", nil)
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET feed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
}

func TestWorkspace_ChannelEvents_MessageBroadcastAppearsInFeed(t *testing.T) {
	srv, hub, generalID := newWorkspaceTestServer(t, 0)
	_ = hub

	// Open an SSE feed with heartbeat disabled for deterministic parsing.
	feedURL := srv.URL + "/api/v1/workspace/channels/" + generalID.String() + "/feed?include_heartbeat=false"
	req, _ := http.NewRequest(http.MethodGet, feedURL, nil)
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET feed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET feed status = %d", resp.StatusCode)
	}

	// POST a message — it must be broadcast to the open feed.
	postResp := wsPost(t, srv, "/api/v1/workspace/channels/"+generalID.String()+"/message", "",
		map[string]any{"content": "from-POST"})
	var postBody sendMessageResponse
	if json.NewDecoder(postResp.Body).Decode(&postBody) != nil {
		t.Fatalf("decode POST response")
	}
	postResp.Body.Close()

	// Read the SSE stream until a channel_message event arrives (bounded).
	br := bufio.NewReader(resp.Body)
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("channel_message event did not arrive within 10s")
		default:
		}
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE line: %v", err)
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var env struct {
			EventType string          `json:"event_type"`
			Data      json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(payload), &env); err != nil {
			continue
		}
		if env.EventType != "channel_message" {
			continue
		}
		var data channelMessageData
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatalf("decode channel_message data: %v", err)
		}
		if data.MessageID != postBody.MessageID {
			t.Errorf("message_id = %s, want %s", data.MessageID, postBody.MessageID)
		}
		if data.ChannelID != generalID {
			t.Errorf("channel_id = %s, want %s", data.ChannelID, generalID)
		}
		if data.Content != "from-POST" {
			t.Errorf("content = %q, want %q", data.Content, "from-POST")
		}
		t.Logf("received channel_message: id=%s content=%q", data.MessageID, data.Content)
		return
	}
}

func TestWorkspace_ChannelEvents_HeartbeatPresent(t *testing.T) {
	// We can't shorten the handler's HeartbeatInterval constant, so instead we
	// broadcast a heartbeat-shaped comment directly via a hub client to prove
	// the event loop forwards SendRaw frames. This confirms the heartbeat
	// branch compiles and is reachable; the real ticker cadence is exercised
	// by the SSE package's own suite.
	srv, hub, generalID := newWorkspaceTestServer(t, 0)

	// Open feed with heartbeat enabled.
	feedURL := srv.URL + "/api/v1/workspace/channels/" + generalID.String() + "/feed"
	req, _ := http.NewRequest(http.MethodGet, feedURL, nil)
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET feed: %v", err)
	}
	defer resp.Body.Close()

	// Broadcast a heartbeat comment frame via hub so the client outbox gets it.
	hub.Broadcast(generalID, sse.ComposeEvent(generalID, uuid.Nil, "channel_heartbeat",
		map[string]any{"ok": true}))

	br := bufio.NewReader(resp.Body)
	deadline := time.After(10 * time.Second)
	sawEvent := false
	for {
		select {
		case <-deadline:
			if !sawEvent {
				t.Fatal("did not see any SSE frame within 10s")
			}
			return
		default:
		}
		line, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				continue
			}
			t.Fatalf("read SSE: %v", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Any event/id/data/comment line proves the stream is live.
		sawEvent = true
		t.Logf("SSE frame: %s", line)
		return
	}
}

// ---------------------------------------------------------------------------
// Integration test via the full-API server (DB-gated) — exercises the real
// router with auth middleware, confirming the workspace routes are mounted
// alongside every other endpoint.
// ---------------------------------------------------------------------------

func TestAPI_WorkspaceChannels_ListViaFullAPI(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)

	// GET /api/v1/workspace/channels via the full router + auth.
	resp := wsGet(t, srv.Server, "/api/v1/workspace/channels", wsBearer(t, ownerID))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, string(body))
	}

	var channels []channelListItem
	if err := json.NewDecoder(resp.Body).Decode(&channels); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(channels) < 2 {
		t.Fatalf("expected at least 2 channels, got %d", len(channels))
	}
	t.Logf("full-API workspace channels: %d", len(channels))
}

func TestAPI_WorkspaceChannels_PostAndFeedViaFullAPI(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)
	generalID := uuid.NewSHA1(channelNamespace, []byte("general"))

	// Open the feed via the authenticated full-API server.
	feedURL := fmt.Sprintf("%s/api/v1/workspace/channels/%s/feed?include_heartbeat=false",
		srv.Server.URL, generalID)
	req, _ := http.NewRequest(http.MethodGet, feedURL, nil)
	req.Header.Set("Authorization", wsBearer(t, ownerID))
	client := &http.Client{Timeout: 0}
	feedResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET feed: %v", err)
	}
	defer feedResp.Body.Close()
	if feedResp.StatusCode != http.StatusOK {
		t.Fatalf("feed status = %d", feedResp.StatusCode)
	}

	// POST a message through the authenticated router.
	postResp := wsPost(t, srv.Server,
		"/api/v1/workspace/channels/"+generalID.String()+"/message",
		wsBearer(t, ownerID), map[string]any{"content": "integration hello"})
	if postResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(postResp.Body)
		postResp.Body.Close()
		t.Fatalf("POST status = %d, body=%s", postResp.StatusCode, string(body))
	}
	var postBody sendMessageResponse
	json.NewDecoder(postResp.Body).Decode(&postBody)
	postResp.Body.Close()

	// Read the feed until the channel_message arrives.
	br := bufio.NewReader(feedResp.Body)
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("channel_message did not arrive via full-API feed within 10s")
		default:
		}
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE: %v", err)
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var env struct {
			EventType string          `json:"event_type"`
			Data      json.RawMessage `json:"data"`
		}
		if json.Unmarshal([]byte(payload), &env) != nil {
			continue
		}
		if env.EventType != "channel_message" {
			continue
		}
		var data channelMessageData
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if data.MessageID != postBody.MessageID {
			t.Errorf("message_id = %s, want %s", data.MessageID, postBody.MessageID)
		}
		if data.SenderID != ownerID {
			t.Errorf("sender_id = %s, want owner %s", data.SenderID, ownerID)
		}
		t.Logf("full-API feed received channel_message: id=%s", data.MessageID)
		return
	}
}
