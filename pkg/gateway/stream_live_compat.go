package gateway

import (
	"bufio"
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
)

type openAIStreamChunk struct {
	ID      string               `json:"id,omitempty"`
	Model   string               `json:"model,omitempty"`
	Choices []openAIStreamChoice `json:"choices,omitempty"`
	Usage   map[string]any       `json:"usage,omitempty"`
}

type openAIStreamChoice struct {
	Index        int               `json:"index,omitempty"`
	Delta        openAIStreamDelta `json:"delta,omitempty"`
	FinishReason string            `json:"finish_reason,omitempty"`
}

type openAIStreamDelta struct {
	Role      string                 `json:"role,omitempty"`
	Content   string                 `json:"content,omitempty"`
	ToolCalls []openAIStreamToolCall `json:"tool_calls,omitempty"`
}

type openAIStreamToolCall struct {
	Index    int                      `json:"index,omitempty"`
	ID       string                   `json:"id,omitempty"`
	Type     string                   `json:"type,omitempty"`
	Function openAIStreamToolFunction `json:"function,omitempty"`
}

type openAIStreamToolFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type liveStreamTranslator interface {
	Consume(chunk openAIStreamChunk) error
	Finish() error
	Error(message string) error
	// HasContent 表示翻译器在 Consume 过程中是否曾产出过任何"有意义"的内容
	// （正文 delta、工具调用 delta，或 finish_reason），用于在自然结束时
	// 判断这条流是否本质上是"空回复"，从而触发上层切换 key 重试。
	HasContent() bool
	// Started 表示翻译器是否已经把任何字节写到客户端（如 claude 的 message_start）。
	// 一旦 Started=true，上层就不能再"切换 key 重试"了——否则会出现两次 message_start。
	Started() bool
}

type claudeLiveStreamTranslator struct {
	w             io.Writer
	flusher       http.Flusher
	messageID     string
	model         string
	started       bool
	textOpen      bool
	textIndex     int
	nextIndex     int
	finishReason  string
	usage         map[string]any
	finished      bool
	hasContent    bool
	toolBlocks    map[int]*claudeToolBlock
	toolBlockKeys []int
}

type claudeToolBlock struct {
	OpenIndex int
	ToolID    string
	ToolName  string
	Closed    bool
}

type geminiLiveStreamTranslator struct {
	w            io.Writer
	flusher      http.Flusher
	model        string
	finishReason string
	usage        map[string]any
	toolCalls    map[int]*geminiToolCallState
	toolCallKeys []int
	finished     bool
	hasContent   bool
	started      bool
}

type geminiToolCallState struct {
	Name string
	Args strings.Builder
}

