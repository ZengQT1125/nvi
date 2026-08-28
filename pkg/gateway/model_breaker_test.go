package gateway

import (
	"testing"
	"time"
)

func TestModelBreaker429Trigger(t *testing.T) {
	b := newModelCircuitBreaker()
	model := "nvidia/llama-3.3-70b"

	// 7 次 429 不应触发熔断（阈值 8）
	for i := 0; i < modelBreakerThreshold-1; i++ {
		b.recordFailure(model, false)
	}
	if b.isOpen(model) {
		t.Fatalf("窗口内 %d 次 429 不应触发熔断", modelBreakerThreshold-1)
	}

	// 第 8 次触发
	b.recordFailure(model, false)
	if !b.isOpen(model) {
		t.Fatal("窗口内达到阈值后应触发熔断")
	}
}

func TestModelBreaker429And5xxSeparate(t *testing.T) {
	b := newModelCircuitBreaker()
	model := "nvidia/deepseek-r1"

	// 大量 429 不影响 5xx 统计
	for i := 0; i < modelBreakerThreshold; i++ {
		b.recordFailure(model, false)
	}
	if !b.isOpen(model) {
		t.Fatal("429 应触发熔断")
	}

	// 不同模型互不影响
	other := "nvidia/gemma-2"
	if b.isOpen(other) {
		t.Fatal("其他模型不应处于熔断状态")
	}

	// 清空 429 熔断（模拟冷却到期），5xx 独立统计再触发
	b.mu.Lock()
	delete(b.openUntil429, model)
	b.recent429[model] = nil
	b.mu.Unlock()
	if b.isOpen(model) {
		t.Fatal("429 熔断清除后模型不应再处于熔断")
	}

	for i := 0; i < modelBreakerThreshold; i++ {
		b.recordFailure(model, true)
	}
	if !b.isOpen(model) {
		t.Fatal("5xx 达到阈值应触发熔断")
	}
}

func TestModelBreakerCooldownExpiry(t *testing.T) {
	b := newModelCircuitBreaker()
	model := "nvidia/llama-3.1-8b"

	for i := 0; i < modelBreakerThreshold; i++ {
		b.recordFailure(model, false)
	}
	if !b.isOpen(model) {
		t.Fatal("应处于熔断中")
	}

	// 手动把熔断截止时间拨到过去，模拟冷却到期
	b.mu.Lock()
	b.openUntil429[model] = time.Now().Add(-time.Second)
	b.mu.Unlock()

	if b.isOpen(model) {
		t.Fatal("冷却到期后应自动恢复")
	}

	// 恢复后窗口已清空，重新计数
	for i := 0; i < modelBreakerThreshold-1; i++ {
		b.recordFailure(model, false)
	}
	if b.isOpen(model) {
		t.Fatal("恢复后需重新积累失败次数才再次熔断")
	}
}

func TestModelBreakerEmptyModel(t *testing.T) {
	b := newModelCircuitBreaker()
	// 空模型不应 panic，也不应记录
	b.recordFailure("", false)
	b.recordFailure("  ", true)
	if b.isOpen("") {
		t.Fatal("空模型不应熔断")
	}
}

func TestModelBreakerStatus(t *testing.T) {
	b := newModelCircuitBreaker()
	model := "nvidia/llama-3.3-70b"
	for i := 0; i < modelBreakerThreshold; i++ {
		b.recordFailure(model, false)
	}
	st := b.status(model)
	if st["open_429"] != true {
		t.Fatalf("status 应显示 429 熔断开启: %v", st)
	}
	if st["recent_429_count"].(int) != 0 {
		t.Fatalf("熔断后窗口应清空: %v", st)
	}
}
