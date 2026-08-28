package gateway

import (
	"context"
	"testing"
	"time"

	"nvidia-api-gateway/pkg/scheduler"
)

func TestComputeHealthScoreAllSuccess(t *testing.T) {
	samples := make([]healthSample, 20)
	for i := range samples {
		samples[i] = healthSample{success: true, statusCode: 200, rtMs: 800}
	}
	score := computeHealthScore(samples)
	if score < 99 {
		t.Fatalf("全成功且 RT 快的评分应接近 100，实际 %.2f", score)
	}
}

func TestComputeHealthScoreAll5xx(t *testing.T) {
	samples := make([]healthSample, 20)
	for i := range samples {
		samples[i] = healthSample{success: false, statusCode: 503, rtMs: 500}
	}
	score := computeHealthScore(samples)
	// 成功率 0 + RT 满分 20 + 无429 满分 20 + 5xx 0 分 = 40。
	// 快速失败的 5xx 评分为 40（5xx 还有实时阶梯退避兜底），不应超过健康阈值。
	if score > healthLowScore {
		t.Fatalf("全 5xx 的评分应不超过 %v，实际 %.2f", healthLowScore, score)
	}

	// 慢速 5xx：成功率 0 + RT 0 + 无429 20 + 5xx 0 = 20，惩罚更深
	slow := make([]healthSample, 20)
	for i := range slow {
		slow[i] = healthSample{success: false, statusCode: 503, rtMs: 15000}
	}
	if s := computeHealthScore(slow); s > 25 {
		t.Fatalf("慢速全 5xx 的评分应低于 25，实际 %.2f", s)
	}
}

func TestComputeHealthScoreAll429(t *testing.T) {
	samples := make([]healthSample, 20)
	for i := range samples {
		samples[i] = healthSample{success: false, statusCode: 429, rtMs: 200}
	}
	score := computeHealthScore(samples)
	// 成功率 0 分 + RT 满分 20 + 429 0 分 + 无5xx 满分 20 = 40。
	// 429 多为上游限流（已有 429→5s 冷却机制处理），评分卡在阈值附近是合理行为。
	if score != 40 {
		t.Fatalf("全 429 的评分应为 40，实际 %.2f", score)
	}
}

func TestComputeHealthScoreAllTransportErrors(t *testing.T) {
	samples := make([]healthSample, 20)
	for i := range samples {
		samples[i] = healthSample{success: false, statusCode: 0, rtMs: 15000} // 连接错误 + 慢
	}
	score := computeHealthScore(samples)
	if score >= healthLowScore {
		t.Fatalf("全连接错误的评分应低于 %v，实际 %.2f", healthLowScore, score)
	}
}

func TestComputeHealthScoreSlowRT(t *testing.T) {
	samples := make([]healthSample, 20)
	for i := range samples {
		samples[i] = healthSample{success: true, statusCode: 200, rtMs: 15000} // 均值 15s > 10s
	}
	score := computeHealthScore(samples)
	// 成功率满分 40 分 + RT 0 分 + 429 满分 20 + 5xx 满分 20 = 80
	if score < 79 || score > 81 {
		t.Fatalf("慢 RT 场景评分应约为 80，实际 %.2f", score)
	}
}

func TestHealthScorerRecordWindow(t *testing.T) {
	h := newHealthScorer(nil)
	for i := 0; i < 150; i++ {
		h.record("key-a", "model-x", true, 200, 100)
	}
	h.mu.Lock()
	state := h.keys["key-a"]
	n := len(state.samples)
	h.mu.Unlock()
	if n != healthSampleWindowSize {
		t.Fatalf("滑动窗口应截断到 %d 条，实际 %d", healthSampleWindowSize, n)
	}
}

func TestHealthScorer5xxEscalationCooling(t *testing.T) {
	sched := scheduler.NewScheduler(nil)
	ctx := context.Background()
	_ = sched.AddKey(ctx, "key-5xx", 1.0)

	h := newHealthScorer(sched)
	// 连续 2 次 5xx 触发模型级阶梯冷却（15s）
	h.record("key-5xx", "model-5xx", false, 500, 100)
	h.record("key-5xx", "model-5xx", false, 502, 100)

	// 冷却后该 (key, model) 不应再被选中
	got, err := sched.AcquireKey(ctx, 3, "model-5xx")
	if err != nil {
		t.Fatalf("AcquireKey 出错: %v", err)
	}
	if got != "" {
		t.Fatalf("连续 5xx 后应进入模型级冷却，但仍选中了 key: %q", got)
	}

	// 其他模型不受影响
	got, err = sched.AcquireKey(ctx, 3, "model-other")
	if err != nil {
		t.Fatalf("AcquireKey 出错: %v", err)
	}
	if got != "key-5xx" {
		t.Fatalf("其他模型不应受影响，期望选中 key-5xx，实际 %q", got)
	}
}

