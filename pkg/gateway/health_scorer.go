package gateway

import (
	"context"
	"log"
	"sync"
	"time"

	"nvidia-api-gateway/pkg/scheduler"
)

// 密钥健康评分器（移植自 AQUA Platform 的被动健康评分思路）
//
// 背景：大密钥池（数百~上千个 key）里必然混杂快慢好坏。
// 平滑加权轮询只按权重选 key，无法区分"健康但慢"或"半死不活"的 key。
// 本模块基于真实请求做被动统计（不额外发送探活请求），
// 低分 key 自动进入渐进冷却；表现恢复后冷却自然到期，评分随之回升。
//
// 评分公式（0~100，每 30 秒后台评估一次）：
//
//	score = 成功率×0.4 + RT得分×0.2 + 429率得分×0.2 + 5xx/传输错误率得分×0.2
//
// 注意：statusCode==0（传输层错误，如连接失败/超时）与 5xx 同样计入“服务不可用”惩罚，
// 否则一个“连不上”的 key 只会轻微扣成功率分，仍然会被反复选中。
//
// 另外提供 5xx 阶梯退避：某个 key 对某模型连续 5xx 时，
// 立即对该 (key, model) 做递增冷却（15s → 30s → 60s 封顶），
// 不等后台周期，避免持续打到正在 5xx 的 key 上。

const (
	healthSampleWindowSize = 100              // 每个 key 保留的滑动窗口样本数
	healthMinSamples       = 10               // 少于该样本数不参与评分
	healthLowScore         = 40.0             // 评分低于此值视为不健康
	healthHealthyScore     = 60.0             // 评分高于此值重置连续低分计数
	healthBaseCooldown     = 60 * time.Second // 低分首次冷却
	healthMaxCooldown      = 10 * time.Minute // 低分冷却封顶

	// 5xx 阶梯退避：连续第 2 次 5xx 起触发冷却
	health5xxEscalationStart = 2
	health5xxBaseStep        = 15 * time.Second
	health5xxMaxCooldown     = 60 * time.Second
)

type healthSample struct {
	success    bool
	statusCode int
	rtMs       int64
}

type keyHealthState struct {
	samples        []healthSample
	consecutive5xx int
	consecutiveLow int
}

type healthScorer struct {
	mu        sync.Mutex
	keys      map[string]*keyHealthState
	scheduler *scheduler.Scheduler
}

func newHealthScorer(sched *scheduler.Scheduler) *healthScorer {
	return &healthScorer{
		keys:      map[string]*keyHealthState{},
		scheduler: sched,
	}
}

// record 记录一次上游结果。statusCode 为 0 表示传输层错误（无 HTTP 状态）。
// 该方法并发安全，内部仅做内存统计；触发的冷却在锁外执行，避免阻塞请求路径。
func (h *healthScorer) record(key, model string, success bool, statusCode int, rtMs int64) {
	if h == nil || key == "" {
		return
	}
	var coolModel string
	var coolDuration time.Duration

	h.mu.Lock()
	state := h.keys[key]
	if state == nil {
		state = &keyHealthState{}
		h.keys[key] = state
	}
	state.samples = append(state.samples, healthSample{success: success, statusCode: statusCode, rtMs: rtMs})
	if len(state.samples) > healthSampleWindowSize {
		state.samples = state.samples[len(state.samples)-healthSampleWindowSize:]
	}

	// 5xx 阶梯退避：连续 5xx 递增冷却（模型级，不影响该 key 的其他模型）
	if statusCode >= 500 {
		state.consecutive5xx++
		if state.consecutive5xx >= health5xxEscalationStart {
			escalation := state.consecutive5xx - health5xxEscalationStart + 1
			coolDuration = time.Duration(escalation) * health5xxBaseStep
			if coolDuration > health5xxMaxCooldown {
				coolDuration = health5xxMaxCooldown
			}
			coolModel = model
		}
	} else {
		state.consecutive5xx = 0
	}
	h.mu.Unlock()

	if coolModel != "" && h.scheduler != nil {
		_ = h.scheduler.MarkModelCooling(context.Background(), key, coolModel, coolDuration)
		log.Printf("healthScorer 5xx阶梯退避: key=%s model=%s 连续5xx=%d 冷却=%v",
			shortKeyID(key), coolModel, state5xxCount(h, key), coolDuration)
	}
}

// runMaintenance 后台循环：每 30 秒评估一次各 key 健康评分。
func (h *healthScorer) runMaintenance(ctx context.Context) {
	if h == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.evaluateScores(ctx)
		}
	}
}

