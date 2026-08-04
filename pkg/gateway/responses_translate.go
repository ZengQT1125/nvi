package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
)

type translatedResponsesRequest struct {
	RequestedModel string
	Prompt         string
	Temperature    *float64
	Stream         bool
	Messages       []map[string]any
}

func TranslateResponsesRequest(body []byte) ([]byte, *translatedResponsesRequest, error) {
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return nil, nil, fmt.Errorf("invalid json: %v", err)
	}
	requestedModel := strings.TrimSpace(stringValue(reqMap["model"]))
	if requestedModel == "" {
		return nil, nil, fmt.Errorf("model is required")
	}

	openAIReq := map[string]any{
		"model":  normalizeModel(requestedModel),
		"stream": false,
	}
	if stream, ok := boolValue(reqMap["stream"]); ok {
		openAIReq["stream"] = stream
	}
	if temperature, ok := floatValue(reqMap["temperature"]); ok {
		openAIReq["temperature"] = temperature
	}
	if maxOutputTokens, ok := intValue(reqMap["max_output_tokens"]); ok {
		openAIReq["max_tokens"] = maxOutputTokens
	} else if maxTokens, ok := intValue(reqMap["max_tokens"]); ok {
		openAIReq["max_tokens"] = maxTokens
	}
	if tools, ok := normalizeResponsesTools(reqMap["tools"]); ok && len(tools) > 0 {
		openAIReq["tools"] = tools
	}
	if toolChoice, ok := normalizeToolChoice(reqMap["tool_choice"]); ok {
		openAIReq["tool_choice"] = toolChoice
	}

	var currentMessages []map[string]any
	switch {
	case reqMap["messages"] != nil:
		normalized, ok := normalizeOpenAIMessages(reqMap["messages"])
		if !ok || len(normalized) == 0 {
			return nil, nil, fmt.Errorf("messages are required")
		}
		currentMessages = normalized
	case reqMap["input"] != nil:
		normalized, ok := normalizeResponsesInput(reqMap["input"])
		if !ok || len(normalized) == 0 {
			return nil, nil, fmt.Errorf("input is required")
		}
		currentMessages = normalized
	default:
		return nil, nil, fmt.Errorf("input or messages is required")
	}

	finalMessages := make([]map[string]any, 0, len(currentMessages)+1)
	previousResponseID := strings.TrimSpace(stringValue(reqMap["previous_response_id"]))
	if previousResponseID != "" {
		previousMessages, ok := responsesStore.conversation(previousResponseID)
		if !ok || len(previousMessages) == 0 {
			return nil, nil, fmt.Errorf("previous_response_id was not found or expired")
		}
		finalMessages = append(finalMessages, previousMessages...)
	}
	if instructions := strings.TrimSpace(extractResponsesInstructions(reqMap["instructions"])); instructions != "" {
		finalMessages = append(finalMessages, map[string]any{
			"role":    "system",
			"content": instructions,
		})
	}
	finalMessages = append(finalMessages, currentMessages...)
	if len(finalMessages) == 0 {
		return nil, nil, fmt.Errorf("input or messages is required")
	}
	openAIReq["messages"] = finalMessages

	translated, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, nil, err
	}
	stream, _ := boolValue(openAIReq["stream"])
	temperature, _ := floatPtrValue(openAIReq["temperature"])
	return translated, &translatedResponsesRequest{
		RequestedModel: requestedModel,
		Prompt:         buildPromptFromMessageMaps(finalMessages),
		Temperature:    temperature,
		Stream:         stream,
		Messages:       cloneStoredConversation(finalMessages),
	}, nil
}

