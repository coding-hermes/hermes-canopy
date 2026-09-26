package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/card"
	"github.com/coding-hermes/hermes-canopy/internal/card/iteration"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

const (
	iterationSSEHeartbeat = 30 * time.Second
	iterationSSEReplayMax = 10000
)

// IterationCardHandler exposes the phase-two iteration-card HTTP/SSE surface.
// The concrete service is intentional: the HTTP adapter needs the typed GET,
// patch, feedback-result, and replay seams added around the phase-one engine.
type IterationCardHandler struct {
	svc       *iteration.IterationCardServiceImpl
	heartbeat time.Duration
}

func NewIterationCardHandler(svc *iteration.IterationCardServiceImpl) *IterationCardHandler {
	return &IterationCardHandler{svc: svc, heartbeat: iterationSSEHeartbeat}
}

// WithSSEHeartbeat is a test seam; production uses the 30-second spec cadence.
func (h *IterationCardHandler) WithSSEHeartbeat(d time.Duration) *IterationCardHandler {
	if d > 0 {
		h.heartbeat = d
	}
	return h
}

// Routes are mounted at /api/v1/cards/iteration. The progress route is mounted
// separately at /api/v1/iteration/progress by the server composition root.
func (h *IterationCardHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", h.CreateCard)
	r.Get("/active", h.ListActiveCards)
	r.Get("/{card_id}", h.GetCard)
	r.Patch("/{card_id}", h.PatchCard)
	r.Post("/{card_id}/feedback", h.SubmitFeedback)
	r.Post("/{card_id}/cancel", h.CancelCard)
	r.Get("/{card_id}/events", h.StreamEvents)
	return r
}

type iterationCreateRequest struct {
	ID          uuid.UUID                  `json:"id"`
	TreeID      uuid.UUID                  `json:"treeId"`
	NodeID      uuid.UUID                  `json:"nodeId"`
	AppID       string                     `json:"appId"`
	Subtype     iteration.IterationSubtype `json:"subtype"`
	Data        json.RawMessage            `json:"data"`
	Actions     []card.CardAction          `json:"actions"`
	ContextHash string                     `json:"contextHash"`
	AgentID     string                     `json:"agentId"`
}

type iterationPatchRequest struct {
	Data         json.RawMessage `json:"data"`
	Presentation json.RawMessage `json:"presentation"`
}

type iterationFeedbackRequest struct {
	CardID       uuid.UUID                  `json:"cardId"`
	Subtype      iteration.IterationSubtype `json:"subtype"`
	FeedbackType iteration.FeedbackKind     `json:"feedbackType"`
	FeedbackKind iteration.FeedbackKind     `json:"feedbackKind"`
	Target       json.RawMessage            `json:"target"`
	Note         string                     `json:"note"`
	SessionID    uuid.UUID                  `json:"sessionId"`
	Timestamp    time.Time                  `json:"timestamp"`
}

type iterationProgressResponse struct {
	Progress []iteration.CardProgress `json:"progress"`
	Summary  iterationProgressSummary `json:"summary"`
}

type iterationProgressSummary struct {
	Active         int `json:"active"`
	Running        int `json:"running"`
	WaitingForUser int `json:"waitingForUser"`
	Completed      int `json:"completed"`
	Failed         int `json:"failed"`
	Cancelled      int `json:"cancelled"`
}

type iterationSSEEvent struct {
	CardID    uuid.UUID                  `json:"cardId"`
	Subtype   iteration.IterationSubtype `json:"subtype"`
	EventType string                     `json:"eventType"`
	Data      json.RawMessage            `json:"data"`
	Sequence  int64                      `json:"sequence"`
	CreatedAt time.Time                  `json:"createdAt"`
}

