package gateway

import (
	"context"
	"io"
	"sync"
	"time"
)

// chunkReadResult 是 reader goroutine 把每次读到的内容发给主循环的载体。
// data 是 buf 的独立 copy，主循环可以自由消费，不会和 reader 的 buf 发生竞争。
type chunkReadResult struct {
	data []byte
	err  error
}

// drainState 描述 drainOpenAIChunks 结束时的原因。
type drainState int

const (
	drainEOF             drainState = iota // 正常结束（io.EOF）
	drainUpstreamErr                       // reader 报错
	drainIdleTimeout                       // 两个 chunk 之间静默超过 chunkReadTimeout
	drainContextCanceled                   // 调用方 ctx 取消
)

// drainOpenAIChunks 接管一段上游流式响应的读取，逐块调用 onChunk 回调。
// 它通过把 buf 独占在一个长寿 goroutine 里、用 abort channel 强制中断、
// 然后等待该 goroutine 真正退出来避免：
//  1. buf 被回收到 pool 后还被旧的 reader.Read 持有 → 跨请求数据竞争
//  2. select 超时退出后 goroutine 持续阻塞在 reader.Read → goroutine 泄漏
//
// 参数说明：
//   - bodyCloser 用于在超时 / ctx 取消时强制 io.Reader.Read 返回。一般传 resp.Body。
//   - onChunk 由调用方实现把数据写到客户端的 ResponseWriter；返回 false 表示提前停止。
//
// 返回的 state 用于让调用方决定要不要补发"流被中断"的 SSE 终止帧。
func drainOpenAIChunks(
	ctx context.Context,
	reader io.Reader,
	bodyCloser io.Closer,
	idleTimeout time.Duration,
	onChunk func(chunk []byte) bool,
) drainState {
	if idleTimeout <= 0 {
		// 默认与 models.DefaultStreamIdleTimeoutSec 对齐（600s），
		// 避免长任务（递归读项目、长输出、工具调用思考）被网关当成"僵尸流"。
		idleTimeout = 600 * time.Second
	}

	abort := make(chan struct{})
	results := make(chan chunkReadResult)
	var aborted sync.Once
	doAbort := func() {
		aborted.Do(func() {
			close(abort)
			if bodyCloser != nil {
				_ = bodyCloser.Close()
			}
		})
	}

	// reader goroutine：独占 buf，每次读完把 copy 发给主循环。
	// 主循环不再持有 buf，避免和 pool 间的并发问题。
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buf := bufferPool.Get().([]byte)
		defer bufferPool.Put(buf)
		for {
			n, err := reader.Read(buf)
			var payload []byte
			if n > 0 {
				payload = make([]byte, n)
				copy(payload, buf[:n])
			}
			select {
			case results <- chunkReadResult{data: payload, err: err}:
			case <-abort:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	// 保证 goroutine 真正退出后再返回：避免 buf 被 pool 复用时仍然被旧 goroutine 持有。
	defer func() {
		doAbort()
		<-readerDone
	}()

	state := drainEOF
	for {
		timer := time.NewTimer(idleTimeout)
		select {
		case rr := <-results:
			timer.Stop()
			if len(rr.data) > 0 {
				if !onChunk(rr.data) {
					// 调用方主动叫停（如客户端连接断开）。
					return drainContextCanceled
				}
			}
			if rr.err != nil {
				if rr.err == io.EOF {
					return drainEOF
				}
				return drainUpstreamErr
			}
		case <-timer.C:
			// 上游静默超过 idleTimeout 没发任何数据：判定为僵尸流，强制结束。
			state = drainIdleTimeout
			return state
		case <-ctx.Done():
			timer.Stop()
			return drainContextCanceled
		}
	}
}