func normalizeResponsesInput(raw any) ([]map[string]any, bool) {
	switch v := raw.(type) {
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return nil, false
		}
		return []map[string]any{{"role": "user", "content": text}}, true
	case []any:
		messages := make([]map[string]any, 0, len(v))
		for _, item := range v {
			switch entry := item.(type) {
			case string:
				text := strings.TrimSpace(entry)
				if text != "" {
					messages = append(messages, map[string]any{"role": "user", "content": text})
				}
			case map[string]any:
				if roleRaw, hasRole := entry["role"]; hasRole {
					role := normalizeResponsesRole(stringValue(roleRaw))
					content := extractResponsesContent(entry["content"])
					if role == "" || content == "" {
						continue
					}
					normalized := map[string]any{"role": role, "content": content}
					if role == "tool" {
						if toolCallID := strings.TrimSpace(stringValue(entry["tool_call_id"])); toolCallID != "" {
							normalized["tool_call_id"] = toolCallID
						}
						if name := strings.TrimSpace(stringValue(entry["name"])); name != "" {
							normalized["name"] = name
						}
					}
					messages = append(messages, normalized)
					continue
				}
				typ := strings.ToLower(strings.TrimSpace(stringValue(entry["type"])))
				switch typ {
				case "input_text", "text":
					text := strings.TrimSpace(stringValue(entry["text"]))
					if text != "" {
						messages = append(messages, map[string]any{"role": "user", "content": text})
					}
				case "output_text":
					text := strings.TrimSpace(stringValue(entry["text"]))
					if text != "" {
						messages = append(messages, map[string]any{"role": "assistant", "content": text})
					}
				case "message":
					role := normalizeResponsesRole(stringValue(entry["role"]))
					content := extractResponsesContent(entry["content"])
					if role != "" && content != "" {
						normalized := map[string]any{"role": role, "content": content}
						if role == "tool" {
							if toolCallID := strings.TrimSpace(stringValue(entry["tool_call_id"])); toolCallID != "" {
								normalized["tool_call_id"] = toolCallID
							}
							if name := strings.TrimSpace(stringValue(entry["name"])); name != "" {
								normalized["name"] = name
							}
						}
						messages = append(messages, normalized)
					}
				case "function_call_output":
					content := normalizeToolOutput(entry["output"])
					if content != "" {
						normalized := map[string]any{"role": "tool", "content": content}
						if toolCallID := strings.TrimSpace(stringValue(entry["call_id"])); toolCallID != "" {
							normalized["tool_call_id"] = toolCallID
						}
						if name := strings.TrimSpace(stringValue(entry["name"])); name != "" {
							normalized["name"] = name
						}
						messages = append(messages, normalized)
					}
				}
			}
		}
		return messages, len(messages) > 0
	default:
		return nil, false
	}
}

func normalizeResponsesRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant":
		return "assistant"
	case "system", "developer":
		return "system"
	case "tool":
		return "tool"
	default:
		return "user"
	}
}

func extractResponsesInstructions(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			switch entry := item.(type) {
			case string:
				if text := strings.TrimSpace(entry); text != "" {
					parts = append(parts, text)
				}
			case map[string]any:
				if text := strings.TrimSpace(stringValue(entry["text"])); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func extractResponsesContent(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			switch entry := item.(type) {
			case string:
				if text := strings.TrimSpace(entry); text != "" {
					parts = append(parts, text)
				}
			case map[string]any:
				typ := strings.ToLower(strings.TrimSpace(stringValue(entry["type"])))
				switch typ {
				case "input_text", "output_text", "text":
					if text := strings.TrimSpace(stringValue(entry["text"])); text != "" {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func normalizeToolOutput(raw any) string {
	if raw == nil {
		return ""
	}
	if text := extractResponsesContent(raw); text != "" {
		return text
	}
	if text, ok := raw.(string); ok {
		return strings.TrimSpace(text)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(encoded))
}

func normalizeResponsesTools(raw any) ([]map[string]any, bool) {
	items, ok := raw.([]any)
	if !ok {
		if typed, ok2 := raw.([]map[string]any); ok2 {
			return typed, len(typed) > 0
		}
		return nil, false
	}
	tools := make([]map[string]any, 0, len(items))
	for _, item := range items {
		toolMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(stringValue(toolMap["type"])) == "function" {
			tools = append(tools, toolMap)
		}
	}
	return tools, len(tools) > 0
}