// evaluateScores 计算各 key 评分，低分 key 进入渐进冷却。
func (h *healthScorer) evaluateScores(ctx context.Context) {
	type coolingPlan struct {
		key      string
		duration time.Duration
	}
	var plans []coolingPlan

	h.mu.Lock()
	for key, state := range h.keys {
		if len(state.samples) < healthMinSamples {
			continue
		}
		score := computeHealthScore(state.samples)
		if score < healthLowScore {
			state.consecutiveLow++
			duration := healthBaseCooldown
			for i := 1; i < state.consecutiveLow; i++ {
				duration *= 2
				if duration >= healthMaxCooldown {
					duration = healthMaxCooldown
					break
				}
			}
			plans = append(plans, coolingPlan{key: key, duration: duration})
			log.Printf("healthScorer 低分冷却: key=%s 评分=%.1f 连续低分=%d 冷却=%v",
				shortKeyID(key), score, state.consecutiveLow, duration)
		} else if score >= healthHealthyScore {
			state.consecutiveLow = 0
		}
	}
	h.mu.Unlock()

	for _, p := range plans {
		if h.scheduler != nil {
			_ = h.scheduler.MarkCooling(ctx, p.key, p.duration)
		}
	}
}

// reset 清除某 key 的全部健康状态（密钥被删除/替换时调用）。
func (h *healthScorer) reset(key string) {
	if h == nil || key == "" {
		return
	}
	h.mu.Lock()
	delete(h.keys, key)
	h.mu.Unlock()
}

// snapshot 返回各 key 的评分快照（管理后台/调试用）。
func (h *healthScorer) snapshot() map[string]any {
	if h == nil {
		return map[string]any{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]any{}
	for key, state := range h.keys {
		if len(state.samples) < healthMinSamples {
			continue
		}
		out[key] = map[string]any{
			"score":           computeHealthScore(state.samples),
			"samples":         len(state.samples),
			"consecutive_low": state.consecutiveLow,
		}
	}
	return out
}

// computeHealthScore 计算样本窗口内的健康评分（0~100）。
func computeHealthScore(samples []healthSample) float64 {
	if len(samples) == 0 {
		return 100
	}
	total := len(samples)
	success := 0
	rate429 := 0
	rate5xx := 0
	var rtSum int64
	rtCount := 0
	for _, s := range samples {
		if s.success {
			success++
		}
		switch {
		case s.statusCode == 429:
			rate429++
		case s.statusCode >= 500 || s.statusCode == 0:
			// statusCode==0 表示传输层错误（连接失败/超时等），
			// 说明该 key 网络质量差，与 5xx 同样按“服务不可用”计入惩罚
			rate5xx++
		}
		if s.rtMs > 0 {
			rtSum += s.rtMs
			rtCount++
		}
	}

	// 成功率得分（40%）
	srScore := float64(success) / float64(total) * 100

	// 响应时间得分（20%）：均值 <2s 满分，>10s 零分，之间线性
	rtScore := 100.0
	if rtCount > 0 {
		avgRt := float64(rtSum) / float64(rtCount) / 1000 // ms -> s
		switch {
		case avgRt < 2:
			rtScore = 100
		case avgRt > 10:
			rtScore = 0
		default:
			rtScore = 100 * (10 - avgRt) / 8
		}
	}

	// 429 率得分（20%）：<5% 满分，>30% 零分，之间线性
	r429 := float64(rate429) / float64(total)
	r429Score := 100.0
	if r429 < 0.05 {
		r429Score = 100
	} else if r429 > 0.30 {
		r429Score = 0
	} else {
		r429Score = 100 * (0.30 - r429) / 0.25
	}

	// 5xx 率得分（20%）：<2% 满分，>20% 零分，之间线性
	r5xx := float64(rate5xx) / float64(total)
	r5xxScore := 100.0
	if r5xx < 0.02 {
		r5xxScore = 100
	} else if r5xx > 0.20 {
		r5xxScore = 0
	} else {
		r5xxScore = 100 * (0.20 - r5xx) / 0.18
	}

	score := srScore*0.4 + rtScore*0.2 + r429Score*0.2 + r5xxScore*0.2
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return score
}

func shortKeyID(key string) string {
	if len(key) <= 8 {
		return key
	}
	return key[:8]
}

// state5xxCount 供日志使用：读取某 key 的当前连续 5xx 计数。
func state5xxCount(h *healthScorer, key string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if state, ok := h.keys[key]; ok {
		return state.consecutive5xx
	}
	return 0
}
