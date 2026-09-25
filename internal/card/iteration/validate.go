package iteration

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	agentIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	stepIDPattern  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	uuidV7Pattern  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-7[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
)

// ValidateSubtype rejects the closed discriminator before any store write.
func ValidateSubtype(subtype IterationSubtype) error {
	switch subtype {
	case IterationSubtypeSearch, IterationSubtypeCodeExec, IterationSubtypeFileRead,
		IterationSubtypeThinking, IterationSubtypeToolCall:
		return nil
	default:
		return fmt.Errorf("iteration: unknown subtype %q", subtype)
	}
}

func validateUUIDv7(id uuid.UUID, name string) error {
	if id == uuid.Nil || !uuidV7Pattern.MatchString(id.String()) || id.Version() != 7 {
		return fmt.Errorf("iteration: %s must be a UUIDv7", name)
	}
	return nil
}

func validateUUIDv7String(value, name string) error {
	if !uuidV7Pattern.MatchString(value) {
		return fmt.Errorf("iteration: %s must be a UUIDv7", name)
	}
	u, err := uuid.Parse(value)
	if err != nil || u.Version() != 7 {
		return fmt.Errorf("iteration: %s must be a UUIDv7", name)
	}
	return nil
}

func decodeObject(raw json.RawMessage, name string) (map[string]any, error) {
	var object map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, fmt.Errorf("iteration: %s must be a JSON object", name)
	}
	return object, nil
}

func stringField(object map[string]any, name string) (string, error) {
	value, ok := object[name].(string)
	if !ok {
		return "", fmt.Errorf("iteration: %s must be a string", name)
	}
	return value, nil
}

func intField(object map[string]any, name string) (int, error) {
	value, ok := object[name].(float64)
	if !ok || value < 0 || value != float64(int(value)) {
		return 0, fmt.Errorf("iteration: %s must be a non-negative integer", name)
	}
	return int(value), nil
}

func validateProgress(value any) error {
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return fmt.Errorf("iteration: progress must be an object")
	}
	for _, name := range []string{"cardId", "type", "title", "current", "total", "status", "updatedAt"} {
		if _, ok := object[name]; !ok {
			return fmt.Errorf("iteration: progress.%s is required", name)
		}
	}
	cardID, err := stringField(object, "cardId")
	if err != nil {
		return err
	}
	if err := validateUUIDv7String(cardID, "progress.cardId"); err != nil {
		return err
	}
	progressType, err := stringField(object, "type")
	if err != nil {
		return err
	}
	validTypes := map[string]bool{"search": true, "code_exec": true, "file_read": true, "thinking": true, "tool_call": true}
	if !validTypes[progressType] {
		return fmt.Errorf("iteration: invalid progress.type %q", progressType)
	}
	title, err := stringField(object, "title")
	if err != nil || strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > 160 {
		return fmt.Errorf("iteration: progress.title must be 1-160 code points")
	}
	current, err := intField(object, "current")
	if err != nil {
		return err
	}
	total, err := intField(object, "total")
	if err != nil {
		return err
	}
	if total > 0 && current > total {
		return fmt.Errorf("iteration: progress.current must not exceed total")
	}
	status, err := stringField(object, "status")
	if err != nil {
		return err
	}
	if status != string(ProgressStatusRunning) && status != string(ProgressStatusCompleted) &&
		status != string(ProgressStatusFailed) && status != string(ProgressStatusCancelled) {
		return fmt.Errorf("iteration: invalid progress.status %q", status)
	}
	updatedAt, err := stringField(object, "updatedAt")
	if err != nil || strings.TrimSpace(updatedAt) == "" {
		return fmt.Errorf("iteration: progress.updatedAt is required")
	}
	return nil
}

