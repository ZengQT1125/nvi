package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"nvidia-api-gateway/pkg/cache"
	"nvidia-api-gateway/pkg/middleware"
	"nvidia-api-gateway/pkg/models"
	"nvidia-api-gateway/pkg/scheduler"

	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

var bufferPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 4096)
	},
}

type XrayRuntimeRepairer interface {
	Ensure(ctx context.Context) error
	Reload(ctx context.Context) error
}

type Gateway struct {
	scheduler    *scheduler.Scheduler
	cache        *cache.SemanticCache
	usageTracker *middleware.UsageTracker
	client       *http.Client
	xrayRepairer XrayRuntimeRepairer
}

type proxyResult struct {
	StatusCode  int
	ContentType string
	Body        []byte
	Headers     map[string]string
}

func NewGateway(sched *scheduler.Scheduler, semanticCache *cache.SemanticCache, usageTracker *middleware.UsageTracker, repairers ...XrayRuntimeRepairer) *Gateway {
	var repairer XrayRuntimeRepairer
	if len(repairers) > 0 {
		repairer = repairers[0]
	}
	return &Gateway{
		scheduler:    sched,
		cache:        semanticCache,
		usageTracker: usageTracker,
		client:       &http.Client{Timeout: 10 * time.Minute},
		xrayRepairer: repairer,
	}
}

func (g *Gateway) acquirePreferredKeyWithQueue(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, maxConcurrency int, allowEarlyHeaders bool, operation, affinityID string) (string, bool, error) {
	affinityID = strings.TrimSpace(affinityID)
	if affinityID != "" && g != nil && g.scheduler != nil {
		if preferredKey, ok := globalConversationAffinityStore.get(affinityID); ok {
			acquired, err := g.scheduler.TryAcquireSpecificKey(ctx, preferredKey, maxConcurrency)
			if err != nil {
				recordUpstreamRuntimeEvent(operation, "scheduler_error", preferredKey, false, 0, err.Error())
				return "", false, err
			}
			if acquired {
				recordUpstreamRuntimeEvent(operation, "key_selected", preferredKey, true, 0, "已复用当前可用的 NVIDIA 官方 Key")
				return preferredKey, true, nil
			}
		}
	}
	key, err := g.acquireKeyWithQueue(ctx, w, flusher, maxConcurrency, allowEarlyHeaders, operation)
	return key, false, err
}

func (g *Gateway) refreshConversationKeyBinding(affinityID, key string, force bool) {
	affinityID = strings.TrimSpace(affinityID)
	key = strings.TrimSpace(key)
	if affinityID == "" || key == "" {
		return
	}
	if !force {
		if existing, ok := globalConversationAffinityStore.get(affinityID); ok && strings.TrimSpace(existing) != "" {
			return
		}
	}
	globalConversationAffinityStore.set(affinityID, key)
}

func (g *Gateway) clearConversationKeyBinding(affinityID, key string) {
	globalConversationAffinityStore.clear(strings.TrimSpace(affinityID), strings.TrimSpace(key))
}

func (g *Gateway) recoverNetworkPathForKey(ctx context.Context, cfg models.SystemConfig, key string, err error) {
	proxyInfo, ok := effectiveProxyRuntimeInfoForAPIKey(cfg, key)
	if ok {
		invalidateTransportCache(proxyInfo.URL)
	} else {
		invalidateTransportCache(effectiveProxyURLForAPIKey(cfg, key))
	}
	if ok && strings.TrimSpace(proxyInfo.ManagedBy) == models.CoreManagedByXray && err != nil && classifyUpstreamTransportError(err) == upstreamFailurePolicyNetworkTransient {
		if altURL, switched := selectAlternateManagedProxyURLForAPIKey(cfg, key, proxyInfo.URL); switched {
			invalidateTransportCache(altURL)
		}
		if replacement, switched := promoteAlternativeManagedProxy(proxyInfo); switched {
			invalidateTransportCache(replacement.URL)
		}
	}
	if err == nil || g == nil || g.xrayRepairer == nil {
		return
	}
	if !ok || strings.TrimSpace(proxyInfo.ManagedBy) != models.CoreManagedByXray {
		return
	}
	if !isLikelyLocalLoopbackProxyError(err) {
		return
	}
	// 只清除失败节点的 transport 缓存，不重启 xray
	// 重启 xray 会杀掉所有正在工作的节点连接，造成雪崩
	// xray 的 30 秒健康检查会自动处理节点恢复
	invalidateTransportCache(proxyInfo.URL)
}