func (h *IterationCardHandler) CreateCard(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "ITERATION_UNAVAILABLE", "iteration cards are unavailable")
		return
	}
	var req iterationCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if req.Subtype == "" {
		writeError(w, http.StatusBadRequest, "ITERATION_SUBTYPE_INVALID", "subtype is required")
		return
	}
	if req.AgentID == "" {
		writeError(w, http.StatusBadRequest, "ITERATION_PROCESS_FORBIDDEN", "agentId is required")
		return
	}
	if req.AppID == "" {
		req.AppID = "canopy.agent"
	}
	created, err := h.svc.CreateCardWithInput(r.Context(), iteration.CreateIterationCardInput{
		ID: req.ID, TreeID: req.TreeID, NodeID: req.NodeID, AppID: req.AppID,
		Subtype: req.Subtype, Data: req.Data, Actions: req.Actions,
		ContextHash: req.ContextHash, AgentID: req.AgentID,
	})
	if err != nil {
		h.writeIterationError(w, r, err, "create")
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *IterationCardHandler) GetCard(w http.ResponseWriter, r *http.Request) {
	cardID, ok := parseIterationCardID(w, r)
	if !ok {
		return
	}
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "ITERATION_UNAVAILABLE", "iteration cards are unavailable")
		return
	}
	value, err := h.svc.GetCard(r.Context(), cardID)
	if err != nil {
		h.writeIterationError(w, r, err, "get")
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (h *IterationCardHandler) PatchCard(w http.ResponseWriter, r *http.Request) {
	cardID, ok := parseIterationCardID(w, r)
	if !ok {
		return
	}
	revision, ok := parseIfMatch(w, r)
	if !ok {
		return
	}
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "ITERATION_UNAVAILABLE", "iteration cards are unavailable")
		return
	}
	var req iterationPatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	isAgent := strings.EqualFold(strings.TrimSpace(roleFromRequest(r)), "agent")
	updated, err := h.svc.PatchCard(r.Context(), cardID, revision, iteration.IterationPatchInput{
		Data: req.Data, Presentation: req.Presentation, Agent: isAgent,
	})
	if err != nil {
		h.writeIterationError(w, r, err, "patch")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *IterationCardHandler) SubmitFeedback(w http.ResponseWriter, r *http.Request) {
	cardID, ok := parseIterationCardID(w, r)
	if !ok {
		return
	}
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "ITERATION_UNAVAILABLE", "iteration cards are unavailable")
		return
	}
	var req iterationFeedbackRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	kind := req.FeedbackType
	if kind == "" {
		kind = req.FeedbackKind
	}
	actor := UserIDFromContext(r.Context()).String()
	if actor == uuid.Nil.String() {
		actor = "user"
	}
	if req.CardID == uuid.Nil {
		req.CardID = cardID
	}
	feedback, err := h.svc.SubmitFeedbackEvent(r.Context(), cardID, iteration.FeedbackInput{
		CardID: req.CardID, Subtype: req.Subtype, FeedbackKind: kind, Target: req.Target,
		Note: req.Note, ActorID: actor, SessionID: req.SessionID, Timestamp: req.Timestamp,
	})
	if err != nil {
		h.writeIterationError(w, r, err, "feedback")
		return
	}
	writeJSON(w, http.StatusAccepted, feedback)
}

func (h *IterationCardHandler) CancelCard(w http.ResponseWriter, r *http.Request) {
	cardID, ok := parseIterationCardID(w, r)
	if !ok {
		return
	}
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "ITERATION_UNAVAILABLE", "iteration cards are unavailable")
		return
	}
	current, err := h.svc.GetCard(r.Context(), cardID)
	if err != nil {
		h.writeIterationError(w, r, err, "cancel")
		return
	}
	var data iteration.IterationCardData
	if err := json.Unmarshal(current.Data, &data); err != nil {
		h.writeIterationError(w, r, err, "cancel")
		return
	}
	if data.State == iteration.IterationStateCompleted || data.State == iteration.IterationStateFailed || data.State == iteration.IterationStateCancelled {
		writeError(w, http.StatusConflict, "ITERATION_ALREADY_COMPLETED", "card is already in a terminal state")
		return
	}
	if err := h.svc.CancelCard(r.Context(), cardID); err != nil {
		h.writeIterationError(w, r, err, "cancel")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"cardId": cardID, "status": string(iteration.IterationStateCancelled)})
}