// ValidateCardData validates the common envelope and its subtype discriminator.
// Subtype-specific event fields are validated separately by ValidateEventPair.
func ValidateCardData(raw json.RawMessage) (IterationCardData, error) {
	object, err := decodeObject(raw, "card data")
	if err != nil {
		return IterationCardData{}, err
	}
	subtypeValue, err := stringField(object, "subtype")
	if err != nil {
		return IterationCardData{}, err
	}
	subtype := IterationSubtype(subtypeValue)
	if err := ValidateSubtype(subtype); err != nil {
		return IterationCardData{}, err
	}
	title, err := stringField(object, "title")
	if err != nil || strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > 160 {
		return IterationCardData{}, fmt.Errorf("iteration: title must be 1-160 code points")
	}
	state, err := stringField(object, "state")
	if err != nil {
		return IterationCardData{}, err
	}
	validStates := map[string]bool{
		string(IterationStateRunning): true, string(IterationStateWaitingForUser): true,
		string(IterationStateCompleted): true, string(IterationStateFailed): true,
		string(IterationStateCancelled): true, string(IterationStateInterrupted): true,
	}
	if !validStates[state] {
		return IterationCardData{}, fmt.Errorf("iteration: invalid state %q", state)
	}
	agentID, err := stringField(object, "agentId")
	if err != nil || !agentIDPattern.MatchString(agentID) {
		return IterationCardData{}, fmt.Errorf("iteration: agentId must be 1-128 URL-safe characters")
	}
	sessionID, err := stringField(object, "sessionId")
	if err != nil {
		return IterationCardData{}, err
	}
	if err := validateUUIDv7String(sessionID, "sessionId"); err != nil {
		return IterationCardData{}, err
	}
	if topicID, ok := object["topicId"]; ok && topicID != nil {
		topic, ok := topicID.(string)
		if !ok {
			return IterationCardData{}, fmt.Errorf("iteration: topicId must be a UUIDv7")
		}
		if err := validateUUIDv7String(topic, "topicId"); err != nil {
			return IterationCardData{}, err
		}
	}
	if err := validateProgress(object["progress"]); err != nil {
		return IterationCardData{}, err
	}
	return IterationCardData{Subtype: subtype, Title: title, State: IterationState(state), AgentID: agentID}, nil
}

func validateURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("iteration: url must be an absolute HTTP(S) URL")
	}
	if len(value) > 4096 {
		return fmt.Errorf("iteration: url exceeds 4096 bytes")
	}
	return nil
}

func requireFields(object map[string]any, names ...string) error {
	for _, name := range names {
		if _, ok := object[name]; !ok {
			return fmt.Errorf("iteration: event data.%s is required", name)
		}
	}
	return nil
}

