package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"nvidia-api-gateway/pkg/models"

	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

type responsesFunctionCallItem struct {
	OutputIndex int
	ItemID      string
	CallID      string
	Name        string
	Arguments   strings.Builder
}

type responsesStreamTranslator struct {
	w                io.Writer
	flusher          http.Flusher
	responseID       string
	model            string
	requestMessages  []map[string]any
	messageItemID    string
	messageOutputIdx int
	messageStarted   bool
	messageText      strings.Builder
	contentStarted   bool
	usage            map[string]any
	finishReason     string
	functionCalls    map[int]*responsesFunctionCallItem
	functionIndexes  []int
	completed        bool
	hasContent       bool
	headersWritten   bool
}

func (g *Gateway) HandleOpenAIResponses(c *fiber.Ctx) error {
	cfg := loadSystemConfig()
	if !protocolEnabled(cfg, "openai") {
		return c.Status(fiber.StatusNotFound).JSON(openAIError("route_disabled", "OpenAI compatibility route is disabled", "invalid_request_error"))
	}
	rawBody := append([]byte(nil), c.Body()...)
	translatedBody, reqMeta, err := TranslateResponsesRequest(rawBody)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(openAIError("invalid_request_error", err.Error(), "invalid_request_error"))
	}
	responseID := newResponseID()
	masterKey, _ := c.Locals("masterKey").(*models.MasterKey)
	affinityID := resolveConversationAffinityID(c.Get("X-Conversation-ID"), rawBody, masterKey)
	estTokens := EstimateTokens(reqMeta.Prompt)
	if err := g.usageTracker.Check(context.Background(), masterKey, estTokens); err != nil {
		status := fiber.StatusTooManyRequests
		errorCode := "rate_limit_exceeded"
		if err.Error() == "Quota exceeded" {
			status = fiber.StatusPaymentRequired
			errorCode = "quota_exceeded"
		}
		return c.Status(status).JSON(openAIError(errorCode, err.Error(), "rate_limit_error"))
	}
	if reqMeta.Stream {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			g.executeResponsesStream(r.Context(), w, translatedBody, responseID, reqMeta.RequestedModel, reqMeta.Messages, estTokens, masterKey, affinityID)
		})
		fasthttpadaptor.NewFastHTTPHandler(handler)(c.Context())
		return nil
	}
	result := g.executeOpenAINonStream(context.Background(), translatedBody, reqMeta.RequestedModel, reqMeta.Prompt, reqMeta.Temperature, estTokens, masterKey, affinityID)
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		if result.ContentType != "" {
			c.Set("Content-Type", result.ContentType)
		}
		applyResponseHeaders(c, result.Headers)
		return c.Status(result.StatusCode).Send(result.Body)
	}
	payload, err := buildResponsesObjectFromOpenAIResult(responseID, reqMeta.RequestedModel, result.Body)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(openAIError("upstream_parse_error", "Failed to render responses object", "api_error"))
	}
	assistantMessage, err := buildAssistantConversationMessageFromOpenAIResult(result.Body)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(openAIError("upstream_parse_error", "Failed to restore responses conversation", "api_error"))
	}
	responsesStore.put(responseID, payload, appendConversationHistory(reqMeta.Messages, assistantMessage))
	c.Set("Content-Type", "application/json")
	return c.Status(fiber.StatusOK).Send(payload)
}

func (g *Gateway) GetOpenAIResponseByID(c *fiber.Ctx) error {
	cfg := loadSystemConfig()
	if !protocolEnabled(cfg, "openai") {
		return c.Status(fiber.StatusNotFound).JSON(openAIError("route_disabled", "OpenAI compatibility route is disabled", "invalid_request_error"))
	}
	responseID := strings.TrimSpace(c.Params("responseId"))
	if responseID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(openAIError("invalid_request_error", "response id is required", "invalid_request_error"))
	}
	payload, ok := responsesStore.get(responseID)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(openAIError("not_found", "response was not found or expired", "invalid_request_error"))
	}
	c.Set("Content-Type", "application/json")
	return c.Status(fiber.StatusOK).Send(payload)
}

