package gateway

import (
	"testing"
	"time"
)

// testBreaker 构造一个小阈值的熔断器，便于测试快速触发。
func testBreaker(threshold int) *modelCircuitBreaker {
	return newModelCircuitBreakerWithConfig(threshold, 30*time.Second, 30*time.Second)
}

func TestModelBreaker429Trigger(t *testing.T) {
	b := testBreaker(3)
	model := "nvidia/llama-3.3-70b"

	// 2 次 429 不应触发熔断（阈值 3）
	b.recordFailure(model, false)
	b.recordFailure(model, false)
	if b.isOpen(model) {
		t.Fatal("窗口内未达阈值的 429 不应触发熔断")
	}

	// 第 3 次触发
	b.recordFailure(model, false)
	if !b.isOpen(model) {
		t.Fatal("窗口内达到阈值后应触发熔断")
	}
}

func TestModelBreaker429And5xxSeparate(t *testing.T) {
	b := testBreaker(3)
	model := "nvidia/deepseek-r1"

	// 大量 429 不影响 5xx 统计
	for i := 0; i < 3; i++ {
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

	for i := 0; i < 3; i++ {
		b.recordFailure(model, true)
	}
	if !b.isOpen(model) {
		t.Fatal("5xx 达到阈值应触发熔断")
	}
}

func TestModelBreakerCooldownExpiry(t *testing.T) {
	b := testBreaker(3)
	model := "nvidia/llama-3.1-8b"

	for i := 0; i < 3; i++ {
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
	for i := 0; i < 2; i++ {
		b.recordFailure(model, false)
	}
	if b.isOpen(model) {
		t.Fatal("恢复后需重新积累失败次数才再次熔断")
	}
}

func TestModelBreakerEmptyModel(t *testing.T) {
	b := testBreaker(3)
	// 空模型不应 panic，也不应记录
	b.recordFailure("", false)
	b.recordFailure("  ", true)
	if b.isOpen("") {
		t.Fatal("空模型不应熔断")
	}
}

func TestModelBreakerStatus(t *testing.T) {
	b := testBreaker(3)
	model := "nvidia/llama-3.3-70b"
	for i := 0; i < 3; i++ {
		b.recordFailure(model, false)
	}
	st := b.status(model)
	if st["open_429"] != true {
		t.Fatalf("status 应显示 429 熔断开启: %v", st)
	}
	if st["recent_429_count"].(int) != 0 {
		t.Fatalf("熔断后窗口应清空: %v", st)
	}
	if st["enabled"] != true {
		t.Fatalf("默认应启用熔断: %v", st)
	}
}

func TestModelBreakerDisabled(t *testing.T) {
	// threshold <= 0 表示禁用：无论多少失败都不熔断
	b := newModelCircuitBreakerWithConfig(0, 30*time.Second, 30*time.Second)
	model := "nvidia/llama-3.3-70b"
	for i := 0; i < 100; i++ {
		b.recordFailure(model, false)
		b.recordFailure(model, true)
	}
	if b.isOpen(model) {
		t.Fatal("禁用状态下不应熔断")
	}
	if st := b.status(model); st["enabled"] != false {
		t.Fatalf("禁用状态应显示 enabled=false: %v", st)
	}
}
