package scheduler

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
)

// distribute 反复获取/释放 key，统计各 key 被选中的次数，用于验证权重倾斜。
func distribute(t *testing.T, sched *Scheduler, rounds int) map[string]int {
	t.Helper()
	ctx := context.Background()
	counts := map[string]int{}
	for i := 0; i < rounds; i++ {
		key, err := sched.AcquireKey(ctx, 1000, "model-x")
		if err != nil {
			t.Fatalf("AcquireKey 出错: %v", err)
		}
		if key == "" {
			t.Fatalf("第 %d 轮未选中任何 key", i)
		}
		counts[key]++
		if err := sched.ReleaseKey(ctx, key); err != nil {
			t.Fatalf("ReleaseKey 出错: %v", err)
		}
	}
	return counts
}

func TestSetHealthMultiplierTiltsWeightedRoundRobin(t *testing.T) {
	ctx := context.Background()
	sched := NewScheduler(nil)
	if err := sched.AddKey(ctx, "key-a", 1.0); err != nil {
		t.Fatalf("AddKey key-a 出错: %v", err)
	}
	if err := sched.AddKey(ctx, "key-b", 1.0); err != nil {
		t.Fatalf("AddKey key-b 出错: %v", err)
	}

	// 健康分 100 → 乘数 2.0；健康分 0 → 乘数 0.5。权重比应为 4:1。
	if err := sched.SetHealthMultiplier(ctx, "key-a", 2.0); err != nil {
		t.Fatalf("SetHealthMultiplier key-a 出错: %v", err)
	}
	if err := sched.SetHealthMultiplier(ctx, "key-b", 0.5); err != nil {
		t.Fatalf("SetHealthMultiplier key-b 出错: %v", err)
	}

	counts := distribute(t, sched, 500)
	if counts["key-a"] < 350 || counts["key-b"] > 150 {
		t.Fatalf("期望 key-a:key-b ≈ 4:1，实际 %d:%d", counts["key-a"], counts["key-b"])
	}
}

func TestSetHealthMultiplierIsIdempotent(t *testing.T) {
	ctx := context.Background()
	sched := NewScheduler(nil)
	_ = sched.AddKey(ctx, "key-a", 1.0)
	_ = sched.AddKey(ctx, "key-b", 1.0)

	// 反复在同一 key 上刷新乘数，不应产生复利效应（否则 key-a 的权重会指数膨胀）。
	for i := 0; i < 5; i++ {
		if err := sched.SetHealthMultiplier(ctx, "key-a", 2.0); err != nil {
			t.Fatalf("SetHealthMultiplier 出错: %v", err)
		}
		if err := sched.SetHealthMultiplier(ctx, "key-a", 1.0); err != nil {
			t.Fatalf("SetHealthMultiplier 出错: %v", err)
		}
	}
	if err := sched.SetHealthMultiplier(ctx, "key-a", 2.0); err != nil {
		t.Fatalf("SetHealthMultiplier 出错: %v", err)
	}

	counts := distribute(t, sched, 300)
	if counts["key-a"] < 180 || counts["key-b"] < 90 {
		t.Fatalf("期望 key-a:key-b ≈ 2:1（而非复利后的极端倾斜），实际 %d:%d",
			counts["key-a"], counts["key-b"])
	}
}

func TestSetHealthMultiplierKeepsBaseWeight(t *testing.T) {
	ctx := context.Background()
	sched := NewScheduler(nil)
	_ = sched.AddKey(ctx, "heavy", 3.0)
	_ = sched.AddKey(ctx, "light", 1.0)

	// heavy 被降权后仍是 3 × 0.5 = 1.5 > light 的 1 × 1.0 = 1.0
	if err := sched.SetHealthMultiplier(ctx, "heavy", 0.5); err != nil {
		t.Fatalf("SetHealthMultiplier 出错: %v", err)
	}

	counts := distribute(t, sched, 500)
	if counts["heavy"] <= counts["light"] {
		t.Fatalf("基础权重更高的 key 仍应获得更多流量，实际 heavy=%d light=%d",
			counts["heavy"], counts["light"])
	}
}

func TestSetHealthMultiplierIgnoresUnknownKey(t *testing.T) {
	ctx := context.Background()
	sched := NewScheduler(nil)
	_ = sched.AddKey(ctx, "key-a", 1.0)

	if err := sched.SetHealthMultiplier(ctx, "missing", 2.0); err != nil {
		t.Fatalf("未知 key 不应报错: %v", err)
	}
	counts := distribute(t, sched, 10)
	if counts["key-a"] != 10 {
		t.Fatalf("未知 key 不应被写回调度池，实际 %v", counts)
	}
}