// ValidateEventPair validates the exact subtype/event payload table in §3.3–§3.7.
func ValidateEventPair(subtype IterationSubtype, eventType string, raw json.RawMessage) error {
	if err := ValidateSubtype(subtype); err != nil {
		return err
	}
	object, err := decodeObject(raw, "event data")
	if err != nil {
		return err
	}
	// These lifecycle events are valid for every subtype and still require an
	// object payload so their durable replay shape remains deterministic.
	if eventType == EventCardCancelled || eventType == EventCardSteered {
		return nil
	}
	allowed := map[IterationSubtype]map[string]bool{
		IterationSubtypeSearch: {
			EventSearchStarted: true, EventURLDiscovered: true, EventSnippetRetrieved: true,
			EventSearchCompleted: true, EventSearchError: true,
		},
		IterationSubtypeCodeExec: {
			EventExecStart: true, EventExecOutput: true, EventExecComplete: true, EventExecError: true,
		},
		IterationSubtypeFileRead: {
			EventFileReadOpened: true, EventFileReadContent: true, EventFileReadError: true,
		},
		IterationSubtypeThinking: {
			EventThoughtStarted: true, EventThoughtProgress: true, EventThoughtComplete: true, EventThoughtError: true,
		},
		IterationSubtypeToolCall: {
			EventToolCallStarted: true, EventToolCallResult: true, EventToolCallError: true,
		},
	}
	if !allowed[subtype][eventType] {
		return fmt.Errorf("iteration: event %q is not valid for subtype %q", eventType, subtype)
	}

	switch eventType {
	case EventSearchStarted:
		if err := requireFields(object, "query", "batch_total"); err != nil {
			return err
		}
		_, err := intField(object, "batch_total")
		return err
	case EventURLDiscovered:
		urlValue, err := stringField(object, "url")
		if err != nil {
			return err
		}
		return validateURL(urlValue)
	case EventSnippetRetrieved:
		if err := requireFields(object, "url", "snippet_id", "snippet"); err != nil {
			return err
		}
		urlValue, err := stringField(object, "url")
		if err != nil {
			return err
		}
		if err := validateURL(urlValue); err != nil {
			return err
		}
		snippet, err := stringField(object, "snippet")
		if err != nil {
			return err
		}
		if len([]byte(snippet)) > 16*1024 {
			return fmt.Errorf("iteration: snippet exceeds 16 KiB")
		}
		return nil
	case EventSearchCompleted:
		if err := requireFields(object, "completed", "total"); err != nil {
			return err
		}
		completed, err := intField(object, "completed")
		if err != nil {
			return err
		}
		total, err := intField(object, "total")
		if err != nil {
			return err
		}
		if total > 0 && completed > total {
			return fmt.Errorf("iteration: completed exceeds total")
		}
		return nil
	case EventSearchError:
		if err := requireFields(object, "code", "message"); err != nil {
			return err
		}
		return nil
	case EventExecStart:
		return requireFields(object, "command", "start_time")
	case EventExecOutput:
		if err := requireFields(object, "stream", "chunk", "offset"); err != nil {
			return err
		}
		stream, err := stringField(object, "stream")
		if err != nil {
			return err
		}
		if stream != "stdout" && stream != "stderr" {
			return fmt.Errorf("iteration: invalid output stream")
		}
		chunk, err := stringField(object, "chunk")
		if err != nil {
			return err
		}
		if len([]byte(chunk)) > 32*1024 {
			return fmt.Errorf("iteration: output chunk exceeds 32 KiB")
		}
		_, err = intField(object, "offset")
		return err
	case EventExecComplete:
		if err := requireFields(object, "exit_code", "end_time", "cancelled", "duration_ms"); err != nil {
			return err
		}
		return nil
	case EventExecError:
		return requireFields(object, "code", "message")
	case EventFileReadOpened:
		return requireFields(object, "path", "absolute_path", "size", "mime_type", "language")
	case EventFileReadContent:
		if err := requireFields(object, "start_line", "end_line", "content", "line_count"); err != nil {
			return err
		}
		start, err := intField(object, "start_line")
		if err != nil {
			return err
		}
		end, err := intField(object, "end_line")
		if err != nil {
			return err
		}
		if start == 0 || end < start {
			return fmt.Errorf("iteration: invalid file range")
		}
		return nil
	case EventFileReadError:
		return requireFields(object, "code", "message")
	case EventThoughtStarted:
		if err := requireFields(object, "step_id", "title"); err != nil {
			return err
		}
		step, err := stringField(object, "step_id")
		if err != nil {
			return err
		}
		if !stepIDPattern.MatchString(step) {
			return fmt.Errorf("iteration: invalid step_id")
		}
		return nil
	case EventThoughtProgress:
		if err := requireFields(object, "step_id", "content"); err != nil {
			return err
		}
		step, err := stringField(object, "step_id")
		if err != nil {
			return err
		}
		if !stepIDPattern.MatchString(step) {
			return fmt.Errorf("iteration: invalid step_id")
		}
		content, err := stringField(object, "content")
		if err != nil {
			return err
		}
		if len([]byte(content)) > 32*1024 {
			return fmt.Errorf("iteration: thought content exceeds 32 KiB")
		}
		return nil
	case EventThoughtComplete:
		if err := requireFields(object, "step_id", "duration_ms"); err != nil {
			return err
		}
		step, err := stringField(object, "step_id")
		if err != nil {
			return err
		}
		if !stepIDPattern.MatchString(step) {
			return fmt.Errorf("iteration: invalid step_id")
		}
		_, err = intField(object, "duration_ms")
		return err
	case EventThoughtError:
		if err := requireFields(object, "step_id", "error"); err != nil {
			return err
		}
		step, err := stringField(object, "step_id")
		if err != nil {
			return err
		}
		if !stepIDPattern.MatchString(step) {
			return fmt.Errorf("iteration: invalid step_id")
		}
		return nil
	case EventToolCallStarted:
		return requireFields(object, "tool_name", "params", "start_time", "gated")
	case EventToolCallResult:
		return requireFields(object, "result", "end_time", "duration_ms")
	case EventToolCallError:
		return requireFields(object, "code", "message")
	}
	return nil
}

