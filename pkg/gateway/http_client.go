package gateway

import (
	"context"
	"net/http"
	"time"

	"nvidia-api-gateway/pkg/models"
)

// newHTTPClient 统一使用系统默认出口（不经过任何代理节点/xray），
// 仅按配置设置请求总超时。
func newHTTPClient(cfg models.SystemConfig) *http.Client {
	timeout := time.Duration(cfg.RequestTimeoutSecond) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &http.Client{Timeout: timeout}
}

// newStreamHTTPClient 流式请求使用：不设总超时（依赖首包/空闲超时控制）。
func newStreamHTTPClient(cfg models.SystemConfig) *http.Client {
	return &http.Client{Timeout: 0}
}

// newHTTPClientForAPIKey 保留签名兼容，统一走系统默认出口。
func newHTTPClientForAPIKey(cfg models.SystemConfig, _ string) *http.Client {
	return newHTTPClient(cfg)
}

// newStreamHTTPClientForAPIKey 保留签名兼容，统一走系统默认出口。
func newStreamHTTPClientForAPIKey(cfg models.SystemConfig, _ string) *http.Client {
	return newStreamHTTPClient(cfg)
}

func newFirstByteContext(ctx context.Context, cfg models.SystemConfig) (context.Context, context.CancelFunc) {
	timeout := time.Duration(cfg.FirstByteTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Duration(models.DefaultFirstByteTimeoutMs) * time.Millisecond
	}
	return context.WithTimeout(ctx, timeout)
}

func firstByteTimeout(cfg models.SystemConfig) time.Duration {
	timeout := time.Duration(cfg.FirstByteTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		return time.Duration(models.DefaultFirstByteTimeoutMs) * time.Millisecond
	}
	return timeout
}