func (g *Gateway) executeTranslatedCompatStream(
	ctx context.Context,
	w http.ResponseWriter,
	translatedBody []byte,
	requestedModel string,
	protocol string,
	estTokens int,
	masterKey *models.MasterKey,
	affinityID string,
) {
	cfg := loadSystemConfig()
	diagnostics := newUpstreamAttemptDiagnostics(protocol + ".stream")
	// 使用更大的重试预算：空回复和网络错误都会消耗重试次数
	// 默认 MaxRetries=5，这里额外加 5 次用于空回复重试
	maxAttempts := crossKeyAttemptBudget(cfg.MaxRetries) + 5
	lastErr := ""
	for i := 0; i < maxAttempts; i++ {
		// 客户端断连后立刻退出，否则被 silent fallback 包成 200 空流。
		if cerr := ctx.Err(); cerr != nil {
			diagnostics.finalize("context_canceled", cerr.Error())
			applyResponseHeaders(w.Header(), diagnostics.headers())
			w.WriteHeader(499)
			return
		}
		key, reusedPreferredKey, err := g.acquirePreferredKeyWithQueue(ctx, nil, nil, cfg.MaxConcurrency, false, protocol+".stream", affinityID)
		if key != "" {
			diagnostics.noteSelectedKey(key)
			g.refreshConversationKeyBinding(affinityID, key, false)
		}
		if err != nil {
			if err.Error() == "queue timeout" {
				diagnostics.finalize("queue_timeout", err.Error())
				applyResponseHeaders(w.Header(), diagnostics.headers())
				writeProtocolJSONError(w, protocol, http.StatusServiceUnavailable, "Queue timeout, no available keys")
				return
			}
			lastErr = err.Error()
			continue
		}

		resp, reader, cancel, retry, err := g.openUpstreamStream(ctx, cfg, key, translatedBody, protocol+".stream", affinityID)
		if retry {
			diagnostics.noteRetry("上一个上游 NVIDIA 官方 Key 请求失败，已继续重试可用 Key")
			if diagnostics.LastRetryCause != "" {
				lastErr = diagnostics.LastRetryCause
			}
			_ = g.scheduler.ReleaseKey(ctx, key)
			if cancel != nil {
				cancel()
			}
			continue
		}
		if err != nil {
			_ = g.scheduler.ReleaseKey(ctx, key)
			if cancel != nil {
				cancel()
			}
			lastErr = err.Error()
			// 非 retry 错误也继续重试，不直接返回错误给客户端
			continue
		}
		if resp == nil {
			_ = g.scheduler.ReleaseKey(ctx, key)
			if cancel != nil {
				cancel()
			}
			lastErr = "upstream returned nil response"
			continue
		}

		g.refreshConversationKeyBinding(affinityID, key, !reusedPreferredKey)
		applyResponseHeaders(w.Header(), diagnostics.headers())
		translator := newLiveStreamTranslator(protocol, w, requestedModel, resp)
		flusher, _ := w.(http.Flusher)
		streamOpts := streamRelayOptions{
			IdleTimeout:   time.Duration(cfg.StreamIdleTimeoutSec) * time.Second,
			KeepAliveSec:  cfg.StreamKeepAliveSec,
			Writer:        w,
			Flusher:       flusher,
		}
		streamErr := relayOpenAIStream(ctx, reader, translator, streamOpts)
		_ = resp.Body.Close()
		if cancel != nil {
			cancel()
		}
		_ = g.scheduler.ReleaseKey(ctx, key)
		// 上游本质空回复（无 delta、无工具调用），且 translator 还没把 headers 写出去：
		// 切到下一个 key 重试；客户端完全感知不到刚才那次失败。
		if errors.Is(streamErr, errStreamEmptyContent) && !translator.Started() {
			recordUpstreamRuntimeEvent(protocol+".stream", "upstream_empty_response", key, false, 0, "上游流式响应未产出任何内容，正在切换下一个 NVIDIA 官方 Key 重试")
			g.clearConversationKeyBinding(affinityID, key)
			lastErr = "upstream stream returned empty content"
			continue
		}
		if streamErr != nil {
			// 流中途断开（idle timeout / 上游 reset 等）：
			// translator.Finish() 已经在 relay 内部调用过；这里只补 diagnostics。
			diagnostics.finalize("upstream_interrupted", streamErr.Error())
			return
		}
		if err := g.usageTracker.Record(ctx, masterKey, estTokens); err == nil {
			incrementQuota(masterKey, estTokens)
		}
		diagnostics.finalize("success", "")
		return
	}
	// 所有重试耗尽：根据 SilentFallbackOnExhaustion 决定走假流还是真错。
	diagnostics.finalize("exhausted", lastErr)
	if !cfg.SilentFallbackOnExhaustion {
		applyResponseHeaders(w.Header(), diagnostics.headers())
		writeProtocolJSONError(w, protocol, http.StatusBadGateway, lastErr)
		return
	}
	applyResponseHeaders(w.Header(), diagnostics.headers())
	translator := newLiveStreamTranslator(protocol, w, requestedModel, nil)
	_ = translator.Finish()
}