func (g *Gateway) acquireKeyWithQueue(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, maxConcurrency int, allowEarlyHeaders bool, operation string) (string, error) {
	if maxConcurrency <= 0 {
		maxConcurrency = 3
	}
	timeout := time.After(45 * time.Second)
	keepAliveTicker := time.NewTicker(15 * time.Second)
	defer keepAliveTicker.Stop()
	pollTicker := time.NewTicker(500 * time.Millisecond)
	defer pollTicker.Stop()

	headersSent := false
	recoveryAttempted := false

	for {
		key, err := g.scheduler.AcquireKey(ctx, maxConcurrency)
		if err != nil {
			recordUpstreamRuntimeEvent(operation, "scheduler_error", "", false, 0, err.Error())
			return "", err
		}
		if key != "" {
			recordUpstreamRuntimeEvent(operation, "key_selected", key, true, 0, "已从上游池选中可用的 NVIDIA 官方 Key")
			return key, nil
		}

		if !recoveryAttempted {
			recoveryAttempted = true
			recordUpstreamRuntimeEvent(operation, "restore_attempt", "", false, 0, "当前没有可用上游 Key，先尝试自动恢复 Cooling/Dead 状态")
			_ = RestoreRecoverableStatuses(ctx, g.scheduler)
			continue
		}

		if allowEarlyHeaders && flusher != nil && w != nil && !headersSent {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()
			headersSent = true
		}

		select {
		case <-ctx.Done():
			recordUpstreamRuntimeEvent(operation, "request_cancelled", "", false, 0, ctx.Err().Error())
			return "", ctx.Err()
		case <-timeout:
			recordUpstreamRuntimeEvent(operation, "queue_timeout", "", false, 503, "上游池中没有可用的 NVIDIA 官方 Key")
			return "", fmt.Errorf("queue timeout")
		case <-keepAliveTicker.C:
			if flusher != nil && headersSent {
				_, _ = w.Write([]byte(": keep-alive\n\n"))
				flusher.Flush()
			}
		case <-pollTicker.C:
		}
	}
}

func shouldUseSemanticCache(temperature *float64) bool {
	return temperature != nil && *temperature == 0
}

func incrementQuota(masterKey *models.MasterKey, tokenCost int) {
	if masterKey == nil || tokenCost <= 0 {
		return
	}
	incrementMasterKeyQuota(masterKey.ID, tokenCost)
	masterKey.UsedQuota += int64(tokenCost)
}

func (g *Gateway) HandleChatCompletions(c *fiber.Ctx) error {
	cfg := loadSystemConfig()
	if !protocolEnabled(cfg, "openai") {
		return c.Status(fiber.StatusNotFound).JSON(openAIError("route_disabled", "OpenAI compatibility route is disabled", "invalid_request_error"))
	}

	rawBody := append([]byte(nil), c.Body()...)
	translatedBody, translatedReq, promptStr, temperature, err := TranslateRequest(rawBody)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(openAIError("invalid_request_error", "Invalid request body format", "invalid_request_error"))
	}

	estTokens := EstimateTokens(promptStr)
	if estTokens > 100000 {
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(openAIError("context_length_exceeded", "Token limit exceeded", "invalid_request_error"))
	}

	masterKey, _ := c.Locals("masterKey").(*models.MasterKey)
	affinityID := resolveConversationAffinityID(c.Get("X-Conversation-ID"), rawBody, masterKey)
	if err := g.usageTracker.Check(context.Background(), masterKey, estTokens); err != nil {
		status := fiber.StatusTooManyRequests
		errorCode := "rate_limit_exceeded"
		if err.Error() == "Quota exceeded" {
			status = fiber.StatusPaymentRequired
			errorCode = "quota_exceeded"
		}
		return c.Status(status).JSON(openAIError(errorCode, err.Error(), "rate_limit_error"))
	}

	if !translatedReq.Stream {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			result := g.executeOpenAINonStream(r.Context(), translatedBody, translatedReq.Model, promptStr, temperature, estTokens, masterKey, affinityID)
			if result.ContentType != "" {
				w.Header().Set("Content-Type", result.ContentType)
			}
			applyResponseHeaders(w.Header(), result.Headers)
			w.WriteHeader(result.StatusCode)
			_, _ = w.Write(result.Body)
		})
		fasthttpadaptor.NewFastHTTPHandler(handler)(c.Context())
		return nil
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.executeOpenAIStream(r.Context(), w, translatedBody, translatedReq.Model, promptStr, temperature, estTokens, masterKey, affinityID)
	})
	fasthttpadaptor.NewFastHTTPHandler(handler)(c.Context())
	return nil
}

func (g *Gateway) HandleOpenAIModels(c *fiber.Ctx) error {
	cfg := loadSystemConfig()
	if !protocolEnabled(cfg, "openai") {
		return c.Status(fiber.StatusNotFound).JSON(openAIError("route_disabled", "OpenAI compatibility route is disabled", "invalid_request_error"))
	}
	result := g.fetchUpstreamModels(context.Background())
	if result.ContentType != "" {
		c.Set("Content-Type", result.ContentType)
	}
	applyResponseHeaders(c, result.Headers)
	return c.Status(result.StatusCode).Send(result.Body)
}

func (g *Gateway) HandleOpenAIModel(c *fiber.Ctx) error {
	result := g.fetchUpstreamModels(context.Background())
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		if result.ContentType != "" {
			c.Set("Content-Type", result.ContentType)
		}
		applyResponseHeaders(c, result.Headers)
		return c.Status(result.StatusCode).Send(result.Body)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Body, &payload); err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(openAIError("upstream_parse_error", "Failed to parse upstream models response", "api_error"))
	}
	target := strings.TrimSpace(c.Params("modelId"))
	data, _ := payload["data"].([]any)
	for _, item := range data {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(stringValue(itemMap["id"])) == target {
			return c.JSON(itemMap)
		}
	}
	return c.Status(fiber.StatusNotFound).JSON(openAIError("model_not_found", "Requested model was not found", "invalid_request_error"))
}