func (h *IterationCardHandler) ListActiveCards(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "ITERATION_UNAVAILABLE", "iteration cards are unavailable")
		return
	}
	cards, err := h.svc.ListActiveCards(r.Context())
	if err != nil {
		h.writeIterationError(w, r, err, "active list")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cards": cards})
}

func (h *IterationCardHandler) Progress(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "ITERATION_UNAVAILABLE", "iteration cards are unavailable")
		return
	}
	progress, err := h.svc.GetProgress(r.Context())
	if err != nil {
		h.writeIterationError(w, r, err, "progress")
		return
	}
	var summary iterationProgressSummary
	for _, value := range progress {
		summary.Active++
		switch value.Status {
		case iteration.ProgressStatusRunning:
			summary.Running++
		case iteration.ProgressStatusCompleted:
			summary.Completed++
		case iteration.ProgressStatusFailed:
			summary.Failed++
		case iteration.ProgressStatusCancelled:
			summary.Cancelled++
		}
	}
	writeJSON(w, http.StatusOK, iterationProgressResponse{Progress: progress, Summary: summary})
}

func (h *IterationCardHandler) StreamEvents(w http.ResponseWriter, r *http.Request) {
	cardID, ok := parseIterationCardID(w, r)
	if !ok {
		return
	}
	cursor, ok := parseIterationCursor(w, r)
	if !ok {
		return
	}
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "ITERATION_UNAVAILABLE", "iteration cards are unavailable")
		return
	}
	current, err := h.svc.GetCard(r.Context(), cardID)
	if err != nil {
		h.writeIterationError(w, r, err, "events")
		return
	}
	_ = current
	events, unsubscribe, err := h.svc.SubscribeEvents(r.Context(), cardID)
	if err != nil {
		h.writeIterationError(w, r, err, "events")
		return
	}
	defer unsubscribe()
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STREAMING_NOT_SUPPORTED", "streaming responses are not supported")
		return
	}
	// Subscribe first so an append cannot land between replay and live delivery.
	// Fetch replay before committing SSE headers so replay failures retain the
	// canonical JSON error envelope.
	replay, replayErr := h.svc.ListEvents(r.Context(), cardID, cursor, iterationSSEReplayMax+1)
	if replayErr != nil {
		h.writeIterationError(w, r, replayErr, "events")
		return
	}
	if len(replay) > iterationSSEReplayMax {
		writeError(w, http.StatusRequestEntityTooLarge, "ITERATION_SSE_BACKLOG_LIMIT", "iteration event replay exceeds the 10,000-event limit")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// GAP-100: clear the server's whole-response WriteTimeout now that
	// headers are committed and run every frame write below under a bounded,
	// re-armed deadline (see sse.FrameWriter).
	frames := sse.NewFrameWriter(w, r)
	frames.ClearWriteDeadline()

	lastSequence := cursor
	for _, event := range replay {
		if event.Sequence > lastSequence {
			lastSequence = event.Sequence
		}
		frames.BeforeFrame()
		if err := writeIterationEvent(w, event); err != nil {
			return
		}
	}
	flusher.Flush()

	ticker := time.NewTicker(h.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			if event.EventType == iteration.EventCardSnapshot {
				frames.BeforeFrame()
				if err := writeIterationEvent(w, event); err != nil {
					return
				}
				flusher.Flush()
				continue
			}
			if event.Sequence <= lastSequence {
				continue
			}
			frames.BeforeFrame()
			if err := writeIterationEvent(w, event); err != nil {
				return
			}
			lastSequence = event.Sequence
			flusher.Flush()
		case <-ticker.C:
			frames.BeforeFrame()
			body, _ := json.Marshal(map[string]any{"lastSequence": lastSequence, "timestamp": time.Now().UTC()})
			if err := writeIterationSSEFrame(w, "", "heartbeat", body); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeIterationEvent(w interface{ Write([]byte) (int, error) }, event iteration.IterationEvent) error {
	body, err := json.Marshal(iterationSSEEvent{CardID: event.CardID, Subtype: event.Subtype, EventType: event.EventType, Data: event.Data, Sequence: event.Sequence, CreatedAt: event.CreatedAt})
	if err != nil {
		return err
	}
	name := "iteration_event"
	if event.EventType == iteration.EventCardSnapshot {
		return writeIterationSSEFrame(w, "", "card_snapshot", body)
	}
	return writeIterationSSEFrame(w, strconv.FormatInt(event.Sequence, 10), name, body)
}

func writeIterationSSEFrame(w interface{ Write([]byte) (int, error) }, id, event string, data []byte) error {
	if strings.ContainsAny(string(data), "\r\n") {
		return fmt.Errorf("iteration SSE payload contains a line break")
	}
	frame := ""
	if id != "" {
		frame += "id: " + id + "\n"
	}
	frame += "event: " + event + "\n" + "data: " + string(data) + "\n\n"
	_, err := w.Write([]byte(frame))
	return err
}

func parseIterationCardID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "card_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CARD_ID", "card_id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}

func parseIterationCursor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	cursor := int64(0)
	for _, raw := range []string{r.URL.Query().Get("after"), r.URL.Query().Get("after_sequence"), r.Header.Get("Last-Event-ID")} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || value < 0 {
			writeError(w, http.StatusBadRequest, "ITERATION_SSE_CURSOR_INVALID", "SSE cursor must be a non-negative integer")
			return 0, false
		}
		if value > cursor {
			cursor = value
		}
	}
	return cursor, true
}