func (g *Gateway) openUpstreamStream(ctx context.Context, cfg models.SystemConfig, key string, body []byte, operation, affinityID string) (*http.Response, io.Reader, context.CancelFunc, bool, error) {
	cfg = models.NormalizeSystemConfig(cfg)
	attemptBudget := sameKeyTransportRetryBudget(cfg) + 1
	var lastErr error
	for attempt := 0; attempt < attemptBudget; attempt++ {
		resp, reader, cancel, err := g.openUpstreamStreamWithPrefetch(ctx, cfg, key, body)
		if err != nil {
			if cancel != nil {
				cancel()
			}
			lastErr = err
			if classifyUpstreamTransportError(err) == upstreamFailurePolicyNetworkTransient {
				stage := "upstream_error"
				message := err.Error()
				if errors.Is(err, errUpstreamFirstByteTimeout) {
					stage = "first_chunk_timeout"
					message = "流式首个 chunk 超时，正在重试当前 NVIDIA 官方 Key"
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
				return nil, nil, nil, true, err
			}
			recordUpstreamRuntimeEvent(operation, "upstream_error", key, false, 0, err.Error())
			return nil, nil, nil, false, err
		}
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		policy := classifyUpstreamStatusCode(resp.StatusCode)
		switch policy {
		case upstreamFailurePolicyKeyRateLimited:
			bodyBytes, _ := io.ReadAll(reader)
			_ = resp.Body.Close()
			if cancel != nil {
				cancel()
			}
			recordUpstreamRuntimeEventWithRaw(operation, "rate_limited", key, false, resp.StatusCode, "上游 NVIDIA 官方 Key 被限流，已进入冷却", buildUpstreamHTTPRawDetail(resp.StatusCode, contentType, resp.Header.Get("Retry-After"), bodyBytes))
			g.markCooling(ctx, key, resp.Header.Get("Retry-After"))
			updateAPIKeyStatusByPlaintext(key, APIKeyStatusCooling)
			g.clearConversationKeyBinding(affinityID, key)
			return nil, nil, nil, true, fmt.Errorf("upstream rate limited")
		case upstreamFailurePolicyKeyAuthRejected:
			bodyBytes, _ := io.ReadAll(reader)
			_ = resp.Body.Close()
			if cancel != nil {
				cancel()
			}
			recordUpstreamRuntimeEventWithRaw(operation, "auth_rejected", key, false, resp.StatusCode, "上游 NVIDIA 官方 Key 鉴权失败，已标记为不可用", buildUpstreamHTTPRawDetail(resp.StatusCode, contentType, resp.Header.Get("Retry-After"), bodyBytes))
			_ = g.scheduler.MarkDead(ctx, key)
			updateAPIKeyStatusByPlaintext(key, APIKeyStatusDead)
			g.clearConversationKeyBinding(affinityID, key)
			return nil, nil, nil, true, fmt.Errorf("upstream auth rejected key")
		case upstreamFailurePolicyNetworkTransient:
			bodyBytes, _ := io.ReadAll(reader)
			_ = resp.Body.Close()
			if cancel != nil {
				cancel()
			}
			parsedErr := parseUpstreamError(bodyBytes, "upstream stream request failed")
			lastErr = errors.New(parsedErr)
			recordUpstreamRuntimeEventWithRaw(operation, "upstream_failed", key, false, resp.StatusCode, parsedErr, buildUpstreamHTTPRawDetail(resp.StatusCode, contentType, resp.Header.Get("Retry-After"), bodyBytes))
			if attempt+1 < attemptBudget {
				g.recoverNetworkPathForKey(ctx, cfg, key, nil)
				if !sleepWithContext(ctx, transportRetryBackoff(cfg)) {
					break
				}
				continue
			}
			g.clearConversationKeyBinding(affinityID, key)
			return nil, nil, nil, true, errors.New(parsedErr)
		default:
			if resp.StatusCode >= http.StatusBadRequest {
				bodyBytes, _ := io.ReadAll(reader)
				_ = resp.Body.Close()
				if cancel != nil {
					cancel()
				}
				parsedErr := parseUpstreamError(bodyBytes, "upstream stream request failed")
				recordUpstreamRuntimeEventWithRaw(operation, "upstream_failed", key, false, resp.StatusCode, parsedErr, buildUpstreamHTTPRawDetail(resp.StatusCode, contentType, resp.Header.Get("Retry-After"), bodyBytes))
				return nil, nil, nil, false, errors.New(parsedErr)
			}
			recordUpstreamRuntimeEventWithRaw(operation, "upstream_ok", key, true, resp.StatusCode, "已成功建立到 NVIDIA 官方接口的流式连接", buildUpstreamHTTPRawDetail(resp.StatusCode, contentType, resp.Header.Get("Retry-After"), nil))
			return resp, reader, cancel, false, nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("upstream stream request failed")
	}
	return nil, nil, nil, false, lastErr
}

// errStreamEmptyContent 表示上游流式响应自然结束（EOF 或 [DONE]）但
// 翻译器没有产出任何"有意义"的 delta；调用方可以据此切到下一个 key 重试，
// 前提是 translator.Started() == false（也即头部还没写出）。
var errStreamEmptyContent = errors.New("upstream stream returned empty content")

// streamRelayOptions 控制 relayOpenAIStream 的行为，便于按系统配置动态调整。
type streamRelayOptions struct {
	// IdleTimeout 上游两个 chunk 之间最大允许的静默时间。
	// 设置为 0 / 负数时使用 models.DefaultStreamIdleTimeoutSec。
	IdleTimeout time.Duration
	// KeepAliveSec 空闲心跳间隔（秒），0 表示禁用。
	// 心跳通过 Writer 写入 SSE 注释帧（": keep-alive\n\n"），
	// 防止中间链路（系统代理、CDN、Cloudflare）因长时间无字节传输而 RST。
	KeepAliveSec int
	Writer       io.Writer
	Flusher      http.Flusher
}

func relayOpenAIStream(ctx context.Context, body io.Reader, translator liveStreamTranslator, opts streamRelayOptions) error {
	reader := bufio.NewReader(body)
	dataLines := make([]string, 0, 2)
	flushData := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		if payload == "" {
			return nil
		}
		if payload == "[DONE]" {
			return translator.Finish()
		}
		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return err
		}
		return translator.Consume(chunk)
	}

	idleTimeout := opts.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = time.Duration(models.DefaultStreamIdleTimeoutSec) * time.Second
	}
	keepAlive := time.Duration(opts.KeepAliveSec) * time.Second

	writeKeepAlive := func() {
		if opts.Writer == nil {
			return
		}
		if _, err := io.WriteString(opts.Writer, ": keep-alive\n\n"); err != nil {
			return
		}
		if opts.Flusher != nil {
			opts.Flusher.Flush()
		}
	}

	type lineResult struct {
		line string
		err  error
	}
	lineCh := make(chan lineResult, 1)
	readerLaunched := false
	launchReader := func() {
		readerLaunched = true
		go func() {
			l, readErr := reader.ReadString('\n')
			lineCh <- lineResult{line: l, err: readErr}
		}()
	}

	for {
		if !readerLaunched {
			launchReader()
		}
		idleTimer := time.NewTimer(idleTimeout)
		var keepAliveCh <-chan time.Time
		var keepAliveTicker *time.Ticker
		if keepAlive > 0 {
			keepAliveTicker = time.NewTicker(keepAlive)
			keepAliveCh = keepAliveTicker.C
		}

		var lr lineResult
		gotLine := false
	wait:
		for {
			select {
			case lr = <-lineCh:
				readerLaunched = false
				gotLine = true
				idleTimer.Stop()
				if keepAliveTicker != nil {
					keepAliveTicker.Stop()
				}
				break wait
			case <-keepAliveCh:
				writeKeepAlive()
			case <-idleTimer.C:
				if keepAliveTicker != nil {
					keepAliveTicker.Stop()
				}
				_ = flushData()
				if err := translator.Finish(); err != nil {
					return err
				}
				if !translator.HasContent() && !translator.Started() {
					return errStreamEmptyContent
				}
				return nil
			case <-ctx.Done():
				idleTimer.Stop()
				if keepAliveTicker != nil {
					keepAliveTicker.Stop()
				}
				_ = flushData()
				if err := translator.Finish(); err != nil {
					return err
				}
				if !translator.HasContent() && !translator.Started() {
					return errStreamEmptyContent
				}
				return nil
			}
		}

		if !gotLine {
			if err := translator.Finish(); err != nil {
				return err
			}
			if !translator.HasContent() && !translator.Started() {
				return errStreamEmptyContent
			}
			return nil
		}

		if len(lr.line) > 0 {
			trimmed := strings.TrimRight(lr.line, "\r\n")
			switch {
			case trimmed == "":
				if flushErr := flushData(); flushErr != nil {
					return flushErr
				}
			case strings.HasPrefix(trimmed, "data:"):
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
			case strings.HasPrefix(trimmed, ":"):
				// SSE comment / keep-alive, ignore
			}
		}
		if lr.err != nil {
			if lr.err == io.EOF {
				if flushErr := flushData(); flushErr != nil {
					return flushErr
				}
				if err := translator.Finish(); err != nil {
					return err
				}
				if !translator.HasContent() && !translator.Started() {
					return errStreamEmptyContent
				}
				return nil
			}
			_ = flushData()
			if err := translator.Finish(); err != nil {
				return err
			}
			if !translator.HasContent() && !translator.Started() {
				return errStreamEmptyContent
			}
			return nil
		}
	}
}