func TestHealthScorerSuccessResets5xx(t *testing.T) {
	sched := scheduler.NewScheduler(nil)
	ctx := context.Background()
	_ = sched.AddKey(ctx, "key-reset", 1.0)

	h := newHealthScorer(sched)
	h.record("key-reset", "model-x", false, 500, 100)
	h.record("key-reset", "model-x", true, 200, 100) // 成功重置连续 5xx

	// 连续 5xx 计数已重置，再次 5xx 不触发冷却
	h.record("key-reset", "model-x", false, 500, 100)

	got, err := sched.AcquireKey(ctx, 3, "model-x")
	if err != nil {
		t.Fatalf("AcquireKey 出错: %v", err)
	}
	if got != "key-reset" {
		t.Fatalf("成功重置后单次 5xx 不应触发冷却，期望选中 key-reset，实际 %q", got)
	}
}

func TestHealthScorerLowScoreCooling(t *testing.T) {
	sched := scheduler.NewScheduler(nil)
	ctx := context.Background()
	_ = sched.AddKey(ctx, "key-bad", 1.0)

	h := newHealthScorer(sched)
	// 20 次全失败（连接错误），评分必然 < 40
	for i := 0; i < 20; i++ {
		h.record("key-bad", "model-x", false, 0, 5000)
	}

	h.evaluateScores(ctx)

	// 低分 key 应被冷却，不再被选中
	got, err := sched.AcquireKey(ctx, 3, "model-x")
	if err != nil {
		t.Fatalf("AcquireKey 出错: %v", err)
	}
	if got != "" {
		t.Fatalf("低分 key 应进入冷却，但仍选中了: %q", got)
	}
}

func TestHealthScorerHealthyKeyNotCooled(t *testing.T) {
	sched := scheduler.NewScheduler(nil)
	ctx := context.Background()
	_ = sched.AddKey(ctx, "key-good", 1.0)

	h := newHealthScorer(sched)
	for i := 0; i < 20; i++ {
		h.record("key-good", "model-x", true, 200, 300)
	}

	h.evaluateScores(ctx)

	got, err := sched.AcquireKey(ctx, 3, "model-x")
	if err != nil {
		t.Fatalf("AcquireKey 出错: %v", err)
	}
	if got != "key-good" {
		t.Fatalf("健康 key 不应被冷却，期望选中 key-good，实际 %q", got)
	}
}

func TestHealthScorerConsecutiveLowEscalation(t *testing.T) {
	sched := scheduler.NewScheduler(nil)
	ctx := context.Background()
	_ = sched.AddKey(ctx, "key-bad2", 1.0)

	h := newHealthScorer(sched)
	// 第一轮低分
	for i := 0; i < 20; i++ {
		h.record("key-bad2", "model-x", false, 0, 5000)
	}
	h.evaluateScores(ctx)

	h.mu.Lock()
	low := h.keys["key-bad2"].consecutiveLow
	h.mu.Unlock()
	if low != 1 {
		t.Fatalf("第一轮低分后 consecutiveLow 应为 1，实际 %d", low)
	}

	// 第二轮仍低分（冷却中无新样本不影响评估，直接沿用旧样本）
	h.evaluateScores(ctx)
	h.mu.Lock()
	low = h.keys["key-bad2"].consecutiveLow
	h.mu.Unlock()
	if low != 2 {
		t.Fatalf("第二轮低分后 consecutiveLow 应为 2，实际 %d", low)
	}
}

func TestHealthScorerReset(t *testing.T) {
	h := newHealthScorer(nil)
	h.record("key-a", "model-x", true, 200, 100)
	h.reset("key-a")
	h.mu.Lock()
	_, exists := h.keys["key-a"]
	h.mu.Unlock()
	if exists {
		t.Fatal("reset 后应删除该 key 的健康状态")
	}
}

var _ = time.Second // 保持 time 导入（供未来测试扩展）
