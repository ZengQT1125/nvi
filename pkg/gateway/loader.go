package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"nvidia-api-gateway/pkg/db"
	"nvidia-api-gateway/pkg/scheduler"
	"nvidia-api-gateway/pkg/utils"
)

const (
	APIKeyStatusActive   = "Active"
	APIKeyStatusCooling  = "Cooling"
	APIKeyStatusDead     = "Dead"
	APIKeyStatusDisabled = "Disabled"
)

func LoadActiveKeys(ctx context.Context, sched *scheduler.Scheduler) error {
	rootKey := strings.TrimSpace(utils.GetEncryptionKey())
	if rootKey == "" {
		return errors.New("missing ENCRYPTION_KEY")
	}

	store, err := db.ReadStore()
	if err != nil {
		return fmt.Errorf("load api keys: %w", err)
	}
	if err := rebuildUpstreamRuntime(store); err != nil {
		return fmt.Errorf("rebuild upstream runtime: %w", err)
	}

	// 清除所有 transport 缓存，确保代理配置变更后新连接使用最新设置
	invalidateAllTransportCache()

	if err := sched.Reset(ctx); err != nil {
		return fmt.Errorf("reset scheduler state: %w", err)
	}

	for _, key := range store.APIKeys {
		if key.Status != APIKeyStatusActive || key.ProbeOnly {
			continue
		}
		plaintext, err := utils.Decrypt(key.Key, rootKey)
		if err != nil {
			return fmt.Errorf("decrypt api key %d: %w", key.ID, err)
		}
		if err := sched.AddKey(ctx, plaintext, key.Weight); err != nil {
			return fmt.Errorf("add api key %d: %w", key.ID, err)
		}
	}

	return nil
}

func StartSchedulerRefresher(ctx context.Context, sched *scheduler.Scheduler, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = LoadActiveKeys(ctx, sched)
		}
	}
}