func newLiveStreamTranslator(protocol string, w http.ResponseWriter, requestedModel string, resp *http.Response) liveStreamTranslator {
	flusher, _ := w.(http.Flusher)
	// 注意：这里不再立即写 SSE 头 + WriteHeader(200)。
	// 因为如果上游本质上是"空回复"（预取漏判），我们希望能切到下一个 key 重试；
	// 一旦 headers 已经写出去，就再也回不了头了。Headers 推迟到 translator
	// 真正要写第一个事件时再写（参考 claudeLiveStreamTranslator.ensureHeaders）。
	usage := map[string]any{}
	if protocol == "claude" {
		return &claudeLiveStreamTranslator{
			w:          w,
			flusher:    flusher,
			model:      requestedModel,
			usage:      usage,
			toolBlocks: make(map[int]*claudeToolBlock),
		}
	}
	return &geminiLiveStreamTranslator{
		w:         w,
		flusher:   flusher,
		model:     requestedModel,
		usage:     usage,
		toolCalls: make(map[int]*geminiToolCallState),
	}
}

// writeStreamHeaders 把 SSE 响应头写出去；只能调一次。
// 调用方：translator 在准备写出"第一个有意义事件"前调一次。
func writeStreamHeaders(w http.ResponseWriter, flusher http.Flusher) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}
}

