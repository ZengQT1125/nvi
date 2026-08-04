package gateway

import (
	"context"
	"errors"
	"strings"
	"time"

	"nvidia-api-gateway/pkg/models"
)

type upstreamFailurePolicy int

const (
	upstreamFailurePolicyTerminal upstreamFailurePolicy = iota
	upstreamFailurePolicyNetworkTransient
	upstreamFailurePolicyKeyRateLimited
	upstreamFailurePolicyKeyAuthRejected
)

func classifyUpstreamTransportError(err error) upstreamFailurePolicy {
	if err == nil {
		return upstreamFailurePolicyTerminal
	}
	if errors.Is(err, errUpstreamFirstByteTimeout) || errors.Is(err, errUpstreamEmptyResponse) {
		return upstreamFailurePolicyNetworkTransient
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(message, "tls handshake timeout"):
		return upstreamFailurePolicyNetworkTransient
	case strings.Contains(message, "context deadline exceeded"):
		return upstreamFailurePolicyNetworkTransient
	case strings.Contains(message, "timeout"):
		return upstreamFailurePolicyNetworkTransient
	case strings.Contains(message, "eof"):
		return upstreamFailurePolicyNetworkTransient
	case strings.Contains(message, "empty response"):
		return upstreamFailurePolicyNetworkTransient
	case strings.Contains(message, "no data"):
		return upstreamFailurePolicyNetworkTransient
	case strings.Contains(message, "server closed idle connection"):
		return upstreamFailurePolicyNetworkTransient
	case strings.Contains(message, "connection reset"):
		return upstreamFailurePolicyNetworkTransient
	case strings.Contains(message, "forcibly closed"):
		return upstreamFailurePolicyNetworkTransient
	case strings.Contains(message, "actively refused"):
		return upstreamFailurePolicyNetworkTransient
	case strings.Contains(message, "connectex"):
		return upstreamFailurePolicyNetworkTransient
	case strings.Contains(message, "broken pipe"):
		return upstreamFailurePolicyNetworkTransient
	case strings.Contains(message, "connection refused"):
		return upstreamFailurePolicyNetworkTransient
	case strings.Contains(message, "i/o timeout"):
		return upstreamFailurePolicyNetworkTransient
	default:
		return upstreamFailurePolicyTerminal
	}
}

func classifyUpstreamStatusCode(status int) upstreamFailurePolicy {
	switch status {
	case 429:
		return upstreamFailurePolicyKeyRateLimited
	case 401, 403:
		return upstreamFailurePolicyKeyAuthRejected
	case 502, 503, 504:
		return upstreamFailurePolicyNetworkTransient
	default:
		if status >= 500 {
			return upstreamFailurePolicyNetworkTransient
		}
		return upstreamFailurePolicyTerminal
	}
}

func crossKeyAttemptBudget(maxRetries int) int {
	if maxRetries < 0 {
		maxRetries = models.DefaultMaxRetries
	}
	return maxRetries + 1
}

func sameKeyTransportRetryBudget(cfg models.SystemConfig) int {
	cfg = models.NormalizeSystemConfig(cfg)
	if cfg.TransportRetryCount < 0 {
		return models.DefaultTransportRetryCount
	}
	return cfg.TransportRetryCount
}

func transportRetryBackoff(cfg models.SystemConfig) time.Duration {
	cfg = models.NormalizeSystemConfig(cfg)
	backoff := cfg.TransportRetryBackoffMs
	if backoff <= 0 {
		backoff = models.DefaultTransportRetryBackoffMs
	}
	return time.Duration(backoff) * time.Millisecond
}

func sleepWithContext(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return true
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func isLikelyLocalLoopbackProxyError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	// 必须包含本地回环地址才认为是 xray 本地代理问题
	isLoopback := strings.Contains(message, "127.0.0.1:") ||
		strings.Contains(message, "localhost:") ||
		strings.Contains(message, "[::1]:")
	if !isLoopback {
		return false
	}
	return strings.Contains(message, "connectex") ||
		strings.Contains(message, "actively refused") ||
		strings.Contains(message, "forcibly closed") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "wsarecv") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "eof") ||
		strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "i/o timeout")
}
