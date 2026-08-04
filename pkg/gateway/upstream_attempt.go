package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"nvidia-api-gateway/pkg/models"
)

var (
	errUpstreamFirstByteTimeout = errors.New("upstream first byte timeout")
	errUpstreamEmptyResponse    = errors.New("upstream returned empty response")
)

type upstreamDoResult struct {
	resp *http.Response
	err  error
}

type firstReadResult struct {
	data []byte
	err  error
}

func headerWaitTimeoutForRequest(cfg models.SystemConfig, method, endpointPath string, enabled bool) time.Duration {
	if !enabled {
		return 0
	}
	return firstByteTimeout(cfg)
}

func (g *Gateway) shouldUseHeaderWaitTimeout(ctx context.Context, method, endpointPath string) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	endpointPath = strings.Trim(strings.TrimSpace(endpointPath), "/")
	if method == http.MethodPost && endpointPath == "chat/completions" {
		if g == nil || g.scheduler == nil {
			return false
		}
		stats, err := g.scheduler.Stats(ctx)
		if err == nil && stats != nil && stats.Active <= 1 {
			return false
		}
	}
	return true
}

func (g *Gateway) openUpstreamHeadersWithTimeout(
	ctx context.Context,
	cfg models.SystemConfig,
	key, method, endpointPath string,
	body []byte,
	accept string,
) (*http.Response, context.CancelFunc, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(reqCtx, method, buildUpstreamURL(cfg, endpointPath), bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	resultCh := make(chan upstreamDoResult, 1)
	go func() {
		resp, doErr := g.httpClient(cfg, key).Do(req)
		resultCh <- upstreamDoResult{resp: resp, err: doErr}
	}()

	timeout := headerWaitTimeoutForRequest(cfg, method, endpointPath, g.shouldUseHeaderWaitTimeout(ctx, method, endpointPath))
	if timeout <= 0 {
		result := <-resultCh
		if result.err != nil {
			cancel()
			return nil, nil, result.err
		}
		return result.resp, cancel, nil
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case result := <-resultCh:
		if result.err != nil {
			cancel()
			return nil, nil, result.err
		}
		return result.resp, cancel, nil
	case <-timer.C:
		cancel()
		return nil, nil, errUpstreamFirstByteTimeout
	case <-ctx.Done():
		cancel()
		return nil, nil, ctx.Err()
	}
}

func (g *Gateway) openUpstreamStreamWithPrefetch(
	ctx context.Context,
	cfg models.SystemConfig,
	key string,
	body []byte,
) (*http.Response, io.Reader, context.CancelFunc, error) {
	// 流式请求用完整请求超时（默认 600s），而不是首包超时（90s）
	// nvidia 大模型推理首个 token 可能需要较长时间
	timeout := time.Duration(cfg.RequestTimeoutSecond) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	reqCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, buildUpstreamURL(cfg, "chat/completions"), bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "text/event-stream")

	startedAt := time.Now()
	resultCh := make(chan upstreamDoResult, 1)
	go func() {
		resp, doErr := g.streamHTTPClient(cfg, key).Do(req)
		resultCh <- upstreamDoResult{resp: resp, err: doErr}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var resp *http.Response
	select {
	case result := <-resultCh:
		if result.err != nil {
			cancel()
			return nil, nil, nil, result.err
		}
		resp = result.resp
	case <-timer.C:
		cancel()
		return nil, nil, nil, errUpstreamFirstByteTimeout
	case <-ctx.Done():
		cancel()
		return nil, nil, nil, ctx.Err()
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
		return resp, resp.Body, cancel, nil
	}

	var prefetched bytes.Buffer
	for {
		remaining := timeout - time.Since(startedAt)
		if remaining <= 0 {
			_ = resp.Body.Close()
			cancel()
			return nil, nil, nil, errUpstreamFirstByteTimeout
		}

		result, readErr := readResponseBodyOnce(ctx, resp.Body, remaining)
		if readErr != nil {
			_ = resp.Body.Close()
			cancel()
			return nil, nil, nil, readErr
		}
		if len(result.data) > 0 {
			_, _ = prefetched.Write(result.data)
		}

		meaningful, done := inspectOpenAIStreamPrefetch(prefetched.Bytes())
		if done && !meaningful {
			_ = resp.Body.Close()
			cancel()
			return nil, nil, nil, errUpstreamEmptyResponse
		}
		if meaningful || prefetched.Len() >= maxOpenAIStreamPrefetchBytes {
			return resp, io.MultiReader(bytes.NewReader(prefetched.Bytes()), resp.Body), cancel, nil
		}

		if result.err != nil {
			if result.err == io.EOF {
				_ = resp.Body.Close()
				cancel()
				return nil, nil, nil, errUpstreamEmptyResponse
			}
			_ = resp.Body.Close()
			cancel()
			return nil, nil, nil, result.err
		}
	}
}

func readResponseBodyOnce(ctx context.Context, body io.Reader, timeout time.Duration) (firstReadResult, error) {
	if timeout <= 0 {
		timeout = time.Duration(models.DefaultFirstByteTimeoutMs) * time.Millisecond
	}
	readCh := make(chan firstReadResult, 1)
	go func() {
		buf := make([]byte, 4096)
		n, readErr := body.Read(buf)
		payload := []byte(nil)
		if n > 0 {
			payload = append(payload, buf[:n]...)
		}
		readCh <- firstReadResult{data: payload, err: readErr}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-readCh:
		return result, nil
	case <-timer.C:
		return firstReadResult{}, errUpstreamFirstByteTimeout
	case <-ctx.Done():
		return firstReadResult{}, ctx.Err()
	}
}
