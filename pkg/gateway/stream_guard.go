package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
)

const maxOpenAIStreamPrefetchBytes = 64 * 1024

func inspectOpenAIStreamPrefetch(data []byte) (bool, bool) {
	if len(bytes.TrimSpace(data)) == 0 {
		return false, false
	}
	meaningful := false
	done := false
	dataLines := make([]string, 0, 2)
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		if payload == "" {
			return
		}
		if payload == "[DONE]" {
			done = true
			return
		}
		if openAIStreamPayloadHasMeaningfulDelta(payload) {
			meaningful = true
		}
	}

	lines := strings.Split(string(data), "\n")
	for idx, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		if idx == len(lines)-1 && line != "" && !bytes.HasSuffix(data, []byte("\n")) {
			break
		}
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case strings.HasPrefix(line, ":"):
			continue
		}
	}
	return meaningful, done
}

func openAIStreamPayloadHasMeaningfulDelta(payload string) bool {
	payload = strings.TrimSpace(payload)
	if payload == "" || payload == "[DONE]" {
		return false
	}
	var chunk openAIStreamChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err == nil {
		if openAIStreamChunkHasMeaningfulDelta(chunk) {
			return true
		}
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		// A non-empty non-JSON SSE payload is not an empty answer. Let the downstream
		// relay surface it instead of hiding it behind empty-response retries.
		return true
	}
	if _, hasError := raw["error"]; hasError {
		return true
	}
	choices, ok := raw["choices"].([]any)
	if !ok || len(choices) == 0 {
		return false
	}
	for _, item := range choices {
		choice, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if valueHasMeaningfulDelta(choice["message"], true) {
			return true
		}
		if valueHasMeaningfulDelta(choice["delta"], true) {
			return true
		}
	}
	return false
}

func openAIStreamChunkHasMeaningfulDelta(chunk openAIStreamChunk) bool {
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			return true
		}
		for _, toolCall := range choice.Delta.ToolCalls {
			if strings.TrimSpace(toolCall.ID) != "" || strings.TrimSpace(toolCall.Type) != "" || strings.TrimSpace(toolCall.Function.Name) != "" || strings.TrimSpace(toolCall.Function.Arguments) != "" {
				return true
			}
		}
	}
	return false
}

func valueHasMeaningfulDelta(value any, skipRoleOnly bool) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		for key, child := range typed {
			if skipRoleOnly && key == "role" {
				continue
			}
			if valueHasMeaningfulDelta(child, false) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func isOpenAIChatCompletionEmpty(body []byte) bool {
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	if _, hasError := payload["error"]; hasError {
		return false
	}
	choices, ok := payload["choices"].([]any)
	if !ok || len(choices) == 0 {
		return true
	}
	for _, item := range choices {
		choice, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if valueHasMeaningfulDelta(choice["message"], true) {
			return false
		}
	}
	return true
}

func openAIStreamBytesContainDone(data []byte) bool {
	return bytes.Contains(data, []byte("data: [DONE]")) || bytes.Contains(data, []byte("data:[DONE]"))
}
