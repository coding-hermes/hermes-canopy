package iteration

import (
	"encoding/json"
	"fmt"
)

// reduceEvent updates the current materialized data from one typed event. It
// is intentionally small: thinking and search are implemented in phase one;
// other valid subtype events keep the current projection until their reducer is
// added, rather than being silently routed through a generic progress event.
func reduceEvent(current json.RawMessage, subtype IterationSubtype, eventType string, eventData json.RawMessage) (json.RawMessage, error) {
	var data map[string]any
	if err := json.Unmarshal(current, &data); err != nil {
		return nil, fmt.Errorf("iteration: decode materialized data: %w", err)
	}
	var event map[string]any
	if err := json.Unmarshal(eventData, &event); err != nil {
		return nil, fmt.Errorf("iteration: decode event data: %w", err)
	}

	setState := func(state IterationState) {
		data["state"] = string(state)
		if progress, ok := data["progress"].(map[string]any); ok {
			if state == IterationStateCompleted {
				progress["status"] = string(ProgressStatusCompleted)
			}
			if state == IterationStateFailed {
				progress["status"] = string(ProgressStatusFailed)
			}
			if state == IterationStateCancelled {
				progress["status"] = string(ProgressStatusCancelled)
			}
		}
	}
	setProgress := func(current, total int) {
		if progress, ok := data["progress"].(map[string]any); ok {
			progress["current"] = current
			progress["total"] = total
		}
	}
	numberField := func(name string) (int, error) {
		value, ok := event[name].(float64)
		if !ok || value != float64(int(value)) {
			return 0, fmt.Errorf("iteration: reducer event %s must be an integer", name)
		}
		return int(value), nil
	}
	stringField := func(name string) (string, error) {
		value, ok := event[name].(string)
		if !ok {
			return "", fmt.Errorf("iteration: reducer event %s must be a string", name)
		}
		return value, nil
	}

	switch subtype {
	case IterationSubtypeSearch:
		switch eventType {
		case EventSearchStarted:
			batchTotal, err := numberField("batch_total")
			if err != nil {
				return nil, err
			}
			data["searchProgress"] = map[string]any{"completed": 0, "total": batchTotal}
			setProgress(0, batchTotal)
			setState(IterationStateRunning)
		case EventURLDiscovered:
			url, err := stringField("url")
			if err != nil {
				return nil, err
			}
			urls, _ := data["urlsSearched"].([]any)
			seen := false
			for _, item := range urls {
				if item == url {
					seen = true
					break
				}
			}
			if !seen {
				data["urlsSearched"] = append(urls, url)
			}
		case EventSnippetRetrieved:
			batch, _ := data["currentBatch"].([]any)
			batch = append(batch, map[string]any{
				"url": event["url"], "snippet": event["snippet"], "snippetId": event["snippet_id"], "status": "retrieved",
			})
			data["currentBatch"] = batch
		case EventSearchCompleted:
			completed, err := numberField("completed")
			if err != nil {
				return nil, err
			}
			total, err := numberField("total")
			if err != nil {
				return nil, err
			}
			data["searchProgress"] = map[string]any{"completed": completed, "total": total}
			setProgress(completed, total)
			if completed >= total {
				setState(IterationStateCompleted)
			}
		case EventSearchError:
			setState(IterationStateFailed)
		}
	case IterationSubtypeThinking:
		steps, _ := data["steps"].([]any)
		findStep := func(id string) (map[string]any, int) {
			for index, raw := range steps {
				if step, ok := raw.(map[string]any); ok && step["id"] == id {
					return step, index
				}
			}
			return nil, -1
		}
		stepID, _ := event["step_id"].(string)
		switch eventType {
		case EventThoughtStarted:
			step, index := findStep(stepID)
			if step == nil {
				step = map[string]any{"id": stepID, "title": event["title"], "status": "active", "content": nil, "durationMs": nil, "error": nil}
				steps = append(steps, step)
			} else {
				step["status"] = "active"
				steps[index] = step
			}
			data["steps"] = steps
			data["currentStepId"] = stepID
		case EventThoughtProgress:
			step, index := findStep(stepID)
			if step == nil {
				return nil, fmt.Errorf("iteration: thought step %q does not exist", stepID)
			}
			step["content"] = event["content"]
			step["status"] = "active"
			steps[index] = step
			data["steps"] = steps
			data["currentStepId"] = stepID
		case EventThoughtComplete:
			step, index := findStep(stepID)
			if step == nil {
				return nil, fmt.Errorf("iteration: thought step %q does not exist", stepID)
			}
			step["status"] = "completed"
			if content, ok := event["content"]; ok {
				step["content"] = content
			}
			step["durationMs"] = event["duration_ms"]
			step["error"] = nil
			steps[index] = step
			data["steps"] = steps
			data["currentStepId"] = nil
		case EventThoughtError:
			step, index := findStep(stepID)
			if step == nil {
				return nil, fmt.Errorf("iteration: thought step %q does not exist", stepID)
			}
			step["status"] = "failed"
			step["error"] = event["error"]
			if duration, ok := event["duration_ms"]; ok {
				step["durationMs"] = duration
			}
			steps[index] = step
			data["steps"] = steps
			data["currentStepId"] = nil
			setState(IterationStateFailed)
		}
	case IterationSubtypeCodeExec, IterationSubtypeFileRead, IterationSubtypeToolCall:
		// The event remains durable and replayable; subtype materialization lands
		// in the next phase. The common envelope is left unchanged.
	}

	if eventType == EventAgentError {
		setState(IterationStateInterrupted)
	}
	if eventType == EventAgentProgress {
		if message, _ := event["message"].(string); message == "resumed" {
			setState(IterationStateRunning)
		}
	}
	if eventType == EventCardCancelled {
		setState(IterationStateCancelled)
	}
	if eventType == EventCardSteered {
		if _, ok := data["state"]; !ok {
			setState(IterationStateRunning)
		}
	}
	updated, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("iteration: encode materialized data: %w", err)
	}
	return updated, nil
}