func buildResponsesObjectFromOpenAIResult(responseID, requestedModel string, raw []byte) ([]byte, error) {
	var upstream map[string]any
	if err := json.Unmarshal(raw, &upstream); err != nil {
		return nil, err
	}
	message, usage, _ := extractOpenAIResponsePieces(upstream)
	output := make([]any, 0, 2)
	outputText := strings.TrimSpace(message.Content)
	if outputText != "" || len(message.ToolCalls) == 0 {
		messageContent := make([]any, 0, 1)
		if outputText != "" {
			messageContent = append(messageContent, map[string]any{
				"type": "output_text",
				"text": outputText,
			})
		}
		output = append(output, map[string]any{
			"id":      newResponseMessageID(),
			"type":    "message",
			"role":    "assistant",
			"status":  "completed",
			"content": messageContent,
		})
	}
	for _, toolCall := range message.ToolCalls {
		output = append(output, map[string]any{
			"id":        newFunctionCallItemID(),
			"type":      "function_call",
			"call_id":   firstNonEmpty(toolCall.ID, newCallID()),
			"name":      toolCall.Function.Name,
			"arguments": normalizeJSONString(toolCall.Function.Arguments),
			"status":    "completed",
		})
	}
	response := map[string]any{
		"id":          responseID,
		"type":        "response",
		"object":      "response",
		"created_at":  time.Now().Unix(),
		"status":      "completed",
		"model":       firstNonEmpty(strings.TrimSpace(requestedModel), stringValue(upstream["model"])),
		"output":      output,
		"output_text": outputText,
		"usage": map[string]any{
			"input_tokens":  lookupUsageValue(usage, "prompt_tokens"),
			"output_tokens": lookupUsageValue(usage, "completion_tokens"),
			"total_tokens":  lookupUsageValue(usage, "total_tokens"),
		},
	}
	return json.Marshal(response)
}

func buildAssistantConversationMessageFromOpenAIResult(raw []byte) (map[string]any, error) {
	var upstream map[string]any
	if err := json.Unmarshal(raw, &upstream); err != nil {
		return nil, err
	}
	message, _, _ := extractOpenAIResponsePieces(upstream)
	return buildAssistantConversationMessage(message), nil
}

func buildAssistantConversationMessage(message openAIMessagePayload) map[string]any {
	assistant := map[string]any{
		"role":    "assistant",
		"content": strings.TrimSpace(message.Content),
	}
	if len(message.ToolCalls) > 0 {
		assistant["tool_calls"] = buildConversationToolCalls(message.ToolCalls)
	}
	return assistant
}

func buildConversationToolCalls(toolCalls []openAIToolCall) []map[string]any {
	if len(toolCalls) == 0 {
		return nil
	}
	items := make([]map[string]any, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		items = append(items, map[string]any{
			"id":   firstNonEmpty(toolCall.ID, newCallID()),
			"type": firstNonEmpty(toolCall.Type, "function"),
			"function": map[string]any{
				"name":      toolCall.Function.Name,
				"arguments": normalizeJSONString(toolCall.Function.Arguments),
			},
		})
	}
	return items
}

func appendConversationHistory(history []map[string]any, assistant map[string]any) []map[string]any {
	combined := cloneStoredConversation(history)
	if len(assistant) == 0 {
		return combined
	}
	return append(combined, cloneStoredConversation([]map[string]any{assistant})[0])
}