func (t *claudeLiveStreamTranslator) Consume(chunk openAIStreamChunk) error {
	if chunk.Model != "" {
		t.model = firstNonEmpty(t.model, chunk.Model)
	}
	if chunk.ID != "" {
		t.messageID = firstNonEmpty(t.messageID, chunk.ID)
	}
	if chunk.Usage != nil {
		t.usage = chunk.Usage
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			t.hasContent = true
			if err := t.ensureStarted(); err != nil {
				return err
			}
			if err := t.ensureTextBlock(); err != nil {
				return err
			}
			if err := t.emit("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": t.textIndex,
				"delta": map[string]any{
					"type": "text_delta",
					"text": choice.Delta.Content,
				},
			}); err != nil {
				return err
			}
		}
		if len(choice.Delta.ToolCalls) > 0 {
			// 工具调用算"有内容"，但需要内层判断是否有 name/id/args 至少一项。
			hasMeaningfulToolCall := false
			for _, toolCall := range choice.Delta.ToolCalls {
				if strings.TrimSpace(toolCall.ID) != "" || strings.TrimSpace(toolCall.Type) != "" || strings.TrimSpace(toolCall.Function.Name) != "" || strings.TrimSpace(toolCall.Function.Arguments) != "" {
					hasMeaningfulToolCall = true
					break
				}
			}
			if hasMeaningfulToolCall {
				t.hasContent = true
				if err := t.ensureStarted(); err != nil {
					return err
				}
				if t.textOpen {
					if err := t.closeTextBlock(); err != nil {
						return err
					}
				}
				for _, toolCall := range choice.Delta.ToolCalls {
					if err := t.consumeToolCall(toolCall); err != nil {
						return err
					}
				}
			}
		}
		if choice.FinishReason != "" {
			t.finishReason = choice.FinishReason
		}
	}
	return nil
}

