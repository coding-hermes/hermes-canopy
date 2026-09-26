package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/card"
	"github.com/coding-hermes/hermes-canopy/internal/card/iteration"
)

const iterationTestSecret = "iteration-handler-test-secret"

type iterationTestServer struct {
	t   *testing.T
	svc *iteration.IterationCardServiceImpl
	r   *chi.Mux
	h   *IterationCardHandler
	srv *httptest.Server
	mgr *card.CardDBManager
}

func newIterationTestServer(t *testing.T) *iterationTestServer {
	t.Helper()
	mgr := card.NewCardDBManager(t.TempDir())
	svc := iteration.NewIterationCardService(mgr)
	h := NewIterationCardHandler(svc).WithSSEHeartbeat(25 * time.Millisecond)
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(AuthMiddleware(iterationTestSecret))
		r.Mount("/cards/iteration", h.Routes())
		r.Get("/iteration/progress", h.Progress)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		_ = mgr.Close()
	})
	return &iterationTestServer{t: t, svc: svc, r: r, h: h, srv: srv, mgr: mgr}
}

func iterationToken(t *testing.T, role string) string {
	t.Helper()
	claims := jwt.MapClaims{"sub": uuid.New().String(), "exp": time.Now().Add(time.Hour).Unix()}
	if role != "" {
		claims["role"] = role
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(iterationTestSecret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func iterationRequest(t *testing.T, srv *httptest.Server, method, path, role string, body any, revision string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if role != "" {
		req.Header.Set("Authorization", "Bearer "+iterationToken(t, role))
	}
	if revision != "" {
		req.Header.Set("If-Match", revision)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeIterationCard(t *testing.T, resp *http.Response) card.Card {
	t.Helper()
	defer resp.Body.Close()
	var value card.Card
	if err := json.NewDecoder(resp.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func createIterationCard(t *testing.T, ts *iterationTestServer, subtype iteration.IterationSubtype, agentID string) card.Card {
	t.Helper()
	resp := iterationRequest(t, ts.srv, http.MethodPost, "/api/v1/cards/iteration", "agent", map[string]any{
		"subtype": subtype, "agentId": agentID, "appId": "canopy.agent",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create status = %d, body = %s", resp.StatusCode, body)
	}
	return decodeIterationCard(t, resp)
}

func sessionID(t *testing.T, value card.Card) uuid.UUID {
	t.Helper()
	var data struct {
		SessionID uuid.UUID `json:"sessionId"`
	}
	if err := json.Unmarshal(value.Data, &data); err != nil {
		t.Fatal(err)
	}
	return data.SessionID
}

type testIterationProcess struct{ id string }

func (p testIterationProcess) ID() string { return p.id }
func (p testIterationProcess) SubscribeFeedback(context.Context, uuid.UUID) (<-chan iteration.FeedbackEvent, error) {
	return make(chan iteration.FeedbackEvent), nil
}
func (p testIterationProcess) Cancel(context.Context, uuid.UUID) error { return nil }
func (p testIterationProcess) Status() iteration.AgentProcessStatus {
	return iteration.AgentProcessStatusRunning
}

func TestIterationRoutesMountedOnTheSameRouterShape(t *testing.T) {
	ts := newIterationTestServer(t)
	want := map[string]bool{
		"POST /api/v1/cards/iteration":             true,
		"GET /api/v1/cards/iteration/active":       true,
		"GET /api/v1/cards/iteration/{}":           true,
		"PATCH /api/v1/cards/iteration/{}":         true,
		"POST /api/v1/cards/iteration/{}/feedback": true,
		"POST /api/v1/cards/iteration/{}/cancel":   true,
		"GET /api/v1/cards/iteration/{}/events":    true,
		"GET /api/v1/iteration/progress":           true,
	}
	got := map[string]bool{}
	if err := chi.Walk(ts.r, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		pattern = strings.ReplaceAll(pattern, "{card_id}", "{}")
		pattern = strings.TrimSuffix(pattern, "/")
		got[method+" "+pattern] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for route := range want {
		if !got[route] {
			t.Errorf("missing route %s", route)
		}
	}
}

func TestIterationCreateGetAndInvalidSubtype(t *testing.T) {
	ts := newIterationTestServer(t)
	created := createIterationCard(t, ts, iteration.IterationSubtypeSearch, "agent-create")
	get := iterationRequest(t, ts.srv, http.MethodGet, "/api/v1/cards/iteration/"+created.ID.String(), "browser", nil, "")
	if got := decodeIterationCard(t, get); got.ID != created.ID || got.CardType != card.CardTypeIteration {
		t.Fatalf("GET card = %+v", got)
	}

	bad := iterationRequest(t, ts.srv, http.MethodPost, "/api/v1/cards/iteration", "agent", map[string]any{
		"subtype": "iteration_unknown", "agentId": "agent-bad",
	}, "")
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid subtype status = %d", bad.StatusCode)
	}
	var envelope map[string]map[string]string
	if err := json.NewDecoder(bad.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["error"]["code"] != "ITERATION_SUBTYPE_INVALID" {
		t.Fatalf("invalid subtype envelope = %v", envelope)
	}

	unknown := iterationRequest(t, ts.srv, http.MethodGet, "/api/v1/cards/iteration/"+uuid.New().String(), "browser", nil, "")
	defer unknown.Body.Close()
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown card status = %d", unknown.StatusCode)
	}
}

func TestIterationPatchIfMatchAndActorPermissions(t *testing.T) {
	ts := newIterationTestServer(t)
	created := createIterationCard(t, ts, iteration.IterationSubtypeSearch, "agent-patch")

	missing := iterationRequest(t, ts.srv, http.MethodPatch, "/api/v1/cards/iteration/"+created.ID.String(), "browser", map[string]any{"presentation": map[string]any{"highlighted": true}}, "")
	missing.Body.Close()
	if missing.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match status = %d, want 428", missing.StatusCode)
	}

	browser := iterationRequest(t, ts.srv, http.MethodPatch, "/api/v1/cards/iteration/"+created.ID.String(), "browser", map[string]any{"data": map[string]any{"title": "not browser mutable"}}, "1")
	browser.Body.Close()
	if browser.StatusCode != http.StatusForbidden {
		t.Fatalf("browser data patch status = %d, want 403", browser.StatusCode)
	}

	process := testIterationProcess{id: "agent-patch"}
	if err := ts.svc.RegisterProcess(context.Background(), created.ID, process); err != nil {
		t.Fatal(err)
	}
	agent := iterationRequest(t, ts.srv, http.MethodPatch, "/api/v1/cards/iteration/"+created.ID.String(), "agent", map[string]any{"data": map[string]any{"title": "agent update"}}, "1")
	updated := decodeIterationCard(t, agent)
	var data map[string]any
	if err := json.Unmarshal(updated.Data, &data); err != nil || data["title"] != "agent update" {
		t.Fatalf("agent patch data = %s", updated.Data)
	}
}

func TestIterationFeedbackAcknowledgeAndCancelTerminal(t *testing.T) {
	ts := newIterationTestServer(t)
	search := createIterationCard(t, ts, iteration.IterationSubtypeSearch, "agent-feedback")
	feedbackResp := iterationRequest(t, ts.srv, http.MethodPost, "/api/v1/cards/iteration/"+search.ID.String()+"/feedback", "browser", map[string]any{
		"subtype": "iteration_search", "feedbackType": "relevance", "target": map[string]any{"url": "https://example.com/docs"}, "sessionId": sessionID(t, search),
	}, "")
	if feedbackResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(feedbackResp.Body)
		feedbackResp.Body.Close()
		t.Fatalf("feedback status = %d, body=%s", feedbackResp.StatusCode, body)
	}
	var feedback iteration.FeedbackEvent
	if err := json.NewDecoder(feedbackResp.Body).Decode(&feedback); err != nil {
		t.Fatal(err)
	}
	feedbackResp.Body.Close()
	if feedback.ID == uuid.Nil {
		t.Fatal("feedback response did not include generated ID")
	}
	if err := ts.svc.Acknowledge(context.Background(), search.ID, feedback.ID); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	code := createIterationCard(t, ts, iteration.IterationSubtypeCodeExec, "agent-cancel")
	if err := ts.svc.RegisterProcess(context.Background(), code.ID, testIterationProcess{id: "agent-cancel"}); err != nil {
		t.Fatal(err)
	}
	cancel := iterationRequest(t, ts.srv, http.MethodPost, "/api/v1/cards/iteration/"+code.ID.String()+"/cancel", "browser", map[string]string{"reason": "stop"}, "")
	cancel.Body.Close()
	if cancel.StatusCode != http.StatusAccepted {
		t.Fatalf("cancel status = %d", cancel.StatusCode)
	}
	again := iterationRequest(t, ts.srv, http.MethodPost, "/api/v1/cards/iteration/"+code.ID.String()+"/cancel", "browser", nil, "")
	again.Body.Close()
	if again.StatusCode != http.StatusConflict {
		t.Fatalf("terminal cancel status = %d, want 409", again.StatusCode)
	}
}

func appendSearchStarted(t *testing.T, ts *iterationTestServer, value card.Card) {
	t.Helper()
	if err := ts.svc.AppendEvent(context.Background(), value.ID, iteration.IterationEvent{
		Subtype: iteration.IterationSubtypeSearch, EventType: iteration.EventSearchStarted,
		Data: json.RawMessage(`{"query":"docs","batch_total":1}`), AgentSessionID: sessionID(t, value),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIterationActiveOrderingAndProgressAggregation(t *testing.T) {
	ts := newIterationTestServer(t)
	first := createIterationCard(t, ts, iteration.IterationSubtypeSearch, "agent-one")
	second := createIterationCard(t, ts, iteration.IterationSubtypeCodeExec, "agent-two")
	appendSearchStarted(t, ts, first)

	active := iterationRequest(t, ts.srv, http.MethodGet, "/api/v1/cards/iteration/active", "browser", nil, "")
	defer active.Body.Close()
	var list struct {
		Cards []card.Card `json:"cards"`
	}
	if err := json.NewDecoder(active.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Cards) != 2 || list.Cards[0].ID != first.ID || list.Cards[1].ID != second.ID {
		t.Fatalf("active ordering = %v", []uuid.UUID{list.Cards[0].ID, list.Cards[1].ID})
	}

	progress := iterationRequest(t, ts.srv, http.MethodGet, "/api/v1/iteration/progress", "browser", nil, "")
	defer progress.Body.Close()
	var response iterationProgressResponse
	if err := json.NewDecoder(progress.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Summary.Active != 2 || response.Summary.Running != 2 || len(response.Progress) != 2 {
		t.Fatalf("progress response = %+v", response)
	}
}

func TestIterationSSESequenceAndHeartbeat(t *testing.T) {
	ts := newIterationTestServer(t)
	created := createIterationCard(t, ts, iteration.IterationSubtypeSearch, "agent-sse")
	request, err := http.NewRequest(http.MethodGet, ts.srv.URL+"/api/v1/cards/iteration/"+created.ID.String()+"/events?after=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+iterationToken(t, "browser"))
	response, err := ts.srv.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("SSE response = %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}

	lines := make(chan string, 32)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	appendSearchStarted(t, ts, created)

	var sawEvent, sawHeartbeat bool
	deadline := time.After(2 * time.Second)
	for !(sawEvent && sawHeartbeat) {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatal("SSE stream closed before event and heartbeat")
			}
			if strings.Contains(line, "url_discovered") || strings.Contains(line, "search_started") {
				sawEvent = true
			}
			if line == "event: heartbeat" {
				sawHeartbeat = true
			}
		case <-deadline:
			t.Fatalf("SSE signals: event=%v heartbeat=%v", sawEvent, sawHeartbeat)
		}
	}
}

func TestIterationCrashSSEEmitsErrorAndSnapshot(t *testing.T) {
	ts := newIterationTestServer(t)
	created := createIterationCard(t, ts, iteration.IterationSubtypeSearch, "agent-crash-sse")
	process := testIterationProcess{id: "agent-crash-sse"}
	if err := ts.svc.RegisterProcess(context.Background(), created.ID, process); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, ts.srv.URL+"/api/v1/cards/iteration/"+created.ID.String()+"/events?after=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+iterationToken(t, "browser"))
	response, err := ts.srv.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("SSE response = %d", response.StatusCode)
	}
	lines := make(chan string, 32)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	if err := ts.svc.ReportProcessStatus(context.Background(), created.ID, process.id, iteration.AgentProcessStatusCrashed); err != nil {
		t.Fatal(err)
	}
	var sawError, sawSnapshot bool
	deadline := time.After(2 * time.Second)
	for !(sawError && sawSnapshot) {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatal("crash SSE stream closed before both frames")
			}
			if strings.Contains(line, `"eventType":"agent_error"`) {
				sawError = true
			}
			if line == "event: card_snapshot" {
				sawSnapshot = true
			}
		case <-deadline:
			t.Fatalf("crash SSE frames: agent_error=%v card_snapshot=%v", sawError, sawSnapshot)
		}
	}
}

func TestIterationRecoveryRequiredMapsToConflict(t *testing.T) {
	ts := newIterationTestServer(t)
	created := createIterationCard(t, ts, iteration.IterationSubtypeSearch, "agent-http-recovery")
	process := testIterationProcess{id: "agent-http-recovery"}
	if err := ts.svc.RegisterProcess(context.Background(), created.ID, process); err != nil {
		t.Fatal(err)
	}
	if err := ts.svc.ReportProcessStatus(context.Background(), created.ID, process.id, iteration.AgentProcessStatusCrashed); err != nil {
		t.Fatal(err)
	}
	response := iterationRequest(t, ts.srv, http.MethodPatch, "/api/v1/cards/iteration/"+created.ID.String(), "agent", map[string]any{
		"data": map[string]any{"title": "must recover first"},
	}, "2")
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("recovery-required status = %d, want 409", response.StatusCode)
	}
	var envelope map[string]map[string]string
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["error"]["code"] != "ITERATION_RECOVERY_REQUIRED" {
		t.Fatalf("recovery-required envelope = %v", envelope)
	}
}