func (g *Gateway) HandleClaudeModels(c *fiber.Ctx) error {
	cfg := loadSystemConfig()
	if !protocolEnabled(cfg, "claude") {
		return c.Status(fiber.StatusNotFound).JSON(claudeErrorResponse("Claude compatibility route is disabled"))
	}
	result := g.fetchUpstreamModels(context.Background())
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		applyResponseHeaders(c, result.Headers)
		return c.Status(result.StatusCode).JSON(claudeErrorResponse(parseUpstreamError(result.Body, "Failed to fetch models")))
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Body, &payload); err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(claudeErrorResponse("Failed to parse upstream models response"))
	}
	items := make([]map[string]any, 0)
	data, _ := payload["data"].([]any)
	for _, item := range data {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		items = append(items, map[string]any{
			"id":           stringValue(itemMap["id"]),
			"type":         "model",
			"display_name": stringValue(itemMap["id"]),
			"created_at":   time.Now().Unix(),
		})
	}
	applyResponseHeaders(c, result.Headers)
	return c.JSON(fiber.Map{"data": items, "has_more": false})
}

func (g *Gateway) HandleClaudeMessages(c *fiber.Ctx) error {
	cfg := loadSystemConfig()
	if !protocolEnabled(cfg, "claude") {
		return c.Status(fiber.StatusNotFound).JSON(claudeErrorResponse("Claude compatibility route is disabled"))
	}
	rawBody := append([]byte(nil), c.Body()...)
	translatedBody, requestedModel, temperature, stream, promptStr, err := TranslateClaudeRequest(rawBody)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(claudeErrorResponse(err.Error()))
	}
	masterKey, _ := c.Locals("masterKey").(*models.MasterKey)
	affinityID := resolveConversationAffinityID(c.Get("X-Conversation-ID"), rawBody, masterKey)
	estTokens := EstimateTokens(promptStr)
	if err := g.usageTracker.Check(context.Background(), masterKey, estTokens); err != nil {
		status := fiber.StatusTooManyRequests
		if err.Error() == "Quota exceeded" {
			status = fiber.StatusPaymentRequired
		}
		return c.Status(status).JSON(claudeErrorResponse(err.Error()))
	}
	if stream {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 走真正的流式路径：实时转发上游 SSE 事件给客户端
			// 不再用"先非流式请求再合成流式"的方式，避免长时间无数据导致客户端超时
			streamBody := setStreamFlag(translatedBody, true)
			g.executeTranslatedCompatStream(r.Context(), w, streamBody, requestedModel, "claude", estTokens, masterKey, affinityID)
		})
		fasthttpadaptor.NewFastHTTPHandler(handler)(c.Context())
		return nil
	}
	result := g.executeOpenAINonStream(context.Background(), translatedBody, requestedModel, promptStr, temperature, estTokens, masterKey, affinityID)
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		applyResponseHeaders(c, result.Headers)
		return c.Status(result.StatusCode).JSON(claudeErrorResponse(parseUpstreamError(result.Body, "Claude request failed")))
	}
	converted, err := RenderClaudeResponse(result.Body, requestedModel)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(claudeErrorResponse("Failed to render Claude response"))
	}
	c.Set("Content-Type", "application/json")
	applyResponseHeaders(c, result.Headers)
	return c.Status(fiber.StatusOK).Send(converted)
}

func (g *Gateway) HandleGeminiContent(c *fiber.Ctx) error {
	cfg := loadSystemConfig()
	if !protocolEnabled(cfg, "gemini") {
		return c.Status(fiber.StatusNotFound).JSON(geminiErrorResponse("Gemini compatibility route is disabled"))
	}
	target := c.Params("target")
	isStream := strings.HasSuffix(target, ":streamGenerateContent")
	rawBody := append([]byte(nil), c.Body()...)
	translatedBody, requestedModel, temperature, promptStr, err := TranslateGeminiRequest(target, rawBody, isStream)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(geminiErrorResponse(err.Error()))
	}
	masterKey, _ := c.Locals("masterKey").(*models.MasterKey)
	affinityID := resolveConversationAffinityID(c.Get("X-Conversation-ID"), rawBody, masterKey)
	estTokens := EstimateTokens(promptStr)
	if err := g.usageTracker.Check(context.Background(), masterKey, estTokens); err != nil {
		status := fiber.StatusTooManyRequests
		if err.Error() == "Quota exceeded" {
			status = fiber.StatusPaymentRequired
		}
		return c.Status(status).JSON(geminiErrorResponse(err.Error()))
	}
	if isStream {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			g.executeTranslatedCompatStream(r.Context(), w, translatedBody, requestedModel, "gemini", estTokens, masterKey, affinityID)
		})
		fasthttpadaptor.NewFastHTTPHandler(handler)(c.Context())
		return nil
	}
	result := g.executeOpenAINonStream(context.Background(), translatedBody, requestedModel, promptStr, temperature, estTokens, masterKey, affinityID)
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		applyResponseHeaders(c, result.Headers)
		return c.Status(result.StatusCode).JSON(geminiErrorResponse(parseUpstreamError(result.Body, "Gemini request failed")))
	}
	converted, err := RenderGeminiResponse(result.Body, requestedModel)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(geminiErrorResponse("Failed to render Gemini response"))
	}
	c.Set("Content-Type", "application/json")
	applyResponseHeaders(c, result.Headers)
	return c.Status(fiber.StatusOK).Send(converted)
}