func (t *claudeLiveStreamTranslator) Finish() error {
	if t.finished {
		return nil
	}
	// 没有任何有意义的内容产出 → 不写 message_start / message_stop，
	// 让上层 relay 调用方有机会切到下一个 key 重试。
	if !t.hasContent && !t.started {
		t.finished = true
		return nil
	}
	if err := t.ensureStarted(); err != nil {
		return err
	}
	if t.textOpen {
		if err := t.closeTextBlock(); err != nil {
			return err
		}
	}
	for _, key := range t.toolBlockKeys {
		block := t.toolBlocks[key]
		if block == nil || block.Closed {
			continue
		}
		if err := t.emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": block.OpenIndex}); err != nil {
			return err
		}
		block.Closed = true
	}
	if err := t.emit("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   mapFinishReasonToClaude(t.finishReason),
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"output_tokens": ensureNonZeroUsage(lookupUsageValue(t.usage, "completion_tokens")),
		},
	}); err != nil {
		return err
	}
	if err := t.emit("message_stop", map[string]any{"type": "message_stop"}); err != nil {
		return err
	}
	t.finished = true
	return nil
}

func (t *claudeLiveStreamTranslator) Error(message string) error {
	if t.finished {
		return nil
	}
	if err := t.ensureStarted(); err != nil {
		return err
	}
	return t.emit("error", claudeErrorResponse(message))
}

func (t *claudeLiveStreamTranslator) HasContent() bool {
	return t.hasContent
}

func (t *claudeLiveStreamTranslator) Started() bool {
	return t.started
}

func (t *claudeLiveStreamTranslator) ensureStarted() error {
	if t.started {
		return nil
	}
	// 第一次真正要写入时才写 SSE headers，便于上游"空回复"时仍能切 key 重试。
	if rw, ok := t.w.(http.ResponseWriter); ok {
		writeStreamHeaders(rw, t.flusher)
	}
	t.started = true
	messageID := firstNonEmpty(t.messageID, fmt.Sprintf("msg_%d", unixNowNano()))
	return t.emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":    messageID,
			"type":  "message",
			"role":  "assistant",
			"model": t.model,
			"usage": map[string]any{"input_tokens": ensureNonZeroUsage(lookupUsageValue(t.usage, "prompt_tokens")), "output_tokens": 0},
		},
	})
}

func (t *claudeLiveStreamTranslator) ensureTextBlock() error {
	if t.textOpen {
		return nil
	}
	t.textIndex = t.nextIndex
	t.nextIndex++
	t.textOpen = true
	return t.emit("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": t.textIndex,
		"content_block": map[string]any{
			"type": "text",
			"text": "",
		},
	})
}

func (t *claudeLiveStreamTranslator) closeTextBlock() error {
	if !t.textOpen {
		return nil
	}
	t.textOpen = false
	return t.emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": t.textIndex})
}

func (t *claudeLiveStreamTranslator) consumeToolCall(toolCall openAIStreamToolCall) error {
	block, ok := t.toolBlocks[toolCall.Index]
	if !ok {
		block = &claudeToolBlock{
			OpenIndex: t.nextIndex,
			ToolID:    firstNonEmpty(toolCall.ID, fmt.Sprintf("toolu_%d", toolCall.Index)),
			ToolName:  firstNonEmpty(toolCall.Function.Name, fmt.Sprintf("tool_%d", toolCall.Index)),
		}
		t.toolBlocks[toolCall.Index] = block
		t.toolBlockKeys = append(t.toolBlockKeys, toolCall.Index)
		t.nextIndex++
		if err := t.emit("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": block.OpenIndex,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    block.ToolID,
				"name":  block.ToolName,
				"input": map[string]any{},
			},
		}); err != nil {
			return err
		}
	} else {
		if block.ToolID == "" && toolCall.ID != "" {
			block.ToolID = toolCall.ID
		}
		if block.ToolName == "" && toolCall.Function.Name != "" {
			block.ToolName = toolCall.Function.Name
		}
	}
	if toolCall.Function.Arguments != "" {
		return t.emit("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": block.OpenIndex,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": toolCall.Function.Arguments,
			},
		})
	}
	return nil
}

