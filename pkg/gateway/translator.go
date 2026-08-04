package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature *float64      `json:"temperature,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
}

type openAIMessagePayload struct {
	Role      string           `json:"role"`
	Content   string           `json:"content,omitempty"`
	ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

var modelMapping = map[string]string{
	"gpt-4o":                   "meta/llama-3.1-70b-instruct",
	"gpt-4.1":                  "meta/llama-3.1-70b-instruct",
	"gpt-4.1-mini":             "meta/llama-3.1-8b-instruct",
	"gpt-4-turbo":              "meta/llama-3.1-70b-instruct",
	"gpt-3.5-turbo":            "meta/llama-3.1-8b-instruct",
	"claude-3-opus-20240229":   "meta/llama-3.1-70b-instruct",
	"claude-3-sonnet-20240229": "meta/llama-3.1-70b-instruct",
	"claude-sonnet-4-6":        "meta/llama-3.1-70b-instruct",
	"claude-opus-4-6":          "meta/llama-3.1-70b-instruct",
	"claude-haiku-3.5":         "meta/llama-3.1-8b-instruct",
	"gemini-2.5-pro":           "meta/llama-3.1-70b-instruct",
	"gemini-2.5-flash":         "meta/llama-3.1-8b-instruct",
	"text-embedding-3-small":   "nvidia/nv-embed-v1",
	"text-embedding-3-large":   "nvidia/nv-embed-v1",
	"text-embedding-ada-002":   "nvidia/nv-embed-v1",
}

func TranslateRequest(body []byte) ([]byte, *ChatRequest, string, *float64, error) {
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return nil, nil, "", nil, fmt.Errorf("invalid json: %v", err)
	}

	model := normalizeModel(stringValue(reqMap["model"]))
	if model == "" {
		return nil, nil, "", nil, fmt.Errorf("model is required")
	}
	reqMap["model"] = model
	delete(reqMap, "logit_bias")
	if toolChoice, ok := normalizeToolChoice(reqMap["tool_choice"]); ok {
		reqMap["tool_choice"] = toolChoice
	} else {
		delete(reqMap, "tool_choice")
	}

	messages, ok := normalizeOpenAIMessages(reqMap["messages"])
	if !ok || len(messages) == 0 {
		return nil, nil, "", nil, fmt.Errorf("messages are required")
	}
	reqMap["messages"] = messages

	// 确保 max_tokens 足够大，避免长回复被截断
	// 注意：这是单次输出上限，不是上下文长度。大多数模型输出上限是 4K-16K，
	// 设置 32768 足够覆盖 99% 的场景。过大的值可能被上游拒绝。
	if maxTokens, ok := intValue(reqMap["max_tokens"]); ok {
		if maxTokens < 32768 {
			reqMap["max_tokens"] = 32768
		}
	} else {
		reqMap["max_tokens"] = 32768
	}

	prompt := buildPromptFromMessages(messages)
	stream, _ := boolValue(reqMap["stream"])
	temperature, _ := floatPtrValue(reqMap["temperature"])

	newBody, err := json.Marshal(reqMap)
	if err != nil {
		return nil, nil, "", nil, err
	}

	translated := &ChatRequest{
		Model:       model,
		Messages:    convertMessagesForMeta(messages),
		Temperature: temperature,
		Stream:      stream,
	}
	return newBody, translated, prompt, temperature, nil
}

func TranslateClaudeRequest(body []byte) ([]byte, string, *float64, bool, string, error) {
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return nil, "", nil, false, "", fmt.Errorf("invalid json: %v", err)
	}

	requestedModel := strings.TrimSpace(stringValue(reqMap["model"]))
	if requestedModel == "" {
		return nil, "", nil, false, "", fmt.Errorf("model is required")
	}
	openAIReq := map[string]any{
		"model":    normalizeModel(requestedModel),
		"stream":   false,
		"messages": make([]map[string]any, 0),
	}
	if v, ok := boolValue(reqMap["stream"]); ok {
		openAIReq["stream"] = v
	}
	if temp, ok := floatValue(reqMap["temperature"]); ok {
		openAIReq["temperature"] = temp
	}
	if maxTokens, ok := intValue(reqMap["max_tokens"]); ok {
		// 确保 max_tokens 足够大，避免长回复被截断导致 Claude Code 中断
		// 注意：这是单次输出上限，不是上下文长度。32768 足够覆盖大部分模型
		if maxTokens < 32768 {
			maxTokens = 32768
		}
		openAIReq["max_tokens"] = maxTokens
	} else {
		openAIReq["max_tokens"] = 32768
	}
	if system := strings.TrimSpace(extractClaudeSystem(reqMap["system"])); system != "" {
		openAIReq["messages"] = append(openAIReq["messages"].([]map[string]any), map[string]any{
			"role":    "system",
			"content": system,
		})
	}

	claudeMessages, ok := reqMap["messages"].([]any)
	if !ok || len(claudeMessages) == 0 {
		return nil, "", nil, false, "", fmt.Errorf("messages are required")
	}
	for _, item := range claudeMessages {
		msgMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.TrimSpace(stringValue(msgMap["role"]))
		if role == "" {
			continue
		}
		content := strings.TrimSpace(extractClaudeContent(msgMap["content"]))
		if content == "" {
			continue
		}
		openAIReq["messages"] = append(openAIReq["messages"].([]map[string]any), map[string]any{
			"role":    normalizeRole(role),
			"content": content,
		})
	}
	if len(openAIReq["messages"].([]map[string]any)) == 0 {
		return nil, "", nil, false, "", fmt.Errorf("messages are required")
	}
	if tools := convertClaudeTools(reqMap["tools"]); len(tools) > 0 {
		openAIReq["tools"] = tools
	}

	translated, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, "", nil, false, "", err
	}
	stream, _ := boolValue(openAIReq["stream"])
	temperature, _ := floatPtrValue(openAIReq["temperature"])
	return translated, requestedModel, temperature, stream, buildPromptFromMessageMaps(openAIReq["messages"].([]map[string]any)), nil
}

func TranslateGeminiRequest(routeTarget string, body []byte, stream bool) ([]byte, string, *float64, string, error) {
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return nil, "", nil, "", fmt.Errorf("invalid json: %v", err)
	}
	requestedModel := extractGeminiModel(routeTarget)
	if requestedModel == "" {
		return nil, "", nil, "", fmt.Errorf("model is required")
	}

	openAIReq := map[string]any{
		"model":    normalizeModel(requestedModel),
		"stream":   stream,
		"messages": make([]map[string]any, 0),
	}
	if system := strings.TrimSpace(extractGeminiSystemInstruction(reqMap["systemInstruction"])); system != "" {
		openAIReq["messages"] = append(openAIReq["messages"].([]map[string]any), map[string]any{
			"role":    "system",
			"content": system,
		})
	}
	if generationConfig, ok := reqMap["generationConfig"].(map[string]any); ok {
		if temp, ok := floatValue(generationConfig["temperature"]); ok {
			openAIReq["temperature"] = temp
		}
		if maxTokens, ok := intValue(generationConfig["maxOutputTokens"]); ok {
			if maxTokens < 32768 {
				maxTokens = 32768
			}
			openAIReq["max_tokens"] = maxTokens
		} else {
			openAIReq["max_tokens"] = 32768
		}
	} else {
		// 没有 generationConfig 时也设置 max_tokens
		openAIReq["max_tokens"] = 32768
	}
	contents, ok := reqMap["contents"].([]any)
	if !ok || len(contents) == 0 {
		return nil, "", nil, "", fmt.Errorf("contents are required")
	}
	for _, item := range contents {
		contentMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := normalizeGeminiRole(stringValue(contentMap["role"]))
		text := strings.TrimSpace(extractGeminiParts(contentMap["parts"]))
		if text == "" {
			continue
		}
		openAIReq["messages"] = append(openAIReq["messages"].([]map[string]any), map[string]any{
			"role":    role,
			"content": text,
		})
	}
	if len(openAIReq["messages"].([]map[string]any)) == 0 {
		return nil, "", nil, "", fmt.Errorf("contents are required")
	}
	if tools := convertGeminiTools(reqMap["tools"]); len(tools) > 0 {
		openAIReq["tools"] = tools
	}
	translated, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, "", nil, "", err
	}
	temperature, _ := floatPtrValue(openAIReq["temperature"])
	return translated, requestedModel, temperature, buildPromptFromMessageMaps(openAIReq["messages"].([]map[string]any)), nil
}

func RenderClaudeResponse(raw []byte, requestedModel string) ([]byte, error) {
	var upstream map[string]any
	if err := json.Unmarshal(raw, &upstream); err != nil {
		return nil, err
	}
	message, usage, finishReason := extractOpenAIResponsePieces(upstream)
	content := make([]map[string]any, 0)
	if strings.TrimSpace(message.Content) != "" {
		content = append(content, map[string]any{
			"type": "text",
			"text": message.Content,
		})
	}
	for _, toolCall := range message.ToolCalls {
		args := map[string]any{}
		if strings.TrimSpace(toolCall.Function.Arguments) != "" {
			_ = json.Unmarshal([]byte(toolCall.Function.Arguments), &args)
		}
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    toolCall.ID,
			"name":  toolCall.Function.Name,
			"input": args,
		})
	}
	response := map[string]any{
		"id":            stringValue(upstream["id"]),
		"type":          "message",
		"role":          "assistant",
		"model":         firstNonEmpty(strings.TrimSpace(requestedModel), stringValue(upstream["model"])),
		"content":       content,
		"stop_reason":   mapFinishReasonToClaude(finishReason),
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  usage["prompt_tokens"],
			"output_tokens": usage["completion_tokens"],
		},
	}
	return json.Marshal(response)
}

func RenderGeminiResponse(raw []byte, requestedModel string) ([]byte, error) {
	var upstream map[string]any
	if err := json.Unmarshal(raw, &upstream); err != nil {
		return nil, err
	}
	message, usage, finishReason := extractOpenAIResponsePieces(upstream)
	parts := make([]map[string]any, 0)
	if strings.TrimSpace(message.Content) != "" {
		parts = append(parts, map[string]any{"text": message.Content})
	}
	for _, toolCall := range message.ToolCalls {
		args := map[string]any{}
		if strings.TrimSpace(toolCall.Function.Arguments) != "" {
			_ = json.Unmarshal([]byte(toolCall.Function.Arguments), &args)
		}
		parts = append(parts, map[string]any{
			"functionCall": map[string]any{
				"name": toolCall.Function.Name,
				"args": args,
			},
		})
	}
	response := map[string]any{
		"candidates": []map[string]any{{
			"index": 0,
			"content": map[string]any{
				"role":  "model",
				"parts": parts,
			},
			"finishReason": mapFinishReasonToGemini(finishReason),
		}},
		"usageMetadata": map[string]any{
			"promptTokenCount":     usage["prompt_tokens"],
			"candidatesTokenCount": usage["completion_tokens"],
			"totalTokenCount":      usage["total_tokens"],
		},
		"modelVersion": firstNonEmpty(strings.TrimSpace(requestedModel), stringValue(upstream["model"])),
	}
	return json.Marshal(response)
}

func extractOpenAIResponsePieces(upstream map[string]any) (openAIMessagePayload, map[string]any, string) {
	message := openAIMessagePayload{}
	usage := map[string]any{
		"prompt_tokens":     0,
		"completion_tokens": 0,
		"total_tokens":      0,
	}
	finishReason := ""
	if usageMap, ok := upstream["usage"].(map[string]any); ok {
		for k, v := range usageMap {
			usage[k] = v
		}
	}
	choices, _ := upstream["choices"].([]any)
	if len(choices) == 0 {
		return message, usage, finishReason
	}
	choiceMap, _ := choices[0].(map[string]any)
	if msgMap, ok := choiceMap["message"].(map[string]any); ok {
		message.Role = stringValue(msgMap["role"])
		message.Content = strings.TrimSpace(stringValue(msgMap["content"]))
		if toolCalls, ok := msgMap["tool_calls"]; ok {
			message.ToolCalls = parseToolCalls(toolCalls)
		}
	}
	finishReason = strings.TrimSpace(stringValue(choiceMap["finish_reason"]))
	return message, usage, finishReason
}

func parseToolCalls(raw any) []openAIToolCall {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]openAIToolCall, 0, len(items))
	for _, item := range items {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		call := openAIToolCall{
			ID:   stringValue(itemMap["id"]),
			Type: stringValue(itemMap["type"]),
		}
		if fnMap, ok := itemMap["function"].(map[string]any); ok {
			call.Function.Name = stringValue(fnMap["name"])
			call.Function.Arguments = stringValue(fnMap["arguments"])
		}
		result = append(result, call)
	}
	return result
}

func buildPromptFromMessages(messages []map[string]any) string {
	var promptBuilder bytes.Buffer
	for _, msg := range messages {
		promptBuilder.WriteString(normalizeRole(stringValue(msg["role"])))
		promptBuilder.WriteString(":")
		promptBuilder.WriteString(strings.TrimSpace(stringValue(msg["content"])))
		promptBuilder.WriteString("\n")
	}
	return promptBuilder.String()
}

func buildPromptFromMessageMaps(messages []map[string]any) string {
	return buildPromptFromMessages(messages)
}

func normalizeOpenAIMessages(raw any) ([]map[string]any, bool) {
	items, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	messages := make([]map[string]any, 0, len(items))
	for _, item := range items {
		msgMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := normalizeRole(stringValue(msgMap["role"]))
		content := extractOpenAIContent(msgMap["content"])
		if role == "" || (content == "" && role != "assistant") {
			continue
		}
		normalized := map[string]any{
			"role":    role,
			"content": content,
		}
		if role == "assistant" {
			if toolCalls, ok := normalizeOpenAIToolCalls(msgMap["tool_calls"]); ok {
				normalized["tool_calls"] = toolCalls
			}
		}
		if role == "tool" {
			if toolCallID := strings.TrimSpace(stringValue(msgMap["tool_call_id"])); toolCallID != "" {
				normalized["tool_call_id"] = toolCallID
			}
			if name := strings.TrimSpace(stringValue(msgMap["name"])); name != "" {
				normalized["name"] = name
			}
		}
		messages = append(messages, normalized)
	}
	return messages, true
}

func convertMessagesForMeta(messages []map[string]any) []ChatMessage {
	result := make([]ChatMessage, 0, len(messages))
	for _, msg := range messages {
		result = append(result, ChatMessage{
			Role:    stringValue(msg["role"]),
			Content: stringValue(msg["content"]),
		})
	}
	return result
}

func normalizeOpenAIToolCalls(raw any) ([]map[string]any, bool) {
	parsed := parseToolCalls(raw)
	if len(parsed) == 0 {
		return nil, false
	}
	items := make([]map[string]any, 0, len(parsed))
	for _, toolCall := range parsed {
		items = append(items, map[string]any{
			"id":   firstNonEmpty(toolCall.ID, newCallID()),
			"type": firstNonEmpty(toolCall.Type, "function"),
			"function": map[string]any{
				"name":      toolCall.Function.Name,
				"arguments": normalizeJSONString(toolCall.Function.Arguments),
			},
		})
	}
	return items, true
}

func normalizeToolChoice(raw any) (any, bool) {
	switch v := raw.(type) {
	case nil:
		return nil, false
	case string:
		value := strings.TrimSpace(v)
		if value == "" {
			return nil, false
		}
		switch strings.ToLower(value) {
		case "auto", "none", "required":
			return strings.ToLower(value), true
		default:
			return value, true
		}
	case map[string]any:
		if mode := strings.ToLower(strings.TrimSpace(stringValue(v["type"]))); mode == "none" || mode == "required" {
			return mode, true
		}
		if functionMap, ok := v["function"].(map[string]any); ok {
			if strings.TrimSpace(stringValue(functionMap["name"])) != "" {
				return "auto", true
			}
		}
		if name := strings.TrimSpace(stringValue(v["name"])); name != "" {
			return "auto", true
		}
		return nil, false
	default:
		return nil, false
	}
}

func extractOpenAIContent(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if strings.TrimSpace(stringValue(itemMap["type"])) == "text" {
				text := strings.TrimSpace(stringValue(itemMap["text"]))
				if text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func extractClaudeSystem(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if strings.TrimSpace(stringValue(itemMap["type"])) == "text" {
				text := strings.TrimSpace(stringValue(itemMap["text"]))
				if text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func extractClaudeContent(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch strings.TrimSpace(stringValue(itemMap["type"])) {
			case "text":
				text := strings.TrimSpace(stringValue(itemMap["text"]))
				if text != "" {
					parts = append(parts, text)
				}
			case "tool_result":
				text := strings.TrimSpace(stringValue(itemMap["content"]))
				if text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func convertClaudeTools(raw any) []map[string]any {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		toolMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(stringValue(toolMap["name"]))
		if name == "" {
			continue
		}
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": strings.TrimSpace(stringValue(toolMap["description"])),
				"parameters":  toolMap["input_schema"],
			},
		})
	}
	return result
}

func extractGeminiSystemInstruction(raw any) string {
	msgMap, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	return extractGeminiParts(msgMap["parts"])
}

func extractGeminiParts(raw any) string {
	items, ok := raw.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if text := strings.TrimSpace(stringValue(itemMap["text"])); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func convertGeminiTools(raw any) []map[string]any {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0)
	for _, item := range items {
		toolMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		declarations, ok := toolMap["functionDeclarations"].([]any)
		if !ok {
			declarations, _ = toolMap["function_declarations"].([]any)
		}
		for _, declaration := range declarations {
			declMap, ok := declaration.(map[string]any)
			if !ok {
				continue
			}
			name := strings.TrimSpace(stringValue(declMap["name"]))
			if name == "" {
				continue
			}
			result = append(result, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        name,
					"description": strings.TrimSpace(stringValue(declMap["description"])),
					"parameters":  declMap["parameters"],
				},
			})
		}
	}
	return result
}

func normalizeModel(model string) string {
	model = strings.TrimSpace(model)
	if mapped, ok := modelMapping[model]; ok {
		return mapped
	}
	switch {
	case strings.HasPrefix(model, "claude-"):
		return "meta/llama-3.1-70b-instruct"
	case strings.HasPrefix(model, "gemini-"):
		return "meta/llama-3.1-70b-instruct"
	case strings.HasPrefix(model, "text-embedding-"):
		return "nvidia/nv-embed-v1"
	default:
		return model
	}
}

func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant", "model":
		return "assistant"
	case "system":
		return "system"
	case "tool":
		return "tool"
	default:
		return "user"
	}
}

func normalizeGeminiRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "model" {
		return "assistant"
	}
	if role == "user" {
		return "user"
	}
	return "user"
}

func extractGeminiModel(routeTarget string) string {
	routeTarget = strings.TrimSpace(routeTarget)
	if decoded, err := url.PathUnescape(routeTarget); err == nil {
		routeTarget = decoded
	}
	if strings.HasSuffix(routeTarget, ":generateContent") {
		return strings.TrimSuffix(routeTarget, ":generateContent")
	}
	if strings.HasSuffix(routeTarget, ":streamGenerateContent") {
		return strings.TrimSuffix(routeTarget, ":streamGenerateContent")
	}
	return routeTarget
}

func mapFinishReasonToClaude(reason string) any {
	switch strings.TrimSpace(reason) {
	case "length":
		// 不返回 max_tokens，否则 Claude Code 会认为回复被截断而中断任务
		// 返回 end_turn 让客户端认为这是正常结束
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "stop", "":
		return "end_turn"
	default:
		return reason
	}
}

func mapFinishReasonToGemini(reason string) string {
	switch strings.TrimSpace(reason) {
	case "length":
		// 不返回 MAX_TOKENS，避免客户端认为被截断
		return "STOP"
	case "tool_calls":
		return "STOP"
	case "stop", "":
		return "STOP"
	default:
		return strings.ToUpper(reason)
	}
}

func stringValue(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}

func floatValue(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case json.Number:
		fv, err := v.Float64()
		return fv, err == nil
	default:
		return 0, false
	}
}

func floatPtrValue(raw any) (*float64, bool) {
	v, ok := floatValue(raw)
	if !ok {
		return nil, false
	}
	return &v, true
}

func intValue(raw any) (int, bool) {
	switch v := raw.(type) {
	case float64:
		return int(v), true
	case float32:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case int32:
		return int(v), true
	case json.Number:
		iv, err := v.Int64()
		return int(iv), err == nil
	default:
		return 0, false
	}
}

func boolValue(raw any) (bool, bool) {
	v, ok := raw.(bool)
	return v, ok
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
