package scheduler

import (
	"context"
	"testing"
	"time"
)

// TestModelLevelCoolingLocalScheduler 验证 (key, model) 维度的冷却：
// 1. 某 key 对模型 m1 冷却后，AcquireKey(m1) 会跳过它选别的 key；
// 2. 同一个 key 对模型 m2 不受影响；
// 3. TryAcquireSpecificKey 同样遵守模型级冷却；
// 4. Reset 保留冷却状态（防止恢复后同一模型立刻又撞限流）；
// 5. 冷却过期后自动恢复。
func TestModelLevelCoolingLocalScheduler(t *testing.T) {
	sched := NewScheduler(nil)
	ctx := context.Background()

	if err := sched.AddKey(ctx, "key1", 2); err != nil {
		t.Fatalf("AddKey key1 failed: %v", err)
	}
	if err := sched.AddKey(ctx, "key2", 1); err != nil {
		t.Fatalf("AddKey key2 failed: %v", err)
	}

	// key1 对模型 m1 冷却 3 分钟
	if err := sched.MarkModelCooling(ctx, "key1", "m1", 3*time.Minute); err != nil {
		t.Fatalf("MarkModelCooling failed: %v", err)
	}

	// 1. 请求模型 m1 时应跳过 key1，选 key2
	acquired, err := sched.AcquireKey(ctx, 1, "m1")
	if err != nil {
		t.Fatalf("AcquireKey(m1) failed: %v", err)
	}
	if acquired != "key2" {
		t.Fatalf("expected key2 for model m1 (key1 model-cooling), got %s", acquired)
	}
	_ = sched.ReleaseKey(ctx, acquired)

	// 2. 请求模型 m2 时 key1 不受影响（key2 已被占用释放，权重轮询应选 key1）
	acquired2, err := sched.AcquireKey(ctx, 1, "m2")
	if err != nil {
		t.Fatalf("AcquireKey(m2) failed: %v", err)
	}
	if acquired2 != "key1" {
		t.Fatalf("expected key1 for model m2 (unaffected), got %s", acquired2)
	}
	_ = sched.ReleaseKey(ctx, acquired2)

	// 3. 会话亲和路径同样遵守模型级冷却
	if ok, err := sched.TryAcquireSpecificKey(ctx, "key1", 1, "m1"); err != nil || ok {
		t.Fatalf("expected TryAcquireSpecificKey(key1, m1) to be blocked, got ok=%v err=%v", ok, err)
	}
	if ok, err := sched.TryAcquireSpecificKey(ctx, "key1", 1, "m2"); err != nil || !ok {
		t.Fatalf("expected TryAcquireSpecificKey(key1, m2) to succeed, got ok=%v err=%v", ok, err)
	}
	_ = sched.ReleaseKey(ctx, "key1")

	// 4. Reset 不清冷却表（真实流程：RestoreRecoverableStatuses → Reset → LoadActiveKeys 重载）
	if err := sched.Reset(ctx); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}
	// 模拟 LoadActiveKeys 从 DB 重新加载 active key
	if err := sched.AddKey(ctx, "key1", 2); err != nil {
		t.Fatalf("AddKey key1 after Reset failed: %v", err)
	}
	if err := sched.AddKey(ctx, "key2", 1); err != nil {
		t.Fatalf("AddKey key2 after Reset failed: %v", err)
	}
	acquired3, err := sched.AcquireKey(ctx, 1, "m1")
	if err != nil {
		t.Fatalf("AcquireKey(m1) after Reset failed: %v", err)
	}
	if acquired3 != "key2" {
		t.Fatalf("expected key2 after Reset (cooling preserved), got %s", acquired3)
	}
	_ = sched.ReleaseKey(ctx, acquired3)

	// 5. 冷却过期后自动恢复（本地模式冷却用秒级时间戳，sleep 需保证跨过冷却到期秒）
	if err := sched.MarkModelCooling(ctx, "key1", "m3", 2*time.Second); err != nil {
		t.Fatalf("MarkModelCooling(m3) failed: %v", err)
	}
	if got, _ := sched.AcquireKey(ctx, 1, "m3"); got != "key2" {
		t.Fatalf("expected key2 while key1 model-cooling for m3, got %s", got)
	}
	_ = sched.ReleaseKey(ctx, "key2")
	time.Sleep(3 * time.Second)
	acquired4, err := sched.AcquireKey(ctx, 1, "m3")
	if err != nil {
		t.Fatalf("AcquireKey(m3) after expiry failed: %v", err)
	}
	if acquired4 != "key1" {
		t.Fatalf("expected key1 after model cooling expiry, got %s", acquired4)
	}
	_ = sched.ReleaseKey(ctx, acquired4)
}