func TestClampHealthMultiplier(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0.1, 0.5},
		{0.5, 0.5},
		{1.0, 1.0},
		{2.0, 2.0},
		{9.9, 2.0},
	}
	for _, c := range cases {
		if got := clampHealthMultiplier(c.in); got != c.want {
			t.Fatalf("clampHealthMultiplier(%v) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

// --- Redis 模式（生产路径）---

func newTestRedisScheduler(t *testing.T) (*Scheduler, *redis.Client) {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skip("Redis 未运行，跳过 Redis 模式测试")
	}
	if err := client.FlushAll(context.Background()).Err(); err != nil {
		t.Fatalf("FlushAll 出错: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return NewScheduler(client), client
}

func TestRedisSetHealthMultiplierKeepsBaseWeight(t *testing.T) {
	ctx := context.Background()
	sched, client := newTestRedisScheduler(t)

	if err := sched.AddKey(ctx, "key-a", 3.0); err != nil {
		t.Fatalf("AddKey 出错: %v", err)
	}

	// 反复写入同一乘数：score 必须是 3.0 × 2.0 = 6.0，而不是复利后的 3.0 × 2.0^n
	for i := 0; i < 5; i++ {
		if err := sched.SetHealthMultiplier(ctx, "key-a", 2.0); err != nil {
			t.Fatalf("SetHealthMultiplier 出错: %v", err)
		}
	}
	score, err := client.ZScore(ctx, "nvidia:keys:active", "key-a").Result()
	if err != nil {
		t.Fatalf("ZScore 出错: %v", err)
	}
	if score < 5.99 || score > 6.01 {
		t.Fatalf("期望 score = 基础权重 3.0 × 乘数 2.0 = 6.0，实际 %v（乘数可能发生了复利）", score)
	}

	// 模拟 LoadActiveKeys 的 5 分钟刷新：AddKey 应重新应用已保存的乘数
	if err := sched.AddKey(ctx, "key-a", 3.0); err != nil {
		t.Fatalf("AddKey 刷新出错: %v", err)
	}
	score, err = client.ZScore(ctx, "nvidia:keys:active", "key-a").Result()
	if err != nil {
		t.Fatalf("ZScore 出错: %v", err)
	}
	if score < 5.99 || score > 6.01 {
		t.Fatalf("AddKey 刷新后应仍为 6.0（乘数不能被抹掉），实际 %v", score)
	}

	// 乘数归中后应回到基础权重
	if err := sched.SetHealthMultiplier(ctx, "key-a", 1.0); err != nil {
		t.Fatalf("SetHealthMultiplier 出错: %v", err)
	}
	score, _ = client.ZScore(ctx, "nvidia:keys:active", "key-a").Result()
	if score < 2.99 || score > 3.01 {
		t.Fatalf("乘数归中后期望 score = 3.0，实际 %v", score)
	}
}

func TestRedisSetHealthMultiplierIgnoresUnknownKey(t *testing.T) {
	ctx := context.Background()
	sched, client := newTestRedisScheduler(t)

	if err := sched.SetHealthMultiplier(ctx, "missing", 2.0); err != nil {
		t.Fatalf("未知 key 不应报错: %v", err)
	}
	if err := client.ZScore(ctx, "nvidia:keys:active", "missing").Err(); err != redis.Nil {
		t.Fatalf("未知 key 不应被写回 nvidia:keys:active，实际 err=%v", err)
	}
}

func TestRedisSetHealthMultiplierTiltsSelection(t *testing.T) {
	ctx := context.Background()
	sched, _ := newTestRedisScheduler(t)

	_ = sched.AddKey(ctx, "key-a", 1.0)
	_ = sched.AddKey(ctx, "key-b", 1.0)
	_ = sched.SetHealthMultiplier(ctx, "key-a", 2.0)
	_ = sched.SetHealthMultiplier(ctx, "key-b", 0.5)

	counts := map[string]int{}
	for i := 0; i < 400; i++ {
		key, err := sched.AcquireKey(ctx, 1000, "model-x")
		if err != nil {
			t.Fatalf("AcquireKey 出错: %v", err)
		}
		if key == "" {
			t.Fatalf("第 %d 轮未选中任何 key", i)
		}
		counts[key]++
		_ = sched.ReleaseKey(ctx, key)
	}
	if counts["key-a"] < 260 || counts["key-b"] > 140 {
		t.Fatalf("期望 key-a:key-b ≈ 4:1，实际 %d:%d", counts["key-a"], counts["key-b"])
	}
}

func TestRedisResetClearsWeightHashes(t *testing.T) {
	ctx := context.Background()
	sched, client := newTestRedisScheduler(t)

	_ = sched.AddKey(ctx, "key-a", 1.0)
	_ = sched.SetHealthMultiplier(ctx, "key-a", 2.0)

	if err := sched.Reset(ctx); err != nil {
		t.Fatalf("Reset 出错: %v", err)
	}
	for _, hash := range []string{keyBaseWeightHashKey, keyHealthMultHashKey} {
		if n, err := client.HLen(ctx, hash).Result(); err != nil || n != 0 {
			t.Fatalf("Reset 后 %s 应为空，实际 len=%d err=%v", hash, n, err)
		}
	}
}