func (g *Gateway) executeResponsesStream(ctx context.Context, w http.ResponseWriter, translatedBody []byte, responseID, requestedModel string, requestMessages []map[string]any, estTokens int, masterKey *models.MasterKey, affinityID string) {
	cfg := loadSystemConfig()
	diagnostics := newUpstreamAttemptDiagnostics("responses.stream")
	lastErr := "upstream request failed"
	for i := 0; i < crossKeyAttemptBudget(cfg.MaxRetries); i++ {
		key, reusedPreferredKey, err := g.acquirePreferredKeyWithQueue(ctx, nil, nil, cfg.MaxConcurrency, false, "responses.stream", affinityID)
		if key != "" {
			diagnostics.noteSelectedKey(key)
			g.refreshConversationKeyBinding(affinityID, key, false)
		}
		if err != nil {
			if err.Error() == "queue timeout" {
				writeResponsesStreamError(w, responseID, requestedModel, http.StatusServiceUnavailable, "Queue timeout, no available keys")
				return
			}
			lastErr = err.Error()
			continue
		}
		resp, reader, cancel, retry, err := g.openUpstreamStream(ctx, cfg, key, translatedBody, "responses.stream", affinityID)
		if retry {
			diagnostics.noteRetry("上一个上游 NVIDIA 官方 Key 请求失败，已继续重试可用 Key")
			if diagnostics.LastRetryCause != "" {
				lastErr = diagnostics.LastRetryCause
			}
			_ = g.scheduler.ReleaseKey(ctx, key)
			if cancel != nil {
				cancel()
			}
			if err != nil {
				lastErr = err.Error()
			}
			continue
		}
		if err != nil {
			_ = g.scheduler.ReleaseKey(ctx, key)
			if cancel != nil {
				cancel()
			}
			writeResponsesStreamError(w, responseID, requestedModel, http.StatusBadGateway, err.Error())
			return
		}
		g.refreshConversationKeyBinding(affinityID, key, !reusedPreferredKey)
		applyResponseHeaders(w.Header(), diagnostics.headers())
		translator := newResponsesStreamTranslator(w, responseID, requestedModel, requestMessages)
		respFlusher, _ := w.(http.Flusher)
		streamErr := relayOpenAIStream(ctx, reader, translator, streamRelayOptions{
			IdleTimeout:  time.Duration(cfg.StreamIdleTimeoutSec) * time.Second,
			KeepAliveSec: cfg.StreamKeepAliveSec,
			Writer:       w,
			Flusher:      respFlusher,
		})
		_ = resp.Body.Close()
		if cancel != nil {
			cancel()
		}
		_ = g.scheduler.ReleaseKey(ctx, key)
		if errors.Is(streamErr, errStreamEmptyContent) && !translator.Started() {
			recordUpstreamRuntimeEvent("responses.stream", "upstream_empty_response", key, false, 0, "上游流式响应未产出任何内容，正在切换下一个 NVIDIA 官方 Key 重试")
			g.clearConversationKeyBinding(affinityID, key)
			lastErr = "upstream stream returned empty content"
			continue
		}
		if streamErr != nil {
			_ = translator.Error(streamErr.Error())
			return
		}
		if err := g.usageTracker.Record(ctx, masterKey, estTokens); err == nil {
			incrementQuota(masterKey, estTokens)
		}
		return
	}
	writeResponsesStreamError(w, responseID, requestedModel, http.StatusBadGateway, lastErr)
}

func newResponsesStreamTranslator(w http.ResponseWriter, responseID, requestedModel string, requestMessages []map[string]any) *responsesStreamTranslator {
	flusher, _ := w.(http.Flusher)
	// Headers 推迟到第一个事件 emit 时再写，便于空回复时切 key 重试。
	translator := &responsesStreamTranslator{
		w:                w,
		flusher:          flusher,
		responseID:       responseID,
		model:            requestedModel,
		requestMessages:  cloneStoredConversation(requestMessages),
		messageOutputIdx: -1,
		usage:            map[string]any{},
		functionCalls:    make(map[int]*responsesFunctionCallItem),
	}
	return translator
}

func (t *responsesStreamTranslator) Consume(chunk openAIStreamChunk) error {
	if chunk.Model != "" {
		t.model = firstNonEmpty(t.model, chunk.Model)
	}
	if chunk.Usage != nil {
		t.usage = chunk.Usage
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			t.hasContent = true
			if err := t.ensureMessageItem(); err != nil {
				return err
			}
			t.messageText.WriteString(choice.Delta.Content)
			if err := t.emitEvent("response.output_text.delta", map[string]any{
				"type":          "response.output_text.delta",
				"id":            t.responseID,
				"response_id":   t.responseID,
				"item_id":       t.messageItemID,
				"output_index":  t.messageOutputIdx,
				"content_index": 0,
				"delta":         choice.Delta.Content,
			}); err != nil {
				return err
			}
		}
		for _, toolCall := range choice.Delta.ToolCalls {
			if strings.TrimSpace(toolCall.ID) != "" || strings.TrimSpace(toolCall.Type) != "" || strings.TrimSpace(toolCall.Function.Name) != "" || strings.TrimSpace(toolCall.Function.Arguments) != "" {
				t.hasContent = true
			}
			if err := t.consumeToolCall(toolCall); err != nil {
				return err
			}
		}
		if choice.FinishReason != "" {
			t.finishReason = choice.FinishReason
		}
	}
	return nil
}

