package scheduler

import (
	"context"
	"testing"
	"time"
)

func TestTryAcquireSpecificKeyLocalScheduler(t *testing.T) {
	sched := NewScheduler(nil)
	ctx := context.Background()
	if err := sched.AddKey(ctx, "key1", 1); err != nil {
		t.Fatalf("AddKey key1 failed: %v", err)
	}
	acquired, err := sched.TryAcquireSpecificKey(ctx, "key1", 1)
	if err != nil {
		t.Fatalf("TryAcquireSpecificKey failed: %v", err)
	}
	if !acquired {
		t.Fatal("expected specific key to be acquired")
	}
	second, err := sched.TryAcquireSpecificKey(ctx, "key1", 1)
	if err != nil {
		t.Fatalf("second TryAcquireSpecificKey failed: %v", err)
	}
	if second {
		t.Fatal("expected concurrency limit to block second acquire")
	}
	if err := sched.ReleaseKey(ctx, "key1"); err != nil {
		t.Fatalf("ReleaseKey failed: %v", err)
	}
	if err := sched.MarkCooling(ctx, "key1", time.Minute); err != nil {
		t.Fatalf("MarkCooling failed: %v", err)
	}
	coolingAcquire, err := sched.TryAcquireSpecificKey(ctx, "key1", 1)
	if err != nil {
		t.Fatalf("TryAcquireSpecificKey while cooling failed: %v", err)
	}
	if coolingAcquire {
		t.Fatal("expected cooling key to remain unavailable")
	}
}