func roleFromRequest(r *http.Request) string {
	role, _ := r.Context().Value(roleContextKey{}).(string)
	return role
}

func (h *IterationCardHandler) writeIterationError(w http.ResponseWriter, r *http.Request, err error, operation string) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "CARD_NOT_FOUND", "card not found")
	case errors.Is(err, iteration.ErrAgentNotRegistered):
		if operation == "cancel" {
			writeError(w, http.StatusNotFound, "ITERATION_AGENT_NOT_FOUND", "agent process not found for card")
		} else {
			writeError(w, http.StatusForbidden, "ITERATION_PROCESS_FORBIDDEN", "agent process is not registered for card")
		}
	case errors.Is(err, iteration.ErrRecoveryRequired):
		writeError(w, http.StatusConflict, "ITERATION_RECOVERY_REQUIRED", "interrupted card requires explicit recovery")
	case errors.Is(err, iteration.ErrTerminalCard):
		writeError(w, http.StatusConflict, "ITERATION_TERMINAL_STATE", "iteration card is in a terminal state")
	case errors.Is(err, iteration.ErrPatchForbidden):
		writeError(w, http.StatusForbidden, "ITERATION_PROCESS_FORBIDDEN", "patch contains fields that this actor cannot change")
	case errors.Is(err, card.ErrRevisionConflict):
		writeError(w, http.StatusPreconditionFailed, "CARD_REVISION_CONFLICT", "If-Match revision does not match the current card revision")
	case errors.Is(err, iteration.ErrCardNotActive):
		writeError(w, http.StatusConflict, "CARD_STATUS_INACTIVE", "iteration card is not active")
	default:
		message := err.Error()
		code := "ITERATION_DATA_INVALID"
		status := http.StatusBadRequest
		if strings.Contains(message, "unknown subtype") || strings.Contains(message, "subtype is required") {
			code = "ITERATION_SUBTYPE_INVALID"
		} else if strings.Contains(operation, "feedback") || strings.Contains(message, "feedback") {
			code = "ITERATION_FEEDBACK_INVALID"
		} else if strings.Contains(message, "not found") {
			code = "CARD_NOT_FOUND"
			status = http.StatusNotFound
		} else if strings.Contains(message, "cancellation rejected") {
			code = "ITERATION_CANCEL_FAILED"
			status = http.StatusConflict
		} else if !strings.HasPrefix(message, "iteration:") {
			code = "ITERATION_INTERNAL_ERROR"
			status = http.StatusInternalServerError
		}
		if status >= http.StatusInternalServerError {
			log.Ctx(r.Context()).Error().Err(err).Str("operation", operation).Msg("iteration request failed")
		}
		writeError(w, status, code, message)
	}
}