func (t *responsesStreamTranslator) HasContent() bool {
	return t.hasContent
}

func (t *responsesStreamTranslator) Started() bool {
	return t.headersWritten
}

func (t *responsesStreamTranslator) Finish() error {
	if t.completed {
		return nil
	}
	if !t.hasContent && !t.headersWritten {
		t.completed = true
		return nil
	}
	if t.messageStarted {
		text := t.messageText.String()
		part := map[string]any{
			"type": "output_text",
			"text": text,
		}
		if err := t.emitEvent("response.output_text.done", map[string]any{
			"type":          "response.output_text.done",
			"id":            t.responseID,
			"response_id":   t.responseID,
			"item_id":       t.messageItemID,
			"output_index":  t.messageOutputIdx,
			"content_index": 0,
			"text":          text,
		}); err != nil {
			return err
		}
		if err := t.emitEvent("response.content_part.done", map[string]any{
			"type":          "response.content_part.done",
			"id":            t.responseID,
			"response_id":   t.responseID,
			"item_id":       t.messageItemID,
			"output_index":  t.messageOutputIdx,
			"content_index": 0,
			"part":          part,
		}); err != nil {
			return err
		}
		if err := t.emitEvent("response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"id":           t.responseID,
			"response_id":  t.responseID,
			"output_index": t.messageOutputIdx,
			"item_id":      t.messageItemID,
			"item": map[string]any{
				"id":      t.messageItemID,
				"type":    "message",
				"role":    "assistant",
				"status":  "completed",
				"content": []any{part},
			},
		}); err != nil {
			return err
		}
	}
	for _, idx := range t.sortedFunctionIndexes() {
		call := t.functionCalls[idx]
		if call == nil {
			continue
		}
		arguments := normalizeJSONString(call.Arguments.String())
		if err := t.emitEvent("response.function_call_arguments.done", map[string]any{
			"type":         "response.function_call_arguments.done",
			"id":           t.responseID,
			"response_id":  t.responseID,
			"item_id":      call.ItemID,
			"output_index": call.OutputIndex,
			"call_id":      call.CallID,
			"name":         call.Name,
			"arguments":    arguments,
		}); err != nil {
			return err
		}
		if err := t.emitEvent("response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"id":           t.responseID,
			"response_id":  t.responseID,
			"output_index": call.OutputIndex,
			"item_id":      call.ItemID,
			"item": map[string]any{
				"id":        call.ItemID,
				"type":      "function_call",
				"call_id":   call.CallID,
				"name":      call.Name,
				"arguments": arguments,
				"status":    "completed",
			},
		}); err != nil {
			return err
		}
	}
	responseObj, err := t.buildResponseObject()
	if err != nil {
		return err
	}
	responsesStore.put(t.responseID, responseObj, appendConversationHistory(t.requestMessages, t.assistantConversationMessage()))
	var response map[string]any
	if err := json.Unmarshal(responseObj, &response); err != nil {
		return err
	}
	if err := t.emitEvent("response.completed", map[string]any{
		"type":        "response.completed",
		"response_id": t.responseID,
		"response":    response,
	}); err != nil {
		return err
	}
	if _, err := io.WriteString(t.w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	if t.flusher != nil {
		t.flusher.Flush()
	}
	t.completed = true
	return nil
}

func (t *responsesStreamTranslator) Error(message string) error {
	payload := map[string]any{
		"type":        "response.failed",
		"id":          t.responseID,
		"response_id": t.responseID,
		"object":      "response",
		"model":       t.model,
		"status":      "failed",
		"status_code": http.StatusBadGateway,
		"error": map[string]any{
			"message": message,
			"type":    "api_error",
			"code":    "api_error",
			"param":   nil,
		},
	}
	if err := t.emitEvent("response.failed", payload); err != nil {
		return err
	}
	_, err := io.WriteString(t.w, "data: [DONE]\n\n")
	if t.flusher != nil {
		t.flusher.Flush()
	}
	return err
}

