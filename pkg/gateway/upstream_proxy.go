package gateway

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nvidia-api-gateway/pkg/models"

	xproxy "golang.org/x/net/proxy"
)

type upstreamProxyMode int

const (
	upstreamProxyModeInherit upstreamProxyMode = iota
	upstreamProxyModeDirect
	upstreamProxyModeProxy
	upstreamProxyModeInvalid
)

type upstreamProxySetting struct {
	Raw  string
	Mode upstreamProxyMode
	URL  *url.URL
}

type hostLookupFunc func(ctx context.Context, network, host string) ([]string, error)

var defaultHostLookup hostLookupFunc = func(ctx context.Context, _ string, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

func parseUpstreamProxySetting(raw string) (upstreamProxySetting, error) {
	trimmed := strings.TrimSpace(raw)
	setting := upstreamProxySetting{Raw: trimmed}

	if trimmed == "" {
		setting.Mode = upstreamProxyModeInherit
		return setting, nil
	}
	if strings.EqualFold(trimmed, "direct") || strings.EqualFold(trimmed, "none") {
		setting.Mode = upstreamProxyModeDirect
		return setting, nil
	}

	parsedURL, err := url.Parse(trimmed)
	if err != nil {
		setting.Mode = upstreamProxyModeInvalid
		return setting, fmt.Errorf("parse proxy URL failed: %w", err)
	}
	if strings.TrimSpace(parsedURL.Scheme) == "" || strings.TrimSpace(parsedURL.Host) == "" {
		setting.Mode = upstreamProxyModeInvalid
		return setting, fmt.Errorf("proxy URL missing scheme/host")
	}

	scheme := strings.ToLower(strings.TrimSpace(parsedURL.Scheme))
	switch scheme {
	case "http", "https", "socks5", "socks5h", "dokodemo":
		parsedURL.Scheme = scheme
		setting.Mode = upstreamProxyModeProxy
		setting.URL = parsedURL
		return setting, nil
	default:
		setting.Mode = upstreamProxyModeInvalid
		return setting, fmt.Errorf("unsupported proxy scheme: %s", parsedURL.Scheme)
	}
}

func validateUpstreamProxySetting(raw string) error {
	_, err := parseUpstreamProxySetting(raw)
	return err
}

func hardenProxyTransport(transport *http.Transport) {
	if transport == nil {
		return
	}
	// 允许 HTTP/2：nvidia CDN 部分节点要求 HTTP/2，禁用会导致 EOF
	transport.ForceAttemptHTTP2 = true
	// 保持连接复用，避免流式长连接中断
	transport.DisableKeepAlives = false
	transport.MaxIdleConns = 64
	transport.MaxIdleConnsPerHost = 16
	transport.IdleConnTimeout = 90 * time.Second
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	// 允许 h2 和 http/1.1 双协商，避免 CDN 拒绝连接
	transport.TLSClientConfig.NextProtos = []string{"h2", "http/1.1"}
}

func cloneDefaultTransport() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok && transport != nil {
		return transport.Clone()
	}
	return &http.Transport{}
}

func buildUpstreamProxyTransport(raw string) (*http.Transport, upstreamProxyMode, error) {
	setting, err := parseUpstreamProxySetting(raw)
	if err != nil {
		return nil, setting.Mode, err
	}

	switch setting.Mode {
	case upstreamProxyModeInherit:
		return nil, setting.Mode, nil
	case upstreamProxyModeDirect:
		transport := cloneDefaultTransport()
		transport.Proxy = nil
		return transport, setting.Mode, nil
	case upstreamProxyModeProxy:
		transport := cloneDefaultTransport()
		switch setting.URL.Scheme {
		case "http", "https":
			transport.Proxy = http.ProxyURL(setting.URL)
			return transport, setting.Mode, nil
		case "socks5", "socks5h":
			dialContext, err := buildSOCKS5DialContext(setting.URL)
			if err != nil {
				return nil, setting.Mode, err
			}
			transport.Proxy = nil
			transport.DialContext = dialContext
			return transport, setting.Mode, nil
		}
	}

	return nil, setting.Mode, nil
}