// ValidatePayloadEnvelope validates the storage envelope independently from the
// subtype event table.
func ValidatePayloadEnvelope(raw json.RawMessage) (PayloadEnvelope, error) {
	object, err := decodeObject(raw, "payload envelope")
	if err != nil {
		return PayloadEnvelope{}, err
	}
	subtypeValue, err := stringField(object, "subtype")
	if err != nil {
		return PayloadEnvelope{}, err
	}
	subtype := IterationSubtype(subtypeValue)
	if err := ValidateSubtype(subtype); err != nil {
		return PayloadEnvelope{}, err
	}
	dataRaw, ok := object["data"]
	if !ok {
		return PayloadEnvelope{}, fmt.Errorf("iteration: payload data is required")
	}
	data, err := json.Marshal(dataRaw)
	if err != nil {
		return PayloadEnvelope{}, err
	}
	session, err := stringField(object, "agent_session_id")
	if err != nil {
		return PayloadEnvelope{}, err
	}
	if err := validateUUIDv7String(session, "agent_session_id"); err != nil {
		return PayloadEnvelope{}, err
	}
	var sessionID uuid.UUID
	sessionID, _ = uuid.Parse(session)
	var traceID *uuid.UUID
	if value, ok := object["trace_id"]; ok && value != nil {
		trace, ok := value.(string)
		if !ok {
			return PayloadEnvelope{}, fmt.Errorf("iteration: trace_id must be a UUIDv7")
		}
		if err := validateUUIDv7String(trace, "trace_id"); err != nil {
			return PayloadEnvelope{}, err
		}
		parsed, _ := uuid.Parse(trace)
		traceID = &parsed
	}
	return PayloadEnvelope{Subtype: subtype, Data: data, AgentSessionID: sessionID, TraceID: traceID}, nil
}

func validateFeedback(input FeedbackInput, data IterationCardData) error {
	valid := map[FeedbackKind]bool{FeedbackKindRelevance: true, FeedbackKindCorrection: true, FeedbackKindSteer: true, FeedbackKindCancel: true, FeedbackKindHighlight: true, FeedbackKindApprove: true, FeedbackKindReject: true}
	if !valid[input.FeedbackKind] {
		return fmt.Errorf("iteration: unknown feedback kind %q", input.FeedbackKind)
	}
	if input.ActorID == "" {
		return fmt.Errorf("iteration: actorId is required")
	}
	if err := validateUUIDv7(input.SessionID, "sessionId"); err != nil {
		return err
	}
	if input.FeedbackKind == FeedbackKindRelevance && data.Subtype != IterationSubtypeSearch {
		return fmt.Errorf("iteration: relevance feedback requires search")
	}
	if input.FeedbackKind == FeedbackKindHighlight && data.Subtype != IterationSubtypeFileRead && data.Subtype != IterationSubtypeSearch {
		return fmt.Errorf("iteration: highlight feedback requires file read or search")
	}
	if (input.FeedbackKind == FeedbackKindRelevance || input.FeedbackKind == FeedbackKindHighlight || input.FeedbackKind == FeedbackKindApprove || input.FeedbackKind == FeedbackKindReject) && len(input.Target) == 0 {
		return fmt.Errorf("iteration: feedback target is required")
	}
	if input.FeedbackKind == FeedbackKindCancel && len(input.Target) != 0 && string(input.Target) != "null" {
		return fmt.Errorf("iteration: cancel feedback has no target")
	}
	if len(input.Note) > 16*1024 {
		return fmt.Errorf("iteration: feedback note exceeds 16 KiB")
	}
	if len(input.Target) > 64*1024 {
		return fmt.Errorf("iteration: feedback target exceeds 64 KiB")
	}
	if len(input.Target) > 0 {
		if _, err := decodeObject(input.Target, "feedback target"); err != nil {
			return err
		}
	}
	if input.FeedbackKind == FeedbackKindRelevance {
		object, _ := decodeObject(input.Target, "feedback target")
		if object == nil {
			return fmt.Errorf("iteration: relevance feedback target requires url or snippet ID")
		}
		urlValue, hasURL := object["url"].(string)
		snippetID, hasSnippet := object["snippetId"].(string)
		if !hasSnippet {
			snippetID, hasSnippet = object["snippet_id"].(string)
		}
		if !hasURL && !hasSnippet {
			return fmt.Errorf("iteration: relevance feedback target requires url or snippet ID")
		}
		if hasURL {
			if err := validateURL(urlValue); err != nil {
				return err
			}
		}
		if hasSnippet && strings.TrimSpace(snippetID) == "" {
			return fmt.Errorf("iteration: feedback snippet ID must not be empty")
		}
	}
	if (input.FeedbackKind == FeedbackKindApprove || input.FeedbackKind == FeedbackKindReject) && len(input.Target) == 0 {
		return fmt.Errorf("iteration: feedback target is required")
	}
	return nil
}