func (t *responsesStreamTranslator) ensureMessageItem() error {
	if t.messageStarted {
		return nil
	}
	t.messageStarted = true
	t.messageOutputIdx = 0
	t.messageItemID = newResponseMessageID()
	item := map[string]any{
		"id":      t.messageItemID,
		"type":    "message",
		"role":    "assistant",
		"status":  "in_progress",
		"content": []any{},
	}
	if err := t.emitEvent("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"id":           t.responseID,
		"response_id":  t.responseID,
		"output_index": t.messageOutputIdx,
		"item_id":      t.messageItemID,
		"item":         item,
	}); err != nil {
		return err
	}
	part := map[string]any{"type": "output_text", "text": ""}
	if err := t.emitEvent("response.content_part.added", map[string]any{
		"type":          "response.content_part.added",
		"id":            t.responseID,
		"response_id":   t.responseID,
		"item_id":       t.messageItemID,
		"output_index":  t.messageOutputIdx,
		"content_index": 0,
		"part":          part,
	}); err != nil {
		return err
	}
	t.contentStarted = true
	return nil
}

func (t *responsesStreamTranslator) consumeToolCall(toolCall openAIStreamToolCall) error {
	call, ok := t.functionCalls[toolCall.Index]
	if !ok {
		call = &responsesFunctionCallItem{
			OutputIndex: t.nextFunctionOutputIndex(),
			ItemID:      newFunctionCallItemID(),
			CallID:      firstNonEmpty(toolCall.ID, newCallID()),
			Name:        toolCall.Function.Name,
		}
		t.functionCalls[toolCall.Index] = call
		t.functionIndexes = append(t.functionIndexes, toolCall.Index)
		item := map[string]any{
			"id":        call.ItemID,
			"type":      "function_call",
			"call_id":   call.CallID,
			"name":      call.Name,
			"arguments": "",
			"status":    "in_progress",
		}
		if err := t.emitEvent("response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"id":           t.responseID,
			"response_id":  t.responseID,
			"output_index": call.OutputIndex,
			"item_id":      call.ItemID,
			"item":         item,
		}); err != nil {
			return err
		}
	}
	if call.Name == "" && toolCall.Function.Name != "" {
		call.Name = toolCall.Function.Name
	}
	if call.CallID == "" && toolCall.ID != "" {
		call.CallID = toolCall.ID
	}
	if toolCall.Function.Arguments != "" {
		call.Arguments.WriteString(toolCall.Function.Arguments)
		return t.emitEvent("response.function_call_arguments.delta", map[string]any{
			"type":         "response.function_call_arguments.delta",
			"id":           t.responseID,
			"response_id":  t.responseID,
			"item_id":      call.ItemID,
			"output_index": call.OutputIndex,
			"call_id":      call.CallID,
			"delta":        toolCall.Function.Arguments,
		})
	}
	return nil
}

func (t *responsesStreamTranslator) buildResponseObject() ([]byte, error) {
	output := make([]any, 0, 1+len(t.functionCalls))
	outputText := strings.TrimSpace(t.messageText.String())
	if t.messageStarted {
		messageContent := []any{}
		if outputText != "" {
			messageContent = append(messageContent, map[string]any{
				"type": "output_text",
				"text": outputText,
			})
		}
		output = append(output, map[string]any{
			"id":      t.messageItemID,
			"type":    "message",
			"role":    "assistant",
			"status":  "completed",
			"content": messageContent,
		})
	}
	for _, idx := range t.sortedFunctionIndexes() {
		call := t.functionCalls[idx]
		if call == nil {
			continue
		}
		output = append(output, map[string]any{
			"id":        call.ItemID,
			"type":      "function_call",
			"call_id":   call.CallID,
			"name":      call.Name,
			"arguments": normalizeJSONString(call.Arguments.String()),
			"status":    "completed",
		})
	}
	response := map[string]any{
		"id":          t.responseID,
		"type":        "response",
		"object":      "response",
		"created_at":  time.Now().Unix(),
		"status":      "completed",
		"model":       t.model,
		"output":      output,
		"output_text": outputText,
		"usage": map[string]any{
			"input_tokens":  lookupUsageValue(t.usage, "prompt_tokens"),
			"output_tokens": lookupUsageValue(t.usage, "completion_tokens"),
			"total_tokens":  lookupUsageValue(t.usage, "total_tokens"),
		},
	}
	return json.Marshal(response)
}

