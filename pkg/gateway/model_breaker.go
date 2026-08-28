package gateway

import (
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 模型级熔断器（移植自 AQUA Platform 的模型级 429/5xx 熔断思路）
//
// 背景：密钥池很大时，如果上游对某个模型整体限流或故障，
// 池内所有密钥都会反复 429/5xx → 反复冷却 → 反复重试，
// 形成"尝试→失败→冷却→再尝试"的恶性循环，白白浪费调度与上游请求。
//
// 机制：每个模型维护一个短时间窗口内的失败计数，
// 窗口内失败次数超过阈值就整体熔断该模型一段时间。
// 熔断期间直接向客户端返回 429，不再尝试任何密钥。
//
// 与密钥级冷却的关系：密钥级冷却保护单个密钥，模型级熔断保护整个模型；
// 两者互补，模型级熔断是更上层的"闸门"。
//
// 参数可通过环境变量配置（默认值偏保守，避免误伤正常请求）：
//
//	MODEL_BREAKER_THRESHOLD       窗口内失败次数阈值（默认 50，设为 0 表示禁用熔断）
//	MODEL_BREAKER_WINDOW_SECONDS  失败统计窗口（默认 30 秒）
//	MODEL_BREAKER_COOLDOWN_SECONDS 熔断时长（默认 15 秒）
//
// 注意：阈值过小会让熔断过于敏感——对 Codex 等带自动重试的客户端，
// 熔断期间返回的 429 会被客户端反复重试直到"exceeded retry limit"。

const (
	modelBreakerDefaultThreshold = 50               // 默认：30 秒窗口内 50 次失败才熔断
	modelBreakerDefaultWindow    = 30 * time.Second // 默认统计窗口
	modelBreakerDefaultCooldown  = 15 * time.Second // 默认熔断时长
)

type modelCircuitBreaker struct {
	mu sync.Mutex

	window    time.Duration
	threshold int // <= 0 表示禁用熔断
	cooldown  time.Duration

	// model -> 最近失败时间戳（429 与 5xx 分开统计）
	recent429 map[string][]time.Time
	recent5xx map[string][]time.Time
	// model -> 熔断截止时间
	openUntil429 map[string]time.Time
	openUntil5xx map[string]time.Time
}

func newModelCircuitBreaker() *modelCircuitBreaker {
	threshold := envInt("MODEL_BREAKER_THRESHOLD", modelBreakerDefaultThreshold)
	window := time.Duration(envInt("MODEL_BREAKER_WINDOW_SECONDS", int(modelBreakerDefaultWindow.Seconds()))) * time.Second
	cooldown := time.Duration(envInt("MODEL_BREAKER_COOLDOWN_SECONDS", int(modelBreakerDefaultCooldown.Seconds()))) * time.Second
	if window <= 0 {
		window = modelBreakerDefaultWindow
	}
	if cooldown <= 0 {
		cooldown = modelBreakerDefaultCooldown
	}
	return newModelCircuitBreakerWithConfig(threshold, window, cooldown)
}

// newModelCircuitBreakerWithConfig 仅供测试注入小阈值/短窗口。
func newModelCircuitBreakerWithConfig(threshold int, window, cooldown time.Duration) *modelCircuitBreaker {
	return &modelCircuitBreaker{
		window:       window,
		threshold:    threshold,
		cooldown:     cooldown,
		recent429:    map[string][]time.Time{},
		recent5xx:    map[string][]time.Time{},
		openUntil429: map[string]time.Time{},
		openUntil5xx: map[string]time.Time{},
	}
}

// recordFailure 记录一次模型级失败，并检查是否触发熔断。
// is5xx 为 true 时计入 5xx 统计，否则计入 429 统计。
func (b *modelCircuitBreaker) recordFailure(model string, is5xx bool) {
	if b == nil || b.threshold <= 0 {
		return // 禁用熔断
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	if is5xx {
		b.recent5xx[model] = appendRecentWithin(b.recent5xx[model], now, b.window)
		if len(b.recent5xx[model]) >= b.threshold {
			b.openUntil5xx[model] = now.Add(b.cooldown)
			b.recent5xx[model] = nil
			log.Printf("模型级熔断触发: model=%s 窗口内5xx>=%d次，熔断%v（5xx）", model, b.threshold, b.cooldown)
		}
	} else {
		b.recent429[model] = appendRecentWithin(b.recent429[model], now, b.window)
		if len(b.recent429[model]) >= b.threshold {
			b.openUntil429[model] = now.Add(b.cooldown)
			b.recent429[model] = nil
			log.Printf("模型级熔断触发: model=%s 窗口内429>=%d次，熔断%v（429）", model, b.threshold, b.cooldown)
		}
	}
}

// isOpen 检查模型当前是否处于熔断中。
func (b *modelCircuitBreaker) isOpen(model string) bool {
	if b == nil || b.threshold <= 0 {
		return false // 禁用熔断
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	if until, ok := b.openUntil429[model]; ok && now.Before(until) {
		return true
	}
	if until, ok := b.openUntil5xx[model]; ok && now.Before(until) {
		return true
	}
	return false
}

// 状态查询（管理后台/调试用）
func (b *modelCircuitBreaker) status(model string) map[string]any {
	model = strings.TrimSpace(model)
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	open429 := false
	open5xx := false
	remain429 := float64(0)
	remain5xx := float64(0)
	if until, ok := b.openUntil429[model]; ok && now.Before(until) {
		open429 = true
		remain429 = until.Sub(now).Seconds()
	}
	if until, ok := b.openUntil5xx[model]; ok && now.Before(until) {
		open5xx = true
		remain5xx = until.Sub(now).Seconds()
	}
	return map[string]any{
		"model":             model,
		"open_429":          open429,
		"open_5xx":          open5xx,
		"remaining_429_sec": remain429,
		"remaining_5xx_sec": remain5xx,
		"recent_429_count":  len(b.recent429[model]),
		"recent_5xx_count":  len(b.recent5xx[model]),
		"threshold":         b.threshold,
		"window_seconds":    b.window.Seconds(),
		"cooldown_seconds":  b.cooldown.Seconds(),
		"enabled":           b.threshold > 0,
	}
}

// appendRecentWithin 将 now 追加进队列，同时丢弃窗口外的旧时间戳。
func appendRecentWithin(items []time.Time, now time.Time, window time.Duration) []time.Time {
	cutoff := now.Add(-window)
	kept := items[:0]
	for _, t := range items {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	return kept
}

// envInt 读取环境变量整数，非法或空时返回默认值。
func envInt(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}