func (t *claudeLiveStreamTranslator) emit(event string, payload any) error {
	var encoded []byte
	switch v := payload.(type) {
	case []byte:
		encoded = v
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		encoded = data
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

func (t *geminiLiveStreamTranslator) Consume(chunk openAIStreamChunk) error {
	if chunk.Model != "" {
		t.model = firstNonEmpty(t.model, chunk.Model)
	}
	if chunk.Usage != nil {
		t.usage = chunk.Usage
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			t.hasContent = true
			if err := t.emit(map[string]any{
				"candidates": []map[string]any{{
					"index": 0,
					"content": map[string]any{
						"role":  "model",
						"parts": []map[string]any{{"text": choice.Delta.Content}},
					},
				}},
				"modelVersion": t.model,
			}); err != nil {
				return err
			}
		}
		for _, toolCall := range choice.Delta.ToolCalls {
			state, ok := t.toolCalls[toolCall.Index]
			if !ok {
				state = &geminiToolCallState{Name: toolCall.Function.Name}
				t.toolCalls[toolCall.Index] = state
				t.toolCallKeys = append(t.toolCallKeys, toolCall.Index)
			}
			if state.Name == "" && toolCall.Function.Name != "" {
				state.Name = toolCall.Function.Name
			}
			if toolCall.Function.Arguments != "" {
				state.Args.WriteString(toolCall.Function.Arguments)
				t.hasContent = true
			}
		}
		if choice.FinishReason != "" {
			t.finishReason = choice.FinishReason
		}
	}
	return nil
}

func (t *geminiLiveStreamTranslator) Finish() error {
	if t.finished {
		return nil
	}
	if !t.hasContent && !t.started {
		t.finished = true
		return nil
	}
	sort.Ints(t.toolCallKeys)
	for _, idx := range t.toolCallKeys {
		state := t.toolCalls[idx]
		if state == nil {
			continue
		}
		args := parseFunctionArgs(state.Args.String())
		if err := t.emit(map[string]any{
			"candidates": []map[string]any{{
				"index": 0,
				"content": map[string]any{
					"role": "model",
					"parts": []map[string]any{{
						"functionCall": map[string]any{
							"name": state.Name,
							"args": args,
						},
					}},
				},
			}},
			"modelVersion": t.model,
		}); err != nil {
			return err
		}
	}
	if err := t.emit(map[string]any{
		"candidates": []map[string]any{{
			"index":        0,
			"finishReason": mapFinishReasonToGemini(t.finishReason),
			"content": map[string]any{
				"role":  "model",
				"parts": []map[string]any{},
			},
		}},
		"usageMetadata": map[string]any{
			"promptTokenCount":     lookupUsageValue(t.usage, "prompt_tokens"),
			"candidatesTokenCount": lookupUsageValue(t.usage, "completion_tokens"),
			"totalTokenCount":      lookupUsageValue(t.usage, "total_tokens"),
		},
		"modelVersion": t.model,
	}); err != nil {
		return err
	}
	t.finished = true
	return nil
}

func (t *geminiLiveStreamTranslator) Error(message string) error {
	if t.finished {
		return nil
	}
	return t.emit(geminiErrorResponse(message))
}

func (t *geminiLiveStreamTranslator) HasContent() bool {
	return t.hasContent
}

func (t *geminiLiveStreamTranslator) Started() bool {
	return t.started
}

func (t *geminiLiveStreamTranslator) emit(payload any) error {
	if !t.started {
		if rw, ok := t.w.(http.ResponseWriter); ok {
			writeStreamHeaders(rw, t.flusher)
		}
		t.started = true
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
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

func writeProtocolJSONError(w http.ResponseWriter, protocol string, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var payload []byte
	if protocol == "claude" {
		payload = mustJSON(claudeErrorResponse(message))
	} else {
		payload = mustJSON(geminiErrorResponse(message))
	}
	_, _ = w.Write(payload)
}

func parseFunctionArgs(raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
		return parsed
	}
	return map[string]any{"raw": trimmed}
}

func lookupUsageValue(usage map[string]any, key string) any {
	if usage == nil {
		return 0
	}
	if value, ok := usage[key]; ok {
		return value
	}
	return 0
}

// ensureNonZeroUsage 确保 usage 值不为 0。
// nvidia 流式响应可能不包含 usage 字段，返回 0 会让 Claude Code 认为响应无效。
func ensureNonZeroUsage(value any) any {
	switch v := value.(type) {
	case int:
		if v > 0 {
			return v
		}
	case float64:
		if v > 0 {
			return v
		}
	case int64:
		if v > 0 {
			return v
		}
	}
	// 返回一个合理的默认值
	return 100
}
