package gateway

import (
	"encoding/base64"
	"strconv"
	"strings"
)

type upstreamAttemptDiagnostics struct {
	Operation        string
	AttemptCount     int
	SelectedNames    []string
	LastSelectedName string
	Switched         bool
	LastRetryCause   string
	// Status 在 ChatCompletions 这一类高层入口结束前由 finalize 显式置位，
	// 用于让客户端/监控分辨"成功 / 重试耗尽 / 上游全部失败 / 客户端断连"几种结局。
	// 空字符串表示尚未结束。
	Status     string
	FinalError string
}

func newUpstreamAttemptDiagnostics(operation string) *upstreamAttemptDiagnostics {
	return &upstreamAttemptDiagnostics{Operation: strings.TrimSpace(operation)}
}

func (d *upstreamAttemptDiagnostics) noteSelectedKey(plaintextKey string) {
	if d == nil {
		return
	}
	d.AttemptCount++
	name := lookupUpstreamKeyNameByPlaintext(plaintextKey)
	if name == "" {
		name = "(unknown)"
	}
	d.SelectedNames = append(d.SelectedNames, name)
	if d.LastSelectedName != "" && d.LastSelectedName != name {
		d.Switched = true
	}
	d.LastSelectedName = name
}

func (d *upstreamAttemptDiagnostics) noteRetry(cause string) {
	if d == nil {
		return
	}
	d.LastRetryCause = strings.TrimSpace(cause)
}

// finalize 标记此次请求的最终状态。status 取 "success" / "exhausted" / "context_canceled" / "upstream_failed"。
// finalErr 是给运维看的简短原因，可为空。
func (d *upstreamAttemptDiagnostics) finalize(status, finalErr string) {
	if d == nil {
		return
	}
	d.Status = strings.TrimSpace(status)
	d.FinalError = strings.TrimSpace(finalErr)
}

func encodeDebugHeaderValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func (d *upstreamAttemptDiagnostics) headers() map[string]string {
	if d == nil {
		return nil
	}
	selected := ""
	chain := ""
	if len(d.SelectedNames) > 0 {
		selected = d.SelectedNames[len(d.SelectedNames)-1]
		chain = strings.Join(d.SelectedNames, " -> ")
	}
	return map[string]string{
		"X-Gateway-Upstream-Operation":       d.Operation,
		"X-Gateway-Upstream-Key-Name":        selected,
		"X-Gateway-Upstream-Key-Name-B64":    encodeDebugHeaderValue(selected),
		"X-Gateway-Upstream-Key-Chain":       chain,
		"X-Gateway-Upstream-Key-Chain-B64":   encodeDebugHeaderValue(chain),
		"X-Gateway-Upstream-Attempts":        strconv.Itoa(d.AttemptCount),
		"X-Gateway-Upstream-Switched":        strconv.FormatBool(d.Switched),
		"X-Gateway-Upstream-Last-Error":      d.LastRetryCause,
		"X-Gateway-Upstream-Last-Error-B64":  encodeDebugHeaderValue(d.LastRetryCause),
		"X-Gateway-Upstream-Status":          d.Status,
		"X-Gateway-Upstream-Final-Error":     d.FinalError,
		"X-Gateway-Upstream-Final-Error-B64": encodeDebugHeaderValue(d.FinalError),
	}
}

func applyProxyHeaders(result *proxyResult, headers map[string]string) {
	if result == nil || len(headers) == 0 {
		return
	}
	if result.Headers == nil {
		result.Headers = map[string]string{}
	}
	for key, value := range headers {
		if strings.TrimSpace(value) == "" {
			continue
		}
		result.Headers[key] = value
	}
}

func applyResponseHeaders(headersSink interface{ Set(string, string) }, headers map[string]string) {
	if headersSink == nil || len(headers) == 0 {
		return
	}
	for key, value := range headers {
		if strings.TrimSpace(value) == "" {
			continue
		}
		headersSink.Set(key, value)
	}
}
