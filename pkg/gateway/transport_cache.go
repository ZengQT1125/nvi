package gateway

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"nvidia-api-gateway/pkg/models"
)

type transportCacheKey struct {
	ProxyURL              string
	Stream                bool
	RequestTimeoutSecond  int
	FirstByteTimeoutMs    int
	ConnectTimeoutMs      int
	TLSHandshakeTimeoutMs int
	Direct                bool
	Inherit               bool
	ProxyScheme           string
}

var transportCache = struct {
	mu    sync.Mutex
	items map[transportCacheKey]*http.Transport
}{items: map[transportCacheKey]*http.Transport{}}

func effectiveConnectTimeout(cfg models.SystemConfig) time.Duration {
	cfg = models.NormalizeSystemConfig(cfg)
	timeout := 30 * time.Second
	requestTimeout := time.Duration(cfg.RequestTimeoutSecond) * time.Second
	if requestTimeout > 0 && requestTimeout < timeout {
		timeout = requestTimeout
	}
	return timeout
}

func effectiveTLSHandshakeTimeout(cfg models.SystemConfig) time.Duration {
	cfg = models.NormalizeSystemConfig(cfg)
	timeout := time.Duration(cfg.FirstByteTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Duration(models.DefaultFirstByteTimeoutMs) * time.Millisecond
	}
	requestTimeout := time.Duration(cfg.RequestTimeoutSecond) * time.Second
	if requestTimeout > 0 && requestTimeout < timeout {
		timeout = requestTimeout
	}
	if timeout < 10*time.Second {
		timeout = 10 * time.Second
	}
	return timeout
}

func cachedTransportForProxySetting(cfg models.SystemConfig, rawProxyURL string, stream bool) (*http.Transport, error) {
	cfg = models.NormalizeSystemConfig(cfg)
	setting, err := parseUpstreamProxySetting(rawProxyURL)
	if err != nil {
		return nil, err
	}
	connectTimeout := effectiveConnectTimeout(cfg)
	tlsTimeout := effectiveTLSHandshakeTimeout(cfg)
	cacheKey := transportCacheKey{
		ProxyURL:              strings.TrimSpace(rawProxyURL),
		Stream:                stream,
		RequestTimeoutSecond:  cfg.RequestTimeoutSecond,
		FirstByteTimeoutMs:    cfg.FirstByteTimeoutMs,
		ConnectTimeoutMs:      int(connectTimeout / time.Millisecond),
		TLSHandshakeTimeoutMs: int(tlsTimeout / time.Millisecond),
		Direct:                setting.Mode == upstreamProxyModeDirect,
		Inherit:               setting.Mode == upstreamProxyModeInherit,
	}
	if setting.URL != nil {
		cacheKey.ProxyScheme = setting.URL.Scheme
	}
	transportCache.mu.Lock()
	defer transportCache.mu.Unlock()
	if existing, ok := transportCache.items[cacheKey]; ok {
		return existing, nil
	}
	transport, err := buildConfiguredUpstreamTransport(cfg, rawProxyURL)
	if err != nil {
		return nil, err
	}
	transportCache.items[cacheKey] = transport
	return transport, nil
}

func invalidateTransportCache(rawProxyURL string) {
	rawProxyURL = strings.TrimSpace(rawProxyURL)
	transportCache.mu.Lock()
	defer transportCache.mu.Unlock()
	for key, transport := range transportCache.items {
		if strings.TrimSpace(key.ProxyURL) != rawProxyURL {
			continue
		}
		if transport != nil {
			transport.CloseIdleConnections()
		}
		delete(transportCache.items, key)
	}
}

func invalidateLoopbackTransportCache() {
	transportCache.mu.Lock()
	defer transportCache.mu.Unlock()
	for key, transport := range transportCache.items {
		proxyURL := strings.TrimSpace(key.ProxyURL)
		if !strings.Contains(proxyURL, "127.0.0.1") && !strings.Contains(proxyURL, "localhost") && !strings.Contains(proxyURL, "[::1]") {
			continue
		}
		if transport != nil {
			transport.CloseIdleConnections()
		}
		delete(transportCache.items, key)
	}
}

// invalidateAllTransportCache 清除所有 transport 缓存。
// 用于系统配置变更或 xray 完全重载时，确保新配置生效。
func invalidateAllTransportCache() {
	transportCache.mu.Lock()
	defer transportCache.mu.Unlock()
	for key, transport := range transportCache.items {
		if transport != nil {
			transport.CloseIdleConnections()
		}
		delete(transportCache.items, key)
	}
}