func (g *Gateway) executeOpenAINonStream(ctx context.Context, translatedBody []byte, model, promptStr string, temperature *float64, estTokens int, masterKey *models.MasterKey, affinityID string) proxyResult {
	cfg := loadSystemConfig()
	diagnostics := newUpstreamAttemptDiagnostics("chat.nonstream")
	cacheEnabled := shouldUseSemanticCache(temperature)
	if cacheEnabled {
		cached, cacheErr := g.cache.Get(ctx, model, promptStr)
		if cacheErr == nil && cached != "" {
			if err := g.usageTracker.Record(ctx, masterKey, estTokens); err == nil {
				incrementQuota(masterKey, estTokens)
			}
			return proxyResult{StatusCode: http.StatusOK, ContentType: "application/json", Body: []byte(cached)}
		}
	}
	lastErr := "upstream request failed"
	maxAttempts := crossKeyAttemptBudget(cfg.MaxRetries) + 5
	for i := 0; i < maxAttempts; i++ {
		// 在每次循环开头检查客户端是否已经断开。
		// 否则 ctx.Err 只会在 acquirePreferredKeyWithQueue 内部捕获后被 continue 吞掉，
		// 导致客户端取消的请求仍然走完所有重试，最后被 silent fallback 包成 200。
		if err := ctx.Err(); err != nil {
			diagnostics.finalize("context_canceled", err.Error())
			result := proxyResult{
				StatusCode:  499, // Client Closed Request（nginx 风格扩展码）
				ContentType: "application/json",
				Body:        mustJSON(openAIError("client_closed_request", err.Error(), "client_error")),
			}
			applyProxyHeaders(&result, diagnostics.headers())
			return result
		}
		key, reusedPreferredKey, err := g.acquirePreferredKeyWithQueue(ctx, nil, nil, cfg.MaxConcurrency, false, "chat.nonstream", affinityID)
		if key != "" {
			diagnostics.noteSelectedKey(key)
			g.refreshConversationKeyBinding(affinityID, key, false)
		}
		if err != nil {
			if err.Error() == "queue timeout" {
				diagnostics.finalize("queue_timeout", err.Error())
				result := proxyResult{StatusCode: http.StatusServiceUnavailable, ContentType: "application/json", Body: mustJSON(openAIError("queue_timeout", "Queue timeout, no available keys", "server_error"))}
				applyProxyHeaders(&result, diagnostics.headers())
				return result
			}
			lastErr = err.Error()
			continue
		}
		result, retry := g.callUpstreamChat(ctx, cfg, key, translatedBody, affinityID)
		_ = g.scheduler.ReleaseKey(ctx, key)
		if retry {
			diagnostics.noteRetry("上一个上游 NVIDIA 官方 Key 请求失败，已继续重试可用 Key")
			if diagnostics.LastRetryCause != "" {
				lastErr = diagnostics.LastRetryCause
			}
			continue
		}
		if result.StatusCode >= 200 && result.StatusCode < 300 {
			if isOpenAIChatCompletionEmpty(result.Body) {
				lastErr = "upstream returned empty response"
				recordUpstreamRuntimeEvent("chat.nonstream", "upstream_error", key, false, result.StatusCode, "upstream returned empty response; retrying with next available key")
				// 清除 affinity 绑定，避免下次循环继续选同一个返回空回复的 key
				g.clearConversationKeyBinding(affinityID, key)
				g.recoverNetworkPathForKey(ctx, cfg, key, errUpstreamEmptyResponse)
				continue
			}
			g.refreshConversationKeyBinding(affinityID, key, !reusedPreferredKey)
			if err := g.usageTracker.Record(ctx, masterKey, estTokens); err == nil {
				incrementQuota(masterKey, estTokens)
			}
			if cacheEnabled && len(result.Body) > 0 {
				_ = g.cache.Set(ctx, model, promptStr, string(result.Body), 24*time.Hour)
			}
			diagnostics.finalize("success", "")
			applyProxyHeaders(&result, diagnostics.headers())
			return result
		}
		if len(result.Body) > 0 {
			diagnostics.finalize("upstream_failed", fmt.Sprintf("upstream status %d", result.StatusCode))
			applyProxyHeaders(&result, diagnostics.headers())
			return result
		}
		lastErr = fmt.Sprintf("upstream status %d", result.StatusCode)
	}
	// 所有重试耗尽：根据 SilentFallbackOnExhaustion 决定返回 200 假回复还是 502 真错误。
	diagnostics.finalize("exhausted", lastErr)
	if !cfg.SilentFallbackOnExhaustion {
		result := proxyResult{
			StatusCode:  http.StatusBadGateway,
			ContentType: "application/json",
			Body:        mustJSON(openAIError("upstream_exhausted", lastErr, "api_error")),
		}
		applyProxyHeaders(&result, diagnostics.headers())
		return result
	}
	// 静默回退：用客户端实际请求的 model（不再硬编码 meta/llama-3.1-70b-instruct），
	// 这样客户端解析响应时不会因为 model 不匹配而报错。
	fallbackModel := strings.TrimSpace(model)
	if fallbackModel == "" {
		fallbackModel = "unknown"
	}
	emptyResponse := map[string]any{
		"id":     "chatcmpl-retry-exhausted",
		"object": "chat.completion",
		"model":  fallbackModel,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": ""},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": estTokens, "completion_tokens": 0, "total_tokens": estTokens},
	}
	result := proxyResult{StatusCode: http.StatusOK, ContentType: "application/json", Body: mustJSON(emptyResponse)}
	applyProxyHeaders(&result, diagnostics.headers())
	return result
}