func buildConfiguredUpstreamTransport(cfg models.SystemConfig, raw string) (*http.Transport, error) {
	setting, err := parseUpstreamProxySetting(raw)
	if err != nil {
		return nil, err
	}
	transport := cloneDefaultTransport()
	connectTimeout := effectiveConnectTimeout(cfg)
	transport.TLSHandshakeTimeout = effectiveTLSHandshakeTimeout(cfg)
	transport.ExpectContinueTimeout = 1 * time.Second
	transport.IdleConnTimeout = 90 * time.Second
	transport.MaxIdleConns = 256
	transport.MaxIdleConnsPerHost = 64
	switch setting.Mode {
	case upstreamProxyModeInherit:
		dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
		transport.DialContext = dialer.DialContext
		return transport, nil
	case upstreamProxyModeDirect:
		transport.Proxy = nil
		dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
		transport.DialContext = dialer.DialContext
		return transport, nil
	case upstreamProxyModeProxy:
		switch setting.URL.Scheme {
		case "http", "https":
			transport.Proxy = http.ProxyURL(setting.URL)
			dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 60 * time.Second}
			transport.DialContext = dialer.DialContext
			hardenProxyTransport(transport)
			return transport, nil
		case "socks5", "socks5h", "dokodemo":
			// dokodemo-door / socks5 透明代理模式：
			// Go 直接 TCP 连接本地端口，xray 自动转发到目标地址
			proxyHost := setting.URL.Host
			dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 60 * time.Second}
			transport.Proxy = nil
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				// 直接连接 xray 本地端口（dokodemo-door），不走 SOCKS5 协议
				return dialer.DialContext(ctx, "tcp", proxyHost)
			}
			hardenProxyTransport(transport)
			return transport, nil
		}
	}
	return transport, nil
}

func buildSOCKS5DialContext(proxyURL *url.URL) (func(context.Context, string, string) (net.Conn, error), error) {
	return buildSOCKS5DialContextWithTimeout(proxyURL, 15*time.Second)
}

func buildSOCKS5DialContextWithTimeout(proxyURL *url.URL, timeout time.Duration) (func(context.Context, string, string) (net.Conn, error), error) {
	if proxyURL == nil {
		return nil, fmt.Errorf("proxy URL is nil")
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	var authCfg *xproxy.Auth
	if proxyURL.User != nil {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		if username != "" || password != "" {
			authCfg = &xproxy.Auth{User: username, Password: password}
		}
	}
	// 使用较长的 KeepAlive（60s）维持 SOCKS5 长连接，避免流式传输中途被断开
	forward := &net.Dialer{Timeout: timeout, KeepAlive: 60 * time.Second}
	dialer, err := xproxy.SOCKS5("tcp", proxyURL.Host, authCfg, forward)
	if err != nil {
		return nil, fmt.Errorf("create SOCKS5 dialer failed: %w", err)
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		target, err := resolveSOCKS5Target(ctx, proxyURL.Scheme, address, defaultHostLookup)
		if err != nil {
			return nil, err
		}
		if ctxDialer, ok := dialer.(xproxy.ContextDialer); ok {
			return ctxDialer.DialContext(ctx, network, target)
		}
		return dialer.Dial(network, target)
	}, nil
}

func resolveSOCKS5Target(ctx context.Context, scheme, address string, lookup hostLookupFunc) (string, error) {
	// socks5h 表示由代理服务器负责 DNS 解析，直接透传域名，不在本地解析
	// socks5 才需要本地 DNS 预解析
	if strings.ToLower(strings.TrimSpace(scheme)) != "socks5" {
		return address, nil
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	// 已经是 IP 地址，无需解析
	if net.ParseIP(host) != nil {
		return address, nil
	}
	if lookup == nil {
		lookup = defaultHostLookup
	}
	addrs, err := lookup(ctx, "ip", host)
	if err != nil {
		// DNS 解析失败时，直接透传域名让代理处理，而不是直接报错中断
		return address, nil
	}
	if len(addrs) == 0 {
		return address, nil
	}
	return net.JoinHostPort(addrs[0], port), nil
}