func (t *responsesStreamTranslator) emitEvent(event string, payload any) error {
	if !t.headersWritten {
		if rw, ok := t.w.(http.ResponseWriter); ok {
			writeStreamHeaders(rw, t.flusher)
		}
		t.headersWritten = true
		// 第一次写入前补一条 response.created（旧实现就是在 newResponsesStreamTranslator 里发的，
		// 这里 inline 进来，保持调用方语义一致）。
		if event != "response.created" {
			created, _ := json.Marshal(map[string]any{
				"type":        "response.created",
				"id":          t.responseID,
				"response_id": t.responseID,
				"object":      "response",
				"model":       t.model,
				"status":      "in_progress",
			})
			_, _ = io.WriteString(t.w, "event: response.created\n")
			_, _ = io.WriteString(t.w, "data: ")
			_, _ = t.w.Write(created)
			_, _ = io.WriteString(t.w, "\n\n")
			if t.flusher != nil {
				t.flusher.Flush()
			}
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(t.w, "event: "+event+"\n"); err != nil {
		return err
	}
	if _, err := io.WriteString(t.w, "data: "); err != nil {
		return err
	}
	if _, err := t.w.Write(encoded); err != nil {
		return err
	}
	if _, err := io.WriteString(t.w, "\n\n"); err != nil {
		return err
	}
	if t.flusher != nil {
		t.flusher.Flush()
	}
	return nil
}

func (t *responsesStreamTranslator) nextFunctionOutputIndex() int {
	base := 0
	if t.messageStarted {
		base = 1
	}
	return base + len(t.functionIndexes)
}

func (t *responsesStreamTranslator) sortedFunctionIndexes() []int {
	indexes := append([]int(nil), t.functionIndexes...)
	sort.Ints(indexes)
	return indexes
}

func (t *responsesStreamTranslator) assistantConversationMessage() map[string]any {
	toolCalls := make([]openAIToolCall, 0, len(t.functionIndexes))
	for _, idx := range t.sortedFunctionIndexes() {
		call := t.functionCalls[idx]
		if call == nil {
			continue
		}
		toolCall := openAIToolCall{
			ID:   call.CallID,
			Type: "function",
		}
		toolCall.Function.Name = call.Name
		toolCall.Function.Arguments = normalizeJSONString(call.Arguments.String())
		toolCalls = append(toolCalls, toolCall)
	}
	return buildAssistantConversationMessage(openAIMessagePayload{
		Role:      "assistant",
		Content:   strings.TrimSpace(t.messageText.String()),
		ToolCalls: toolCalls,
	})
}

func normalizeJSONString(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "{}"
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return string(b)
}

func responsesErrorType(status int) string {
	switch status {
	case 400, 404, 422:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 429:
		return "rate_limit_error"
	case 503:
		return "service_unavailable_error"
	default:
		if status >= 500 {
			return "api_error"
		}
		return "invalid_request_error"
	}
}

func writeResponsesStreamError(w http.ResponseWriter, responseID, model string, status int, message string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(status)
	payload := map[string]any{
		"type":        "response.failed",
		"id":          responseID,
		"response_id": responseID,
		"object":      "response",
		"model":       model,
		"status":      "failed",
		"status_code": status,
		"error": map[string]any{
			"message": message,
			"type":    responsesErrorType(status),
			"code":    "api_error",
			"param":   nil,
		},
	}
	encoded := mustJSON(payload)
	_, _ = io.WriteString(w, "event: response.failed\n")
	_, _ = io.WriteString(w, "data: ")
	_, _ = w.Write(encoded)
	_, _ = io.WriteString(w, "\n\ndata: [DONE]\n\n")
}

func newResponseID() string {
	return "resp_" + fmt.Sprintf("%d", time.Now().UnixNano())
}

func newResponseMessageID() string {
	return "msg_" + fmt.Sprintf("%d", time.Now().UnixNano())
}

func newFunctionCallItemID() string {
	return "fc_" + fmt.Sprintf("%d", time.Now().UnixNano())
}

func newCallID() string {
	return "call_" + fmt.Sprintf("%d", time.Now().UnixNano())
}