func (g *Gateway) executeOpenAIStream(ctx context.Context, w http.ResponseWriter, translatedBody []byte, model, promptStr string, temperature *float64, estTokens int, masterKey *models.MasterKey, affinityID string) {
	cfg := loadSystemConfig()
	diagnostics := newUpstreamAttemptDiagnostics("chat.stream")
	flusher, canFlush := w.(http.Flusher)
	cacheEnabled := shouldUseSemanticCache(temperature)
	if cacheEnabled {
		cached, cacheErr := g.cache.Get(ctx, model, promptStr)
		if cacheErr == nil && cached != "" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(cached))
			if canFlush {
				flusher.Flush()
			}
			if err := g.usageTracker.Record(ctx, masterKey, estTokens); err == nil {
				incrementQuota(masterKey, estTokens)
			}
			return
		}
	}
	lastErr := ""
	maxStreamAttempts := crossKeyAttemptBudget(cfg.MaxRetries) + 5
	for i := 0; i < maxStreamAttempts; i++ {
		// 客户端断连后立刻退出，否则会被 silent fallback 包成 200 SSE 空流，
		// 让客户端误以为请求成功完成。
		if err := ctx.Err(); err != nil {
			diagnostics.finalize("context_canceled", err.Error())
			applyResponseHeaders(w.Header(), diagnostics.headers())
			// headers 还没写出去之前才能 WriteHeader；写过的话只能写诊断头，状态码已定。
			w.WriteHeader(499)
			return
		}
		key, _, err := g.acquirePreferredKeyWithQueue(ctx, nil, nil, cfg.MaxConcurrency, false, "chat.stream", affinityID)
		if key != "" {
			diagnostics.noteSelectedKey(key)
			g.refreshConversationKeyBinding(affinityID, key, false)
		}
		if err != nil {
			if err.Error() == "queue timeout" {
				diagnostics.finalize("queue_timeout", err.Error())
				applyResponseHeaders(w.Header(), diagnostics.headers())
				http.Error(w, "Queue timeout, no available keys", http.StatusServiceUnavailable)
				return
			}
			lastErr = err.Error()
			continue
		}
		resp, reader, cancel, retry, err := g.openUpstreamStream(ctx, cfg, key, translatedBody, "chat.stream", affinityID)
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
			// 非 retry 错误也继续重试，不直接返回错误给客户端
			lastErr = err.Error()
			continue
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		applyResponseHeaders(w.Header(), diagnostics.headers())
		w.WriteHeader(http.StatusOK)
		var responseBuffer bytes.Buffer
		streamInterrupted := false
		// chunk 间读超时：来自系统配置 StreamIdleTimeoutSec（默认 600s）。
		// 不再被 RequestTimeoutSecond 往小里 cap —— 长任务（递归读项目 / 工具调用思考）
		// 经常出现超过 90 秒的合法静默，旧版会把它判成"僵尸流"提前发 [DONE]，
		// 导致 Claude Code 误以为响应已完成而中断后续对话。
		chunkReadTimeout := time.Duration(cfg.StreamIdleTimeoutSec) * time.Second
		if chunkReadTimeout <= 0 {
			chunkReadTimeout = time.Duration(models.DefaultStreamIdleTimeoutSec) * time.Second
		}
		keepAliveInterval := time.Duration(cfg.StreamKeepAliveSec) * time.Second
		var keepAliveTicker *time.Ticker
		keepAliveDone := make(chan struct{})
		if keepAliveInterval > 0 && canFlush {
			keepAliveTicker = time.NewTicker(keepAliveInterval)
			go func() {
				for {
					select {
					case <-keepAliveDone:
						return
					case <-keepAliveTicker.C:
						// SSE 注释帧：客户端会忽略，但能让中间链路保持活跃，避免长时间无字节传输被 CDN/代理 RST。
						_, _ = w.Write([]byte(": keep-alive\n\n"))
						flusher.Flush()
					}
				}
			}()
		}
		// drainOpenAIChunks 已经处理了 buf 所有权 + goroutine 退出，
		// 上层只需要决定每个 chunk 怎么写出、以及结束态怎么补帧。
		drainResult := drainOpenAIChunks(ctx, reader, resp.Body, chunkReadTimeout, func(chunk []byte) bool {
			_, _ = w.Write(chunk)
			responseBuffer.Write(chunk)
			if canFlush {
				flusher.Flush()
			}
			return true
		})
		// 流读完后停止心跳 goroutine（如果有）。
		if keepAliveTicker != nil {
			keepAliveTicker.Stop()
			close(keepAliveDone)
		}
		switch drainResult {
		case drainEOF:
			// 正常 EOF，不补帧。
		case drainIdleTimeout:
			streamInterrupted = true
			recordUpstreamRuntimeEvent("chat.stream", "chunk_read_timeout", key, false, 0, "上游流式传输中途静默超时，已主动终止")
			g.recoverNetworkPathForKey(ctx, cfg, key, errors.New("stream chunk read timeout"))
			if !openAIStreamBytesContainDone(responseBuffer.Bytes()) {
				_, _ = w.Write([]byte("\n\ndata: {\"id\":\"chatcmpl-interrupted\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
			}
			if canFlush {
				flusher.Flush()
			}
		case drainUpstreamErr:
			streamInterrupted = true
			recordUpstreamRuntimeEvent("chat.stream", "upstream_error", key, false, 0, "上游流式传输中途出错")
			g.recoverNetworkPathForKey(ctx, cfg, key, errors.New("stream upstream error"))
			if !openAIStreamBytesContainDone(responseBuffer.Bytes()) {
				_, _ = w.Write([]byte("\n\ndata: {\"id\":\"chatcmpl-interrupted\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
			}
			if canFlush {
				flusher.Flush()
			}
		case drainContextCanceled:
			streamInterrupted = true
			// 客户端断连：不补 [DONE]，因为对端不再读了，写出去也是浪费。
		}
		_ = resp.Body.Close()
		if cancel != nil {
			cancel()
		}
		_ = g.scheduler.ReleaseKey(ctx, key)
		if err := g.usageTracker.Record(ctx, masterKey, estTokens); err == nil {
			incrementQuota(masterKey, estTokens)
		}
		if cacheEnabled && responseBuffer.Len() > 0 && !streamInterrupted && openAIStreamBytesContainDone(responseBuffer.Bytes()) {
			_ = g.cache.Set(ctx, model, promptStr, responseBuffer.String(), 24*time.Hour)
		}
		// 注意：此处不能再写响应头，headers 已经在循环里通过 WriteHeader(200) 写出去了。
		// Status 只能写到 diagnostics 自己用于日志/可观测，但响应头里看不到。
		// TODO：未来可以在 WriteHeader 之前 finalize 一次再覆盖。
		if streamInterrupted {
			diagnostics.finalize("upstream_interrupted", "stream interrupted mid-transfer")
		} else {
			diagnostics.finalize("success", "")
		}
		return
	}
	// 所有重试耗尽：根据 SilentFallbackOnExhaustion 决定假流 vs 真错误。
	diagnostics.finalize("exhausted", lastErr)
	if !cfg.SilentFallbackOnExhaustion {
		applyResponseHeaders(w.Header(), diagnostics.headers())
		http.Error(w, lastErr, http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	applyResponseHeaders(w.Header(), diagnostics.headers())
	w.WriteHeader(http.StatusOK)
	// 静默回退也使用客户端实际请求的 model，避免客户端因 model 字段不匹配解析失败。
	fallbackModel := strings.TrimSpace(model)
	if fallbackModel == "" {
		fallbackModel = "unknown"
	}
	exhaustedChunk := map[string]any{
		"id":     "chatcmpl-retry-exhausted",
		"object": "chat.completion.chunk",
		"model":  fallbackModel,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{"role": "assistant", "content": ""},
			"finish_reason": "stop",
		}},
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", mustJSON(exhaustedChunk))
	if canFlush {
		flusher.Flush()
	}
}

func (g *Gateway) fetchUpstreamModels(ctx context.Context) proxyResult {
	cfg := loadSystemConfig()
	diagnostics := newUpstreamAttemptDiagnostics("models.list")
	lastErr := "upstream request failed"
	for i := 0; i < crossKeyAttemptBudget(cfg.MaxRetries); i++ {
		key, _, err := g.acquirePreferredKeyWithQueue(ctx, nil, nil, cfg.MaxConcurrency, false, "models.list", "")
		if key != "" {
			diagnostics.noteSelectedKey(key)
		}
		if err != nil {
			if err.Error() == "queue timeout" {
				return proxyResult{StatusCode: http.StatusServiceUnavailable, ContentType: "application/json", Body: mustJSON(openAIError("queue_timeout", "Queue timeout, no available keys", "server_error"))}
			}
			lastErr = err.Error()
			continue
		}
		result, retry := g.executeUpstreamJSONRequest(ctx, cfg, key, http.MethodGet, "models", nil, "application/json", "models.list", "")
		_ = g.scheduler.ReleaseKey(ctx, key)
		if retry {
			diagnostics.noteRetry("上游 NVIDIA 官方 Key 已不可继续使用，已切换其他 Key")
			if diagnostics.LastRetryCause != "" {
				lastErr = diagnostics.LastRetryCause
			}
			continue
		}
		if result.StatusCode >= 200 && result.StatusCode < 300 {
			applyProxyHeaders(&result, diagnostics.headers())
			return result
		}
		if len(result.Body) > 0 {
			lastErr = parseUpstreamError(result.Body, lastErr)
			applyProxyHeaders(&result, diagnostics.headers())
			return result
		}
		lastErr = "upstream request failed"
	}
	result := proxyResult{StatusCode: http.StatusBadGateway, ContentType: "application/json", Body: mustJSON(openAIError("upstream_error", lastErr, "api_error"))}
	applyProxyHeaders(&result, diagnostics.headers())
	return result
}

func (g *Gateway) executeUpstreamJSONRequest(ctx context.Context, cfg models.SystemConfig, key, method, endpointPath string, body []byte, accept, operation, affinityID string) (proxyResult, bool) {
	cfg = models.NormalizeSystemConfig(cfg)
	var lastNetworkErr string
	attemptBudget := sameKeyTransportRetryBudget(cfg) + 1
	for attempt := 0; attempt < attemptBudget; attempt++ {
		resp, cancel, err := g.openUpstreamHeadersWithTimeout(ctx, cfg, key, method, endpointPath, body, accept)
		if err != nil {
			if cancel != nil {
				cancel()
			}
			policy := classifyUpstreamTransportError(err)
			if policy == upstreamFailurePolicyNetworkTransient {
				lastNetworkErr = err.Error()
				stage := "upstream_error"
				message := err.Error()
				if errors.Is(err, errUpstreamFirstByteTimeout) {
					stage = "first_byte_timeout"
					message = "上游首包超时，正在重试当前 NVIDIA 官方 Key"
				}
				recordUpstreamRuntimeEvent(operation, stage, key, false, 0, message)
				if attempt+1 < attemptBudget {
					g.recoverNetworkPathForKey(ctx, cfg, key, err)
					if !sleepWithContext(ctx, transportRetryBackoff(cfg)) {
						break
					}
					continue
				}
				g.clearConversationKeyBinding(affinityID, key)
				return proxyResult{}, true
			}
			recordUpstreamRuntimeEvent(operation, "upstream_error", key, false, 0, err.Error())
			return proxyResult{StatusCode: http.StatusBadGateway, ContentType: "application/json", Body: mustJSON(openAIError("upstream_request_error", err.Error(), "api_error"))}, false
		}
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if cancel != nil {
			cancel()
		}
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest && strings.Trim(strings.TrimSpace(endpointPath), "/") == "chat/completions" && isOpenAIChatCompletionEmpty(respBody) {
			lastNetworkErr = errUpstreamEmptyResponse.Error()
			recordUpstreamRuntimeEventWithRaw(operation, "upstream_failed", key, false, resp.StatusCode, "upstream returned empty response; retrying", buildUpstreamHTTPRawDetail(resp.StatusCode, contentType, resp.Header.Get("Retry-After"), respBody))
			if attempt+1 < attemptBudget {
				g.recoverNetworkPathForKey(ctx, cfg, key, errUpstreamEmptyResponse)
				if !sleepWithContext(ctx, transportRetryBackoff(cfg)) {
					break
				}
				continue
			}
			g.clearConversationKeyBinding(affinityID, key)
			return proxyResult{}, true
		}
		policy := classifyUpstreamStatusCode(resp.StatusCode)
		switch policy {
		case upstreamFailurePolicyKeyRateLimited:
			recordUpstreamRuntimeEventWithRaw(operation, "rate_limited", key, false, resp.StatusCode, "上游 NVIDIA 官方 Key 被限流，已进入冷却", buildUpstreamHTTPRawDetail(resp.StatusCode, contentType, resp.Header.Get("Retry-After"), respBody))
			g.markCooling(ctx, key, resp.Header.Get("Retry-After"))
			updateAPIKeyStatusByPlaintext(key, APIKeyStatusCooling)
			g.clearConversationKeyBinding(affinityID, key)
			return proxyResult{}, true
		case upstreamFailurePolicyKeyAuthRejected:
			recordUpstreamRuntimeEventWithRaw(operation, "auth_rejected", key, false, resp.StatusCode, "上游 NVIDIA 官方 Key 鉴权失败，已标记为不可用", buildUpstreamHTTPRawDetail(resp.StatusCode, contentType, resp.Header.Get("Retry-After"), respBody))
			_ = g.scheduler.MarkDead(ctx, key)
			updateAPIKeyStatusByPlaintext(key, APIKeyStatusDead)
			g.clearConversationKeyBinding(affinityID, key)
			return proxyResult{}, true
		case upstreamFailurePolicyNetworkTransient:
			parsedErr := parseUpstreamError(respBody, "upstream request failed")
			lastNetworkErr = parsedErr
			recordUpstreamRuntimeEventWithRaw(operation, "upstream_failed", key, false, resp.StatusCode, parsedErr, buildUpstreamHTTPRawDetail(resp.StatusCode, contentType, resp.Header.Get("Retry-After"), respBody))
			if attempt+1 < attemptBudget {
				g.recoverNetworkPathForKey(ctx, cfg, key, nil)
				if !sleepWithContext(ctx, transportRetryBackoff(cfg)) {
					break
				}
				continue
			}
			g.clearConversationKeyBinding(affinityID, key)
			return proxyResult{}, true
		default:
			if resp.StatusCode >= http.StatusBadRequest {
				recordUpstreamRuntimeEventWithRaw(operation, "upstream_failed", key, false, resp.StatusCode, parseUpstreamError(respBody, "upstream request failed"), buildUpstreamHTTPRawDetail(resp.StatusCode, contentType, resp.Header.Get("Retry-After"), respBody))
				return proxyResult{StatusCode: resp.StatusCode, ContentType: contentType, Body: respBody}, false
			}
			recordUpstreamRuntimeEventWithRaw(operation, "upstream_ok", key, true, resp.StatusCode, "上游响应成功", buildUpstreamHTTPRawDetail(resp.StatusCode, contentType, resp.Header.Get("Retry-After"), nil))
			return proxyResult{StatusCode: resp.StatusCode, ContentType: contentType, Body: respBody}, false
		}
	}
	if strings.TrimSpace(lastNetworkErr) == "" {
		lastNetworkErr = "upstream request failed"
	}
	return proxyResult{StatusCode: http.StatusBadGateway, ContentType: "application/json", Body: mustJSON(openAIError("upstream_request_error", lastNetworkErr, "api_error"))}, false
}

func (g *Gateway) callUpstreamJSONPath(ctx context.Context, cfg models.SystemConfig, key, endpointPath string, body []byte, affinityID string) (proxyResult, bool) {
	return g.executeUpstreamJSONRequest(ctx, cfg, key, http.MethodPost, endpointPath, body, "application/json", endpointPath, affinityID)
}

func (g *Gateway) callUpstreamChat(ctx context.Context, cfg models.SystemConfig, key string, body []byte, affinityID string) (proxyResult, bool) {
	return g.executeUpstreamJSONRequest(ctx, cfg, key, http.MethodPost, "chat/completions", body, "application/json", "chat/completions", affinityID)
}

func (g *Gateway) httpClient(cfg models.SystemConfig, key string) *http.Client {
	client := newHTTPClientForAPIKey(cfg, key)
	if g == nil || g.client == nil {
		return client
	}
	if g.client.Jar != nil {
		client.Jar = g.client.Jar
	}
	if g.client.CheckRedirect != nil {
		client.CheckRedirect = g.client.CheckRedirect
	}
	return client
}

func (g *Gateway) streamHTTPClient(cfg models.SystemConfig, key string) *http.Client {
	client := newStreamHTTPClientForAPIKey(cfg, key)
	if g == nil || g.client == nil {
		return client
	}
	if g.client.Jar != nil {
		client.Jar = g.client.Jar
	}
	if g.client.CheckRedirect != nil {
		client.CheckRedirect = g.client.CheckRedirect
	}
	return client
}

func (g *Gateway) markCooling(ctx context.Context, key, retryAfter string) {
	duration := 60 * time.Second
	if seconds, err := time.ParseDuration(strings.TrimSpace(retryAfter) + "s"); err == nil {
		duration = seconds
	}
	_ = g.scheduler.MarkCooling(ctx, key, duration)
}

func setStreamFlag(body []byte, stream bool) []byte {
	payload := map[string]any{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	payload["stream"] = stream
	updated, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return updated
}

func claudeErrorResponse(message string) fiber.Map {
	return fiber.Map{
		"type": "error",
		"error": fiber.Map{
			"type":    "invalid_request_error",
			"message": message,
		},
	}
}

func geminiErrorResponse(message string) fiber.Map {
	return fiber.Map{
		"error": fiber.Map{
			"code":    400,
			"message": message,
			"status":  "INVALID_ARGUMENT",
		},
	}
}

func buildChatCompletionSuccessRawDetail(operation string, respStatus int, contentType, retryAfter string, body []byte) string {
	raw := buildUpstreamHTTPRawDetail(respStatus, contentType, retryAfter, nil)
	if strings.TrimSpace(operation) != "chat/completions" {
		return raw
	}
	var payload struct {
		Choices []struct {
			FinishReason string `json:"finish_reason,omitempty"`
			Message      struct {
				Role      string `json:"role,omitempty"`
				Content   string `json:"content,omitempty"`
				ToolCalls []any  `json:"tool_calls,omitempty"`
			} `json:"message,omitempty"`
		} `json:"choices,omitempty"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Choices) == 0 {
		return raw
	}
	choice := payload.Choices[0]
	parts := []string{raw}
	if strings.TrimSpace(choice.FinishReason) != "" {
		parts = append(parts, "finish_reason: "+strings.TrimSpace(choice.FinishReason))
	}
	if strings.TrimSpace(choice.Message.Role) != "" {
		parts = append(parts, "message_role: "+strings.TrimSpace(choice.Message.Role))
	}
	parts = append(parts, fmt.Sprintf("content_chars: %d", len([]rune(choice.Message.Content))))
	parts = append(parts, fmt.Sprintf("tool_calls: %d", len(choice.Message.ToolCalls)))
	return strings.Join(parts, "\n")
}

func parseUpstreamError(raw []byte, fallback string) string {
	message := strings.TrimSpace(string(raw))
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err == nil {
		if errObj, ok := payload["error"].(map[string]any); ok {
			if msg, ok := errObj["message"].(string); ok && strings.TrimSpace(msg) != "" {
				return strings.TrimSpace(msg)
			}
		}
	}
	if message == "" {
		return fallback
	}
	return message
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}
