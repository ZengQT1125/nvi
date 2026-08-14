package models

import (
	"strings"
	"time"
)

const (
	DefaultUpstreamBaseURL           = "https://integrate.api.nvidia.com/v1"
	DefaultSchedulerStrategy         = "weighted_round_robin"
	DefaultMaxRetries                = 5
	DefaultMaxConcurrency            = 3
	DefaultRequestTimeoutSecond      = 600
	DefaultFirstByteTimeoutMs        = 90000
	DefaultHealthProbeTimeoutSec     = 45
	// DefaultStreamIdleTimeoutSec 控制流式响应"两个 chunk 之间最大允许的静默时间"。
	// Claude Code 跑长任务时，上游真实推理 + 工具调用思考很容易超过 90 秒，
	// 默认调大到 600 秒，避免被网关当成"僵尸流"提前发 message_stop 让客户端误判完成。
	DefaultStreamIdleTimeoutSec      = 600
	// DefaultStreamKeepAliveSec 控制空闲期 SSE 心跳注释帧的发送间隔；
	// 防止中间代理 / CDN / Cloudflare 因为长时间没有响应字节而 RST 连接。
	DefaultStreamKeepAliveSec        = 15
	DefaultTransportRetryCount       = 2
	DefaultTransportRetryBackoffMs   = 300
)

type APIKey struct {
	ID        uint      `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Weight    float64   `json:"weight"`
	Status    string    `json:"status"`
	ProbeOnly bool      `json:"probe_only,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type MasterKey struct {
	ID        uint      `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	RPM       int       `json:"rpm"`
	TPM       int       `json:"tpm"`
	Quota     int64     `json:"quota"`
	UsedQuota int64     `json:"used_quota"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SystemConfig struct {
	UpstreamBaseURL         string `json:"upstream_base_url"`
	SchedulerStrategy       string `json:"scheduler_strategy"`
	MaxRetries              int    `json:"max_retries"`
	MaxConcurrency          int    `json:"max_concurrency"`
	RequestTimeoutSecond    int    `json:"request_timeout_second"`
	EnableOpenAI            bool   `json:"enable_openai"`
	EnableClaude            bool   `json:"enable_claude"`
	EnableGemini            bool   `json:"enable_gemini"`
	AnonymousAccess         bool   `json:"anonymous_access"`
	FirstByteTimeoutMs      int    `json:"first_byte_timeout_ms"`
	HealthProbeTimeoutSec   int    `json:"health_probe_timeout_second"`
	// StreamIdleTimeoutSec 控制流式响应中"两个 chunk 之间最大允许的静默时间"，
	// 触发后网关会主动发送正常的 SSE 结束帧（message_stop / [DONE]），并停止重试。
	// 设置为 0 或负数时使用 DefaultStreamIdleTimeoutSec。
	StreamIdleTimeoutSec    int    `json:"stream_idle_timeout_second,omitempty"`
	// StreamKeepAliveSec 控制流式空闲期注入 SSE 心跳注释帧（": keep-alive\n\n"）的间隔，
	// 防止中间代理 / CDN 因为长时间没有响应字节而 RST。0 表示禁用心跳。
	StreamKeepAliveSec      int    `json:"stream_keep_alive_second,omitempty"`
	TransportRetryCount     int    `json:"transport_retry_count,omitempty"`
	TransportRetryBackoffMs int    `json:"transport_retry_backoff_ms,omitempty"`
	// SilentFallbackOnExhaustion 控制"所有上游 Key 重试耗尽"时的行为：
	//   true（默认）：返回 200 + 一个空 content 的合法响应，调用方不感知失败（保留旧版 UX）。
	//   false：返回 502 + 真实失败原因，便于监控告警拿到准确信号。
	// 无论开关如何，响应头里始终带上 X-Gateway-Upstream-Status / X-Gateway-Upstream-Final-Error，
	// 这样即使开启了静默回退，日志/网关入口侧仍然能观测到。
	SilentFallbackOnExhaustion bool `json:"silent_fallback_on_exhaustion"`
}

func DefaultSystemConfig() SystemConfig {
	return SystemConfig{
		UpstreamBaseURL:         DefaultUpstreamBaseURL,
		SchedulerStrategy:       DefaultSchedulerStrategy,
		MaxRetries:              DefaultMaxRetries,
		MaxConcurrency:          DefaultMaxConcurrency,
		RequestTimeoutSecond:    DefaultRequestTimeoutSecond,
		EnableOpenAI:            true,
		EnableClaude:            true,
		EnableGemini:            true,
		AnonymousAccess:         false,
		FirstByteTimeoutMs:      DefaultFirstByteTimeoutMs,
		HealthProbeTimeoutSec:   DefaultHealthProbeTimeoutSec,
		StreamIdleTimeoutSec:    DefaultStreamIdleTimeoutSec,
		StreamKeepAliveSec:      DefaultStreamKeepAliveSec,
		TransportRetryCount:     DefaultTransportRetryCount,
		TransportRetryBackoffMs: DefaultTransportRetryBackoffMs,
		// 默认开启静默回退，保留旧版 UX；运维想要观测可在 admin UI 里关闭。
		SilentFallbackOnExhaustion: true,
	}
}





func NormalizeSystemConfig(cfg SystemConfig) SystemConfig {
	defaults := DefaultSystemConfig()

	cfg.UpstreamBaseURL = strings.TrimSpace(cfg.UpstreamBaseURL)
	if cfg.UpstreamBaseURL == "" {
		cfg.UpstreamBaseURL = defaults.UpstreamBaseURL
	}
	cfg.SchedulerStrategy = strings.TrimSpace(cfg.SchedulerStrategy)
	if cfg.SchedulerStrategy == "" {
		cfg.SchedulerStrategy = defaults.SchedulerStrategy
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = defaults.MaxRetries
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = defaults.MaxConcurrency
	}
	if cfg.RequestTimeoutSecond <= 0 {
		cfg.RequestTimeoutSecond = defaults.RequestTimeoutSecond
	}
	if cfg.FirstByteTimeoutMs <= 0 {
		cfg.FirstByteTimeoutMs = defaults.FirstByteTimeoutMs
	}
	if cfg.HealthProbeTimeoutSec <= 0 {
		cfg.HealthProbeTimeoutSec = defaults.HealthProbeTimeoutSec
	}
	if cfg.StreamIdleTimeoutSec <= 0 {
		cfg.StreamIdleTimeoutSec = defaults.StreamIdleTimeoutSec
	}
	if cfg.StreamKeepAliveSec == 0 {
		cfg.StreamKeepAliveSec = defaults.StreamKeepAliveSec
	} else if cfg.StreamKeepAliveSec < 0 {
		// 允许显式传 -1 关闭心跳
		cfg.StreamKeepAliveSec = 0
	}
	if cfg.TransportRetryCount <= 0 {
		cfg.TransportRetryCount = defaults.TransportRetryCount
	}
	if cfg.TransportRetryBackoffMs <= 0 {
		cfg.TransportRetryBackoffMs = defaults.TransportRetryBackoffMs
	}

	if !cfg.EnableOpenAI && !cfg.EnableClaude && !cfg.EnableGemini {
		cfg.EnableOpenAI = defaults.EnableOpenAI
		cfg.EnableClaude = defaults.EnableClaude
		cfg.EnableGemini = defaults.EnableGemini
	}

	return cfg
}




